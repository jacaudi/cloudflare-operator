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

// nn is a small helper for building NamespacedName lookups in tests.
func nn(ns, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: ns, Name: name}
}

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

	result := Aggregate("test-tunnel-id", rules, routing)

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
		if result.Decisions[nn("apps", name)].Status != RuleIncluded {
			t.Errorf("%s: expected Included, got %v", name, result.Decisions[nn("apps", name)])
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
	result := Aggregate("test-tunnel-id", rules, nil)

	if result.Decisions[nn("apps", "earlier")].Status != RuleIncluded {
		t.Errorf("earlier should win, got decision %+v", result.Decisions[nn("apps", "earlier")])
	}
	if result.Decisions[nn("apps", "later")].Status != RuleDuplicateHostname {
		t.Errorf("later should be rejected as duplicate, got %+v", result.Decisions[nn("apps", "later")])
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

// TestAggregate_DuplicateHostname_SwapEvictsHigherPriority exercises the
// swap path: higher-priority rule X is processed first (because Aggregate
// orders by priority desc), but rule Y has an earlier creationTimestamp and
// thus wins the first-writer tiebreak — X is evicted from the included set
// and Y takes the hostname.
func TestAggregate_DuplicateHostname_SwapEvictsHigherPriority(t *testing.T) {
	early := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)
	rules := []cloudflarev1alpha1.CloudflareTunnelRule{
		// X: high priority but later creation — processed first by sort.
		ruleAt("x-high", "apps", late, 200, "home", []string{"a.example.com"}, "http://x:8080"),
		// Y: lower priority but earlier creation — wins the first-writer tiebreak.
		ruleAt("y-low", "apps", early, 50, "home", []string{"a.example.com"}, "http://y:8080"),
	}
	result := Aggregate("test-tunnel-id", rules, nil)

	if got := result.Decisions[nn("apps", "y-low")].Status; got != RuleIncluded {
		t.Errorf("y-low (earlier creationTimestamp) should be Included, got %v", got)
	}
	if got := result.Decisions[nn("apps", "x-high")].Status; got != RuleDuplicateHostname {
		t.Errorf("x-high should be evicted as DuplicateHostname, got %v", got)
	}
	rendered := string(result.Rendered)
	if !strings.Contains(rendered, "http://y:8080") {
		t.Errorf("expected y's backend in render:\n%s", rendered)
	}
	if strings.Contains(rendered, "http://x:8080") {
		t.Errorf("did NOT expect x's backend in render after eviction:\n%s", rendered)
	}
	if len(result.Decisions) != len(rules) {
		t.Errorf("expected %d decisions, got %d", len(rules), len(result.Decisions))
	}
}

// TestAggregate_DuplicateHostname_ReleasedClaimReusable verifies that when an
// included rule is evicted, all hostnames it had claimed are released, so a
// later-processed rule can claim the now-free hostname.
//
// Setup:
//   - P (mid priority, mid creationTimestamp): claims A AND B.
//   - Q (highest priority, earliest creationTimestamp): claims A. Processed
//     before P due to priority. Q wins fresh; nothing yet on A.
//   - Wait, that won't trigger eviction. Reorder so the swap happens:
//   - P (highest priority, MID creation): claims A,B. Processed first.
//   - Q (mid priority, EARLIEST creation): claims A. Processed after P, finds
//     conflict on A, wins tiebreak by earlier creation, evicts P (releasing
//     both A AND B).
//   - R (lowest priority, LATEST creation): claims B. Processed last; B is
//     free again because P was evicted, so R is included.
func TestAggregate_DuplicateHostname_ReleasedClaimReusable(t *testing.T) {
	earliest := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)
	latest := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	rules := []cloudflarev1alpha1.CloudflareTunnelRule{
		ruleAt("p", "apps", mid, 300, "home", []string{"a.example.com", "b.example.com"}, "http://p:8080"),
		ruleAt("q", "apps", earliest, 200, "home", []string{"a.example.com"}, "http://q:8080"),
		ruleAt("r", "apps", latest, 50, "home", []string{"b.example.com"}, "http://r:8080"),
	}
	result := Aggregate("test-tunnel-id", rules, nil)

	// P is evicted by Q (Q has earlier creationTimestamp).
	if got := result.Decisions[nn("apps", "p")].Status; got != RuleDuplicateHostname {
		t.Errorf("p should be evicted as DuplicateHostname, got %v", got)
	}
	// Q is included.
	if got := result.Decisions[nn("apps", "q")].Status; got != RuleIncluded {
		t.Errorf("q should be Included, got %v", got)
	}
	// R must be included because P's claim on B was released when P was evicted.
	if got := result.Decisions[nn("apps", "r")].Status; got != RuleIncluded {
		t.Errorf("r should be Included (B was released by P's eviction), got %v", got)
	}

	rendered := string(result.Rendered)
	if !strings.Contains(rendered, "http://q:8080") {
		t.Errorf("expected q's backend in render:\n%s", rendered)
	}
	if !strings.Contains(rendered, "http://r:8080") {
		t.Errorf("expected r's backend in render (released-claim reuse):\n%s", rendered)
	}
	if strings.Contains(rendered, "http://p:8080") {
		t.Errorf("did NOT expect p's backend in render after eviction:\n%s", rendered)
	}
	// R must specifically be paired with hostname b.example.com.
	if !strings.Contains(rendered, "hostname: b.example.com\n    service: http://r:8080") {
		t.Errorf("expected R to be the rule rendered for b.example.com:\n%s", rendered)
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
	if Aggregate("test-tunnel-id", rulesA, nil).ConfigHash != Aggregate("test-tunnel-id", rulesB, nil).ConfigHash {
		t.Fatal("hash must be stable regardless of input order")
	}
}

// TestAggregate_HashChangesWithRouting locks in that routing.DefaultBackend
// participates in the rendered config and therefore the ConfigHash. If this
// test ever passes for the wrong reason (i.e. hashes match), the rollout
// gating in the tunnel controller would silently miss routing changes.
func TestAggregate_HashChangesWithRouting(t *testing.T) {
	t0 := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	rules := []cloudflarev1alpha1.CloudflareTunnelRule{
		ruleAt("a", "apps", t0, 100, "home", []string{"a.example.com"}, "http://a:8080"),
	}
	routing := &cloudflarev1alpha1.TunnelRoutingSpec{
		DefaultBackend: &cloudflarev1alpha1.TunnelRuleBackend{URL: strPtr("https://default.svc:443")},
	}
	hashWithout := Aggregate("test-tunnel-id", rules, nil).ConfigHash
	hashWith := Aggregate("test-tunnel-id", rules, routing).ConfigHash
	if hashWithout == hashWith {
		t.Fatalf("ConfigHash must differ when routing.DefaultBackend changes; both = %s", hashWithout)
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
	result := Aggregate("test-tunnel-id", []cloudflarev1alpha1.CloudflareTunnelRule{r}, nil)
	got := string(result.Rendered)
	if !strings.Contains(got, "http://rickroll.selfhosted.svc.cluster.local:8080") {
		t.Errorf("expected resolved serviceRef in render:\n%s", got)
	}
}

// TestAggregate_RuleInvalid covers the validation path that produces a
// RuleInvalid decision and excludes the rule from the rendered config.
func TestAggregate_RuleInvalid(t *testing.T) {
	t0 := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)

	t.Run("empty hostnames", func(t *testing.T) {
		bad := cloudflarev1alpha1.CloudflareTunnelRule{
			ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: "apps", UID: "bad-uid", CreationTimestamp: metav1.NewTime(t0)},
			Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
				TunnelRef: cloudflarev1alpha1.TunnelReference{Name: "home"},
				Hostnames: nil,
				Backend:   cloudflarev1alpha1.TunnelRuleBackend{URL: strPtr("http://x:8080")},
				Priority:  100,
			},
		}
		good := ruleAt("good", "apps", t0, 100, "home", []string{"good.example.com"}, "http://good:8080")
		result := Aggregate("test-tunnel-id", []cloudflarev1alpha1.CloudflareTunnelRule{bad, good}, nil)

		if got := result.Decisions[nn("apps", "bad")].Status; got != RuleInvalid {
			t.Errorf("bad rule status = %v, want RuleInvalid", got)
		}
		if msg := result.Decisions[nn("apps", "bad")].Message; !strings.Contains(msg, "hostnames") {
			t.Errorf("bad rule Message should mention hostnames, got %q", msg)
		}
		if strings.Contains(string(result.Rendered), "http://x:8080") {
			t.Errorf("rendered config must not contain invalid rule's backend:\n%s", string(result.Rendered))
		}
		if len(result.Decisions) != 2 {
			t.Errorf("expected 2 decisions, got %d", len(result.Decisions))
		}
	})

	t.Run("multi-set backend", func(t *testing.T) {
		status := 503
		bad := cloudflarev1alpha1.CloudflareTunnelRule{
			ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: "apps", UID: "bad-uid", CreationTimestamp: metav1.NewTime(t0)},
			Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
				TunnelRef: cloudflarev1alpha1.TunnelReference{Name: "home"},
				Hostnames: []string{"bad.example.com"},
				Backend: cloudflarev1alpha1.TunnelRuleBackend{
					URL:        strPtr("http://x:8080"),
					HTTPStatus: &status,
				},
				Priority: 100,
			},
		}
		result := Aggregate("test-tunnel-id", []cloudflarev1alpha1.CloudflareTunnelRule{bad}, nil)

		if got := result.Decisions[nn("apps", "bad")].Status; got != RuleInvalid {
			t.Errorf("bad rule status = %v, want RuleInvalid", got)
		}
		if msg := result.Decisions[nn("apps", "bad")].Message; !strings.Contains(msg, "exactly one") {
			t.Errorf("bad rule Message should mention 'exactly one', got %q", msg)
		}
		if strings.Contains(string(result.Rendered), "bad.example.com") {
			t.Errorf("rendered config must not contain invalid rule's hostname:\n%s", string(result.Rendered))
		}
	})
}

// TestAggregate_WildcardSortsAfterSpecific verifies that at equal priority, a
// literal hostname renders before a wildcard hostname even when the wildcard
// rule's name sorts lexicographically first. This guards against the shadowing
// bug described in adyanth/cloudflare-operator#147: cloudflared matches
// top-down, so a wildcard that renders first would shadow any literal that
// follows.
func TestAggregate_WildcardSortsAfterSpecific(t *testing.T) {
	t0 := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	rules := []cloudflarev1alpha1.CloudflareTunnelRule{
		// "alpha" sorts before "beta" lexicographically; alpha has the wildcard.
		ruleAt("alpha", "apps", t0, 100, "home", []string{"*.example.com"}, "http://alpha:8080"),
		ruleAt("beta", "apps", t0, 100, "home", []string{"api.example.com"}, "http://beta:8080"),
	}
	result := Aggregate("test-tunnel-id", rules, nil)

	got := string(result.Rendered)
	idxLiteral := strings.Index(got, "api.example.com")
	idxWildcard := strings.Index(got, "*.example.com")
	if idxLiteral < 0 {
		t.Fatalf("api.example.com missing from render:\n%s", got)
	}
	if idxWildcard < 0 {
		t.Fatalf("*.example.com missing from render:\n%s", got)
	}
	if idxLiteral > idxWildcard {
		t.Errorf("wildcard *.example.com must render AFTER literal api.example.com; got wrong order:\n%s", got)
	}
	// Both rules are included (no duplicate hostname).
	if result.Decisions[nn("apps", "alpha")].Status != RuleIncluded {
		t.Errorf("alpha: want Included, got %v", result.Decisions[nn("apps", "alpha")])
	}
	if result.Decisions[nn("apps", "beta")].Status != RuleIncluded {
		t.Errorf("beta: want Included, got %v", result.Decisions[nn("apps", "beta")])
	}
}

// TestAggregate_WildcardSpecificityWithinRule verifies that a single rule
// whose hostnames slice contains a mix of wildcards and literals renders them
// in specificity order (more-specific first) rather than input order.
func TestAggregate_WildcardSpecificityWithinRule(t *testing.T) {
	t0 := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	rules := []cloudflarev1alpha1.CloudflareTunnelRule{
		// Input order: wildcard-broad, literal, wildcard-narrow — render must reorder.
		ruleAt("mixed", "apps", t0, 100, "home",
			[]string{"*.example.com", "api.example.com", "*.api.example.com"},
			"http://mixed:8080"),
	}
	result := Aggregate("test-tunnel-id", rules, nil)

	got := string(result.Rendered)
	idxLiteral := strings.Index(got, "api.example.com")
	idxNarrowWild := strings.Index(got, "*.api.example.com")
	idxBroadWild := strings.Index(got, "*.example.com")
	for _, pair := range []struct {
		name  string
		token string
		idx   int
	}{
		{"api.example.com", "api.example.com", idxLiteral},
		{"*.api.example.com", "*.api.example.com", idxNarrowWild},
		{"*.example.com", "*.example.com", idxBroadWild},
	} {
		if pair.idx < 0 {
			t.Fatalf("%s missing from render:\n%s", pair.name, got)
		}
	}
	// Expected order: api.example.com < *.api.example.com < *.example.com
	if idxLiteral > idxNarrowWild {
		t.Errorf("api.example.com must render before *.api.example.com:\n%s", got)
	}
	if idxNarrowWild > idxBroadWild {
		t.Errorf("*.api.example.com must render before *.example.com:\n%s", got)
	}
}

// TestAggregate_PriorityBeatsSpecificity verifies that priority is the primary
// sort key: a higher-priority wildcard rule must render before a lower-priority
// literal rule, even though specificity would prefer the literal. Specificity
// is a tiebreak only at equal priority.
func TestAggregate_PriorityBeatsSpecificity(t *testing.T) {
	t0 := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	rules := []cloudflarev1alpha1.CloudflareTunnelRule{
		ruleAt("wildcard-high", "apps", t0, 200, "home", []string{"*.example.com"}, "http://wild:8080"),
		ruleAt("literal-low", "apps", t0, 100, "home", []string{"api.example.com"}, "http://lit:8080"),
	}
	result := Aggregate("test-tunnel-id", rules, nil)

	got := string(result.Rendered)
	idxWildcard := strings.Index(got, "*.example.com")
	idxLiteral := strings.Index(got, "api.example.com")
	if idxWildcard < 0 {
		t.Fatalf("*.example.com missing from render:\n%s", got)
	}
	if idxLiteral < 0 {
		t.Fatalf("api.example.com missing from render:\n%s", got)
	}
	// Higher priority must come first, even though it's a wildcard.
	if idxWildcard > idxLiteral {
		t.Errorf("higher-priority wildcard must render BEFORE lower-priority literal:\n%s", got)
	}
	if result.Decisions[nn("apps", "wildcard-high")].Status != RuleIncluded {
		t.Errorf("wildcard-high: want Included, got %v", result.Decisions[nn("apps", "wildcard-high")])
	}
	if result.Decisions[nn("apps", "literal-low")].Status != RuleIncluded {
		t.Errorf("literal-low: want Included, got %v", result.Decisions[nn("apps", "literal-low")])
	}
}

// TestHostnameSpecificity verifies the specificity tuple (wildcardCount asc,
// labelCount desc, hostname asc) returned by hostnameSpecificity.
func TestHostnameSpecificity(t *testing.T) {
	cases := []struct {
		hostname      string
		wantWildcards int
		wantLabels    int
	}{
		{"api.example.com", 0, 3},
		{"*.example.com", 1, 3},
		{"*.api.example.com", 1, 4},
		{"*.*.example.com", 2, 4},
		{"example.com", 0, 2},
	}
	for _, tc := range cases {
		t.Run(tc.hostname, func(t *testing.T) {
			gotW, gotL := hostnameSpecificity(tc.hostname)
			if gotW != tc.wantWildcards {
				t.Errorf("hostnameSpecificity(%q) wildcardCount = %d, want %d", tc.hostname, gotW, tc.wantWildcards)
			}
			if gotL != tc.wantLabels {
				t.Errorf("hostnameSpecificity(%q) labelCount = %d, want %d", tc.hostname, gotL, tc.wantLabels)
			}
		})
	}
}

// TestAggregate_RendersTunnelIdentityHeader verifies that the rendered
// config.yaml contains top-level `tunnel:` and `credentials-file:` keys
// ahead of the `ingress:` block. cloudflared resolves the tunnel from one
// of these (#58).
func TestAggregate_RendersTunnelIdentityHeader(t *testing.T) {
	t0 := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	rule := ruleAt("r", "apps", t0, 100, "home", []string{"h.example.com"}, "http://b:8080")
	result := Aggregate("a-tunnel-id-1234", []cloudflarev1alpha1.CloudflareTunnelRule{rule}, nil)
	got := string(result.Rendered)

	tunnelIdx := strings.Index(got, "tunnel: a-tunnel-id-1234\n")
	credsIdx := strings.Index(got, "credentials-file: /etc/cloudflared/credentials/credentials.json\n")
	ingressIdx := strings.Index(got, "ingress:\n")

	if tunnelIdx < 0 {
		t.Errorf("rendered config missing `tunnel:` header:\n%s", got)
	}
	if credsIdx < 0 {
		t.Errorf("rendered config missing `credentials-file:` header:\n%s", got)
	}
	if ingressIdx < 0 {
		t.Fatalf("rendered config missing `ingress:` block:\n%s", got)
	}
	if !(tunnelIdx < ingressIdx && credsIdx < ingressIdx) {
		t.Errorf("identity header must precede ingress: block:\n%s", got)
	}
}

// TestAggregate_TunnelIDChangesHash verifies that two Aggregate calls with
// the same rules but different tunnel IDs produce different ConfigHash
// values. This drives connector pod rolls when a tunnel is re-adopted.
func TestAggregate_TunnelIDChangesHash(t *testing.T) {
	t0 := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	rules := []cloudflarev1alpha1.CloudflareTunnelRule{
		ruleAt("r", "apps", t0, 100, "home", []string{"h.example.com"}, "http://b:8080"),
	}
	hashA := Aggregate("tunnel-id-A", rules, nil).ConfigHash
	hashB := Aggregate("tunnel-id-B", rules, nil).ConfigHash
	if hashA == hashB {
		t.Errorf("ConfigHash should differ across tunnel IDs: A=%q B=%q", hashA, hashB)
	}
}

// TestMergedOriginRequest exercises the originRequest merge semantics
// indirectly through Aggregate's rendered output. mergedOriginRequest is not
// exposed for direct testing.
func TestMergedOriginRequest(t *testing.T) {
	t0 := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	mkRule := func(own *cloudflarev1alpha1.TunnelRuleOriginRequest) cloudflarev1alpha1.CloudflareTunnelRule {
		return cloudflarev1alpha1.CloudflareTunnelRule{
			ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "apps", UID: "r-uid", CreationTimestamp: metav1.NewTime(t0)},
			Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
				TunnelRef:     cloudflarev1alpha1.TunnelReference{Name: "home"},
				Hostnames:     []string{"h.example.com"},
				Backend:       cloudflarev1alpha1.TunnelRuleBackend{URL: strPtr("http://b:8080")},
				Priority:      100,
				OriginRequest: own,
			},
		}
	}

	t.Run("tunnel default only", func(t *testing.T) {
		routing := &cloudflarev1alpha1.TunnelRoutingSpec{
			OriginRequest: &cloudflarev1alpha1.TunnelRuleOriginRequest{
				NoTLSVerify:      true,
				OriginServerName: "tunnel.example.com",
			},
		}
		got := string(Aggregate("test-tunnel-id", []cloudflarev1alpha1.CloudflareTunnelRule{mkRule(nil)}, routing).Rendered)
		if !strings.Contains(got, "originRequest:") {
			t.Errorf("expected originRequest block in render:\n%s", got)
		}
		if !strings.Contains(got, "noTLSVerify: true") {
			t.Errorf("expected tunnel noTLSVerify:true in render:\n%s", got)
		}
		if !strings.Contains(got, "originServerName: tunnel.example.com") {
			t.Errorf("expected tunnel originServerName in render:\n%s", got)
		}
	})

	t.Run("rule only", func(t *testing.T) {
		own := &cloudflarev1alpha1.TunnelRuleOriginRequest{
			NoTLSVerify:      true,
			OriginServerName: "rule.example.com",
		}
		got := string(Aggregate("test-tunnel-id", []cloudflarev1alpha1.CloudflareTunnelRule{mkRule(own)}, nil).Rendered)
		if !strings.Contains(got, "noTLSVerify: true") {
			t.Errorf("expected rule noTLSVerify:true in render:\n%s", got)
		}
		if !strings.Contains(got, "originServerName: rule.example.com") {
			t.Errorf("expected rule originServerName in render:\n%s", got)
		}
	})

	t.Run("both set: rule wins entirely", func(t *testing.T) {
		routing := &cloudflarev1alpha1.TunnelRoutingSpec{
			OriginRequest: &cloudflarev1alpha1.TunnelRuleOriginRequest{
				NoTLSVerify:      true,
				OriginServerName: "tunnel.example.com",
				HTTPHostHeader:   "tunnel-host",
			},
		}
		own := &cloudflarev1alpha1.TunnelRuleOriginRequest{
			NoTLSVerify:      true,
			OriginServerName: "rule.example.com",
			// No HTTPHostHeader on rule — must NOT inherit from tunnel.
		}
		got := string(Aggregate("test-tunnel-id", []cloudflarev1alpha1.CloudflareTunnelRule{mkRule(own)}, routing).Rendered)
		if !strings.Contains(got, "originServerName: rule.example.com") {
			t.Errorf("expected rule originServerName to win:\n%s", got)
		}
		if strings.Contains(got, "originServerName: tunnel.example.com") {
			t.Errorf("did NOT expect tunnel originServerName when rule overrides:\n%s", got)
		}
		if strings.Contains(got, "tunnel-host") {
			t.Errorf("did NOT expect tunnel HTTPHostHeader to leak through (rule wins entirely):\n%s", got)
		}
	})

	t.Run("rule explicit false overrides tunnel true (no noTLSVerify line)", func(t *testing.T) {
		routing := &cloudflarev1alpha1.TunnelRoutingSpec{
			OriginRequest: &cloudflarev1alpha1.TunnelRuleOriginRequest{
				NoTLSVerify: true,
			},
		}
		// Rule sets NoTLSVerify=false (zero value) but provides a non-nil
		// OriginRequest — per the documented merge semantic, the rule wins
		// entirely, so the rendered output must NOT contain a noTLSVerify line
		// (cloudflared's default of false then takes effect).
		own := &cloudflarev1alpha1.TunnelRuleOriginRequest{
			NoTLSVerify:      false,
			OriginServerName: "rule.example.com",
		}
		got := string(Aggregate("test-tunnel-id", []cloudflarev1alpha1.CloudflareTunnelRule{mkRule(own)}, routing).Rendered)
		if strings.Contains(got, "noTLSVerify") {
			t.Errorf("rule's NoTLSVerify=false must suppress the noTLSVerify line entirely (rule wins):\n%s", got)
		}
		if !strings.Contains(got, "originServerName: rule.example.com") {
			t.Errorf("expected rule originServerName in render:\n%s", got)
		}
	})

	t.Run("neither set: no originRequest block", func(t *testing.T) {
		got := string(Aggregate("test-tunnel-id", []cloudflarev1alpha1.CloudflareTunnelRule{mkRule(nil)}, nil).Rendered)
		if strings.Contains(got, "originRequest") {
			t.Errorf("did NOT expect originRequest block when neither side sets it:\n%s", got)
		}
	})
}
