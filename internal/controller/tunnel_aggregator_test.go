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
	"strings"
	"testing"
	"time"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func ruleAt(name, ns string, created time.Time, priority int, tunnel string, hostnames []string, backendURL string) cloudflarev1alpha1.CloudflareTunnelRule {
	return cloudflarev1alpha1.CloudflareTunnelRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         ns,
			UID:               types.UID(name + "-uid"),
			CreationTimestamp: metav1.NewTime(created),
		},
		Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
			TunnelRef: cloudflarev1alpha1.TunnelReference{Name: tunnel},
			Hostnames: hostnames,
			Backend:   cloudflarev1alpha1.TunnelRuleBackend{URL: strPtr(backendURL)},
			Priority:  priority,
		},
	}
}

func strPtr(s string) *string { return &s }

func TestAggregate_SortAndRender(t *testing.T) {
	t0 := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	rules := []cloudflarev1alpha1.CloudflareTunnelRule{
		ruleAt("b-low", "apps", t0, 50, "home", []string{"low.example.com"}, "http://low:8080"),
		ruleAt("a-high", "apps", t0, 200, "home", []string{"high.example.com"}, "http://high:8080"),
		ruleAt("c-mid", "apps", t0, 100, "home", []string{"mid.example.com"}, "http://mid:8080"),
	}
	routing := &cloudflarev1alpha1.TunnelRoutingSpec{
		DefaultBackend: &cloudflarev1alpha1.TunnelRuleBackend{URL: strPtr("https://envoy.network.svc:443")},
	}

	result := Aggregate(rules, routing)

	if len(result.Rendered) == 0 {
		t.Fatal("expected rendered bytes")
	}
	// Priority 200 > 100 > 50, then default backend, then http_status:404
	got := string(result.Rendered)
	wantOrder := []string{"high.example.com", "mid.example.com", "low.example.com", "envoy.network.svc", "http_status:404"}
	prev := -1
	for _, token := range wantOrder {
		idx := strings.Index(got, token)
		if idx < 0 {
			t.Fatalf("missing %q in render:\n%s", token, got)
		}
		if idx <= prev {
			t.Fatalf("render order wrong: %q appears before expected predecessors:\n%s", token, got)
		}
		prev = idx
	}
	if result.ConfigHash == "" {
		t.Error("expected non-empty hash")
	}
	// All three rules should be Included.
	for _, name := range []string{"a-high", "b-low", "c-mid"} {
		if result.Decisions[types.NamespacedName{Namespace: "apps", Name: name}].Status != RuleIncluded {
			t.Errorf("%s: expected Included, got %v", name, result.Decisions[types.NamespacedName{Namespace: "apps", Name: name}])
		}
	}
	// Map length must match the number of rules processed.
	if len(result.Decisions) != len(rules) {
		t.Errorf("expected %d decisions, got %d", len(rules), len(result.Decisions))
	}
}

func TestAggregate_DuplicateHostname_FirstWriterWins(t *testing.T) {
	early := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)
	rules := []cloudflarev1alpha1.CloudflareTunnelRule{
		ruleAt("later", "apps", late, 100, "home", []string{"dup.example.com"}, "http://b:8080"),
		ruleAt("earlier", "apps", early, 100, "home", []string{"dup.example.com"}, "http://a:8080"),
	}
	result := Aggregate(rules, nil)

	if result.Decisions[types.NamespacedName{Namespace: "apps", Name: "earlier"}].Status != RuleIncluded {
		t.Errorf("earlier should win, got decision %+v", result.Decisions[types.NamespacedName{Namespace: "apps", Name: "earlier"}])
	}
	if result.Decisions[types.NamespacedName{Namespace: "apps", Name: "later"}].Status != RuleDuplicateHostname {
		t.Errorf("later should be rejected as duplicate, got %+v", result.Decisions[types.NamespacedName{Namespace: "apps", Name: "later"}])
	}
	// The rendered config should contain "http://a:8080" and NOT "http://b:8080".
	got := string(result.Rendered)
	if !strings.Contains(got, "http://a:8080") {
		t.Errorf("expected http://a:8080 in render:\n%s", got)
	}
	if strings.Contains(got, "http://b:8080") {
		t.Errorf("did NOT expect http://b:8080 in render:\n%s", got)
	}
	// Both rules produce a decision.
	if len(result.Decisions) != len(rules) {
		t.Errorf("expected %d decisions, got %d", len(rules), len(result.Decisions))
	}
}

func TestAggregate_HashStableAcrossShuffle(t *testing.T) {
	t0 := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	rulesA := []cloudflarev1alpha1.CloudflareTunnelRule{
		ruleAt("a", "apps", t0, 100, "home", []string{"a.example.com"}, "http://a:8080"),
		ruleAt("b", "apps", t0, 100, "home", []string{"b.example.com"}, "http://b:8080"),
	}
	rulesB := []cloudflarev1alpha1.CloudflareTunnelRule{
		ruleAt("b", "apps", t0, 100, "home", []string{"b.example.com"}, "http://b:8080"),
		ruleAt("a", "apps", t0, 100, "home", []string{"a.example.com"}, "http://a:8080"),
	}
	if Aggregate(rulesA, nil).ConfigHash != Aggregate(rulesB, nil).ConfigHash {
		t.Fatal("hash must be stable regardless of input order")
	}
}

func TestAggregate_ResolvesServiceRef(t *testing.T) {
	t0 := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	r := cloudflarev1alpha1.CloudflareTunnelRule{
		ObjectMeta: metav1.ObjectMeta{Name: "svc-rule", Namespace: "selfhosted", UID: "uid", CreationTimestamp: metav1.NewTime(t0)},
		Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
			TunnelRef: cloudflarev1alpha1.TunnelReference{Name: "home"},
			Hostnames: []string{"rickroll.example.com"},
			Backend: cloudflarev1alpha1.TunnelRuleBackend{
				ServiceRef: &cloudflarev1alpha1.TunnelRuleServiceRef{
					Name:      "rickroll",
					Namespace: "selfhosted",
					Port:      intstr.FromInt(8080),
					Scheme:    "http",
				},
			},
			Priority: 100,
		},
	}
	result := Aggregate([]cloudflarev1alpha1.CloudflareTunnelRule{r}, nil)
	got := string(result.Rendered)
	if !strings.Contains(got, "http://rickroll.selfhosted.svc.cluster.local:8080") {
		t.Errorf("expected resolved serviceRef in render:\n%s", got)
	}
}
