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

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// aggTestScheme builds a scheme with v1alpha1, corev1, and appsv1 registered.
// testScheme (from cloudflarednsrecord_controller_test.go) only registers
// v1alpha1 and corev1; we extend it here with appsv1 for Deployment support.
func aggTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := cloudflarev1alpha1.AddToScheme(s); err != nil {
		panic("add v1alpha1 to scheme: " + err.Error())
	}
	if err := corev1.AddToScheme(s); err != nil {
		panic("add corev1 to scheme: " + err.Error())
	}
	if err := appsv1.AddToScheme(s); err != nil {
		panic("add appsv1 to scheme: " + err.Error())
	}
	return s
}

// ---- helpers ----------------------------------------------------------------

// buildAggFakeClient builds a fake client with the aggregation scheme
// (v1alpha1 + corev1 + appsv1) and registers status subresources for any
// CloudflareTunnel or CloudflareTunnelRule objects passed in.
func buildAggFakeClient(objs ...client.Object) client.Client {
	s := aggTestScheme()
	var statusObjs []client.Object
	for _, o := range objs {
		switch o.(type) {
		case *cloudflarev1alpha1.CloudflareTunnel, *cloudflarev1alpha1.CloudflareTunnelRule:
			statusObjs = append(statusObjs, o)
		}
	}
	return fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(statusObjs...).
		Build()
}

// newTunnelForAgg returns a CloudflareTunnel with TunnelCNAME set (i.e. already
// provisioned) and optionally connector enabled.
func newTunnelForAgg(name, ns string, connectorEnabled bool) *cloudflarev1alpha1.CloudflareTunnel {
	tun := &cloudflarev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  ns,
			UID:        types.UID(name + "-uid"),
			Generation: 1,
		},
		Spec: cloudflarev1alpha1.CloudflareTunnelSpec{
			Name:                name,
			SecretRef:           cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
			GeneratedSecretName: name + "-credentials",
		},
	}
	if connectorEnabled {
		tun.Spec.Connector = &cloudflarev1alpha1.ConnectorSpec{
			Enabled:  true,
			Replicas: 2,
		}
	}
	tun.Status.TunnelCNAME = "test-tunnel-id.cfargotunnel.com"
	tun.Status.TunnelID = "test-tunnel-id"
	return tun
}

// newRuleForTunnel returns a minimal CloudflareTunnelRule pointing at tunnelName
// in tunnelNs. If tunnelNs is empty the TunnelRef.Namespace is also empty (defaults
// to rule's own namespace at match time).
func newRuleForTunnel(ruleName, ruleNs, tunnelName, tunnelNs string) *cloudflarev1alpha1.CloudflareTunnelRule {
	u := strPtr("http://backend.svc:8080")
	return &cloudflarev1alpha1.CloudflareTunnelRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:       ruleName,
			Namespace:  ruleNs,
			Generation: 1,
		},
		Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
			TunnelRef: cloudflarev1alpha1.TunnelReference{
				Name:      tunnelName,
				Namespace: tunnelNs,
			},
			Hostnames: []string{"app.example.com"},
			Backend:   cloudflarev1alpha1.TunnelRuleBackend{URL: u},
			Priority:  100,
		},
	}
}

// ---- filterRulesForTunnel pure-function tests --------------------------------

// TestFilterRulesForTunnel_Basic verifies that only rules whose TunnelRef
// resolves to the target tunnel are returned.
func TestFilterRulesForTunnel_Basic(t *testing.T) {
	rules := []cloudflarev1alpha1.CloudflareTunnelRule{
		// explicit namespace match
		*newRuleForTunnel("r1", "apps", "home", "network"),
		// same name, different namespace — must NOT match
		*newRuleForTunnel("r2", "apps", "home", "other"),
		// empty namespace — defaults to rule's own namespace "apps" ≠ "network"
		*newRuleForTunnel("r3", "apps", "home", ""),
	}
	got := filterRulesForTunnel(rules, "home", "network")
	if len(got) != 1 {
		t.Fatalf("expected 1 match, got %d: %+v", len(got), got)
	}
	if got[0].Name != "r1" {
		t.Errorf("expected r1, got %q", got[0].Name)
	}
}

// TestFilterRulesForTunnel_EmptyNamespaceDefaultsToRuleNs verifies that a rule
// with TunnelRef.Namespace="" in namespace "network" matches tunnel "network/home".
func TestFilterRulesForTunnel_EmptyNamespaceDefaultsToRuleNs(t *testing.T) {
	rules := []cloudflarev1alpha1.CloudflareTunnelRule{
		*newRuleForTunnel("r1", "network", "home", ""), // empty → defaults to "network"
	}
	got := filterRulesForTunnel(rules, "home", "network")
	if len(got) != 1 {
		t.Fatalf("expected 1 match, got %d", len(got))
	}
}

// TestFilterRulesForTunnel_CrossNsNoMatch verifies that a rule in namespace
// "apps" with TunnelRef.Namespace="" does NOT match tunnel "network/home".
func TestFilterRulesForTunnel_CrossNsNoMatch(t *testing.T) {
	rules := []cloudflarev1alpha1.CloudflareTunnelRule{
		*newRuleForTunnel("r1", "apps", "home", ""), // empty → defaults to "apps" ≠ "network"
	}
	got := filterRulesForTunnel(rules, "home", "network")
	if len(got) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(got))
	}
}

// TestFilterRulesForTunnel_SameNameDifferentNs verifies two tunnels with the
// same name in different namespaces don't cross-match.
func TestFilterRulesForTunnel_SameNameDifferentNs(t *testing.T) {
	rules := []cloudflarev1alpha1.CloudflareTunnelRule{
		*newRuleForTunnel("r-a", "apps", "home", "network"),
		*newRuleForTunnel("r-b", "apps", "home", "staging"),
	}
	networkRules := filterRulesForTunnel(rules, "home", "network")
	stagingRules := filterRulesForTunnel(rules, "home", "staging")
	if len(networkRules) != 1 || networkRules[0].Name != "r-a" {
		t.Errorf("network: expected [r-a], got %+v", networkRules)
	}
	if len(stagingRules) != 1 || stagingRules[0].Name != "r-b" {
		t.Errorf("staging: expected [r-b], got %+v", stagingRules)
	}
}

// ---- ReconcileConnectorAndRules integration tests ---------------------------

// TestReconcileAggregation_HappyPath tests the nominal path: one rule, connector
// enabled, Deployment created, rule status TunnelAccepted=True, tunnel
// IngressConfigured=True, ConnectorReady=False (no ready replicas in fake).
func TestReconcileAggregation_HappyPath(t *testing.T) {
	tun := newTunnelForAgg("home", "network", true)
	rule := newRuleForTunnel("r1", "network", "home", "network")
	c := buildAggFakeClient(tun, rule)

	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("ReconcileConnectorAndRules: %v", err)
	}

	// --- Deployment created ---
	n := ConnectorNames(tun)
	var dep appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: n.Deployment}, &dep); err != nil {
		t.Fatalf("Deployment not created: %v", err)
	}

	// --- ConfigMap created ---
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: n.ConfigMap}, &cm); err != nil {
		t.Fatalf("ConfigMap not created: %v", err)
	}

	// --- Config-hash propagation: cm == dep pod template == agg hash ---
	cmHash := cm.Annotations[AnnotationConfigHash]
	depHash := dep.Spec.Template.Annotations[AnnotationConfigHash]
	if cmHash == "" {
		t.Error("ConfigMap config-hash annotation is empty")
	}
	if cmHash != depHash {
		t.Errorf("hash mismatch: cm=%q dep=%q", cmHash, depHash)
	}

	// --- Rule status: TunnelAccepted=True ---
	var updatedRule cloudflarev1alpha1.CloudflareTunnelRule
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: rule.Namespace, Name: rule.Name}, &updatedRule); err != nil {
		t.Fatalf("get rule: %v", err)
	}
	assertCondition(t, updatedRule.Status.Conditions, cloudflarev1alpha1.ConditionTypeTunnelAccepted, metav1.ConditionTrue)
	assertCondition(t, updatedRule.Status.Conditions, cloudflarev1alpha1.ConditionTypeValid, metav1.ConditionTrue)
	if len(updatedRule.Status.Conditions) != 3 {
		t.Errorf("expected 3 conditions on rule, got %d: %+v", len(updatedRule.Status.Conditions), updatedRule.Status.Conditions)
	}

	// --- Tunnel status: re-fetch to verify persistence ---
	var tunOut cloudflarev1alpha1.CloudflareTunnel
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: tun.Name}, &tunOut); err != nil {
		t.Fatalf("re-fetch tunnel: %v", err)
	}

	// --- Tunnel status: IngressConfigured=True ---
	assertCondition(t, tunOut.Status.Conditions, cloudflarev1alpha1.ConditionTypeIngressConfigured, metav1.ConditionTrue)

	// --- ConnectorReady=False (no ready replicas) ---
	assertCondition(t, tunOut.Status.Conditions, cloudflarev1alpha1.ConditionTypeConnectorReady, metav1.ConditionFalse)
}

// TestReconcileAggregation_EmptyRuleList verifies that zero rules still produces
// a valid ConfigMap and sets IngressConfigured=True.
func TestReconcileAggregation_EmptyRuleList(t *testing.T) {
	tun := newTunnelForAgg("home", "network", true)
	c := buildAggFakeClient(tun)

	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("ReconcileConnectorAndRules: %v", err)
	}

	n := ConnectorNames(tun)
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: n.ConfigMap}, &cm); err != nil {
		t.Fatalf("ConfigMap not created: %v", err)
	}

	var tunOut cloudflarev1alpha1.CloudflareTunnel
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: tun.Name}, &tunOut); err != nil {
		t.Fatalf("re-fetch tunnel: %v", err)
	}
	assertCondition(t, tunOut.Status.Conditions, cloudflarev1alpha1.ConditionTypeIngressConfigured, metav1.ConditionTrue)
}

// TestReconcileAggregation_ConnectorDisabled verifies that no Deployment or
// ConfigMap is created when connector.enabled=false, ConnectorReady condition
// is absent, and tun.Status.Connector is nil.
func TestReconcileAggregation_ConnectorDisabled(t *testing.T) {
	tun := newTunnelForAgg("home", "network", false)
	// Explicitly set connector spec to disabled.
	tun.Spec.Connector = &cloudflarev1alpha1.ConnectorSpec{Enabled: false}
	c := buildAggFakeClient(tun)

	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("ReconcileConnectorAndRules: %v", err)
	}

	n := ConnectorNames(tun)
	var dep appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: n.Deployment}, &dep); err == nil {
		t.Error("expected Deployment NOT to be created when connector disabled")
	}
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: n.ConfigMap}, &cm); err == nil {
		t.Error("expected ConfigMap NOT to be created when connector disabled")
	}

	// Re-fetch tunnel to verify persisted status.
	var tunOut cloudflarev1alpha1.CloudflareTunnel
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: tun.Name}, &tunOut); err != nil {
		t.Fatalf("re-fetch tunnel: %v", err)
	}

	// ConnectorReady condition must be absent (not just False).
	for _, cond := range tunOut.Status.Conditions {
		if cond.Type == cloudflarev1alpha1.ConditionTypeConnectorReady {
			t.Errorf("ConnectorReady condition must be absent when connector disabled, got: %+v", cond)
		}
	}

	// Connector sub-status must be nil.
	if tunOut.Status.Connector != nil {
		t.Errorf("tun.Status.Connector must be nil when connector disabled, got: %+v", tunOut.Status.Connector)
	}
}

// TestReconcileAggregation_ConnectorNilSpec verifies the nil-connector path
// (spec.connector == nil) behaves the same as connector disabled.
func TestReconcileAggregation_ConnectorNilSpec(t *testing.T) {
	tun := newTunnelForAgg("home", "network", false)
	tun.Spec.Connector = nil // ensure nil path
	c := buildAggFakeClient(tun)

	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("ReconcileConnectorAndRules: %v", err)
	}

	var tunOut cloudflarev1alpha1.CloudflareTunnel
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: tun.Name}, &tunOut); err != nil {
		t.Fatalf("re-fetch tunnel: %v", err)
	}
	for _, cond := range tunOut.Status.Conditions {
		if cond.Type == cloudflarev1alpha1.ConditionTypeConnectorReady {
			t.Errorf("ConnectorReady must be absent when spec.connector is nil")
		}
	}
	if tunOut.Status.Connector != nil {
		t.Errorf("tun.Status.Connector must be nil when spec.connector is nil")
	}
}

// TestReconcileAggregation_DeploymentAdoptionRefusal verifies that a Deployment
// pre-existing with different OwnerReferences causes ErrUnownedDeployment.
func TestReconcileAggregation_DeploymentAdoptionRefusal(t *testing.T) {
	tun := newTunnelForAgg("home", "network", true)
	n := ConnectorNames(tun)

	// Pre-create a Deployment with no owner (not owned by this tunnel).
	preExisting := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      n.Deployment,
			Namespace: tun.Namespace,
			// No OwnerReferences — not owned by this tunnel.
		},
	}
	c := buildAggFakeClient(tun, preExisting)

	err := ReconcileConnectorAndRules(context.Background(), c, tun, nil)
	if err == nil {
		t.Fatal("expected error for unowned Deployment, got nil")
	}
	if !errors.Is(err, ErrUnownedDeployment) {
		t.Errorf("expected errors.Is(err, ErrUnownedDeployment), got: %v", err)
	}
}

// TestReconcileAggregation_DeploymentUpdate verifies that a pre-existing
// Deployment owned by the tunnel is updated (not rejected).
func TestReconcileAggregation_DeploymentUpdate(t *testing.T) {
	tun := newTunnelForAgg("home", "network", true)
	n := ConnectorNames(tun)

	controller := true
	blockDel := true
	preExisting := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      n.Deployment,
			Namespace: tun.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         cloudflarev1alpha1.GroupVersion.String(),
				Kind:               "CloudflareTunnel",
				Name:               tun.Name,
				UID:                tun.UID,
				Controller:         &controller,
				BlockOwnerDeletion: &blockDel,
			}},
		},
	}
	c := buildAggFakeClient(tun, preExisting)

	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("expected successful update, got: %v", err)
	}

	// Verify the Deployment was updated (spec overwritten with correct config-hash).
	var dep appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: n.Deployment}, &dep); err != nil {
		t.Fatalf("get updated Deployment: %v", err)
	}
	if dep.Spec.Template.Annotations[AnnotationConfigHash] == "" {
		t.Error("expected config-hash annotation after update")
	}
}

// TestReconcileAggregation_RuleDecisionBranches tests all three decision
// branches (Included, DuplicateHostname, Invalid) in a single aggregation.
func TestReconcileAggregation_RuleDecisionBranches(t *testing.T) {
	tun := newTunnelForAgg("home", "network", false) // connector off to keep test focused

	goodURL := strPtr("http://svc:8080")
	// Rule 1: valid, included.
	r1 := &cloudflarev1alpha1.CloudflareTunnelRule{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "network", Generation: 1},
		Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
			TunnelRef: cloudflarev1alpha1.TunnelReference{Name: "home", Namespace: "network"},
			Hostnames: []string{"a.example.com"},
			Backend:   cloudflarev1alpha1.TunnelRuleBackend{URL: goodURL},
			Priority:  200,
		},
	}
	// Rule 2: same hostname, lower priority → DuplicateHostname.
	r2 := &cloudflarev1alpha1.CloudflareTunnelRule{
		ObjectMeta: metav1.ObjectMeta{Name: "r2", Namespace: "network", Generation: 1},
		Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
			TunnelRef: cloudflarev1alpha1.TunnelReference{Name: "home", Namespace: "network"},
			Hostnames: []string{"a.example.com"},
			Backend:   cloudflarev1alpha1.TunnelRuleBackend{URL: goodURL},
			Priority:  100,
		},
	}
	// Rule 3: no hostnames → Invalid.
	r3 := &cloudflarev1alpha1.CloudflareTunnelRule{
		ObjectMeta: metav1.ObjectMeta{Name: "r3", Namespace: "network", Generation: 1},
		Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
			TunnelRef: cloudflarev1alpha1.TunnelReference{Name: "home", Namespace: "network"},
			Hostnames: []string{},
			Backend:   cloudflarev1alpha1.TunnelRuleBackend{URL: goodURL},
			Priority:  100,
		},
	}

	c := buildAggFakeClient(tun, r1, r2, r3)
	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("ReconcileConnectorAndRules: %v", err)
	}

	// r1: Valid=True, TunnelAccepted=True, Conflict=False.
	var ur1 cloudflarev1alpha1.CloudflareTunnelRule
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "network", Name: "r1"}, &ur1); err != nil {
		t.Fatalf("get r1: %v", err)
	}
	assertCondition(t, ur1.Status.Conditions, cloudflarev1alpha1.ConditionTypeValid, metav1.ConditionTrue)
	assertCondition(t, ur1.Status.Conditions, cloudflarev1alpha1.ConditionTypeTunnelAccepted, metav1.ConditionTrue)
	assertCondition(t, ur1.Status.Conditions, cloudflarev1alpha1.ConditionTypeConflict, metav1.ConditionFalse)

	// r2: Valid=True, TunnelAccepted=False (duplicate), Conflict=True.
	var ur2 cloudflarev1alpha1.CloudflareTunnelRule
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "network", Name: "r2"}, &ur2); err != nil {
		t.Fatalf("get r2: %v", err)
	}
	assertCondition(t, ur2.Status.Conditions, cloudflarev1alpha1.ConditionTypeValid, metav1.ConditionTrue)
	assertCondition(t, ur2.Status.Conditions, cloudflarev1alpha1.ConditionTypeTunnelAccepted, metav1.ConditionFalse)
	assertCondition(t, ur2.Status.Conditions, cloudflarev1alpha1.ConditionTypeConflict, metav1.ConditionTrue)

	// r3: Valid=False, TunnelAccepted=False, Conflict=False.
	var ur3 cloudflarev1alpha1.CloudflareTunnelRule
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "network", Name: "r3"}, &ur3); err != nil {
		t.Fatalf("get r3: %v", err)
	}
	assertCondition(t, ur3.Status.Conditions, cloudflarev1alpha1.ConditionTypeValid, metav1.ConditionFalse)
	assertCondition(t, ur3.Status.Conditions, cloudflarev1alpha1.ConditionTypeTunnelAccepted, metav1.ConditionFalse)
	assertCondition(t, ur3.Status.Conditions, cloudflarev1alpha1.ConditionTypeConflict, metav1.ConditionFalse)
}

// TestReconcileAggregation_ConfigHashPropagation verifies that the config-hash
// is identical on the ConfigMap annotation and Deployment pod-template
// annotation.
func TestReconcileAggregation_ConfigHashPropagation(t *testing.T) {
	tun := newTunnelForAgg("home", "network", true)
	rule := newRuleForTunnel("r1", "network", "home", "network")
	c := buildAggFakeClient(tun, rule)

	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("ReconcileConnectorAndRules: %v", err)
	}

	n := ConnectorNames(tun)
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: n.ConfigMap}, &cm); err != nil {
		t.Fatalf("get ConfigMap: %v", err)
	}
	var dep appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: n.Deployment}, &dep); err != nil {
		t.Fatalf("get Deployment: %v", err)
	}
	cmHash := cm.Annotations[AnnotationConfigHash]
	depHash := dep.Spec.Template.Annotations[AnnotationConfigHash]

	var tunOut cloudflarev1alpha1.CloudflareTunnel
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: tun.Name}, &tunOut); err != nil {
		t.Fatalf("re-fetch tunnel: %v", err)
	}
	if tunOut.Status.Connector == nil {
		t.Fatal("tun.Status.Connector is nil after reconcile")
	}
	connHash := tunOut.Status.Connector.ConfigHash

	if cmHash == "" || depHash == "" || connHash == "" {
		t.Fatalf("one or more hashes empty: cm=%q dep=%q conn=%q", cmHash, depHash, connHash)
	}
	if cmHash != depHash || depHash != connHash {
		t.Errorf("hash mismatch: cm=%q dep=%q conn=%q", cmHash, depHash, connHash)
	}
}

// TestReconcileAggregation_ConnectorReadyReplicas_NotFound tests that when
// the Deployment does not yet exist (NotFound), ConnectorReady=False/Reconciling.
func TestReconcileAggregation_ConnectorReadyReplicas_NotFound(t *testing.T) {
	tun := newTunnelForAgg("home", "network", true)
	c := buildAggFakeClient(tun)

	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("ReconcileConnectorAndRules: %v", err)
	}

	// After first call the Deployment IS created; but .Status.ReadyReplicas is
	// 0 since the fake client doesn't run controllers.
	var tunOut cloudflarev1alpha1.CloudflareTunnel
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: tun.Name}, &tunOut); err != nil {
		t.Fatalf("re-fetch tunnel: %v", err)
	}
	assertConditionWithReason(t, tunOut.Status.Conditions, cloudflarev1alpha1.ConditionTypeConnectorReady,
		metav1.ConditionFalse, cloudflarev1alpha1.ReasonReconciling)
	if tunOut.Status.Connector == nil {
		t.Fatal("tun.Status.Connector is nil")
	}
	if tunOut.Status.Connector.ReadyReplicas != 0 {
		t.Errorf("ReadyReplicas = %d, want 0", tunOut.Status.Connector.ReadyReplicas)
	}
}

// TestReconcileAggregation_ConnectorReadyReplicas_ZeroReadyReplicas tests that
// when the Deployment exists with ReadyReplicas=0, ConnectorReady=False/Reconciling.
func TestReconcileAggregation_ConnectorReadyReplicas_ZeroReadyReplicas(t *testing.T) {
	tun := newTunnelForAgg("home", "network", true)
	c := buildAggFakeClient(tun)

	// First reconcile: creates the Deployment.
	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("first ReconcileConnectorAndRules: %v", err)
	}

	// Re-fetch tunnel to verify persisted status.
	var tunOut cloudflarev1alpha1.CloudflareTunnel
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: tun.Name}, &tunOut); err != nil {
		t.Fatalf("re-fetch tunnel: %v", err)
	}
	// ReadyReplicas is already 0 (fake client default). Confirm condition.
	assertConditionWithReason(t, tunOut.Status.Conditions, cloudflarev1alpha1.ConditionTypeConnectorReady,
		metav1.ConditionFalse, cloudflarev1alpha1.ReasonReconciling)
}

// TestReconcileAggregation_ConnectorReadyReplicas_Ready tests that when the
// Deployment has ReadyReplicas>=1, ConnectorReady=True/ReconcileSuccess.
func TestReconcileAggregation_ConnectorReadyReplicas_Ready(t *testing.T) {
	tun := newTunnelForAgg("home", "network", true)
	c := buildAggFakeClient(tun)

	// First reconcile to create the Deployment.
	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("first ReconcileConnectorAndRules: %v", err)
	}

	// Simulate the Deployment becoming ready: update its status.
	n := ConnectorNames(tun)
	var dep appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: n.Deployment}, &dep); err != nil {
		t.Fatalf("get Deployment: %v", err)
	}
	dep.Status.ReadyReplicas = 2
	if err := c.Status().Update(context.Background(), &dep); err != nil {
		t.Fatalf("update Deployment status: %v", err)
	}

	// Re-run reconcile.
	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("second ReconcileConnectorAndRules: %v", err)
	}

	// Re-fetch tunnel to verify persisted status.
	var tunOut cloudflarev1alpha1.CloudflareTunnel
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: tun.Name}, &tunOut); err != nil {
		t.Fatalf("re-fetch tunnel: %v", err)
	}
	assertConditionWithReason(t, tunOut.Status.Conditions, cloudflarev1alpha1.ConditionTypeConnectorReady,
		metav1.ConditionTrue, cloudflarev1alpha1.ReasonReconcileSuccess)
	if tunOut.Status.Connector == nil {
		t.Fatal("tun.Status.Connector is nil after ready")
	}
	if tunOut.Status.Connector.ReadyReplicas != 2 {
		t.Errorf("ReadyReplicas = %d, want 2", tunOut.Status.Connector.ReadyReplicas)
	}
}

// TestReconcileAggregation_ConnectorStatusPopulated verifies that
// ConnectorStatus fields are populated correctly: Replicas, ReadyReplicas,
// ConfigHash, Image.
func TestReconcileAggregation_ConnectorStatusPopulated(t *testing.T) {
	tun := newTunnelForAgg("home", "network", true)
	c := buildAggFakeClient(tun)

	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("ReconcileConnectorAndRules: %v", err)
	}

	var tunOut cloudflarev1alpha1.CloudflareTunnel
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: tun.Name}, &tunOut); err != nil {
		t.Fatalf("re-fetch tunnel: %v", err)
	}
	cs := tunOut.Status.Connector
	if cs == nil {
		t.Fatal("tun.Status.Connector is nil")
	}
	if cs.Replicas != 2 {
		t.Errorf("Replicas = %d, want 2", cs.Replicas)
	}
	if cs.ConfigHash == "" {
		t.Error("ConfigHash is empty")
	}
	if cs.Image == "" {
		t.Error("Image is empty")
	}
}

// TestReconcileAggregation_RuleObservedGeneration verifies that writeRuleStatus
// sets the rule's ObservedGeneration from the rule's own Generation, not the
// tunnel's.
func TestReconcileAggregation_RuleObservedGeneration(t *testing.T) {
	tun := newTunnelForAgg("home", "network", false)
	tun.Generation = 5 // different from rule generation
	rule := newRuleForTunnel("r1", "network", "home", "network")
	rule.Generation = 3
	c := buildAggFakeClient(tun, rule)

	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("ReconcileConnectorAndRules: %v", err)
	}

	var updated cloudflarev1alpha1.CloudflareTunnelRule
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: rule.Namespace, Name: rule.Name}, &updated); err != nil {
		t.Fatalf("get rule: %v", err)
	}
	if updated.Status.ObservedGeneration != 3 {
		t.Errorf("ObservedGeneration = %d, want 3 (rule's generation)", updated.Status.ObservedGeneration)
	}
}

// TestReconcileAggregation_ResolvedBackendClearedOnNonIncluded verifies that
// writeRuleStatus clears ResolvedBackend when the rule transitions to a
// non-Included decision (Fix 2). The rule is pre-seeded with a stale backend
// URL via the fake client's status subresource so the backing store holds the
// value before reconcile.
func TestReconcileAggregation_ResolvedBackendClearedOnNonIncluded(t *testing.T) {
	tun := newTunnelForAgg("home", "network", false)

	// Two rules share the same hostname → one will be DuplicateHostname.
	goodURL := strPtr("http://svc:8080")
	r1 := &cloudflarev1alpha1.CloudflareTunnelRule{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "network", Generation: 1},
		Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
			TunnelRef: cloudflarev1alpha1.TunnelReference{Name: "home", Namespace: "network"},
			Hostnames: []string{"a.example.com"},
			Backend:   cloudflarev1alpha1.TunnelRuleBackend{URL: goodURL},
			Priority:  200,
		},
	}
	// r2 has the same hostname but lower priority → DuplicateHostname.
	// Pre-seed its status with a stale ResolvedBackend.
	r2 := &cloudflarev1alpha1.CloudflareTunnelRule{
		ObjectMeta: metav1.ObjectMeta{Name: "r2", Namespace: "network", Generation: 1},
		Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
			TunnelRef: cloudflarev1alpha1.TunnelReference{Name: "home", Namespace: "network"},
			Hostnames: []string{"a.example.com"},
			Backend:   cloudflarev1alpha1.TunnelRuleBackend{URL: goodURL},
			Priority:  100,
		},
		Status: cloudflarev1alpha1.CloudflareTunnelRuleStatus{
			ResolvedBackend: "http://stale",
		},
	}

	c := buildAggFakeClient(tun, r1, r2)

	// Persist the stale status into the fake client's status subresource store.
	if err := c.Status().Update(context.Background(), r2); err != nil {
		t.Fatalf("seed r2 stale status: %v", err)
	}

	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("ReconcileConnectorAndRules: %v", err)
	}

	// Re-fetch r2 and assert ResolvedBackend is cleared.
	var ur2 cloudflarev1alpha1.CloudflareTunnelRule
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "network", Name: "r2"}, &ur2); err != nil {
		t.Fatalf("re-fetch r2: %v", err)
	}
	if ur2.Status.ResolvedBackend != "" {
		t.Errorf("ResolvedBackend = %q, want empty string after DuplicateHostname decision", ur2.Status.ResolvedBackend)
	}
	// Confirm it is indeed a DuplicateHostname decision (TunnelAccepted=False, Conflict=True).
	assertCondition(t, ur2.Status.Conditions, cloudflarev1alpha1.ConditionTypeTunnelAccepted, metav1.ConditionFalse)
	assertCondition(t, ur2.Status.Conditions, cloudflarev1alpha1.ConditionTypeConflict, metav1.ConditionTrue)
}

// TestReconcileAggregation_EmptyTunnelIDFailsLoud verifies that
// ReconcileConnectorAndRules returns a clear error when Status.TunnelID
// is empty, rather than rendering an invalid config.yaml. The gating
// invariant at the controller level should make this unreachable in
// practice; this is defense-in-depth.
func TestReconcileAggregation_EmptyTunnelIDFailsLoud(t *testing.T) {
	tun := newTunnelForAgg("home", "network", true)
	tun.Status.TunnelID = "" // simulate the broken invariant
	c := buildAggFakeClient(tun)

	err := ReconcileConnectorAndRules(context.Background(), c, tun, nil)
	if err == nil {
		t.Fatal("expected an error when Status.TunnelID is empty, got nil")
	}
	if !strings.Contains(err.Error(), "tunnel ID is empty") {
		t.Errorf("error %q does not mention empty tunnel ID", err)
	}
	if !strings.Contains(err.Error(), "network/home") {
		t.Errorf("error %q should be self-locating (mention namespace/name): want substring %q", err, "network/home")
	}
}

// ---- P2.7 AppliedToConfigHash -------------------------------------------

// TestReconcileAggregation_AppliedToConfigHash_Set verifies that when a rule is
// included in aggregation, its status.appliedToConfigHash is set to the tunnel's
// config hash.
func TestReconcileAggregation_AppliedToConfigHash_Set(t *testing.T) {
	tun := newTunnelForAgg("home", "network", false) // connector off; focused on status
	rule := newRuleForTunnel("r1", "network", "home", "network")
	c := buildAggFakeClient(tun, rule)

	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("ReconcileConnectorAndRules: %v", err)
	}

	var updatedRule cloudflarev1alpha1.CloudflareTunnelRule
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: rule.Namespace, Name: rule.Name}, &updatedRule); err != nil {
		t.Fatalf("get rule: %v", err)
	}

	if updatedRule.Status.AppliedToConfigHash == "" {
		t.Error("status.appliedToConfigHash is empty for an included rule; expected it to be populated with the aggregation config hash")
	}
}

// TestReconcileAggregation_AppliedToConfigHash_EmptyForNonIncluded verifies
// that a DuplicateHostname rule does NOT get appliedToConfigHash set.
func TestReconcileAggregation_AppliedToConfigHash_EmptyForNonIncluded(t *testing.T) {
	tun := newTunnelForAgg("home", "network", false)

	goodURL := strPtr("http://svc:8080")
	r1 := &cloudflarev1alpha1.CloudflareTunnelRule{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "network", Generation: 1},
		Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
			TunnelRef: cloudflarev1alpha1.TunnelReference{Name: "home", Namespace: "network"},
			Hostnames: []string{"a.example.com"},
			Backend:   cloudflarev1alpha1.TunnelRuleBackend{URL: goodURL},
			Priority:  200,
		},
	}
	// r2 loses the duplicate-hostname conflict.
	r2 := &cloudflarev1alpha1.CloudflareTunnelRule{
		ObjectMeta: metav1.ObjectMeta{Name: "r2", Namespace: "network", Generation: 1},
		Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
			TunnelRef: cloudflarev1alpha1.TunnelReference{Name: "home", Namespace: "network"},
			Hostnames: []string{"a.example.com"},
			Backend:   cloudflarev1alpha1.TunnelRuleBackend{URL: goodURL},
			Priority:  100,
		},
	}

	c := buildAggFakeClient(tun, r1, r2)
	if err := ReconcileConnectorAndRules(context.Background(), c, tun, nil); err != nil {
		t.Fatalf("ReconcileConnectorAndRules: %v", err)
	}

	var ur2 cloudflarev1alpha1.CloudflareTunnelRule
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "network", Name: "r2"}, &ur2); err != nil {
		t.Fatalf("get r2: %v", err)
	}
	if ur2.Status.AppliedToConfigHash != "" {
		t.Errorf("status.appliedToConfigHash = %q for non-included (DuplicateHostname) rule; expected empty", ur2.Status.AppliedToConfigHash)
	}
}

// ---- assertion helpers ------------------------------------------------------

func assertCondition(t *testing.T, conds []metav1.Condition, condType string, wantStatus metav1.ConditionStatus) {
	t.Helper()
	for _, c := range conds {
		if c.Type == condType {
			if c.Status != wantStatus {
				t.Errorf("condition %q: status=%q, want %q (reason=%q, msg=%q)", condType, c.Status, wantStatus, c.Reason, c.Message)
			}
			return
		}
	}
	t.Errorf("condition %q not found in %+v", condType, conds)
}

func assertConditionWithReason(t *testing.T, conds []metav1.Condition, condType string, wantStatus metav1.ConditionStatus, wantReason string) {
	t.Helper()
	for _, c := range conds {
		if c.Type == condType {
			if c.Status != wantStatus {
				t.Errorf("condition %q: status=%q, want %q", condType, c.Status, wantStatus)
			}
			if c.Reason != wantReason {
				t.Errorf("condition %q: reason=%q, want %q", condType, c.Reason, wantReason)
			}
			return
		}
	}
	t.Errorf("condition %q not found in %+v", condType, conds)
}

// buildInterceptedAggClient builds the same fake client as
// buildAggFakeClient, then wraps it with the given interceptor.Funcs.
func buildInterceptedAggClient(funcs interceptor.Funcs, objs ...client.Object) client.Client {
	s := aggTestScheme()
	var statusObjs []client.Object
	for _, o := range objs {
		switch o.(type) {
		case *cloudflarev1alpha1.CloudflareTunnel, *cloudflarev1alpha1.CloudflareTunnelRule:
			statusObjs = append(statusObjs, o)
		}
	}
	base := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(statusObjs...).
		Build()
	return interceptor.NewClient(base, funcs)
}

// TestReconcileConnectorResources_RetriesOnDeploymentConflict verifies that
// reconcileConnectorResources recovers from transient IsConflict errors on
// the Deployment Update path (#59 root cause).
func TestReconcileConnectorResources_RetriesOnDeploymentConflict(t *testing.T) {
	for _, n := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("conflicts=%d", n), func(t *testing.T) {
			tun := newTunnelForAgg("home", "network", true)
			// Pre-create the Deployment so reconcileConnectorResources takes the
			// update branch (where conflicts manifest), not the create branch.
			ndep := ConnectorNames(tun)
			existing := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:            ndep.Deployment,
					Namespace:       tun.Namespace,
					OwnerReferences: connectorOwnerRef(tun),
				},
			}
			// Track calls only against Deployment Updates so unrelated SA/CM
			// updates don't consume the budget.
			calls := 0
			funcs := interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if _, ok := obj.(*appsv1.Deployment); ok {
						calls++
						if calls <= n {
							return apierrors.NewConflict(
								schema.GroupResource{Group: "apps", Resource: "deployments"},
								obj.GetName(),
								fmt.Errorf("simulated conflict %d", calls),
							)
						}
					}
					return c.Update(ctx, obj, opts...)
				},
			}
			c := buildInterceptedAggClient(funcs, tun, existing)

			rules := []cloudflarev1alpha1.CloudflareTunnelRule{}
			agg := Aggregate(tun.Status.TunnelID, rules, nil)
			if err := reconcileConnectorResources(context.Background(), c, tun, agg); err != nil {
				t.Fatalf("expected nil error after %d conflicts, got: %v", n, err)
			}
			if calls < n+1 {
				t.Errorf("expected at least %d Deployment update attempts, got %d", n+1, calls)
			}
		})
	}
}

// TestReconcileConnectorResources_NonConflictErrorShortCircuits verifies
// that non-conflict errors from Update propagate immediately without
// retry — preserves existing semantics for real failures.
func TestReconcileConnectorResources_NonConflictErrorShortCircuits(t *testing.T) {
	tun := newTunnelForAgg("home", "network", true)
	ndep := ConnectorNames(tun)
	existing := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            ndep.Deployment,
			Namespace:       tun.Namespace,
			OwnerReferences: connectorOwnerRef(tun),
		},
	}
	calls := 0
	funcs := interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*appsv1.Deployment); ok {
				calls++
				return apierrors.NewBadRequest("simulated bad request")
			}
			return c.Update(ctx, obj, opts...)
		},
	}
	c := buildInterceptedAggClient(funcs, tun, existing)

	agg := Aggregate(tun.Status.TunnelID, nil, nil)
	err := reconcileConnectorResources(context.Background(), c, tun, agg)
	if err == nil {
		t.Fatal("expected error from non-conflict Update failure, got nil")
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 Deployment update attempt, got %d", calls)
	}
}

// TestReconcileConnectorResources_PersistentConflictPropagates verifies
// that a conflict storm exhausting the retry budget still propagates the
// final error — the controller is no worse off than today in pathological
// cases.
func TestReconcileConnectorResources_PersistentConflictPropagates(t *testing.T) {
	tun := newTunnelForAgg("home", "network", true)
	ndep := ConnectorNames(tun)
	existing := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            ndep.Deployment,
			Namespace:       tun.Namespace,
			OwnerReferences: connectorOwnerRef(tun),
		},
	}
	funcs := interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*appsv1.Deployment); ok {
				return apierrors.NewConflict(
					schema.GroupResource{Group: "apps", Resource: "deployments"},
					obj.GetName(),
					fmt.Errorf("permanent conflict"),
				)
			}
			return c.Update(ctx, obj, opts...)
		},
	}
	c := buildInterceptedAggClient(funcs, tun, existing)

	agg := Aggregate(tun.Status.TunnelID, nil, nil)
	err := reconcileConnectorResources(context.Background(), c, tun, agg)
	if err == nil {
		t.Fatal("expected propagated conflict error after retry budget exhausted, got nil")
	}
	if !apierrors.IsConflict(err) {
		t.Errorf("expected IsConflict error, got %v", err)
	}
}
