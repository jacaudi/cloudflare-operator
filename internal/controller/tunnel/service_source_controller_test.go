/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnel

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

func srcScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, v2alpha1.AddToScheme(s))
	return s
}

// preCreatedTunnel returns a CloudflareTunnel with TunnelCNAME populated so
// the source reconciler can emit a DNSRecord on the first pass without
// waiting for a Watches-driven retrigger.
func preCreatedTunnel(name, namespace string) *v2alpha1.CloudflareTunnel {
	return &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name: name,
			Connector: v2alpha1.ConnectorSpec{
				Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
			},
		},
		Status: v2alpha1.CloudflareTunnelStatus{
			TunnelID:    "tun-abc",
			TunnelCNAME: "tun-abc.cfargotunnel.com",
		},
	}
}

func TestServiceSource_OptInCreatesTunnelAndDNSRecord(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "app-foo",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "foo.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	// Pre-create the tunnel with TunnelCNAME populated so the source
	// reconciler proceeds to emit the DNSRecord on first reconcile.
	tn := preCreatedTunnel("app-foo-payments", "app-foo")
	base := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc, tn).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}, &v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	cache := tunnelsynth.NewCache()
	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: cache,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app-foo", Name: "svc"}})
	require.NoError(t, err)

	// CloudflareTunnel still present in source's namespace.
	var got v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app-foo", Name: "app-foo-payments"}, &got))

	// CloudflareDNSRecord emitted, owner-reffed to the Service, source labels stamped.
	var dnsList v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Len(t, dnsList.Items, 1)
	dr := dnsList.Items[0]
	require.Equal(t, "Service", dr.Labels[conventions.LabelSourceKind])
	require.Equal(t, "svc", dr.Labels[conventions.LabelSourceName])
	require.Equal(t, "app-foo", dr.Labels[conventions.LabelSourceNamespace])
	require.Equal(t, "CNAME", dr.Spec.Type)
	require.Equal(t, "foo.example.com", dr.Spec.Name)
	require.NotNil(t, dr.Spec.Content)
	require.Equal(t, "tun-abc.cfargotunnel.com", *dr.Spec.Content)
	require.Len(t, dr.OwnerReferences, 1)
	require.Equal(t, "svc", dr.OwnerReferences[0].Name)

	// Cache populated under the named-tunnel key.
	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "app-foo", Name: "app-foo-payments"})
	require.Len(t, snap, 1)
	require.Equal(t, "foo.example.com", snap[0].Hostname)
}

// TestServiceSource_AutoCreateStampsSourceLabels is a regression for the bug
// where EnsureTunnelCR called owner.GetObjectKind().GroupVersionKind().Kind to
// derive the source-kind label. The typed controller-runtime client clears
// TypeMeta on Get, so that returns "" — the auto-created CloudflareTunnel
// would land with cloudflare.io/source-kind="", defeating Foundation §7
// auditability. Fixed by passing an explicit ownerKind parameter to
// EnsureTunnelCR; this test asserts all three source labels are present on
// the auto-created CR.
func TestServiceSource_AutoCreateStampsSourceLabels(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "app-foo",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "foo.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	// NO pre-created tunnel — exercise the auto-create path through
	// EnsureTunnelCR so the source-labels stamp is observable.
	base := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: tunnelsynth.NewCache(),
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app-foo", Name: "svc"}})
	require.NoError(t, err)

	var tn v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app-foo", Name: "app-foo-payments"}, &tn))
	require.Equal(t, "Service", tn.Labels[conventions.LabelSourceKind],
		"source-kind label must be the literal 'Service', not '' (typed client clears TypeMeta on Get)")
	require.Equal(t, "svc", tn.Labels[conventions.LabelSourceName])
	require.Equal(t, "app-foo", tn.Labels[conventions.LabelSourceNamespace])
}

func TestServiceSource_NoTunnelNameAttachesToNamespacePool(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "app-foo",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:    "true",
				conventions.AnnotationHostnames: "bar.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	base := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	r := &ServiceSourceReconciler{Client: c, Scheme: srcScheme(t), Cache: tunnelsynth.NewCache()}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app-foo", Name: "svc"}})
	require.NoError(t, err)

	var tn v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app-foo", Name: "app-foo"}, &tn))
}

func TestServiceSource_OptOut_ClearsCache(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{}, // no opt-in
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	base := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	cache := tunnelsynth.NewCache()
	cache.Set(tunnelsynth.TunnelKey{Namespace: "ns", Name: "ns"},
		tunnelsynth.SourceKey{Kind: "Service", Namespace: "ns", Name: "svc"},
		[]tunnelsynth.IngressContribution{{Hostname: "stale.example.com", Service: "http://x:80"}})

	r := &ServiceSourceReconciler{Client: c, Scheme: srcScheme(t), Cache: cache}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "svc"}})
	require.NoError(t, err)

	require.Empty(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "ns"}))
}

// TestServiceSource_OptOutSweepsBothPoolAndNamed verifies Correction C — when
// a Service was previously attached to a named tunnel via annotation, then the
// annotation is dropped (or opt-in disabled), the cache entry under the named
// tunnel-key is swept clean.
func TestServiceSource_OptOutSweepsBothPoolAndNamed(t *testing.T) {
	// Service no longer opted in but still carries the tunnel-name annotation
	// (mid-edit state). The reconciler must clear the named-tunnel entry too.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnelName: "billing", // tunnel-name still set
				// no AnnotationTunnel=true
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	base := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	cache.Set(tunnelsynth.TunnelKey{Namespace: "ns", Name: "ns-billing"},
		tunnelsynth.SourceKey{Kind: "Service", Namespace: "ns", Name: "svc"},
		[]tunnelsynth.IngressContribution{{Hostname: "stale.example.com", Service: "http://x:80"}})

	r := &ServiceSourceReconciler{Client: c, Scheme: srcScheme(t), Cache: cache}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "svc"}})
	require.NoError(t, err)

	require.Empty(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "ns-billing"}))
}

// TestServiceSource_AnnotationChangeSweepsPriorKey verifies Correction C's
// in-memory tracking: a Service opted-in to "ns-payments" gets retargeted
// to "ns-billing"; the prior key must be cleared on the second reconcile.
func TestServiceSource_AnnotationChangeSweepsPriorKey(t *testing.T) {
	// First reconcile — attach to "payments".
	svcA := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "foo.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	tnA := preCreatedTunnel("ns-payments", "ns")
	tnB := preCreatedTunnel("ns-billing", "ns")
	base := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svcA, tnA, tnB).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}, &v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	cache := tunnelsynth.NewCache()
	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: cache,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "svc"}})
	require.NoError(t, err)

	// First snapshot under "payments".
	require.Len(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "ns-payments"}), 1)

	// Mutate the Service in place to switch to "billing".
	var live corev1.Service
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "svc"}, &live))
	live.Annotations[conventions.AnnotationTunnelName] = "billing"
	require.NoError(t, c.Update(context.Background(), &live))

	_, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "svc"}})
	require.NoError(t, err)

	// Prior key cleared.
	require.Empty(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "ns-payments"}),
		"prior tunnel-key must be swept on annotation change")
	// New key populated.
	require.Len(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "ns-billing"}), 1)
}

func TestServiceSource_NameTooLong_StatusEventOnly(t *testing.T) {
	// Name-too-long is a stable failure surfaced via event, not an error
	// return — no panic, no CR created, no cache write.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "very-very-long-namespace-name",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "very-very-long-tunnel-name-here",
				conventions.AnnotationHostnames:  "x.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	base := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	rec := record.NewFakeRecorder(8)
	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: tunnelsynth.NewCache(), Recorder: rec,
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}})
	require.NoError(t, err, "name-too-long is a stable failure surfaced via event, not an error return")

	var list v2alpha1.CloudflareTunnelList
	require.NoError(t, c.List(context.Background(), &list))
	require.Empty(t, list.Items)

	// Event with Reason=NameTooLong emitted.
	select {
	case ev := <-rec.Events:
		require.Contains(t, ev, conventions.ReasonNameTooLong)
	default:
		t.Fatal("expected NameTooLong event")
	}
}

func TestServiceSource_MultipleHostnames_EmitsOneRecordEach(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "app-foo",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "foo.example.com,bar.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	tn := preCreatedTunnel("app-foo-payments", "app-foo")
	base := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc, tn).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}, &v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: tunnelsynth.NewCache(),
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app-foo", Name: "svc"}})
	require.NoError(t, err)

	var dnsList v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Len(t, dnsList.Items, 2)

	names := map[string]bool{}
	for _, dr := range dnsList.Items {
		names[dr.Spec.Name] = true
	}
	require.True(t, names["foo.example.com"])
	require.True(t, names["bar.example.com"])
}

// TestServiceSource_TunnelCNAMEEmpty_DefersEmission verifies Correction A —
// when the tunnel CR exists but TunnelCNAME is empty (e.g. tunnel reconciler
// hasn't completed Create yet), the source reconciler must NOT emit a
// DNSRecord with Content=&"". Defer until the Watches-driven retrigger.
func TestServiceSource_TunnelCNAMEEmpty_DefersEmission(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "app-foo",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "foo.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	// Tunnel CR exists but has no TunnelCNAME.
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "app-foo-payments", Namespace: "app-foo"},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name: "app-foo-payments",
			Connector: v2alpha1.ConnectorSpec{
				Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
			},
		},
	}
	base := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc, tn).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}, &v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: cache,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app-foo", Name: "svc"}})
	require.NoError(t, err)

	// No DNSRecord emitted yet — defer for Watches retrigger.
	var dnsList v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Empty(t, dnsList.Items, "must not emit DNSRecord when TunnelCNAME is empty")

	// Cache is still populated — that's the contract for the tunnel
	// reconciler to drive a config PUT in parallel.
	require.Len(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "app-foo", Name: "app-foo-payments"}), 1)
}

func TestServiceSource_ZoneRefAnnotation_ThreadedToDNSRecord(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "app-foo",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "foo.example.com",
				conventions.AnnotationZoneRef:    "my-zone",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	tn := preCreatedTunnel("app-foo-payments", "app-foo")
	base := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc, tn).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}, &v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: tunnelsynth.NewCache(),
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app-foo", Name: "svc"}})
	require.NoError(t, err)

	var dnsList v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Len(t, dnsList.Items, 1)
	require.NotNil(t, dnsList.Items[0].Spec.ZoneRef)
	require.Equal(t, "my-zone", dnsList.Items[0].Spec.ZoneRef.Name)
	require.Equal(t, "app-foo", dnsList.Items[0].Spec.ZoneRef.Namespace)
}

// TestServiceSource_ServiceDeletedSweepsCache verifies the NotFound branch of
// Reconcile: when the Service is deleted, the in-memory lastAttached entry
// and the corresponding cache slot for the source must be swept clean.
func TestServiceSource_ServiceDeletedSweepsCache(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "foo.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	preTun := preCreatedTunnel("ns-payments", "ns")
	base := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc, preTun).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}, &v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: cache,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}

	tunKey := tunnelsynth.TunnelKey{Namespace: "ns", Name: "ns-payments"}

	// First reconcile populates lastAttached + the cache.
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "svc"}})
	require.NoError(t, err)
	require.Len(t, cache.Snapshot(tunKey), 1)

	// Delete the Service so the next reconcile hits the NotFound branch.
	require.NoError(t, c.Delete(context.Background(), svc))

	_, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "svc"}})
	require.NoError(t, err)
	require.Empty(t, cache.Snapshot(tunKey),
		"NotFound branch must sweep the cache entry for the deleted Service")
}

// TestServiceSource_InvalidName_StatusEventOnly verifies that DeriveTunnelName
// failure (uppercase / underscore in annotation) surfaces as an Event with
// Reason=InvalidName and a nil error return — no CR is created.
func TestServiceSource_InvalidName_StatusEventOnly(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "Invalid_Name", // uppercase + underscore
				conventions.AnnotationHostnames:  "x.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	base := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	rec := record.NewFakeRecorder(10)
	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: tunnelsynth.NewCache(), Recorder: rec,
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "svc"}})
	require.NoError(t, err)

	var list v2alpha1.CloudflareTunnelList
	require.NoError(t, c.List(context.Background(), &list))
	require.Empty(t, list.Items)

	select {
	case ev := <-rec.Events:
		require.Contains(t, ev, conventions.ReasonInvalidName)
	default:
		t.Fatal("expected an InvalidName event")
	}
}

// TestServiceSource_TranslatorWarningsEmitEvents verifies that warnings
// returned by tunnelsynth.TranslateService (e.g. MissingHostnames) are
// fanned out as Warning Events on the Service.
func TestServiceSource_TranslatorWarningsEmitEvents(t *testing.T) {
	// Opted in, but no hostnames annotation — translator emits a
	// MissingHostnames warning. Pre-populate the tunnel so the test reaches
	// the translator-warnings emit path (which runs after EnsureTunnelCR).
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				// no Hostnames annotation
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	preTun := preCreatedTunnel("ns-payments", "ns")
	base := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc, preTun).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	rec := record.NewFakeRecorder(10)
	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: tunnelsynth.NewCache(), Recorder: rec,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "svc"}})
	require.NoError(t, err)

	select {
	case ev := <-rec.Events:
		require.Contains(t, ev, "MissingHostnames")
	default:
		t.Fatal("expected a translator-warning event")
	}
}

// TestEmittedDNSRecordNameDNS1123Compliant exercises edge-case hostnames
// against the hash-suffix logic. Every emitted DNSRecord CR name must be
// a valid DNS-1123 subdomain — labels of [a-z0-9] with optional internal
// hyphens, ≤63 chars per label — AND must end in an alphanumeric character
// (the SHA-256 hex hash guarantees this).
func TestEmittedDNSRecordNameDNS1123Compliant(t *testing.T) {
	// Top-level CR-name regex: lowercase a-z0-9 + internal hyphens, no
	// leading/trailing hyphens, ≤63 chars.
	dns1123Subdomain := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

	cases := []struct {
		name     string
		svcName  string
		hostname string
	}{
		{"trailing-dot", "svc", "foo.example.com."},
		{"consecutive-dots", "svc", "foo..bar.example.com"},
		{"mixed-case", "svc", "Foo.Example.COM"},
		{"long-hostname", "svc", strings.Repeat("a", 80) + ".example.com"},
		{"already-clean", "svc", "foo.example.com"},
		{"hyphen-prefix", "svc", "-leading.example.com"},
		{"underscore", "svc", "foo_bar.example.com"},
		{"all-non-alnum", "svc", "..--..--..--"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := emittedDNSRecordName(tc.hostname)
			require.LessOrEqual(t, len(got), 63, "name must fit DNS-1123 label limit: %q", got)
			require.True(t, dns1123Subdomain.MatchString(got), "name must be DNS-1123 valid: %q", got)
			// Hash is always 8 hex chars at the tail (with separator except for
			// pathological all-non-alnum hostnames which fall back to hash-only).
			require.Regexp(t, `[0-9a-f]{8}$`, got, "name must end with 8-hex-hash: %q", got)
		})
	}
}

// TestEmittedDNSRecordName_NoCollisionOnSanitizedAlias verifies that two
// hostnames which sanitize to the same prefix (because the only difference
// is non-alphanumeric punctuation) still produce distinct CR names. Without
// the hash suffix, the second hostname's DNSRecord would be
// silently overwritten by the second SSA Apply on the same CR name.
func TestEmittedDNSRecordName_NoCollisionOnSanitizedAlias(t *testing.T) {
	a := emittedDNSRecordName("foo.example.com")
	b := emittedDNSRecordName("foo-example-com")
	require.NotEqual(t, a, b, "alias hostnames must produce distinct CR names")
}

// TestEmittedDNSRecordName_NoCollisionOnTruncation verifies that two long
// hostnames sharing the same first N chars (where N is past the truncation
// budget) still produce distinct CR names via the hash suffix.
func TestEmittedDNSRecordName_NoCollisionOnTruncation(t *testing.T) {
	prefix := strings.Repeat("a", 60)
	a := emittedDNSRecordName(prefix + ".one.example.com")
	b := emittedDNSRecordName(prefix + ".two.example.com")
	require.NotEqual(t, a, b, "long hostnames sharing a prefix must produce distinct CR names")
}

// TestServiceSource_OptOut_PrunesEmittedCRs verifies issue #145 fix for the
// Service controller: a Service with cloudflare.io/tunnel="true" that emits
// DNSRecord CRs must have those CRs deleted when the annotation is removed.
// Note: TestServiceSource_OptOut_ClearsCache covers the cache-side of opt-out;
// this test covers the CR-side gap that was missed there.
func TestServiceSource_OptOut_PrunesEmittedCRs(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "app-foo",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "foo.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	tn := preCreatedTunnel("app-foo-payments", "app-foo")
	base := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc, tn).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}, &v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	cache := tunnelsynth.NewCache()
	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: cache,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}

	// Pass 1: opted in → 1 CR.
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app-foo", Name: "svc"}})
	require.NoError(t, err)
	var dnsList v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Len(t, dnsList.Items, 1, "first reconcile should emit one CR")
	require.Equal(t, "Service", dnsList.Items[0].Labels[conventions.LabelSourceKind])

	// Mutate: remove cloudflare.io/tunnel → !enabled branch.
	var got corev1.Service
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app-foo", Name: "svc"}, &got))
	delete(got.Annotations, conventions.AnnotationTunnel)
	require.NoError(t, c.Update(context.Background(), &got))

	// Pass 2: !enabled → expect CR deleted.
	_, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app-foo", Name: "svc"}})
	require.NoError(t, err)
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Empty(t, dnsList.Items, "opt-out prune should delete the previously-emitted CR")
}

// Verify the reconciler satisfies the controller-runtime Reconciler interface.
var _ reconcile.Reconciler = (*ServiceSourceReconciler)(nil)
