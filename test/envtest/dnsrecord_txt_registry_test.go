/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package envtest_test

// TXT registry + observe-mode end-to-end acceptance tests (design §6.3).
//
// Each test function spins up its own isolated manager with a fresh mock.Mock
// — mirroring zone_test.go exactly. Credential resolution falls back to env
// vars (CLOUDFLARE_API_TOKEN / CLOUDFLARE_ACCOUNT_ID) set via t.Setenv. Each
// test scaffolds:
//
//   - A CloudflareZone CR (+ mock-backed zone reconciler) to obtain a real
//     zoneID via Status.ZoneID — the DNSRecord CRD requires zoneID or zoneRef
//     (CEL), so we supply the literal ID captured from the zone status.
//   - A CloudflareOperator singleton ("cluster") consumed by the DNSRecord
//     reconciler to resolve the optional TXT-registry key Secret ref.
//
// Call-count assertions use m.Calls("DNS.CreateRecord") etc. from mock.Mock.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
	"github.com/jacaudi/cloudflare-operator/internal/controller/zone"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/ipresolver"
)

// txtTestSlug converts a test name to a controller-name-safe slug.
// The controller-runtime metrics registry is process-global and rejects
// duplicates; each top-level test needs its own slot — mirroring the pattern
// from service_source_envtest_test.go::sanitizeTestName.
func txtTestSlug(name string) string {
	out := strings.ToLower(name)
	out = strings.ReplaceAll(out, "/", "-")
	out = strings.ReplaceAll(out, "_", "-")
	return out
}

// newTxtRegistryHarness builds an isolated manager with all four zone-bundle
// reconcilers wired to a fresh mock.Mock. Each reconciler is Named with a
// per-test slug so the process-global metrics registry doesn't reject
// duplicates when multiple test functions run in the same process. The manager
// is started in the background; t.Cleanup cancels it. Mirrors the pattern from
// zone_test.go's TestZoneBundle_EnvtestAcceptance and the tunnel source tests.
func newTxtRegistryHarness(t *testing.T) (context.Context, *mock.Mock, client.Client) {
	t.Helper()
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-txt")

	slug := txtTestSlug(t.Name())

	sch := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(sch))
	utilruntime.Must(v1alpha1.AddToScheme(sch))

	mgr, err := ctrl.NewManager(sharedConfig, ctrl.Options{
		Scheme:  sch,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)

	m := mock.New()

	// Zone reconciler: required so the DNSRecord reconciler's ResolveZoneID
	// can look up Status.ZoneID on a CloudflareZone CR.
	zoneR := &zone.CloudflareZoneReconciler{
		Client: mgr.GetClient(),
		Scheme: sch,
		ZoneClientFn: func(_ cloudflare.Credentials) (cloudflare.ZoneClient, error) {
			return m.Zone, nil
		},
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		Named("cloudflarezone-"+slug).
		For(&v1alpha1.CloudflareZone{}).
		Complete(zoneR))

	// ZoneConfig reconciler: wired but not exercised in these tests.
	zcR := &zone.CloudflareZoneConfigReconciler{
		Client: mgr.GetClient(),
		Scheme: sch,
		ZoneConfigClientFn: func(_ cloudflare.Credentials) (cloudflare.ZoneConfigClient, error) {
			return m.ZoneConfig, nil
		},
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		Named("cloudflarezoneconfig-"+slug).
		For(&v1alpha1.CloudflareZoneConfig{}).
		Complete(zcR))

	// DNS reconciler: the CUT for all TXT registry scenarios.
	dnsR := &zone.CloudflareDNSRecordReconciler{
		Client: mgr.GetClient(),
		Scheme: sch,
		DNSClientFn: func(_ cloudflare.Credentials) (cloudflare.DNSClient, error) {
			return m.DNS, nil
		},
		IPResolver: ipresolver.NewResolver(),
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		Named("cloudflarednsrecord-"+slug).
		For(&v1alpha1.CloudflareDNSRecord{}).
		Complete(dnsR))

	// Ruleset reconciler: wired for schema completeness.
	rsR := &zone.CloudflareRulesetReconciler{
		Client: mgr.GetClient(),
		Scheme: sch,
		RulesetClientFn: func(_ cloudflare.Credentials) (cloudflare.RulesetClient, error) {
			return m.Ruleset, nil
		},
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		Named("cloudflareruleset-"+slug).
		For(&v1alpha1.CloudflareRuleset{}).
		Complete(rsR))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() { _ = mgr.Start(ctx) }()

	syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Second)
	defer syncCancel()
	require.True(t, mgr.GetCache().WaitForCacheSync(syncCtx), "manager cache failed to sync")

	return ctx, m, mgr.GetClient()
}

// scaffoldZoneMgr creates a CloudflareZone CR and waits for the zone reconciler
// to populate Status.ZoneID. Returns the zone ID so callers can wire
// CloudflareDNSRecord.Spec.ZoneID directly. Uses the test's context and client.
func scaffoldZoneMgr(t *testing.T, ctx context.Context, c client.Client, crName, namespace string) string {
	t.Helper()
	z := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: namespace},
		Spec: v1alpha1.CloudflareZoneSpec{
			Name:           crName + ".example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, c.Create(ctx, z))
	t.Cleanup(func() { _ = c.Delete(context.Background(), z) })

	var zoneID string
	require.Eventually(t, func() bool {
		var got v1alpha1.CloudflareZone
		if err := c.Get(ctx, types.NamespacedName{Name: crName, Namespace: namespace}, &got); err != nil {
			return false
		}
		if got.Status.ZoneID == "" {
			return false
		}
		zoneID = got.Status.ZoneID
		return true
	}, 15*time.Second, 200*time.Millisecond, "CloudflareZone Status.ZoneID populated")

	return zoneID
}

// scaffoldOperatorSingleton creates the CloudflareOperator "cluster" CR. Calls
// setupSingleton (from helpers_test.go) first to evict any leftover CR from a
// prior test. keyRef may be nil (plaintext mode).
func scaffoldOperatorSingleton(t *testing.T, ctx context.Context, c client.Client, keyRef *v1alpha1.SecretReference) {
	t.Helper()
	setupSingleton(t)

	op := &v1alpha1.CloudflareOperator{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.CloudflareOperatorSingletonName},
		Spec: v1alpha1.CloudflareOperatorSpec{
			Cloudflare: v1alpha1.CloudflareCredentialRef{
				TokenSecretRef:          v1alpha1.SecretReference{Name: "cf-token", Namespace: "default", Key: "token"},
				AccountID:               "acct-txt",
				TxtRegistryKeySecretRef: keyRef,
			},
			Controllers: v1alpha1.ControllersSpec{
				Zone: v1alpha1.ControllerSpec{Enabled: false},
			},
		},
	}
	require.NoError(t, c.Create(ctx, op))
	t.Cleanup(func() {
		_ = c.Delete(context.Background(), op)
		waitFor(t, 10*time.Second, func() bool {
			var got v1alpha1.CloudflareOperator
			err := sharedClient.Get(context.Background(),
				types.NamespacedName{Name: v1alpha1.CloudflareOperatorSingletonName}, &got)
			return err != nil
		})
	})
}

// dnsRecordReadyReason reads the Ready condition Reason from the apiserver.
// Returns "" when the CR cannot be fetched or has no Ready condition yet.
func dnsRecordReadyReason(ctx context.Context, c client.Client, name, namespace string) string {
	var got v1alpha1.CloudflareDNSRecord
	if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &got); err != nil {
		return ""
	}
	for _, cond := range got.Status.Conditions {
		if cond.Type == conventions.ConditionTypeReady {
			return cond.Reason
		}
	}
	return ""
}

// --- Scenario 1: Full adopt cycle with §5.4 migration ---

// TestEnvtest_TxtRegistry_FullAdoptCycle exercises the three-phase adopt
// path described in design §5.4:
//
//  1. Pre-seeded record with NO TXT → Adopt:true refuses (AdoptRefusedNoTXT);
//     assert RecordID empty, no extra CreateRecord calls.
//  2. Switch to Observe mode → reads CF state, zero mutations.
//  3. Write TXT companion externally (§5.4 migration), switch back to
//     Managed+Adopt:true → adoption succeeds (RecordID + TxtRecordID set, Ready).
func TestEnvtest_TxtRegistry_FullAdoptCycle(t *testing.T) {
	ctx, m, c := newTxtRegistryHarness(t)

	zoneID := scaffoldZoneMgr(t, ctx, c, "txtr-full-adopt", "default")
	scaffoldOperatorSingleton(t, ctx, c, nil)

	// Pre-seed the main record with no TXT companion.
	mainContent := "192.0.2.50"
	_, err := m.DNS.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
		Name: "migrate.txtr.example.com", Type: "A", Content: mainContent, TTL: 1,
	})
	require.NoError(t, err)
	createsBefore := m.Calls("DNS.CreateRecord") // snapshot after seeding

	// Phase 1: Adopt:true with no TXT → AdoptRefusedNoTXT.
	content := "192.0.2.50"
	rec := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "txtr-full-adopt-rec", Namespace: "default"},
		Spec: v1alpha1.CloudflareDNSRecordSpec{
			Name:    "migrate.txtr.example.com",
			Type:    "A",
			Content: &content,
			ZoneID:  zoneID,
			Adopt:   true,
			Mode:    v1alpha1.RecordModeManaged,
		},
	}
	require.NoError(t, c.Create(ctx, rec))
	t.Cleanup(func() { _ = c.Delete(context.Background(), rec) })

	require.Eventually(t, func() bool {
		return dnsRecordReadyReason(ctx, c, "txtr-full-adopt-rec", "default") == conventions.ReasonAdoptRefusedNoTXT
	}, 15*time.Second, 200*time.Millisecond, "Phase 1: AdoptRefusedNoTXT condition set")

	var gotRec v1alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "txtr-full-adopt-rec", Namespace: "default"}, &gotRec))
	require.Empty(t, gotRec.Status.RecordID, "RecordID must be empty when adoption refused (no TXT)")
	require.Equal(t, createsBefore, m.Calls("DNS.CreateRecord"),
		"reconciler must not create records when adoption refused")

	// Phase 2: Switch to Observe mode → reads state, zero mutations.
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "txtr-full-adopt-rec", Namespace: "default"}, &gotRec))
	gotRec.Spec.Mode = v1alpha1.RecordModeObserve
	require.NoError(t, c.Update(ctx, &gotRec))

	createsBefore2 := m.Calls("DNS.CreateRecord")
	updatesBefore2 := m.Calls("DNS.UpdateRecord")
	deletesBefore2 := m.Calls("DNS.DeleteRecord")

	require.Eventually(t, func() bool {
		return dnsRecordReadyReason(ctx, c, "txtr-full-adopt-rec", "default") == conventions.ReasonObserving
	}, 15*time.Second, 200*time.Millisecond, "Phase 2: Observing condition set")

	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "txtr-full-adopt-rec", Namespace: "default"}, &gotRec))
	require.Equal(t, mainContent, gotRec.Status.CurrentContent, "Observe: CurrentContent must reflect CF record")
	require.Equal(t, createsBefore2, m.Calls("DNS.CreateRecord"), "Observe: no creates")
	require.Equal(t, updatesBefore2, m.Calls("DNS.UpdateRecord"), "Observe: no updates")
	require.Equal(t, deletesBefore2, m.Calls("DNS.DeleteRecord"), "Observe: no deletes")

	// Phase 3: §5.4 migration — write the TXT companion externally.
	txtName := cloudflare.AffixName("cf-txt", "migrate.txtr.example.com")
	payload := cloudflare.RegistryPayload{V: 1, K: "CloudflareDNSRecord", NS: "default", N: "txtr-full-adopt-rec"}
	plainContent, err := cloudflare.NewPlaintextCodec().Encode(payload)
	require.NoError(t, err)
	_, err = m.DNS.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
		Type: "TXT", Name: txtName, Content: plainContent, TTL: 1,
	})
	require.NoError(t, err)

	// Switch back to Managed+Adopt — adoption must succeed.
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "txtr-full-adopt-rec", Namespace: "default"}, &gotRec))
	gotRec.Spec.Mode = v1alpha1.RecordModeManaged
	require.NoError(t, c.Update(ctx, &gotRec))

	require.Eventually(t, func() bool {
		var r v1alpha1.CloudflareDNSRecord
		if err2 := c.Get(ctx, types.NamespacedName{Name: "txtr-full-adopt-rec", Namespace: "default"}, &r); err2 != nil {
			return false
		}
		return r.Status.RecordID != "" && r.Status.TxtRecordID != "" &&
			dnsRecordReadyReason(ctx, c, "txtr-full-adopt-rec", "default") == conventions.ReasonReady
	}, 20*time.Second, 200*time.Millisecond, "Phase 3: adoption succeeded, RecordID + TxtRecordID set, Ready")

	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "txtr-full-adopt-rec", Namespace: "default"}, &gotRec))
	require.NotEmpty(t, gotRec.Status.RecordID)
	require.NotEmpty(t, gotRec.Status.TxtRecordID)
}

// --- Scenario 2: Foreign adoption refused ---

// TestEnvtest_TxtRegistry_ForeignAdoptionRefused verifies that a TXT companion
// claiming a different owner causes Adopt:true to refuse with
// Reason=AdoptRefusedForeign and leaves Status.RecordID empty.
func TestEnvtest_TxtRegistry_ForeignAdoptionRefused(t *testing.T) {
	ctx, m, c := newTxtRegistryHarness(t)

	zoneID := scaffoldZoneMgr(t, ctx, c, "txtr-foreign", "default")
	scaffoldOperatorSingleton(t, ctx, c, nil)

	// Pre-seed a main record.
	content := "192.0.2.60"
	_, err := m.DNS.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
		Name: "foreign.txtr.example.com", Type: "A", Content: content, TTL: 1,
	})
	require.NoError(t, err)

	// Seed a TXT companion claiming a DIFFERENT owner.
	txtName := cloudflare.AffixName("cf-txt", "foreign.txtr.example.com")
	foreignPayload := cloudflare.RegistryPayload{V: 1, K: "CloudflareDNSRecord", NS: "other-ns", N: "other-rec"}
	foreignContent, err := cloudflare.NewPlaintextCodec().Encode(foreignPayload)
	require.NoError(t, err)
	_, err = m.DNS.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
		Type: "TXT", Name: txtName, Content: foreignContent, TTL: 1,
	})
	require.NoError(t, err)

	recContent := "192.0.2.60"
	rec := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "txtr-foreign-rec", Namespace: "default"},
		Spec: v1alpha1.CloudflareDNSRecordSpec{
			Name:    "foreign.txtr.example.com",
			Type:    "A",
			Content: &recContent,
			ZoneID:  zoneID,
			Adopt:   true,
			Mode:    v1alpha1.RecordModeManaged,
		},
	}
	require.NoError(t, c.Create(ctx, rec))
	t.Cleanup(func() { _ = c.Delete(context.Background(), rec) })

	require.Eventually(t, func() bool {
		return dnsRecordReadyReason(ctx, c, "txtr-foreign-rec", "default") == conventions.ReasonAdoptRefusedForeign
	}, 15*time.Second, 200*time.Millisecond, "AdoptRefusedForeign condition set")

	var gotRec v1alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "txtr-foreign-rec", Namespace: "default"}, &gotRec))
	require.Empty(t, gotRec.Status.RecordID, "RecordID must remain empty after foreign-adoption refusal")

	// Verify Ready=False.
	for _, cond := range gotRec.Status.Conditions {
		if cond.Type == conventions.ConditionTypeReady {
			require.Equal(t, metav1.ConditionFalse, cond.Status, "Ready must be False for AdoptRefusedForeign")
		}
	}
}

// --- Scenario 3: Observe mode happy path ---

// TestEnvtest_TxtRegistry_ObserveMode_HappyPath verifies that Observe mode
// reads CF state into Status.CurrentContent + Status.ObservedTXT and makes
// ZERO mutating Cloudflare calls (no CreateRecord, UpdateRecord, DeleteRecord).
func TestEnvtest_TxtRegistry_ObserveMode_HappyPath(t *testing.T) {
	ctx, m, c := newTxtRegistryHarness(t)

	zoneID := scaffoldZoneMgr(t, ctx, c, "txtr-observe-happy", "default")
	scaffoldOperatorSingleton(t, ctx, c, nil)

	// Pre-seed a main record + matching TXT companion.
	mainContent := "192.0.2.70"
	_, err := m.DNS.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
		Name: "obs.txtr.example.com", Type: "A", Content: mainContent, TTL: 1,
	})
	require.NoError(t, err)

	txtName := cloudflare.AffixName("cf-txt", "obs.txtr.example.com")
	ownerPayload := cloudflare.RegistryPayload{V: 1, K: "CloudflareDNSRecord", NS: "default", N: "txtr-observe-happy-rec"}
	ownerContent, err := cloudflare.NewPlaintextCodec().Encode(ownerPayload)
	require.NoError(t, err)
	_, err = m.DNS.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
		Type: "TXT", Name: txtName, Content: ownerContent, TTL: 1,
	})
	require.NoError(t, err)

	// Snapshot mutation counters BEFORE creating the CR.
	createsBefore := m.Calls("DNS.CreateRecord")
	updatesBefore := m.Calls("DNS.UpdateRecord")
	deletesBefore := m.Calls("DNS.DeleteRecord")

	recContent := "192.0.2.70"
	rec := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "txtr-observe-happy-rec", Namespace: "default"},
		Spec: v1alpha1.CloudflareDNSRecordSpec{
			Name:    "obs.txtr.example.com",
			Type:    "A",
			Content: &recContent,
			ZoneID:  zoneID,
			Mode:    v1alpha1.RecordModeObserve,
		},
	}
	require.NoError(t, c.Create(ctx, rec))
	t.Cleanup(func() { _ = c.Delete(context.Background(), rec) })

	require.Eventually(t, func() bool {
		return dnsRecordReadyReason(ctx, c, "txtr-observe-happy-rec", "default") == conventions.ReasonObserving
	}, 15*time.Second, 200*time.Millisecond, "Observing condition set")

	var gotRec v1alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "txtr-observe-happy-rec", Namespace: "default"}, &gotRec))

	// CurrentContent must reflect CF state.
	require.Equal(t, mainContent, gotRec.Status.CurrentContent, "Observe: CurrentContent must match CF record")

	// ObservedTXT must be populated and decoded correctly.
	require.NotNil(t, gotRec.Status.ObservedTXT, "Observe: ObservedTXT must be set")
	obs := gotRec.Status.ObservedTXT
	require.Equal(t, "default", obs.Namespace, "ObservedTXT.Namespace")
	require.Equal(t, "txtr-observe-happy-rec", obs.Name, "ObservedTXT.Name")
	require.Equal(t, "CloudflareDNSRecord", obs.Kind, "ObservedTXT.Kind")
	require.Equal(t, "plaintext", obs.Codec, "ObservedTXT.Codec")

	// Zero mutating calls since the CR was created.
	require.Equal(t, createsBefore, m.Calls("DNS.CreateRecord"), "Observe: no creates")
	require.Equal(t, updatesBefore, m.Calls("DNS.UpdateRecord"), "Observe: no updates")
	require.Equal(t, deletesBefore, m.Calls("DNS.DeleteRecord"), "Observe: no deletes")
}

// --- Scenario 4: Observe → Managed transition ---

// TestEnvtest_TxtRegistry_ObserveToManagedTransition verifies that a CR
// initially in Observe mode transitions to Managed (Adopt:true) and adopts the
// record once a matching TXT companion is present.
func TestEnvtest_TxtRegistry_ObserveToManagedTransition(t *testing.T) {
	ctx, m, c := newTxtRegistryHarness(t)

	zoneID := scaffoldZoneMgr(t, ctx, c, "txtr-obs-to-mgd", "default")
	scaffoldOperatorSingleton(t, ctx, c, nil)

	// Pre-seed main record + matching TXT.
	mainContent := "192.0.2.80"
	_, err := m.DNS.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
		Name: "transition.txtr.example.com", Type: "A", Content: mainContent, TTL: 1,
	})
	require.NoError(t, err)

	txtName := cloudflare.AffixName("cf-txt", "transition.txtr.example.com")
	ownerPayload := cloudflare.RegistryPayload{V: 1, K: "CloudflareDNSRecord", NS: "default", N: "txtr-obs-to-mgd-rec"}
	ownerContent, err := cloudflare.NewPlaintextCodec().Encode(ownerPayload)
	require.NoError(t, err)
	_, err = m.DNS.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
		Type: "TXT", Name: txtName, Content: ownerContent, TTL: 1,
	})
	require.NoError(t, err)

	recContent := "192.0.2.80"
	rec := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "txtr-obs-to-mgd-rec", Namespace: "default"},
		Spec: v1alpha1.CloudflareDNSRecordSpec{
			Name:    "transition.txtr.example.com",
			Type:    "A",
			Content: &recContent,
			ZoneID:  zoneID,
			Mode:    v1alpha1.RecordModeObserve,
			Adopt:   true, // no-op in Observe; takes effect after switch to Managed
		},
	}
	require.NoError(t, c.Create(ctx, rec))
	t.Cleanup(func() { _ = c.Delete(context.Background(), rec) })

	// Wait for Observe mode to be active.
	require.Eventually(t, func() bool {
		return dnsRecordReadyReason(ctx, c, "txtr-obs-to-mgd-rec", "default") == conventions.ReasonObserving
	}, 15*time.Second, 200*time.Millisecond, "Observing condition set")

	// Transition to Managed → should successfully adopt the record.
	var gotRec v1alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "txtr-obs-to-mgd-rec", Namespace: "default"}, &gotRec))
	gotRec.Spec.Mode = v1alpha1.RecordModeManaged
	require.NoError(t, c.Update(ctx, &gotRec))

	require.Eventually(t, func() bool {
		var r v1alpha1.CloudflareDNSRecord
		if err2 := c.Get(ctx, types.NamespacedName{Name: "txtr-obs-to-mgd-rec", Namespace: "default"}, &r); err2 != nil {
			return false
		}
		return r.Status.RecordID != "" &&
			dnsRecordReadyReason(ctx, c, "txtr-obs-to-mgd-rec", "default") == conventions.ReasonReady
	}, 20*time.Second, 200*time.Millisecond, "Managed adoption: RecordID set + Ready")

	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "txtr-obs-to-mgd-rec", Namespace: "default"}, &gotRec))
	require.NotEmpty(t, gotRec.Status.RecordID)
	require.NotEmpty(t, gotRec.Status.TxtRecordID)

	// Verify the mock never called DeleteRecord (safe transition).
	require.Equal(t, 0, m.Calls("DNS.DeleteRecord"), "Observe→Managed transition must not call DeleteRecord")
}

// --- Scenario 5: Encrypted roundtrip ---

// TestEnvtest_TxtRegistry_EncryptedRoundtrip verifies the AES-256-GCM
// encryption path end-to-end:
//
//  1. Create an AES key Secret + configure TxtRegistryKeySecretRef on the
//     CloudflareOperator singleton.
//  2. Create a Managed CloudflareDNSRecord (no pre-existing CF record) →
//     the reconciler creates the main record + an AES-encrypted TXT companion.
//  3. Assert the TXT content starts with "v1:" (AES envelope marker).
//  4. Assert that decoding with the same key succeeds and yields the correct
//     owner payload (NS, N, K fields).
func TestEnvtest_TxtRegistry_EncryptedRoundtrip(t *testing.T) {
	ctx, m, c := newTxtRegistryHarness(t)

	zoneID := scaffoldZoneMgr(t, ctx, c, "txtr-enc-roundtrip", "default")

	// Create the AES key Secret (32 bytes = AES-256).
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1) // deterministic, non-zero
	}
	keySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "txt-key-enc", Namespace: "default"},
		Data:       map[string][]byte{"key": key[:]},
	}
	require.NoError(t, c.Create(ctx, keySecret))
	t.Cleanup(func() { _ = c.Delete(context.Background(), keySecret) })

	scaffoldOperatorSingleton(t, ctx, c, &v1alpha1.SecretReference{
		Name:      "txt-key-enc",
		Namespace: "default",
		Key:       "key",
	})

	// Create a Managed DNSRecord (no pre-existing CF record) — reconciler
	// creates both the main A record and the AES-encrypted TXT companion.
	content := "192.0.2.90"
	rec := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "txtr-enc-roundtrip-rec", Namespace: "default"},
		Spec: v1alpha1.CloudflareDNSRecordSpec{
			Name:    "enc.txtr.example.com",
			Type:    "A",
			Content: &content,
			ZoneID:  zoneID,
			Mode:    v1alpha1.RecordModeManaged,
		},
	}
	require.NoError(t, c.Create(ctx, rec))
	t.Cleanup(func() { _ = c.Delete(context.Background(), rec) })

	// Wait for Ready — both records written.
	require.Eventually(t, func() bool {
		var r v1alpha1.CloudflareDNSRecord
		if err2 := c.Get(ctx, types.NamespacedName{Name: "txtr-enc-roundtrip-rec", Namespace: "default"}, &r); err2 != nil {
			return false
		}
		return r.Status.RecordID != "" && r.Status.TxtRecordID != "" &&
			dnsRecordReadyReason(ctx, c, "txtr-enc-roundtrip-rec", "default") == conventions.ReasonReady
	}, 20*time.Second, 200*time.Millisecond, "Ready: main record + encrypted TXT companion created")

	// Retrieve the TXT companion from the mock and verify AES encryption.
	txtName := cloudflare.AffixName("cf-txt", "enc.txtr.example.com")
	txtRecs, err := m.DNS.ListRecordsByNameAndType(ctx, zoneID, txtName, "TXT")
	require.NoError(t, err)
	require.Len(t, txtRecs, 1, "exactly one TXT companion expected")

	txtContent := txtRecs[0].Content
	require.True(t, len(txtContent) >= 3 && txtContent[:3] == "v1:",
		"TXT content must start with 'v1:' (AES envelope); got: %q", txtContent)

	// Decode with the same key and verify the owner payload.
	aesCodec := cloudflare.NewAESCodec(key)
	decoded, err := aesCodec.Decode(txtContent)
	require.NoError(t, err, "AES decode must succeed with the configured key")
	require.Equal(t, 1, decoded.V)
	require.Equal(t, "CloudflareDNSRecord", decoded.K)
	require.Equal(t, "default", decoded.NS)
	require.Equal(t, "txtr-enc-roundtrip-rec", decoded.N)
}

// --- Scenario 6: Key rotation refuses old records ---

// TestEnvtest_TxtRegistry_KeyRotation_RefusesOldRecords verifies the
// conservative key-rotation refusal (design §5): a TXT companion written with
// key A cannot be adopted by a reconciler using key B.
//
// Setup:
//  1. Pre-seed main record + AES-encrypted TXT companion (key A) directly in
//     the mock, simulating a record owned by a prior operator run with key A.
//  2. Configure the CloudflareOperator singleton with key B.
//  3. Create a CR with Adopt:true → the auto-detecting codec sees "v1:" but AES
//     decrypt with key B fails → TxtOwnershipUnrecognized → AdoptRefusedNoTXT.
func TestEnvtest_TxtRegistry_KeyRotation_RefusesOldRecords(t *testing.T) {
	ctx, m, c := newTxtRegistryHarness(t)

	zoneID := scaffoldZoneMgr(t, ctx, c, "txtr-key-rotation", "default")

	// Key A: used to write the existing TXT companion.
	var keyA [32]byte
	for i := range keyA {
		keyA[i] = byte(i + 10)
	}

	// Key B: the new operator key — different from A.
	var keyB [32]byte
	for i := range keyB {
		keyB[i] = byte(i + 20)
	}

	// Pre-seed main record.
	mainContent := "192.0.2.100"
	_, err := m.DNS.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
		Name: "rotated.txtr.example.com", Type: "A", Content: mainContent, TTL: 1,
	})
	require.NoError(t, err)

	// Pre-seed TXT companion encrypted with key A.
	aesCodecA := cloudflare.NewAESCodec(keyA)
	ownerPayload := cloudflare.RegistryPayload{
		V: 1, K: "CloudflareDNSRecord", NS: "default", N: "txtr-key-rotation-rec",
	}
	oldTxtContent, err := aesCodecA.Encode(ownerPayload)
	require.NoError(t, err)
	require.True(t, len(oldTxtContent) >= 3 && oldTxtContent[:3] == "v1:",
		"pre-seeded TXT must be encrypted; got: %q", oldTxtContent)

	txtName := cloudflare.AffixName("cf-txt", "rotated.txtr.example.com")
	_, err = m.DNS.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
		Type: "TXT", Name: txtName, Content: oldTxtContent, TTL: 1,
	})
	require.NoError(t, err)

	// Create key B Secret and configure the operator singleton with key B.
	keySecretB := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "txt-key-b", Namespace: "default"},
		Data:       map[string][]byte{"key": keyB[:]},
	}
	require.NoError(t, c.Create(ctx, keySecretB))
	t.Cleanup(func() { _ = c.Delete(context.Background(), keySecretB) })

	scaffoldOperatorSingleton(t, ctx, c, &v1alpha1.SecretReference{
		Name:      "txt-key-b",
		Namespace: "default",
		Key:       "key",
	})

	// Create a CR with Adopt:true — the reconciler uses key B. The auto-
	// detecting codec sniffs "v1:", attempts AES decrypt with key B → fails →
	// TxtOwnershipUnrecognized → AdoptRefusedNoTXT (conservative refusal).
	recContent := "192.0.2.100"
	rec := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "txtr-key-rotation-rec", Namespace: "default"},
		Spec: v1alpha1.CloudflareDNSRecordSpec{
			Name:    "rotated.txtr.example.com",
			Type:    "A",
			Content: &recContent,
			ZoneID:  zoneID,
			Adopt:   true,
			Mode:    v1alpha1.RecordModeManaged,
		},
	}
	require.NoError(t, c.Create(ctx, rec))
	t.Cleanup(func() { _ = c.Delete(context.Background(), rec) })

	require.Eventually(t, func() bool {
		return dnsRecordReadyReason(ctx, c, "txtr-key-rotation-rec", "default") == conventions.ReasonAdoptRefusedNoTXT
	}, 15*time.Second, 200*time.Millisecond, "AdoptRefusedNoTXT: old key-A TXT not decodable with key B")

	var gotRec v1alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "txtr-key-rotation-rec", Namespace: "default"}, &gotRec))
	require.Empty(t, gotRec.Status.RecordID, "RecordID must remain empty after key-rotation refusal")

	// Confirm key B genuinely cannot decrypt key A's ciphertext (sanity).
	aesCodecB := cloudflare.NewAESCodec(keyB)
	_, decodeErr := aesCodecB.Decode(oldTxtContent)
	require.Error(t, decodeErr, "key B must not decode key A's ciphertext")
}
