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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	corev1 "k8s.io/api/core/v1"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

// ---- test constants ---------------------------------------------------------

const (
	testTunnelName    = "home"
	testNsApps        = "apps"
	testNsNetwork     = "network"
	testGatewayName   = "internal"
	testRouteName     = "my-route"
	testRecordTypeTXT = "TXT"
)

// ---- test infrastructure ----------------------------------------------------

// httpRouteScheme builds a scheme with corev1, cloudflarev1alpha1, and gwv1.
func httpRouteScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := cloudflarev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add v1alpha1: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	if err := gwv1.Install(s); err != nil {
		t.Fatalf("add gwv1: %v", err)
	}
	return s
}

func buildHTTPRouteReconciler(s *runtime.Scheme, ownerID string, objs ...client.Object) (*HTTPRouteSourceReconciler, *record.FakeRecorder) {
	recorder := record.NewFakeRecorder(64)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(
			&cloudflarev1alpha1.CloudflareDNSRecord{},
			&cloudflarev1alpha1.CloudflareTunnelRule{},
			&cloudflarev1alpha1.CloudflareTunnel{},
		).
		Build()
	return &HTTPRouteSourceReconciler{
		Client:     c,
		Recorder:   recorder,
		TxtOwnerID: ownerID,
	}, recorder
}

// newGateway returns a Gateway with optional annotations and optional status addresses.
func newGateway(ns, name string, ann map[string]string) *gwv1.Gateway {
	gw := &gwv1.Gateway{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "gateway.networking.k8s.io/v1",
			Kind:       "Gateway",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			UID:         types.UID(fmt.Sprintf("%s-%s-uid", ns, name)),
			Annotations: ann,
		},
		Spec: gwv1.GatewaySpec{
			GatewayClassName: "cloudflare",
		},
	}
	return gw
}

// newGatewayWithAddress returns a Gateway with a populated status address.
func newGatewayWithAddress(ns, name, addr string, ann map[string]string) *gwv1.Gateway {
	gw := newGateway(ns, name, ann)
	gw.Status.Addresses = []gwv1.GatewayStatusAddress{
		{Value: addr},
	}
	return gw
}

// newHTTPRoute returns an HTTPRoute with optional annotations, hostnames, and parentRefs.
func newHTTPRoute(ns, name string, ann map[string]string, hostnames []gwv1.Hostname, parents []gwv1.ParentReference) *gwv1.HTTPRoute {
	return &gwv1.HTTPRoute{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "gateway.networking.k8s.io/v1",
			Kind:       "HTTPRoute",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			UID:         types.UID(fmt.Sprintf("%s-%s-uid", ns, name)),
			Annotations: ann,
		},
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: parents,
			},
			Hostnames: hostnames,
		},
	}
}

// parentRef returns a ParentReference pointing at a Gateway.
func parentRef(ns, name string) gwv1.ParentReference {
	group := gwv1.Group("gateway.networking.k8s.io")
	kind := gwv1.Kind("Gateway")
	if ns == "" {
		return gwv1.ParentReference{Group: &group, Kind: &kind, Name: gwv1.ObjectName(name)}
	}
	nsPtr := gwv1.Namespace(ns)
	return gwv1.ParentReference{Group: &group, Kind: &kind, Name: gwv1.ObjectName(name), Namespace: &nsPtr}
}

// hostnames converts string slices to gwv1.Hostname slices.
func hostnames(hs ...string) []gwv1.Hostname {
	out := make([]gwv1.Hostname, len(hs))
	for i, h := range hs {
		out[i] = gwv1.Hostname(h)
	}
	return out
}

// ---- TestHTTPRouteSource_RouteNotFound_NoError ------------------------------

func TestHTTPRouteSource_RouteNotFound_NoError(t *testing.T) {
	s := httpRouteScheme(t)
	r, _ := buildHTTPRouteReconciler(s, "owner1")

	_, err := r.Reconcile(context.Background(), req("apps", "missing-route"))
	if err != nil {
		t.Fatalf("expected no error for missing HTTPRoute, got: %v", err)
	}
}

// ---- TestHTTPRouteSource_NoTargetAnnotation_NoEmissions --------------------

func TestHTTPRouteSource_NoTargetAnnotation_NoEmissions(t *testing.T) {
	s := httpRouteScheme(t)
	route := newHTTPRoute("apps", "my-route", nil, hostnames("foo.example.com"), nil)
	r, rec := buildHTTPRouteReconciler(s, "owner1", route)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	if len(evts) != 0 {
		t.Errorf("expected no events, got %v", evts)
	}
	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) != 0 {
		t.Errorf("expected 0 DNS records, got %d", len(records.Items))
	}
}

// ---- TestHTTPRouteSource_MissingTxtOwnerID_Warns ---------------------------

func TestHTTPRouteSource_MissingTxtOwnerID_Warns(t *testing.T) {
	s := httpRouteScheme(t)
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget: "tunnel:home",
	}, hostnames("foo.example.com"), nil)
	r, rec := buildHTTPRouteReconciler(s, "" /* empty TxtOwnerID */, route)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	if len(evts) == 0 {
		t.Error("expected a warning event for missing TxtOwnerID")
	}
	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) != 0 {
		t.Errorf("expected 0 DNS records, got %d", len(records.Items))
	}
}

// ---- TestHTTPRouteSource_EmptyHostnames_Warns ------------------------------

func TestHTTPRouteSource_EmptyHostnames_Warns(t *testing.T) {
	s := httpRouteScheme(t)
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget: "tunnel:home",
	}, []gwv1.Hostname{} /* no hostnames */, nil)
	r, rec := buildHTTPRouteReconciler(s, "owner1", route)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	if len(evts) == 0 {
		t.Error("expected a warning event for empty hostnames")
	}
	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) != 0 {
		t.Errorf("expected 0 DNS records, got %d", len(records.Items))
	}
}

// ---- TestHTTPRouteSource_ParseTargetError_Warns ----------------------------

func TestHTTPRouteSource_ParseTargetError_Warns(t *testing.T) {
	s := httpRouteScheme(t)
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget: "invalid-target",
	}, hostnames("foo.example.com"), nil)
	r, rec := buildHTTPRouteReconciler(s, "owner1", route)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	found := false
	for _, e := range evts {
		if strings.Contains(e, "Warning") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning event for invalid target; got %v", evts)
	}
}

// ---- TestHTTPRouteSource_TunnelTarget_FullEmission -------------------------
// Golden path: tunnel target, tunnel-upstream set → DNS + TXT + rule emitted.

func TestHTTPRouteSource_TunnelTarget_FullEmission(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget:         "tunnel:home",
		AnnotationTunnelUpstream: "http://backend.apps.svc:8080",
	}, hostnames("foo.example.com"), nil)
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list DNS records: %v", err)
	}
	if len(records.Items) != 2 {
		t.Fatalf("expected 2 DNS records (CNAME + TXT), got %d", len(records.Items))
	}

	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 1 {
		t.Fatalf("expected 1 TunnelRule, got %d", len(rules.Items))
	}
	rule := rules.Items[0]
	if rule.Spec.TunnelRef.Name != testTunnelName {
		t.Errorf("expected TunnelRef.Name=%s, got %q", testTunnelName, rule.Spec.TunnelRef.Name)
	}
	if rule.Spec.Backend.URL == nil || *rule.Spec.Backend.URL != "http://backend.apps.svc:8080" {
		t.Errorf("expected Backend.URL=http://backend.apps.svc:8080, got %v", rule.Spec.Backend.URL)
	}
}

// ---- TestHTTPRouteSource_TunnelUpstream_EmitsRule --------------------------

func TestHTTPRouteSource_TunnelUpstream_EmitsRule(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget:         "tunnel:home",
		AnnotationTunnelUpstream: "https://my-backend.example.com",
	}, hostnames("foo.example.com"), nil)
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules.Items))
	}
	if rules.Items[0].Spec.Backend.URL == nil {
		t.Fatal("expected Backend.URL to be set")
	}
	if *rules.Items[0].Spec.Backend.URL != "https://my-backend.example.com" {
		t.Errorf("expected Backend.URL=https://my-backend.example.com, got %q", *rules.Items[0].Spec.Backend.URL)
	}
}

// ---- TestHTTPRouteSource_TunnelUpstreamAbsent_DeletesOrphanRule ------------

func TestHTTPRouteSource_TunnelUpstreamAbsent_DeletesOrphanRule(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget: "tunnel:home",
		// no tunnel-upstream
	}, hostnames("foo.example.com"), nil)

	// Pre-create the orphan rule at the expected name.
	orphanRule := &cloudflarev1alpha1.CloudflareTunnelRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "httproute-apps-my-route",
			Namespace: "apps",
		},
		Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
			TunnelRef: cloudflarev1alpha1.TunnelReference{Name: "home"},
			Hostnames: []string{"foo.example.com"},
			Backend:   cloudflarev1alpha1.TunnelRuleBackend{URL: strPtr("http://old-backend")},
			Priority:  100,
		},
	}
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, tunnel, orphanRule)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 0 {
		t.Errorf("expected orphan rule to be deleted, but found %d rules", len(rules.Items))
	}
}

// ---- TestHTTPRouteSource_TunnelUpstreamAbsent_NoExistingRule_NoError ------

func TestHTTPRouteSource_TunnelUpstreamAbsent_NoExistingRule_NoError(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget: "tunnel:home",
		// no tunnel-upstream
	}, hostnames("foo.example.com"), nil)
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, tunnel)

	// No pre-existing rule — delete should be a no-op.
	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("expected no error when no existing rule and no upstream, got: %v", err)
	}
}

// ---- TestHTTPRouteSource_TunnelNotFound_Warns ------------------------------

func TestHTTPRouteSource_TunnelNotFound_Warns(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	// No tunnel registered.
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget: "tunnel:home",
	}, hostnames("foo.example.com"), nil)
	r, rec := buildHTTPRouteReconciler(s, "owner1", route, zone)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	found := false
	for _, e := range evts {
		if strings.Contains(e, cloudflarev1alpha1.ReasonTunnelNotFound) || strings.Contains(e, "Warning") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected TunnelNotFound warning; got %v", evts)
	}
}

// ---- TestHTTPRouteSource_TunnelNotReady_Requeue ----------------------------

func TestHTTPRouteSource_TunnelNotReady_Requeue(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	// Tunnel exists but no CNAME.
	tunnel := &cloudflarev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "apps"},
		Spec: cloudflarev1alpha1.CloudflareTunnelSpec{
			Name:                "home",
			SecretRef:           cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
			GeneratedSecretName: "home-credentials",
		},
		// Status.TunnelCNAME intentionally empty.
	}
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget: "tunnel:home",
	}, hostnames("foo.example.com"), nil)
	r, rec := buildHTTPRouteReconciler(s, "owner1", route, zone, tunnel)

	result, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue when tunnel not ready")
	}
	evts := drainEvents(rec)
	if len(evts) == 0 {
		t.Error("expected a warning event for tunnel not ready")
	}
}

// ---- TestHTTPRouteSource_AddressTarget_GatewayHasAddresses -----------------

func TestHTTPRouteSource_AddressTarget_GatewayHasAddresses(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	gw := newGatewayWithAddress("apps", "internal", "1.2.3.4", map[string]string{
		AnnotationTarget: "address",
	})
	route := newHTTPRoute("apps", "my-route", nil, hostnames("foo.example.com"),
		[]gwv1.ParentReference{parentRef("apps", "internal")})
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, gw)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) == 0 {
		t.Fatal("expected DNS records to be emitted for address target")
	}
	// Find the CNAME/A record and check its content.
	found := false
	for _, rec := range records.Items {
		if rec.Spec.Type != testRecordTypeTXT {
			if rec.Spec.Content != nil && *rec.Spec.Content == "1.2.3.4" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected DNS record with content=1.2.3.4; records: %+v", records.Items)
	}
}

// ---- TestHTTPRouteSource_AddressTarget_GatewayNoAddresses ------------------

func TestHTTPRouteSource_AddressTarget_GatewayNoAddresses(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	// Gateway has no addresses yet.
	gw := newGateway("apps", "internal", map[string]string{
		AnnotationTarget: "address",
	})
	route := newHTTPRoute("apps", "my-route", nil, hostnames("foo.example.com"),
		[]gwv1.ParentReference{parentRef("apps", "internal")})
	r, rec := buildHTTPRouteReconciler(s, "owner1", route, zone, gw)

	result, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue when gateway has no addresses")
	}
	evts := drainEvents(rec)
	found := false
	for _, e := range evts {
		if strings.Contains(e, cloudflarev1alpha1.ReasonGatewayAddressNotReady) || strings.Contains(e, "Warning") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected GatewayAddressNotReady warning; got %v", evts)
	}
}

// ---- TestHTTPRouteSource_CNAMETarget_EmitsDNSOnly --------------------------

func TestHTTPRouteSource_CNAMETarget_EmitsDNSOnly(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget:         "cname:external.example.net",
		AnnotationTunnelUpstream: "http://should-be-ignored:8080",
	}, hostnames("foo.example.com"), nil)
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list DNS records: %v", err)
	}
	if len(records.Items) != 2 {
		t.Fatalf("expected 2 DNS records (CNAME + TXT), got %d", len(records.Items))
	}
	// No tunnel rule should be emitted for cname target even with tunnel-upstream.
	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 0 {
		t.Errorf("expected 0 TunnelRules for cname target, got %d", len(rules.Items))
	}
}

// ---- TestHTTPRouteSource_MultipleHostnames_EmitsOneRule --------------------

func TestHTTPRouteSource_MultipleHostnames_EmitsOneRule(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget:         "tunnel:home",
		AnnotationTunnelUpstream: "http://backend:8080",
	}, hostnames("foo.example.com", "bar.example.com"), nil)
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list DNS records: %v", err)
	}
	// 2 CNAME + 2 TXT = 4 records.
	if len(records.Items) != 4 {
		t.Fatalf("expected 4 DNS records, got %d", len(records.Items))
	}

	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 1 {
		t.Fatalf("expected 1 TunnelRule for 2 hostnames, got %d", len(rules.Items))
	}
	rule := rules.Items[0]
	if len(rule.Spec.Hostnames) != 2 {
		t.Errorf("expected rule.Spec.Hostnames to have 2 entries, got %v", rule.Spec.Hostnames)
	}
}

// ---- TestHTTPRouteSource_SourceLabels_OnEmittedCRs -------------------------

func TestHTTPRouteSource_SourceLabels_OnEmittedCRs(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget:         "tunnel:home",
		AnnotationTunnelUpstream: "http://backend:8080",
	}, hostnames("foo.example.com"), nil)
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list DNS records: %v", err)
	}
	for _, rec := range records.Items {
		checkSourceLabels(t, rec.Labels, "HTTPRoute", "apps", "my-route")
	}

	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	for _, rule := range rules.Items {
		checkSourceLabels(t, rule.Labels, "HTTPRoute", "apps", "my-route")
	}
}

// ---- TestHTTPRouteSource_OwnerRefCascade -----------------------------------

func TestHTTPRouteSource_OwnerRefCascade(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget:         "tunnel:home",
		AnnotationTunnelUpstream: "http://backend:8080",
	}, hostnames("foo.example.com"), nil)
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list DNS records: %v", err)
	}
	for _, rec := range records.Items {
		assertOwnerRef(t, rec.OwnerReferences, "my-route", types.UID("apps-my-route-uid"))
	}

	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	for _, rule := range rules.Items {
		assertOwnerRef(t, rule.OwnerReferences, "my-route", types.UID("apps-my-route-uid"))
	}
}

// ---- TestHTTPRouteSource_CRNaming_HTTPRoutePrefix --------------------------
// Lock the naming contract: httproute-<ns>-<name>-<hostname> (DNS),
// httproute-<ns>-<name>-<hostname>-txt (TXT), httproute-<ns>-<name> (rule).

func TestHTTPRouteSource_CRNaming_HTTPRoutePrefix(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget:         "tunnel:home",
		AnnotationTunnelUpstream: "http://backend:8080",
	}, hostnames("foo.example.com"), nil)
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedDNSName := "httproute-apps-my-route-foo-example-com"
	expectedTXTName := "httproute-apps-my-route-foo-example-com-txt"
	expectedRuleName := "httproute-apps-my-route"

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	names := make(map[string]bool)
	for _, rec := range records.Items {
		names[rec.Name] = true
	}
	if !names[expectedDNSName] {
		t.Errorf("expected DNS record named %q; got names: %v", expectedDNSName, names)
	}
	if !names[expectedTXTName] {
		t.Errorf("expected TXT record named %q; got names: %v", expectedTXTName, names)
	}

	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 1 || rules.Items[0].Name != expectedRuleName {
		t.Errorf("expected rule named %q; got %v", expectedRuleName, rules.Items)
	}
}

// ---- TestHTTPRouteSource_AnnotationRegistryFor_IsHostname ------------------
// TXT record must carry AnnotationRegistryFor: hostname (not CR name).

func TestHTTPRouteSource_AnnotationRegistryFor_IsHostname(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget:         "tunnel:home",
		AnnotationTunnelUpstream: "http://backend:8080",
	}, hostnames("foo.example.com"), nil)
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}

	for _, rec := range records.Items {
		if rec.Spec.Type == testRecordTypeTXT {
			got := rec.Annotations[AnnotationRegistryFor]
			if got != "foo.example.com" {
				t.Errorf("AnnotationRegistryFor on TXT: expected %q, got %q", "foo.example.com", got)
			}
		}
	}
}

// ---- TestHTTPRouteSource_Inherited_RouteOverridesParent --------------------
// Both Gateway and Route have cloudflare.io/target; Route's value wins.
// Asserts on TunnelRule.Spec.TunnelRef.Name to definitively distinguish
// route-tunnel from gw-tunnel without depending on CNAME string content.

func TestHTTPRouteSource_Inherited_RouteOverridesParent(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("route-tunnel", "apps")
	gw := newGateway("apps", "internal", map[string]string{
		AnnotationTarget: "tunnel:gw-tunnel",
	})
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget:         "tunnel:route-tunnel", // Route overrides Gateway
		AnnotationTunnelUpstream: "http://route-backend:8080",
	}, hostnames("foo.example.com"),
		[]gwv1.ParentReference{parentRef("apps", "internal")})
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, gw, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Assert the TunnelRule references route-tunnel, not gw-tunnel.
	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 1 {
		t.Fatalf("expected 1 TunnelRule, got %d", len(rules.Items))
	}
	if rules.Items[0].Spec.TunnelRef.Name != "route-tunnel" {
		t.Errorf("expected TunnelRef.Name=route-tunnel (Route annotation overrides Gateway), got %q",
			rules.Items[0].Spec.TunnelRef.Name)
	}
}

// ---- TestHTTPRouteSource_Inherited_RouteEmpty_ParentUsed -------------------
// Only Gateway has cf annotations; Route has none → emits using parent's values.

func TestHTTPRouteSource_Inherited_RouteEmpty_ParentUsed(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	gw := newGateway("apps", "internal", map[string]string{
		AnnotationTarget:         "tunnel:home",
		AnnotationTunnelUpstream: "http://gw-backend:8080",
	})
	// Route has no annotations.
	route := newHTTPRoute("apps", "my-route", nil, hostnames("foo.example.com"),
		[]gwv1.ParentReference{parentRef("apps", "internal")})
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, gw, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) < 2 {
		t.Fatalf("expected DNS records emitted via parent annotations; got %d", len(records.Items))
	}

	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 1 {
		t.Fatalf("expected 1 rule via parent annotations; got %d", len(rules.Items))
	}
}

// ---- TestHTTPRouteSource_NoParentAnnotations_NoEmission --------------------
// Parent Gateway has no cf annotations, Route has no target → no emissions.

func TestHTTPRouteSource_NoParentAnnotations_NoEmission(t *testing.T) {
	s := httpRouteScheme(t)
	// Gateway with no cloudflare annotations.
	gw := newGateway("apps", "internal", map[string]string{
		"some-other-annotation": "value",
	})
	route := newHTTPRoute("apps", "my-route", nil, hostnames("foo.example.com"),
		[]gwv1.ParentReference{parentRef("apps", "internal")})
	r, rec := buildHTTPRouteReconciler(s, "owner1", route, gw)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	if len(evts) != 0 {
		t.Errorf("expected no events, got %v", evts)
	}
	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) != 0 {
		t.Errorf("expected 0 DNS records, got %d", len(records.Items))
	}
}

// ---- TestHTTPRouteSource_ParentGatewayCrossNamespace -----------------------
// Gateway in "network", Route in "apps" with parentRef to "network/internal"
// → annotations correctly inherited.

func TestHTTPRouteSource_ParentGatewayCrossNamespace(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "network")
	// Gateway in "network" namespace with cloudflare annotations.
	gw := newGateway("network", "internal", map[string]string{
		AnnotationTarget:             "tunnel:home",
		AnnotationTunnelUpstream:     "http://gw-backend:8080",
		AnnotationTunnelRefNamespace: "network",
	})
	// Route in "apps" with parentRef pointing to "network/internal".
	route := newHTTPRoute("apps", "my-route", nil, hostnames("foo.example.com"),
		[]gwv1.ParentReference{parentRef("network", "internal")})
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, gw, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) == 0 {
		t.Fatal("expected DNS records from cross-namespace parent Gateway annotations")
	}
}

// ---- TestHTTPRouteSource_TunnelRefCrossNamespace ---------------------------
// Route in "apps" with inherited tunnel-ref-namespace: network
// → emitted rule has TunnelRef{Name: home, Namespace: network}.

func TestHTTPRouteSource_TunnelRefCrossNamespace(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := &cloudflarev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "network"},
		Spec: cloudflarev1alpha1.CloudflareTunnelSpec{
			Name:                "home",
			SecretRef:           cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
			GeneratedSecretName: "home-credentials",
		},
		Status: cloudflarev1alpha1.CloudflareTunnelStatus{
			TunnelCNAME: "test-id.cfargotunnel.com",
		},
	}
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget:             "tunnel:home",
		AnnotationTunnelRefNamespace: "network",
		AnnotationTunnelUpstream:     "http://backend:8080",
	}, hostnames("foo.example.com"), nil)
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules.Items))
	}
	tunnelRef := rules.Items[0].Spec.TunnelRef
	if tunnelRef.Namespace != testNsNetwork {
		t.Errorf("expected TunnelRef.Namespace=%s, got %q", testNsNetwork, tunnelRef.Namespace)
	}
	if tunnelRef.Name != testTunnelName {
		t.Errorf("expected TunnelRef.Name=%s, got %q", testTunnelName, tunnelRef.Name)
	}
}

// ---- TestHTTPRouteSource_ErrNoGatewayAddress_Sentinel ----------------------
// Confirm ErrNoGatewayAddress is accessible for errors.Is in tests.

func TestHTTPRouteSource_ErrNoGatewayAddress_Sentinel(t *testing.T) {
	// Wrap the sentinel and confirm errors.Is works.
	wrapped := fmt.Errorf("outer: %w", ErrNoGatewayAddress)
	if !errors.Is(wrapped, ErrNoGatewayAddress) {
		t.Error("errors.Is(wrapped, ErrNoGatewayAddress) returned false")
	}
}

// ---- TestHTTPRouteSource_ZoneNotFound_Warns --------------------------------

func TestHTTPRouteSource_ZoneNotFound_Warns(t *testing.T) {
	s := httpRouteScheme(t)
	// No zone registered.
	tunnel := newReadyTunnel("home", "apps")
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget: "tunnel:home",
	}, hostnames("foo.example.com"), nil)
	r, rec := buildHTTPRouteReconciler(s, "owner1", route, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	if len(evts) == 0 {
		t.Error("expected a warning event for zone not found")
	}
}

// ---- TestHTTPRouteSource_ProxiedParseError_KeepsDefault --------------------
// Invalid proxied annotation value → warning emitted, default is kept (not false).

func TestHTTPRouteSource_ProxiedParseError_KeepsDefault(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget:         "tunnel:home",
		AnnotationTunnelUpstream: "http://backend:8080",
		AnnotationProxied:        "invalid-bool-value",
	}, hostnames("foo.example.com"), nil)
	r, rec := buildHTTPRouteReconciler(s, "owner1", route, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	found := false
	for _, e := range evts {
		if strings.Contains(e, "Warning") && strings.Contains(e, "proxied") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning event for invalid proxied annotation; got %v", evts)
	}
	// For tunnel target, proxied should still default to true.
	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, rec := range records.Items {
		if rec.Spec.Type == testRecordTypeCNAME {
			if rec.Spec.Proxied == nil || !*rec.Spec.Proxied {
				t.Errorf("expected Proxied=true (default for tunnel) even with invalid annotation, got %v", rec.Spec.Proxied)
			}
		}
	}
}

// ---- TestHTTPRouteSource_MapGatewayToRoutes_CrossNamespace -----------------
// Gateway in "network", Route in "apps" with parentRef → mapGatewayToRoutes
// returns the Route's request.

func TestHTTPRouteSource_MapGatewayToRoutes_CrossNamespace(t *testing.T) {
	s := httpRouteScheme(t)
	gw := newGateway("network", "internal", nil)
	route := newHTTPRoute("apps", "my-route", nil, hostnames("foo.example.com"),
		[]gwv1.ParentReference{parentRef("network", "internal")})

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gw, route).
		Build()
	r := &HTTPRouteSourceReconciler{Client: c}

	reqs := r.mapGatewayToRoutes(context.Background(), gw)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d: %v", len(reqs), reqs)
	}
	if reqs[0].Namespace != testNsApps || reqs[0].Name != testRouteName {
		t.Errorf("expected {%s/%s}, got %v", testNsApps, testRouteName, reqs[0])
	}
}

// ---- TestHTTPRouteSource_MapTunnelToRoutes_CrossNamespace ------------------
// Route in "apps" with inherited tunnel-ref-namespace: network
// → mapTunnelToRoutes for tunnel "network/home" returns the Route's request.

func TestHTTPRouteSource_MapTunnelToRoutes_CrossNamespace(t *testing.T) {
	s := httpRouteScheme(t)
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget:             "tunnel:home",
		AnnotationTunnelRefNamespace: "network",
	}, hostnames("foo.example.com"), nil)
	tun := newReadyTunnel("home", "network")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(route, tun).
		Build()
	r := &HTTPRouteSourceReconciler{Client: c}

	reqs := r.mapTunnelToRoutes(context.Background(), tun)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d: %v", len(reqs), reqs)
	}
	if reqs[0].Namespace != testNsApps || reqs[0].Name != testRouteName {
		t.Errorf("expected {%s/%s}, got %v", testNsApps, testRouteName, reqs[0])
	}
}

// ---- TestHTTPRouteSource_MapTunnelToRoutes_SameNamespace -------------------

func TestHTTPRouteSource_MapTunnelToRoutes_SameNamespace(t *testing.T) {
	s := httpRouteScheme(t)
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget: "tunnel:home",
		// no AnnotationTunnelRefNamespace
	}, hostnames("foo.example.com"), nil)
	tun := newReadyTunnel("home", "apps")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(route, tun).
		Build()
	r := &HTTPRouteSourceReconciler{Client: c}

	reqs := r.mapTunnelToRoutes(context.Background(), tun)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d: %v", len(reqs), reqs)
	}
	if reqs[0].Namespace != testNsApps || reqs[0].Name != testRouteName {
		t.Errorf("expected {%s/%s}, got %v", testNsApps, testRouteName, reqs[0])
	}
}

// ---- TestHTTPRouteSource_SourceRef_IsHTTPRoute -----------------------------
// TunnelRule SourceRef should have APIVersion=gateway.networking.k8s.io/v1, Kind=HTTPRoute.

func TestHTTPRouteSource_SourceRef_IsHTTPRoute(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget:         "tunnel:home",
		AnnotationTunnelUpstream: "http://backend:8080",
	}, hostnames("foo.example.com"), nil)
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules.Items))
	}
	sr := rules.Items[0].Spec.SourceRef
	if sr == nil {
		t.Fatal("expected SourceRef to be set")
	}
	if sr.APIVersion != "gateway.networking.k8s.io/v1" {
		t.Errorf("expected SourceRef.APIVersion=gateway.networking.k8s.io/v1, got %q", sr.APIVersion)
	}
	if sr.Kind != "HTTPRoute" {
		t.Errorf("expected SourceRef.Kind=HTTPRoute, got %q", sr.Kind)
	}
	if sr.Namespace != testNsApps {
		t.Errorf("expected SourceRef.Namespace=%s, got %q", testNsApps, sr.Namespace)
	}
	if sr.Name != "my-route" {
		t.Errorf("expected SourceRef.Name=my-route, got %q", sr.Name)
	}
}

// ---- TestHTTPRouteSource_ZoneRef_ExplicitWins ------------------------------

func TestHTTPRouteSource_ZoneRef_ExplicitWins(t *testing.T) {
	s := httpRouteScheme(t)
	explicitZone := newZone("my-zone", "apps", "example.com", "cf-secret-explicit")
	otherZone := newZone("other-zone", "default", "example.com", "cf-secret-other")
	tunnel := newReadyTunnel("home", "apps")
	route := newHTTPRoute("apps", "my-route", map[string]string{
		AnnotationTarget:         "tunnel:home",
		AnnotationTunnelUpstream: "http://backend:8080",
		AnnotationZoneRef:        "my-zone",
	}, hostnames("foo.example.com"), nil)
	r, _ := buildHTTPRouteReconciler(s, "owner1", route, explicitZone, otherZone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, rec := range records.Items {
		if rec.Spec.ZoneRef != nil && rec.Spec.ZoneRef.Name != "my-zone" {
			t.Errorf("expected ZoneRef my-zone, got %q", rec.Spec.ZoneRef.Name)
		}
	}
}

// ---- TestHTTPRouteSource_MapTunnelToRoutes_InheritedAnnotations ------------
// Route has NO cloudflare.io annotations itself; they come from the parent
// Gateway. mapTunnelToRoutes must still enqueue the Route when the target
// tunnel transitions (Fix 1: use mergedAnnotations instead of route.Annotations).

func TestHTTPRouteSource_MapTunnelToRoutes_InheritedAnnotations(t *testing.T) {
	s := httpRouteScheme(t)

	// Gateway in "network" carries the cloudflare.io annotations.
	gw := newGateway("network", "internal", map[string]string{
		AnnotationTarget:             "tunnel:home",
		AnnotationTunnelRefNamespace: "network",
	})

	// Route in "apps" with no cloudflare.io annotations; references the Gateway.
	route := newHTTPRoute("apps", "myapp", nil, hostnames("foo.example.com"),
		[]gwv1.ParentReference{parentRef("network", "internal")})

	// Tunnel in "network".
	tun := newReadyTunnel("home", "network")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gw, route, tun).
		Build()
	r := &HTTPRouteSourceReconciler{Client: c}

	reqs := r.mapTunnelToRoutes(context.Background(), tun)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request (inherited annotation route), got %d: %v", len(reqs), reqs)
	}
	got := reqs[0]
	if got.Namespace != "apps" || got.Name != "myapp" {
		t.Errorf("expected {apps/myapp}, got %v", got)
	}
}

// ---- TestHTTPRouteSource_FirstGatewayAddress_MultiParent_FirstUnready_SecondReady
// When the first parent Gateway has no addresses but the second does, the
// reconciler must continue past the first and use the second's address
// (Fix 2: continue instead of early-return on empty addresses).

func TestHTTPRouteSource_FirstGatewayAddress_MultiParent_FirstUnready_SecondReady(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")

	// First parent: Gateway with no addresses yet.
	gwUnready := newGateway("network", "unready", map[string]string{
		AnnotationTarget: "address",
	})

	// Second parent: Gateway with a ready address.
	gwReady := newGatewayWithAddress("network", "ready", "203.0.113.42", map[string]string{
		AnnotationTarget: "address",
	})

	// Route references both parents; first is unready, second is ready.
	route := newHTTPRoute("apps", "my-route", nil, hostnames("foo.example.com"),
		[]gwv1.ParentReference{
			parentRef("network", "unready"),
			parentRef("network", "ready"),
		})

	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, gwUnready, gwReady)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list DNS records: %v", err)
	}
	found := false
	for _, rec := range records.Items {
		if rec.Spec.Type != testRecordTypeTXT && rec.Spec.Content != nil && *rec.Spec.Content == "203.0.113.42" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected DNS record with content=203.0.113.42 from second ready Gateway; records: %+v", records.Items)
	}
}

// ---- TestHTTPRouteSource_FirstGatewayAddress_SkipNonGatewayParent ---------
// A non-Gateway ParentRef (e.g. Kind: "Service") must be skipped; the next
// Gateway parent's address should be used (Fix 2: continue on non-Gateway kind).

func TestHTTPRouteSource_FirstGatewayAddress_SkipNonGatewayParent(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")

	// Gateway parent with a ready address (the "second" parent logically).
	gwReady := newGatewayWithAddress("network", "ready", "198.51.100.7", map[string]string{
		AnnotationTarget: "address",
	})

	// Build a non-Gateway ParentRef as the first parent.
	svcKind := gwv1.Kind("Service")
	svcGroup := gwv1.Group("")
	svcNs := gwv1.Namespace("network")
	nonGatewayParent := gwv1.ParentReference{
		Group:     &svcGroup,
		Kind:      &svcKind,
		Name:      gwv1.ObjectName("some-service"),
		Namespace: &svcNs,
	}

	route := newHTTPRoute("apps", "my-route", nil, hostnames("foo.example.com"),
		[]gwv1.ParentReference{
			nonGatewayParent,
			parentRef("network", "ready"),
		})

	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, gwReady)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list DNS records: %v", err)
	}
	found := false
	for _, rec := range records.Items {
		if rec.Spec.Type != testRecordTypeTXT && rec.Spec.Content != nil && *rec.Spec.Content == "198.51.100.7" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected DNS record with content=198.51.100.7 from Gateway parent; records: %+v", records.Items)
	}
}

// ---- TestHTTPRouteSource_ReadParentAnnotations_FirstWinsWithCFKey ----------
// When multiple parent Gateways carry cloudflare.io/* annotations, the first
// one's values win (Fix 3: lock first-parent-wins semantics).

func TestHTTPRouteSource_ReadParentAnnotations_FirstWinsWithCFKey(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")

	// Both tunnels in "apps" so tunnel lookup doesn't need tunnel-ref-namespace.
	tunnelA := newReadyTunnel("tunnel-a", "apps")
	tunnelB := newReadyTunnel("tunnel-b", "apps")

	gwFirst := newGateway("apps", "first", map[string]string{
		AnnotationTarget:         "tunnel:tunnel-a",
		AnnotationTunnelUpstream: "http://backend-a:8080",
	})
	gwSecond := newGateway("apps", "second", map[string]string{
		AnnotationTarget:         "tunnel:tunnel-b",
		AnnotationTunnelUpstream: "http://backend-b:8080",
	})

	// Route references first, then second.
	route := newHTTPRoute("apps", "my-route", nil, hostnames("foo.example.com"),
		[]gwv1.ParentReference{
			parentRef("apps", "first"),
			parentRef("apps", "second"),
		})

	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, gwFirst, gwSecond, tunnelA, tunnelB)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// TunnelRule should reference tunnel-a (from the first Gateway), not tunnel-b.
	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 1 {
		t.Fatalf("expected 1 TunnelRule, got %d", len(rules.Items))
	}
	if rules.Items[0].Spec.TunnelRef.Name != "tunnel-a" {
		t.Errorf("expected TunnelRef.Name=tunnel-a (first Gateway wins), got %q", rules.Items[0].Spec.TunnelRef.Name)
	}
}

// ---- TestHTTPRouteSource_ReadParentAnnotations_SkipsNonGatewayKind ---------
// A non-Gateway kind in ParentRefs must be skipped when reading parent
// annotations; the first actual Gateway's annotations should be used (Fix 4).

func TestHTTPRouteSource_ReadParentAnnotations_SkipsNonGatewayKind(t *testing.T) {
	s := httpRouteScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("gw-tunnel", "apps")

	gw := newGateway("apps", "real-gw", map[string]string{
		AnnotationTarget:         "tunnel:gw-tunnel",
		AnnotationTunnelUpstream: "http://gw-backend:8080",
	})

	// Build a non-Gateway ParentRef as the first parent.
	svcKind := gwv1.Kind("Service")
	svcGroup := gwv1.Group("")
	nonGatewayParent := gwv1.ParentReference{
		Group: &svcGroup,
		Kind:  &svcKind,
		Name:  gwv1.ObjectName("some-service"),
	}

	route := newHTTPRoute("apps", "my-route", nil, hostnames("foo.example.com"),
		[]gwv1.ParentReference{
			nonGatewayParent,
			parentRef("apps", "real-gw"),
		})

	r, _ := buildHTTPRouteReconciler(s, "owner1", route, zone, gw, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-route"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should emit rule using the Gateway's tunnel (non-Gateway parent was skipped).
	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 1 {
		t.Fatalf("expected 1 TunnelRule (Gateway annotations used), got %d", len(rules.Items))
	}
	if rules.Items[0].Spec.TunnelRef.Name != "gw-tunnel" {
		t.Errorf("expected TunnelRef.Name=gw-tunnel (Gateway parent used), got %q", rules.Items[0].Spec.TunnelRef.Name)
	}
}
