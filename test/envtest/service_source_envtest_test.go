/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package envtest_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	mockcf "github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
	"github.com/jacaudi/cloudflare-operator/internal/controller/tunnel"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// serviceEnvFixture wires the ServiceSourceReconciler + CloudflareTunnel
// reconciler inline, sharing one tunnelsynth.Cache. The source defers
// DNSRecord emission until Status.TunnelCNAME populates; a small inline
// MapFunc retriggers Service reconciles when the tunnel's status updates.
type serviceEnvFixture struct {
	c    client.Client
	mock *mockcf.Mock
	ns   string
}

// setupServiceEnv builds a per-test manager backed by the package-shared
// envtest config. nsName="" picks a short unique namespace; callers pass an
// explicit name when they need a fixed-length one (NameTooLong test).
func setupServiceEnv(t *testing.T, nsName string) *serviceEnvFixture {
	t.Helper()

	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	sch := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(sch))
	utilruntime.Must(v2alpha1.AddToScheme(sch))

	// Start from an empty cluster: earlier tests' CRs outlive them in the
	// shared apiserver and every manager watches cluster-wide.
	purgeCloudflareCRs(t)

	mgr, err := ctrl.NewManager(sharedConfig, ctrl.Options{
		Scheme:  sch,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)

	m := mockcf.New()
	cache := tunnelsynth.NewCache()

	// CloudflareTunnel reconciler — populates Status.TunnelCNAME so the
	// Service source's DNSRecord emission can advance.
	tunnelR := &tunnel.CloudflareTunnelReconciler{
		Client:   mgr.GetClient(),
		Scheme:   sch,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-tunnel-svc-test"),
		TunnelClientFn: func(_ cloudflare.Credentials) (cloudflare.TunnelClient, error) {
			return m.Tunnel, nil
		},
		Cache:        cache,
		DefaultImage: tunnel.DefaultCloudflaredImage,
		// Short cascade-GC grace so the two-tick self-delete window is
		// seconds, not the production 60s, keeping envtest runtime sane.
		// Shared across all setupServiceEnv callers: verified harmless —
		// no existing caller deletes the last attaching source of an
		// auto-created tunnel and then asserts the tunnel still exists
		// past the grace window (they keep the source alive for the test
		// or only assert creation/emission/prune while it persists).
		PendingDeletionGrace: 3 * time.Second,
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		Named("cloudflaretunnel-"+sanitizeTestName(t.Name())).
		For(&v2alpha1.CloudflareTunnel{}).
		Complete(tunnelR))

	// ServiceSource reconciler. The inline Watch on CloudflareTunnel
	// retriggers attached Services when the tunnel's Status.TunnelCNAME
	// flips from empty → populated (deferred-emission retrigger).
	svcR := &tunnel.ServiceSourceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   sch,
		Cache:    cache,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-svc-source-test"),
		DefaultConnector: v2alpha1.ConnectorSpec{
			Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
		},
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		Named("servicesource-"+sanitizeTestName(t.Name())).
		For(&corev1.Service{}).
		Owns(&v2alpha1.CloudflareDNSRecord{}).
		Watches(&v2alpha1.CloudflareTunnel{},
			handler.EnqueueRequestsFromMapFunc(tunnelToServicesTestMapFunc(mgr))).
		Complete(svcR))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	startManager(t, ctx, mgr)

	syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Second)
	defer syncCancel()
	require.True(t, mgr.GetCache().WaitForCacheSync(syncCtx), "manager cache failed to sync")

	if nsName == "" {
		nsName = shortUniqueNamespace(t)
	}
	require.NoError(t, mgr.GetClient().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: nsName},
	}))

	return &serviceEnvFixture{c: mgr.GetClient(), mock: m, ns: nsName}
}

// tunnelToServicesTestMapFunc mirrors setup.go::tunnelToServices: enqueues
// every annotated Service in the tunnel's namespace on a tunnel event.
// Re-implemented inline to keep the envtest self-contained.
func tunnelToServicesTestMapFunc(mgr ctrl.Manager) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		tn, ok := obj.(*v2alpha1.CloudflareTunnel)
		if !ok {
			return nil
		}
		var svcs corev1.ServiceList
		if err := mgr.GetClient().List(ctx, &svcs, client.InNamespace(tn.Namespace)); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0, len(svcs.Items))
		for _, s := range svcs.Items {
			if s.Annotations[conventions.AnnotationTunnel] == "true" {
				out = append(out, reconcile.Request{
					NamespacedName: types.NamespacedName{Namespace: s.Namespace, Name: s.Name},
				})
			}
		}
		return out
	}
}

// sanitizeTestName produces a controller-name-safe slug from t.Name(). The
// controller-runtime metrics registry is process-global and rejects
// duplicates; each test (and sub-test) needs its own slot.
func sanitizeTestName(name string) string {
	out := strings.ToLower(name)
	out = strings.ReplaceAll(out, "/", "-")
	out = strings.ReplaceAll(out, "_", "-")
	return out
}

// shortUniqueNamespace returns a DNS-1123-valid namespace small enough that
// the derived "<ns>-<tunnel-name>" stays under the 52-char cap enforced
// by DeriveTunnelName. Hard-capped at 20 chars; hex suffix avoids collisions.
func shortUniqueNamespace(t *testing.T) string {
	t.Helper()
	suffix := strconv.FormatInt(time.Now().UnixNano()%0xFFFFFF, 16)
	base := sanitizeTestName(t.Name())
	if len(base) > 12 {
		base = base[:12]
	}
	out := base + "-" + suffix
	if len(out) > 20 {
		out = out[:20]
	}
	return strings.TrimRight(out, "-")
}

// TestServiceSourceEnvtest_OptInAutoCreatesTunnelAndDNS covers design §12.2:
// annotating a Service with tunnel=true + tunnel-name=payments + hostnames
// produces an auto-created CloudflareTunnel CR named <ns>-payments AND a
// dog-fooded CloudflareDNSRecord (CNAME hostname → tunnel CNAME). Exercises
// the full deferred-emission flow: source caches contrib → tunnel reconciler
// creates the Cloudflare-side tunnel + populates Status.TunnelCNAME →
// inline tunnelToServices watch retriggers source → DNSRecord emitted.
func TestServiceSourceEnvtest_OptInAutoCreatesTunnelAndDNS(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupServiceEnv(t, "")
	ctx := context.Background()

	// A CloudflareZone CR for example.com — the emitted CloudflareDNSRecord
	// uses spec.zoneRef (per design §14: tunnel-emitted CRs never set
	// spec.zoneID directly). The CR's admission validation requires one of
	// zoneID/zoneRef; without zoneRef the DNSRecord create would 422.
	zone := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	expectedTunnel := f.ns + "-payments"
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "foo.example.com",
				conventions.AnnotationZoneRef:    "example-com",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, svc))

	// Auto-created CloudflareTunnel CR with the derived name.
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		return f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: expectedTunnel}, &tn) == nil
	}, 15*time.Second, 250*time.Millisecond, "CloudflareTunnel %q created", expectedTunnel)

	// Wait for tunnel status to populate so the deferred emission can advance.
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: expectedTunnel}, &tn); err != nil {
			return false
		}
		return tn.Status.TunnelCNAME != ""
	}, 15*time.Second, 250*time.Millisecond, "tunnel Status.TunnelCNAME populated")

	// Dog-fooded CloudflareDNSRecord emitted once Status.TunnelCNAME populates.
	require.Eventually(t, func() bool {
		var list v2alpha1.CloudflareDNSRecordList
		if err := f.c.List(ctx, &list, client.InNamespace(f.ns)); err != nil {
			return false
		}
		for _, r := range list.Items {
			if r.Spec.Type == "CNAME" && r.Spec.Name == "foo.example.com" &&
				r.Spec.Content != nil && *r.Spec.Content != "" {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond, "CloudflareDNSRecord for foo.example.com emitted with non-empty content")
}

// TestServiceSourceEnvtest_NoTunnelName_AttachesToNamespacePool covers
// design §12.3: a Service annotated with tunnel=true but no tunnel-name
// attaches to the per-namespace pool tunnel cf-<ns>, which is auto-created
// on first attachment.
func TestServiceSourceEnvtest_NoTunnelName_AttachesToNamespacePool(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupServiceEnv(t, "")
	ctx := context.Background()

	expectedPool := f.ns
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:    "true",
				conventions.AnnotationHostnames: "bar.example.com",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, svc))

	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		return f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: expectedPool}, &tn) == nil
	}, 15*time.Second, 250*time.Millisecond, "namespace-pool CloudflareTunnel %q created", expectedPool)
}

// TestServiceSourceEnvtest_NoTLSVerify_ThreadsIntoIngressEntry covers
// design §12.10: cloudflare.io/no-tls-verify=true on a Service threads
// noTLSVerify: true into the synthesized cloudflared ingress entry. We
// observe the PUT'd configuration via the mock's GetConfiguration and look
// for the matching hostname's OriginRequest.NoTLSVerify.
func TestServiceSourceEnvtest_NoTLSVerify_ThreadsIntoIngressEntry(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupServiceEnv(t, "")
	ctx := context.Background()

	expectedTunnel := f.ns
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:      "true",
				conventions.AnnotationHostnames:   "tls.example.com",
				conventions.AnnotationScheme:      "https",
				conventions.AnnotationNoTLSVerify: "true",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 443}},
		},
	}
	require.NoError(t, f.c.Create(ctx, svc))

	// Wait for the tunnel to exist and TunnelID to populate (we need the ID
	// to look up the configuration on the mock).
	var tunnelID string
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: expectedTunnel}, &tn); err != nil {
			return false
		}
		if tn.Status.TunnelID == "" {
			return false
		}
		tunnelID = tn.Status.TunnelID
		return true
	}, 15*time.Second, 250*time.Millisecond, "tunnel %q has Status.TunnelID populated", expectedTunnel)

	// Configuration on the mock carries the no-tls-verify ingress entry for
	// the annotated hostname. The tunnel reconciler PUTs the merged ingress
	// list once contributions are visible in the cache.
	require.Eventually(t, func() bool {
		cfg, err := f.mock.Tunnel.GetConfiguration(ctx, "acct-1", tunnelID)
		if err != nil {
			return false
		}
		for _, e := range cfg.Config.Ingress {
			if e.Hostname != "tls.example.com" {
				continue
			}
			if e.OriginRequest != nil && e.OriginRequest.NoTLSVerify != nil && *e.OriginRequest.NoTLSVerify {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond, "noTLSVerify=true threaded into ingress entry for tls.example.com")
}

// TestServiceSourceEnvtest_OwnsEmittedDNSRecord_RecreatesOnOutOfBandDelete
// closes backlog item #1(d): when the operator's emitted CloudflareDNSRecord
// CR is deleted out-of-band, the source controller must notice and re-emit.
// This requires .Owns(&v2alpha1.CloudflareDNSRecord{}) on the source builder
// — the child is already controller-owner-ref'd via reconcile.SetControllerOwner
// in EmitDNSRecord, but without .Owns the watch isn't wired.
func TestServiceSourceEnvtest_OwnsEmittedDNSRecord_RecreatesOnOutOfBandDelete(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	fx := setupServiceEnv(t, "")
	ctx := context.Background()

	// Create the zone CR so the emitted CloudflareDNSRecord passes admission
	// (zoneID or zoneRef is required by CEL rule on the CRD).
	zone := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: fx.ns},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, fx.c.Create(ctx, zone))

	// Create an annotated Service that triggers tunnel auto-create + DNSRecord
	// emission. Pattern mirrors TestServiceSourceEnvtest_OptInAutoCreatesTunnelAndDNS.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc-owns",
			Namespace: fx.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "t1",
				conventions.AnnotationHostnames:  "owns.example.com",
				conventions.AnnotationZoneRef:    "example-com",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, fx.c.Create(ctx, svc))

	// Wait for the emitted CloudflareDNSRecord (initial emit).
	var emitted v2alpha1.CloudflareDNSRecord
	require.Eventually(t, func() bool {
		var list v2alpha1.CloudflareDNSRecordList
		if err := fx.c.List(ctx, &list, client.InNamespace(fx.ns)); err != nil {
			return false
		}
		for _, r := range list.Items {
			if r.Spec.Name == "owns.example.com" {
				emitted = r
				return true
			}
		}
		return false
	}, 30*time.Second, 200*time.Millisecond, "operator must emit the initial CloudflareDNSRecord for owns.example.com")

	originalUID := emitted.UID
	require.NotEmpty(t, originalUID, "initial emit must have a UID")

	// Out-of-band delete the emitted CR. In production a finalizer added by
	// the DNSRecord controller would run a CF-cleanup pass first; in this
	// envtest there's no DNSRecord controller wired, so the CR is removed
	// cleanly. The point under test is the SOURCE controller noticing the
	// removal and re-emitting.
	require.NoError(t, fx.c.Delete(ctx, &emitted))

	// .Owns must trigger a source reconcile on the child delete event →
	// EmitDNSRecord SSAs the CR back into existence (with a NEW UID).
	// Without .Owns, this Eventually times out.
	require.Eventually(t, func() bool {
		var list v2alpha1.CloudflareDNSRecordList
		if err := fx.c.List(ctx, &list, client.InNamespace(fx.ns)); err != nil {
			return false
		}
		for _, r := range list.Items {
			if r.Spec.Name == "owns.example.com" && r.UID != originalUID {
				return true
			}
		}
		return false
	}, 30*time.Second, 200*time.Millisecond,
		"emitted CR must be re-created (new UID) after out-of-band delete; "+
			"this requires .Owns(&v2alpha1.CloudflareDNSRecord{}) on the source builder")
}

// TestServiceSourceEnvtest_NameTooLong_RejectedNoTunnelCreated covers
// design §12.11: a Service whose derived tunnel CR name exceeds 52 chars
// is rejected (Event with Reason=NameTooLong) and no CloudflareTunnel CR is
// created. Namespace = 40 chars, tunnel-name = 20 chars → derived name
// "<40>-<20>" = 61 chars, well over the cap.
func TestServiceSourceEnvtest_NameTooLong_RejectedNoTunnelCreated(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	// Fixed 40-char DNS-1123-valid namespace. Each test runs once per
	// `go test` invocation against a fresh envtest binary, so no collision.
	nsName := strings.Repeat("a", 40)
	f := setupServiceEnv(t, nsName)
	ctx := context.Background()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: strings.Repeat("b", 20),
				conventions.AnnotationHostnames:  "x.example.com",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, svc))

	// Give the reconciler time to observe the Service and surface the
	// NameTooLong event. The reconcile is non-retryable (nil error, no
	// requeue) so a brief wait is sufficient.
	time.Sleep(2 * time.Second)

	var list v2alpha1.CloudflareTunnelList
	require.NoError(t, f.c.List(ctx, &list, client.InNamespace(f.ns)))
	require.Empty(t, list.Items, "no CloudflareTunnel CR created when derived name exceeds 52 chars")
}

// expectedEmittedDNSRecordName mirrors the package-private production
// helper internal/controller/tunnel.emittedDNSRecordName; locked against
// drift by TestEmittedDNSRecordName_NewShape_DropsOwnerName.
func expectedEmittedDNSRecordName(hostname string) string {
	sum := sha256.Sum256([]byte(hostname))
	short := hex.EncodeToString(sum[:4])
	// sanitize: lowercase, non-alnum→'-', collapse runs, trim hyphens.
	out := make([]byte, 0, len(hostname))
	prevHyphen := false
	for i := 0; i < len(hostname); i++ {
		c := hostname[i]
		switch {
		case c >= 'a' && c <= 'z':
			out = append(out, c)
			prevHyphen = false
		case c >= '0' && c <= '9':
			out = append(out, c)
			prevHyphen = false
		case c >= 'A' && c <= 'Z':
			out = append(out, c+32)
			prevHyphen = false
		default:
			if !prevHyphen {
				out = append(out, '-')
				prevHyphen = true
			}
		}
	}
	san := strings.Trim(string(out), "-")
	const maxSan = 63 - 1 - 8
	if len(san) > maxSan {
		san = strings.TrimRight(san[:maxSan], "-")
	}
	if san == "" {
		return short
	}
	return san + "-" + short
}

// TestServiceSourceEnvtest_S4_MigrationGCsOldFormCR closes backlog #6
// end-to-end: a pre-existing operator-emitted CR with the legacy doubled-
// name shape (and the three source-identity labels marking it as provably-
// own) is migration-GC'd by pruneOrphanedDNSRecords on the next Reconcile,
// while the new-form CR is the one that remains. User-authored CRs without
// the source labels are NOT touched.
func TestServiceSourceEnvtest_S4_MigrationGCsOldFormCR(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupServiceEnv(t, "")
	ctx := context.Background()

	zone := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name: "example.com", Type: "full", DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	const hostname = "myservice.example.com"
	expectedTunnel := f.ns + "-payments"

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  hostname,
				conventions.AnnotationZoneRef:    "example-com",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, svc))

	// Wait for the auto-created CloudflareTunnel + TunnelCNAME to populate
	// so the source reconciler will emit the new-form CR.
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: expectedTunnel}, &tn); err != nil {
			return false
		}
		return tn.Status.TunnelCNAME != ""
	}, 15*time.Second, 250*time.Millisecond, "tunnel Status.TunnelCNAME populated")

	// Wait for the new-form CR to emit (this is the post-S4 derivation).
	newName := expectedEmittedDNSRecordName(hostname)
	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareDNSRecord
		return f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: newName}, &got) == nil
	}, 15*time.Second, 250*time.Millisecond, "new-form CR %q emitted", newName)

	// Refresh the Service so we have its UID for the owner-ref.
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: "svc"}, svc))

	// Now plant an OLD-form CR for the SAME hostname, with the three
	// source-identity labels (so it's provably-own from the pruner's POV)
	// and an owner-ref to the Service (so the source controller's .Owns
	// watch retriggers it). Its metadata.Name uses the legacy
	// <sourceName>-<sanitizedHost>-<hash> doubled shape that S4 collapses.
	oldNameLegacy := "svc-" + strings.ReplaceAll(strings.ReplaceAll(hostname, ".", "-"), "_", "-") + "-deadbeef"
	require.NotEqual(t, newName, oldNameLegacy, "test setup bug: old-form name must differ from new-form")
	oldCR := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      oldNameLegacy,
			Namespace: f.ns,
			Labels: map[string]string{
				conventions.LabelSourceKind:      "Service",
				conventions.LabelSourceName:      "svc",
				conventions.LabelSourceNamespace: f.ns,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "v1",
				Kind:               "Service",
				Name:               svc.Name,
				UID:                svc.UID,
				Controller:         ptr.To(true),
				BlockOwnerDeletion: ptr.To(true),
			}},
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Type:    "CNAME",
			Name:    hostname,
			ZoneRef: &v2alpha1.ZoneReference{Name: "example-com", Namespace: f.ns},
		},
	}
	require.NoError(t, f.c.Create(ctx, oldCR))

	// Trigger another Reconcile of the Service so pruneOrphanedDNSRecords
	// runs again (the .Owns watch on the freshly-created old-form CR
	// fires this automatically; the touch annotation is belt-and-braces).
	patch := client.MergeFrom(svc.DeepCopy())
	if svc.Annotations == nil {
		svc.Annotations = map[string]string{}
	}
	svc.Annotations["cloudflare.io/touch"] = time.Now().Format(time.RFC3339Nano)
	require.NoError(t, f.c.Patch(ctx, svc, patch))

	// Old-form CR must be migration-GC'd within the timeout.
	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareDNSRecord
		err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: oldNameLegacy}, &got)
		return apierrors.IsNotFound(err)
	}, 15*time.Second, 250*time.Millisecond, "old-form CR %q was NOT migration-GC'd by pruneOrphanedDNSRecords", oldNameLegacy)

	// New-form CR must still be present (sanity check — prune must NOT
	// accidentally delete the correctly-named replacement).
	var stillPresent v2alpha1.CloudflareDNSRecord
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: newName}, &stillPresent),
		"new-form CR %q must survive migration-GC", newName)
}

// TestServiceSourceEnvtest_S5_MigrationCascadeGCsOldCfPrefixTunnel closes
// backlog #5 end-to-end. A pre-existing auto-created tunnel CR with the
// legacy "cf-<ns>" shape sits in the apiserver. After S5 a Service with
// the tunnel annotations is created; the source reconciler attaches to
// the NEW-shape "<ns>" tunnel (creating it if absent). The OLD "cf-<ns>"
// tunnel enters the P4 cascade-GC orphan state (no attached sources, no
// owner refs) and self-deletes after the envtest-short grace window.
// Locks the "free migration" property of S5 — no operator-side migration
// code is required; the existing orphan→grace→self-delete pattern handles
// the rename.
func TestServiceSourceEnvtest_S5_MigrationCascadeGCsOldCfPrefixTunnel(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupServiceEnv(t, "")
	ctx := context.Background()

	// Pre-populate the legacy cf-<ns> tunnel CR as if it had been auto-
	// created by the pre-S5 operator. Must carry the auto-created
	// annotation so it's cascadeGCEligible. No OwnerReferences and no
	// Status.AttachedSources means it's already orphan-state-ready once
	// we don't attach any sources to it. To make the self-delete tick
	// deterministic within envtest time, we stamp Status.LastOrphanedAt
	// to ~5s ago (beyond the 3s PendingDeletionGrace) so the FIRST
	// reconcile observes the grace as already elapsed.
	oldTunnelName := "cf-" + f.ns
	oldTunnel := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      oldTunnelName,
			Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationAutoCreated: "true",
			},
		},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name: oldTunnelName,
			Connector: v2alpha1.ConnectorSpec{
				Replicas: 1, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
			},
		},
	}
	require.NoError(t, f.c.Create(ctx, oldTunnel))

	// Stamp LastOrphanedAt to 5 s ago so the FIRST reconcile that sees the
	// old tunnel observes the grace as already elapsed and proceeds to
	// self-delete. Use a retry loop around Get+StatusUpdate so that any
	// resourceVersion conflict caused by a concurrent reconcile is handled
	// transparently.
	require.Eventually(t, func() bool {
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: oldTunnelName}, oldTunnel); err != nil {
			return false
		}
		fiveSecondsAgo := metav1.NewTime(time.Now().Add(-5 * time.Second))
		oldTunnel.Status.LastOrphanedAt = &fiveSecondsAgo
		return f.c.Status().Update(ctx, oldTunnel) == nil
	}, 5*time.Second, 50*time.Millisecond, "stamp LastOrphanedAt on old cf- tunnel")

	// Create a Service annotated to attach to the per-namespace pool
	// (no tunnel-name annotation → DeriveTunnelName -> "<ns>"). After
	// S5 this attaches to f.ns (NOT "cf-"+f.ns).
	zone := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name: "example.com", Type: "full", DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:    "true",
				conventions.AnnotationHostnames: "svc.example.com",
				conventions.AnnotationZoneRef:   "example-com",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, svc))

	// 1. The NEW-shape tunnel CR (named f.ns, no cf- prefix) must appear.
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		return f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: f.ns}, &tn) == nil
	}, 15*time.Second, 250*time.Millisecond, "new-shape tunnel CR %q created", f.ns)

	// 2. The OLD "cf-"+f.ns tunnel CR must enter cascade-GC and self-delete.
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: oldTunnelName}, &tn)
		return apierrors.IsNotFound(err)
	}, 15*time.Second, 250*time.Millisecond, "old cf- tunnel CR %q was NOT cascade-GC'd", oldTunnelName)
}
