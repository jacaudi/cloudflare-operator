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

package zone

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// ──────────────────────────────────────────────────────────────────────────────
// CloudflareZone – force-reconcile
// ──────────────────────────────────────────────────────────────────────────────

// TestReconcile_ForceReconcile_Zone_BypassesNoDriftShortCircuit seeds a zone
// that is already active on Cloudflare (no real drift) and sets the
// cloudflare.io/reconcile-at annotation without an ack. Expects:
//   - Reconcile succeeds.
//   - status.lastReconcileToken is set to the annotation value after reconcile
//     (the ack is written).
func TestReconcile_ForceReconcile_Zone_BypassesNoDriftShortCircuit(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	m := mock.New()
	created, _ := m.Zone.CreateZone(context.Background(), "acct-1", cloudflare.ZoneParams{Name: "example.com", Type: "full"})

	z := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "example",
			Namespace:   "default",
			Finalizers:  []string{conventions.FinalizerName},
			Annotations: map[string]string{conventions.AnnotationReconcileAt: "tkn-1"},
		},
		Spec:   v2alpha1.CloudflareZoneSpec{Name: "example.com", Type: "full", DeletionPolicy: v2alpha1.DeletionPolicyRetain},
		Status: v2alpha1.CloudflareZoneStatus{ZoneID: created.ID, Status: "active"},
	}

	s := zoneTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(z).WithStatusSubresource(&v2alpha1.CloudflareZone{}).Build()
	r := &CloudflareZoneReconciler{
		Client: c, Scheme: s,
		ZoneClientFn: func(_ cloudflare.Credentials) (cloudflare.ZoneClient, error) { return m.Zone, nil },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "example", Namespace: "default"}})
	require.NoError(t, err)

	var got v2alpha1.CloudflareZone
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "example", Namespace: "default"}, &got))
	require.Equal(t, "tkn-1", got.Status.LastReconcileToken, "ack must be written after successful force-reconcile")
	// Verify the CF GetZone was called (zone controller always fetches).
	require.Positive(t, m.Calls("Zone.GetZone"), "GetZone must have been called")
}

// TestReconcile_ForceReconcile_Zone_AlreadyAcked_NoEffect seeds the same
// no-drift scenario but with the ack already matching the annotation.
// Expects the ack stays equal (no spurious re-write beyond what is normal).
func TestReconcile_ForceReconcile_Zone_AlreadyAcked_NoEffect(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	m := mock.New()
	created, _ := m.Zone.CreateZone(context.Background(), "acct-1", cloudflare.ZoneParams{Name: "example.com", Type: "full"})

	z := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "example",
			Namespace:   "default",
			Finalizers:  []string{conventions.FinalizerName},
			Annotations: map[string]string{conventions.AnnotationReconcileAt: "tkn-1"},
		},
		Spec: v2alpha1.CloudflareZoneSpec{Name: "example.com", Type: "full", DeletionPolicy: v2alpha1.DeletionPolicyRetain},
		Status: v2alpha1.CloudflareZoneStatus{
			ZoneID: created.ID, Status: "active",
			LastReconcileToken: "tkn-1", // already acked
		},
	}

	s := zoneTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(z).WithStatusSubresource(&v2alpha1.CloudflareZone{}).Build()
	r := &CloudflareZoneReconciler{
		Client: c, Scheme: s,
		ZoneClientFn: func(_ cloudflare.Credentials) (cloudflare.ZoneClient, error) { return m.Zone, nil },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "example", Namespace: "default"}})
	require.NoError(t, err)

	// Already-acked: the token value must remain unchanged.
	var got v2alpha1.CloudflareZone
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "example", Namespace: "default"}, &got))
	require.Equal(t, "tkn-1", got.Status.LastReconcileToken, "already-acked token must remain equal")
	// Zone has no fast-skip; call count is not a useful signal here.
}

// ──────────────────────────────────────────────────────────────────────────────
// CloudflareZoneConfig – force-reconcile
// ──────────────────────────────────────────────────────────────────────────────

// TestReconcile_ForceReconcile_ZoneConfig_BypassesNoDriftShortCircuit seeds a
// ZoneConfig that is already applied (AppliedSpecHash matches, Phase=Ready)
// so the fast-skip would normally fire. Adds the annotation (without ack) only
// after the object is in the Ready / fast-skip state. Expects:
//   - UpdateSetting is still called (fast-skip bypassed).
//   - status.lastReconcileToken == "tkn-1".
func TestReconcile_ForceReconcile_ZoneConfig_BypassesNoDriftShortCircuit(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	sslMode := "strict"
	// No annotation during the initial converge — added below after Ready.
	cfg := &v2alpha1.CloudflareZoneConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cfg",
			Namespace: "default",
		},
		Spec: v2alpha1.CloudflareZoneConfigSpec{
			ZoneID: "z1",
			SSL:    &v2alpha1.SSLSettings{Mode: &sslMode},
		},
	}
	s := zoneTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cfg).WithStatusSubresource(&v2alpha1.CloudflareZoneConfig{}).Build()
	m := mock.New()
	r := &CloudflareZoneConfigReconciler{
		Client: c, Scheme: s,
		ZoneConfigClientFn: func(_ cloudflare.Credentials) (cloudflare.ZoneConfigClient, error) { return m.ZoneConfig, nil },
	}

	// Converge to Ready so the fast-skip path is armed.
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cfg", Namespace: "default"}})
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cfg", Namespace: "default"}})
	require.NoError(t, err)

	var gotAfterConverge v2alpha1.CloudflareZoneConfig
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "cfg", Namespace: "default"}, &gotAfterConverge))
	require.NotEmpty(t, gotAfterConverge.Status.AppliedSpecHash, "must be Ready after converge")
	require.Equal(t, v2alpha1.PhaseReady, gotAfterConverge.Status.Phase)

	// Now add the force-reconcile annotation (ack is still "" — never set).
	// This simulates an admin running: kubectl annotate ... cloudflare.io/reconcile-at=tkn-1
	gotAfterConverge.Annotations = map[string]string{conventions.AnnotationReconcileAt: "tkn-1"}
	require.NoError(t, c.Update(context.Background(), &gotAfterConverge))

	callsBefore := m.Calls("ZoneConfig.UpdateSetting")

	// Force-reconcile: ack="" ≠ annotation="tkn-1" → fast-skip bypassed.
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cfg", Namespace: "default"}})
	require.NoError(t, err)

	callsAfter := m.Calls("ZoneConfig.UpdateSetting")
	require.Greater(t, callsAfter, callsBefore, "force-reconcile must bypass fast-skip and call UpdateSetting")

	var got v2alpha1.CloudflareZoneConfig
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "cfg", Namespace: "default"}, &got))
	require.Equal(t, "tkn-1", got.Status.LastReconcileToken, "ack must be written after force-reconcile")
}

// TestReconcile_ForceReconcile_ZoneConfig_AlreadyAcked_NoEffect seeds the same
// fast-skip scenario but with the ack already matching. Expects fast-skip fires
// (no UpdateSetting calls added).
func TestReconcile_ForceReconcile_ZoneConfig_AlreadyAcked_NoEffect(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	sslMode := "strict"
	cfg := &v2alpha1.CloudflareZoneConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "cfg",
			Namespace:   "default",
			Annotations: map[string]string{conventions.AnnotationReconcileAt: "tkn-1"},
		},
		Spec: v2alpha1.CloudflareZoneConfigSpec{
			ZoneID: "z1",
			SSL:    &v2alpha1.SSLSettings{Mode: &sslMode},
		},
	}
	s := zoneTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cfg).WithStatusSubresource(&v2alpha1.CloudflareZoneConfig{}).Build()
	m := mock.New()
	r := &CloudflareZoneConfigReconciler{
		Client: c, Scheme: s,
		ZoneConfigClientFn: func(_ cloudflare.Credentials) (cloudflare.ZoneConfigClient, error) { return m.ZoneConfig, nil },
	}

	// Converge to Ready.
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cfg", Namespace: "default"}})
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cfg", Namespace: "default"}})
	require.NoError(t, err)

	// Patch the status to include the ack (simulate already-acked state).
	var live v2alpha1.CloudflareZoneConfig
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "cfg", Namespace: "default"}, &live))
	live.Status.LastReconcileToken = "tkn-1"
	require.NoError(t, c.Status().Update(context.Background(), &live))

	// Already-acked: fast-skip must fire (no new UpdateSetting calls).
	callsBefore := m.Calls("ZoneConfig.UpdateSetting")
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cfg", Namespace: "default"}})
	require.NoError(t, err)
	callsAfter := m.Calls("ZoneConfig.UpdateSetting")

	require.Equal(t, callsBefore, callsAfter, "already-acked fast-skip must produce no new UpdateSetting calls")

	var got v2alpha1.CloudflareZoneConfig
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "cfg", Namespace: "default"}, &got))
	require.Equal(t, "tkn-1", got.Status.LastReconcileToken)
}

// ──────────────────────────────────────────────────────────────────────────────
// CloudflareDNSRecord – force-reconcile
// ──────────────────────────────────────────────────────────────────────────────

// TestReconcile_ForceReconcile_DNSRecord_BypassesNoDriftShortCircuit seeds a
// DNS record that is already in sync (existing record content == desired) and
// sets the annotation without an ack. The DNSRecord reconciler has no global
// early-exit in managed mode, so the test verifies the ack is written after a
// successful reconcile.
func TestReconcile_ForceReconcile_DNSRecord_BypassesNoDriftShortCircuit(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	content := "192.0.2.1"
	m := mock.New()
	// Pre-create the record on the mock so the reconciler finds it and is in sync.
	existing, _ := m.DNS.CreateRecord(context.Background(), "z1", cloudflare.DNSRecordParams{
		Name:    "app.example.com",
		Type:    "A",
		Content: content,
	})

	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "rec",
			Namespace:   "default",
			Finalizers:  []string{conventions.FinalizerName},
			Annotations: map[string]string{conventions.AnnotationReconcileAt: "tkn-1"},
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    "app.example.com",
			Type:    "A",
			Content: &content,
			ZoneID:  "z1",
		},
		Status: v2alpha1.CloudflareDNSRecordStatus{
			RecordID:       existing.ID,
			CurrentContent: content,
		},
	}

	s := zoneTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).Build()
	r := newDNSReconciler(t, c, s, m)

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	require.Equal(t, "tkn-1", got.Status.LastReconcileToken, "ack must be written after successful force-reconcile")
	require.Positive(t, m.Calls("DNS.GetRecord"), "GetRecord must have been called (full reconcile)")
}

// TestReconcile_ForceReconcile_DNSRecord_AlreadyAcked_NoEffect seeds the same
// no-drift scenario but with the ack already matching the annotation token.
// Expects reconcile succeeds and the ack remains stable.
func TestReconcile_ForceReconcile_DNSRecord_AlreadyAcked_NoEffect(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	content := "192.0.2.1"
	m := mock.New()
	existing, _ := m.DNS.CreateRecord(context.Background(), "z1", cloudflare.DNSRecordParams{
		Name:    "app.example.com",
		Type:    "A",
		Content: content,
	})

	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "rec",
			Namespace:   "default",
			Finalizers:  []string{conventions.FinalizerName},
			Annotations: map[string]string{conventions.AnnotationReconcileAt: "tkn-1"},
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    "app.example.com",
			Type:    "A",
			Content: &content,
			ZoneID:  "z1",
		},
		Status: v2alpha1.CloudflareDNSRecordStatus{
			RecordID:           existing.ID,
			CurrentContent:     content,
			LastReconcileToken: "tkn-1", // already acked
		},
	}

	s := zoneTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).Build()
	r := newDNSReconciler(t, c, s, m)

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	require.Equal(t, "tkn-1", got.Status.LastReconcileToken, "already-acked token must remain equal")
}

// ──────────────────────────────────────────────────────────────────────────────
// CloudflareRuleset – force-reconcile
// ──────────────────────────────────────────────────────────────────────────────

// TestReconcile_ForceReconcile_Ruleset_BypassesNoDriftShortCircuit seeds a
// ruleset that matches the desired spec on Cloudflare (rulesetMatches=true)
// and sets the annotation without an ack. Expects:
//   - UpsertPhaseEntrypoint is called even though there is no drift.
//   - status.lastReconcileToken == "tkn-1".
func TestReconcile_ForceReconcile_Ruleset_BypassesNoDriftShortCircuit(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	s := zoneTestScheme(t)
	rs := &v2alpha1.CloudflareRuleset{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "waf",
			Namespace:   "default",
			Finalizers:  []string{conventions.FinalizerName},
			Annotations: map[string]string{conventions.AnnotationReconcileAt: "tkn-1"},
		},
		Spec: v2alpha1.CloudflareRulesetSpec{
			ZoneID: "z1", Name: "waf", Phase: "http_request_firewall_custom",
			Rules: []v2alpha1.RulesetRuleSpec{
				{Action: "block", Expression: `(ip.src eq 192.0.2.4)`},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rs).WithStatusSubresource(&v2alpha1.CloudflareRuleset{}).Build()
	m := mock.New()
	// Pre-seed the mock so the entrypoint already matches spec (no drift).
	_, _ = m.Ruleset.UpsertPhaseEntrypoint(context.Background(), "z1", "http_request_firewall_custom",
		cloudflare.RulesetParams{
			Name: "waf", Phase: "http_request_firewall_custom",
			Rules: []cloudflare.RulesetRule{
				{Action: "block", Expression: `(ip.src eq 192.0.2.4)`, Enabled: true},
			},
		},
	)
	callsBefore := m.Calls("Ruleset.UpsertPhaseEntrypoint")

	r := &CloudflareRulesetReconciler{
		Client: c, Scheme: s,
		RulesetClientFn: func(_ cloudflare.Credentials) (cloudflare.RulesetClient, error) { return m.Ruleset, nil },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "waf", Namespace: "default"}})
	require.NoError(t, err)

	callsAfter := m.Calls("Ruleset.UpsertPhaseEntrypoint")
	require.Greater(t, callsAfter, callsBefore, "force-reconcile must bypass rulesetMatches short-circuit and call Upsert")

	var got v2alpha1.CloudflareRuleset
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "waf", Namespace: "default"}, &got))
	require.Equal(t, "tkn-1", got.Status.LastReconcileToken, "ack must be written after force-reconcile")
}

// TestReconcile_ForceReconcile_Ruleset_AlreadyAcked_NoEffect seeds the same
// no-drift scenario with the ack already matching. Expects the rulesetMatches
// branch fires (no Upsert calls added) and the ack remains stable.
func TestReconcile_ForceReconcile_Ruleset_AlreadyAcked_NoEffect(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	s := zoneTestScheme(t)
	rs := &v2alpha1.CloudflareRuleset{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "waf",
			Namespace:   "default",
			Finalizers:  []string{conventions.FinalizerName},
			Annotations: map[string]string{conventions.AnnotationReconcileAt: "tkn-1"},
		},
		Spec: v2alpha1.CloudflareRulesetSpec{
			ZoneID: "z1", Name: "waf", Phase: "http_request_firewall_custom",
			Rules: []v2alpha1.RulesetRuleSpec{
				{Action: "block", Expression: `(ip.src eq 192.0.2.4)`},
			},
		},
		Status: v2alpha1.CloudflareRulesetStatus{
			LastReconcileToken: "tkn-1", // already acked
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rs).WithStatusSubresource(&v2alpha1.CloudflareRuleset{}).Build()
	m := mock.New()
	// Pre-seed a matching entrypoint.
	_, _ = m.Ruleset.UpsertPhaseEntrypoint(context.Background(), "z1", "http_request_firewall_custom",
		cloudflare.RulesetParams{
			Name: "waf", Phase: "http_request_firewall_custom",
			Rules: []cloudflare.RulesetRule{
				{Action: "block", Expression: `(ip.src eq 192.0.2.4)`, Enabled: true},
			},
		},
	)
	callsBefore := m.Calls("Ruleset.UpsertPhaseEntrypoint")

	r := &CloudflareRulesetReconciler{
		Client: c, Scheme: s,
		RulesetClientFn: func(_ cloudflare.Credentials) (cloudflare.RulesetClient, error) { return m.Ruleset, nil },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "waf", Namespace: "default"}})
	require.NoError(t, err)

	callsAfter := m.Calls("Ruleset.UpsertPhaseEntrypoint")
	require.Equal(t, callsBefore, callsAfter, "already-acked must not trigger Upsert when ruleset matches")

	var got v2alpha1.CloudflareRuleset
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "waf", Namespace: "default"}, &got))
	require.Equal(t, "tkn-1", got.Status.LastReconcileToken)
}
