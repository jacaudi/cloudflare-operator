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

// boolPtr is a tiny helper so test inputs can express *bool inline without
// declaring locals on every line.
func boolPtr(b bool) *bool { return &b }

func TestRuleset_CreatesEntrypoint(t *testing.T) {
	s := zoneTestScheme(t)
	rs := &v2alpha1.CloudflareRuleset{
		ObjectMeta: metav1.ObjectMeta{Name: "waf", Namespace: "default"},
		Spec: v2alpha1.CloudflareRulesetSpec{
			ZoneID: "z1", Name: "waf", Phase: "http_request_firewall_custom",
			Rules: []v2alpha1.RulesetRuleSpec{
				{Action: "block", Expression: `(ip.src eq 192.0.2.4)`},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rs).WithStatusSubresource(&v2alpha1.CloudflareRuleset{}).Build()
	m := mock.New()
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	r := &CloudflareRulesetReconciler{Client: c, Scheme: s,
		RulesetClientFn: func(_ cloudflare.Credentials) (cloudflare.RulesetClient, error) { return m.Ruleset, nil },
	}
	// Converge: finalizer-set requeue + create.
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "waf", Namespace: "default"}})
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "waf", Namespace: "default"}})
	require.NoError(t, err)

	got, err := m.Ruleset.GetPhaseEntrypoint(context.Background(), "z1", "http_request_firewall_custom")
	require.NoError(t, err)
	require.Len(t, got.Rules, 1)

	var stored v2alpha1.CloudflareRuleset
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "waf", Namespace: "default"}, &stored))
	require.Equal(t, got.ID, stored.Status.RulesetID)
	require.Equal(t, 1, stored.Status.RuleCount)
	conds := condMap(stored.Status.Conditions)
	require.Equal(t, metav1.ConditionTrue, conds[conventions.ConditionTypeReady])
}

func TestRuleset_ReplacesRulesOnSpecChange(t *testing.T) {
	s := zoneTestScheme(t)
	rs := &v2alpha1.CloudflareRuleset{
		ObjectMeta: metav1.ObjectMeta{Name: "waf", Namespace: "default", Finalizers: []string{conventions.FinalizerName}},
		Spec: v2alpha1.CloudflareRulesetSpec{
			ZoneID: "z1", Name: "waf", Phase: "http_request_firewall_custom",
			Rules: []v2alpha1.RulesetRuleSpec{
				{Action: "block", Expression: "a"},
				{Action: "log", Expression: "b"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rs).WithStatusSubresource(&v2alpha1.CloudflareRuleset{}).Build()
	m := mock.New()
	// Seed pre-existing single-rule entrypoint.
	_, _ = m.Ruleset.UpsertPhaseEntrypoint(context.Background(), "z1", "http_request_firewall_custom", cloudflare.RulesetParams{
		Name: "waf", Phase: "http_request_firewall_custom",
		Rules: []cloudflare.RulesetRule{{Action: "block", Expression: "a", Enabled: true}},
	})
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	r := &CloudflareRulesetReconciler{Client: c, Scheme: s,
		RulesetClientFn: func(_ cloudflare.Credentials) (cloudflare.RulesetClient, error) { return m.Ruleset, nil },
	}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "waf", Namespace: "default"}})
	require.NoError(t, err)
	got, _ := m.Ruleset.GetPhaseEntrypoint(context.Background(), "z1", "http_request_firewall_custom")
	require.Len(t, got.Rules, 2, "PUT-replaces-list semantics")
}

// TestRuleset_NoDriftLoopOnExplicitLoggingFalse pins the T10 normalization
// contract: setting Logging.Enabled=false must not cause a repeated update
// loop, because the cloudflare-go SDK can't distinguish that case from
// logging being absent.
func TestRuleset_NoDriftLoopOnExplicitLoggingFalse(t *testing.T) {
	s := zoneTestScheme(t)
	loggingDisabled := false
	rs := &v2alpha1.CloudflareRuleset{
		ObjectMeta: metav1.ObjectMeta{Name: "waf", Namespace: "default", Finalizers: []string{conventions.FinalizerName}},
		Spec: v2alpha1.CloudflareRulesetSpec{
			ZoneID: "z1", Name: "waf", Phase: "http_request_firewall_custom",
			Rules: []v2alpha1.RulesetRuleSpec{
				{
					Action: "block", Expression: `(ip.src eq 192.0.2.4)`,
					Logging: &v2alpha1.RuleLogging{Enabled: &loggingDisabled},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rs).WithStatusSubresource(&v2alpha1.CloudflareRuleset{}).Build()
	m := mock.New()
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	r := &CloudflareRulesetReconciler{Client: c, Scheme: s,
		RulesetClientFn: func(_ cloudflare.Credentials) (cloudflare.RulesetClient, error) { return m.Ruleset, nil },
	}
	// First reconcile creates the entrypoint.
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "waf", Namespace: "default"}})
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "waf", Namespace: "default"}})
	require.NoError(t, err)

	// Inject an error into Upsert. If the reconciler tries to update again,
	// the test fails because Reconcile should NOT detect drift on the second
	// pass (Logging.Enabled=false is normalized to nil on both spec-side and
	// observed-side, so they compare equal).
	calls := 0
	m.InjectError("Ruleset.UpsertPhaseEntrypoint", &countingErr{calls: &calls})
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "waf", Namespace: "default"}})
	require.NoError(t, err)
	require.Zero(t, calls, "Logging.Enabled=false must not cause a drift loop")
}

// rulesetMatches per-branch coverage.
func TestRulesetMatches_EmptyExisting(t *testing.T) {
	// existing nil is a "first write" case; the function is only invoked
	// when existing is non-nil. Cover empty rules in existing.
	existing := &cloudflare.Ruleset{Name: "waf"}
	desired := cloudflare.RulesetParams{Name: "waf"}
	require.True(t, rulesetMatches(existing, desired))
}

func TestRulesetMatches_EqualRules(t *testing.T) {
	existing := &cloudflare.Ruleset{Name: "waf", Rules: []cloudflare.RulesetRule{
		{Action: "block", Expression: "a", Enabled: true},
	}}
	desired := cloudflare.RulesetParams{Name: "waf", Rules: []cloudflare.RulesetRule{
		{Action: "block", Expression: "a", Enabled: true},
	}}
	require.True(t, rulesetMatches(existing, desired))
}

func TestRulesetMatches_DifferentRuleCount(t *testing.T) {
	existing := &cloudflare.Ruleset{Name: "waf", Rules: []cloudflare.RulesetRule{
		{Action: "block", Expression: "a", Enabled: true},
	}}
	desired := cloudflare.RulesetParams{Name: "waf", Rules: []cloudflare.RulesetRule{
		{Action: "block", Expression: "a", Enabled: true},
		{Action: "log", Expression: "b", Enabled: true},
	}}
	require.False(t, rulesetMatches(existing, desired))
}

func TestRulesetMatches_DifferentAction(t *testing.T) {
	existing := &cloudflare.Ruleset{Name: "waf", Rules: []cloudflare.RulesetRule{
		{Action: "block", Expression: "a", Enabled: true},
	}}
	desired := cloudflare.RulesetParams{Name: "waf", Rules: []cloudflare.RulesetRule{
		{Action: "log", Expression: "a", Enabled: true},
	}}
	require.False(t, rulesetMatches(existing, desired))
}

func TestRulesetMatches_DifferentExpression(t *testing.T) {
	existing := &cloudflare.Ruleset{Name: "waf", Rules: []cloudflare.RulesetRule{
		{Action: "block", Expression: "a", Enabled: true},
	}}
	desired := cloudflare.RulesetParams{Name: "waf", Rules: []cloudflare.RulesetRule{
		{Action: "block", Expression: "b", Enabled: true},
	}}
	require.False(t, rulesetMatches(existing, desired))
}

func TestRulesetMatches_DifferentEnabled(t *testing.T) {
	existing := &cloudflare.Ruleset{Name: "waf", Rules: []cloudflare.RulesetRule{
		{Action: "block", Expression: "a", Enabled: true},
	}}
	desired := cloudflare.RulesetParams{Name: "waf", Rules: []cloudflare.RulesetRule{
		{Action: "block", Expression: "a", Enabled: false},
	}}
	require.False(t, rulesetMatches(existing, desired))
}

// Both sides specify Logging.Enabled=false: after normalization both become
// nil and the rules compare equal — this is the T10-flagged contract.
func TestRulesetMatches_DifferentLoggingEnabledFalseNormalizes(t *testing.T) {
	existing := &cloudflare.Ruleset{Name: "waf", Rules: []cloudflare.RulesetRule{
		{Action: "block", Expression: "a", Enabled: true, Logging: nil},
	}}
	desired := cloudflare.RulesetParams{Name: "waf", Rules: []cloudflare.RulesetRule{
		{Action: "block", Expression: "a", Enabled: true, Logging: &cloudflare.RuleLogging{Enabled: boolPtr(false)}},
	}}
	require.True(t, rulesetMatches(existing, desired), "Logging.Enabled=false on desired must normalize to nil and match observed nil")
}

func TestRulesetMatches_NilVsNonNilLogging(t *testing.T) {
	existing := &cloudflare.Ruleset{Name: "waf", Rules: []cloudflare.RulesetRule{
		{Action: "block", Expression: "a", Enabled: true, Logging: nil},
	}}
	desired := cloudflare.RulesetParams{Name: "waf", Rules: []cloudflare.RulesetRule{
		{Action: "block", Expression: "a", Enabled: true, Logging: &cloudflare.RuleLogging{Enabled: boolPtr(true)}},
	}}
	require.False(t, rulesetMatches(existing, desired), "Logging.Enabled=true must NOT normalize to nil")
}
