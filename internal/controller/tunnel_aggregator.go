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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

// RuleDecisionStatus is the per-rule verdict from an aggregation pass.
type RuleDecisionStatus string

const (
	// RuleIncluded means the rule is present in the rendered config.yaml.
	RuleIncluded RuleDecisionStatus = "Included"
	// RuleDuplicateHostname means the rule was excluded because a
	// higher-precedence rule already claimed one of its hostnames.
	RuleDuplicateHostname RuleDecisionStatus = "DuplicateHostname"
	// RuleInvalid means the rule's spec did not pass semantic validation
	// (exactly-one-backend rule, non-empty hostnames).
	RuleInvalid RuleDecisionStatus = "Invalid"
)

// originRequestHeader is the literal indented header line written before any
// originRequest field rendering. Centralising the string lets the empty-block
// guard (in renderOriginRequest) compare lengths against the header, instead
// of duplicating the literal — so changes to indent don't silently break
// suppression.
const originRequestHeader = "    originRequest:\n"

// RuleDecision records why a rule was or was not included.
type RuleDecision struct {
	Status          RuleDecisionStatus
	Message         string
	ResolvedBackend string // URL actually written to config.yaml; "" if not included
}

// AggregationResult is the output of Aggregate.
type AggregationResult struct {
	// Rendered is the cloudflared config.yaml content.
	Rendered []byte
	// ConfigHash is sha256 of Rendered, hex-encoded.
	ConfigHash string
	// Decisions maps ruleKey -> per-rule verdict.
	Decisions map[types.NamespacedName]RuleDecision
}

// Aggregate produces the rendered config.yaml content and per-rule verdicts
// for a set of CloudflareTunnelRule CRs targeting the same tunnel, plus the
// tunnel's spec.routing defaults. tunnelID is the parent tunnel's
// Status.TunnelID; it is included in the rendered config.yaml header and
// therefore contributes to ConfigHash.
//
// Ordering rules (spec §4.3):
//  1. Rules sorted by spec.priority desc, then metadata.name asc.
//  2. Duplicate hostnames resolved by first writer (creationTimestamp asc,
//     then UID asc); losers get RuleDuplicateHostname.
//  3. config.yaml order: included rules, then either spec.routing.defaultBackend
//     (if set) OR a final http_status:404 catch-all — never both. When
//     defaultBackend is set it IS the catch-all (cloudflared treats an entry
//     without `hostname:` as a wildcard); appending http_status:404 after it
//     would render that 404 unreachable and, when no rules precede the
//     defaultBackend, cloudflared rejects the config outright (#66).
func Aggregate(tunnelID string, rules []cloudflarev1alpha1.CloudflareTunnelRule, routing *cloudflarev1alpha1.TunnelRoutingSpec) AggregationResult {
	decisions := map[types.NamespacedName]RuleDecision{}
	claims := map[string]types.NamespacedName{} // hostname -> winning rule key

	// Validate each rule first.
	type workItem struct {
		rule *cloudflarev1alpha1.CloudflareTunnelRule
		key  types.NamespacedName
	}
	var work []workItem
	for i := range rules {
		r := &rules[i]
		k := types.NamespacedName{Namespace: r.Namespace, Name: r.Name}
		if !r.Spec.Backend.IsExactlyOne() {
			decisions[k] = RuleDecision{Status: RuleInvalid, Message: "exactly one of backend.serviceRef, backend.url, backend.httpStatus must be set"}
			continue
		}
		if len(r.Spec.Hostnames) == 0 {
			decisions[k] = RuleDecision{Status: RuleInvalid, Message: "spec.hostnames must not be empty"}
			continue
		}
		work = append(work, workItem{rule: r, key: k})
	}

	// Priority desc, name asc, namespace asc as final tiebreak.
	sort.SliceStable(work, func(i, j int) bool {
		a, b := work[i].rule, work[j].rule
		if a.Spec.Priority != b.Spec.Priority {
			return a.Spec.Priority > b.Spec.Priority
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Namespace < b.Namespace
	})

	// Pre-build a key->rule lookup so the swap path doesn't do an O(N) scan
	// of work for every conflict.
	byKey := make(map[types.NamespacedName]*cloudflarev1alpha1.CloudflareTunnelRule, len(work))
	for _, w := range work {
		byKey[w.key] = w.rule
	}

	// Walk in priority order; for each hostname, if a prior claim exists,
	// run the first-writer-wins tiebreak and either swap-out or reject.
	var included []*cloudflarev1alpha1.CloudflareTunnelRule
	resolvedBackend := map[types.NamespacedName]string{}
	includedSet := map[types.NamespacedName]bool{}
	for _, w := range work {
		conflictWith := types.NamespacedName{}
		for _, h := range w.rule.Spec.Hostnames {
			if prior, ok := claims[h]; ok {
				conflictWith = prior
				break
			}
		}
		if conflictWith.Name != "" {
			priorRule := byKey[conflictWith]
			// First-writer tiebreak: whichever rule has an earlier
			// creationTimestamp (then UID) wins, regardless of priority order.
			if earlier(w.rule, priorRule) {
				// Swap: evict prior, include current.
				includedSet[conflictWith] = false
				decisions[conflictWith] = RuleDecision{
					Status:  RuleDuplicateHostname,
					Message: fmt.Sprintf("evicted by earlier rule %s/%s", w.rule.Namespace, w.rule.Name),
				}
				// Release prior's hostname claims so other rules can take them.
				for hostname, owner := range claims {
					if owner == conflictWith {
						delete(claims, hostname)
					}
				}
				// Fall through to claim as if fresh.
			} else {
				decisions[w.key] = RuleDecision{
					Status:  RuleDuplicateHostname,
					Message: fmt.Sprintf("hostname already claimed by %s/%s", conflictWith.Namespace, conflictWith.Name),
				}
				continue
			}
		}
		// Claim all hostnames for this rule.
		for _, h := range w.rule.Spec.Hostnames {
			claims[h] = w.key
		}
		included = append(included, w.rule)
		includedSet[w.key] = true
	}

	// Drop any rule whose earlier include got flipped to false during a swap.
	final := included[:0]
	for _, r := range included {
		k := types.NamespacedName{Namespace: r.Namespace, Name: r.Name}
		if includedSet[k] {
			final = append(final, r)
		}
	}
	included = final

	// Flatten included rules to per-(rule, hostname) entries for render ordering.
	// This allows specificity-based tiebreaking within and across rules at equal
	// priority — without this, a rule with hostnames=["*.example.com",
	// "api.example.com"] would emit them in input order, and a wildcard rule
	// whose name sorts before a literal rule at equal priority would shadow the
	// literal in cloudflared's top-down first-match evaluation.
	//
	// Sort key: (priority desc, wildcardCount asc, labelCount desc, hostname asc,
	// namespace asc, name asc). Priority is the primary key; specificity is a
	// tiebreak at equal priority. See hostnameSpecificity for the specificity
	// tuple definition.
	totalHostnames := 0
	for _, r := range included {
		totalHostnames += len(r.Spec.Hostnames)
	}
	entries := make([]renderEntry, 0, totalHostnames)
	for _, r := range included {
		for _, h := range r.Spec.Hostnames {
			entries = append(entries, renderEntry{rule: r, hostname: h})
		}
	}
	sort.SliceStable(entries, renderEntryLess(entries))

	// Render in flat order and record resolved backends. Identity header
	// (tunnel + credentials-file) is written first so cloudflared can resolve
	// the tunnel from the config alone — fixes #58, where the operator-managed
	// Deployment crash-looped because neither Args nor config carried the
	// tunnel identity.
	var b strings.Builder
	fmt.Fprintf(&b, "tunnel: %s\n", tunnelID)
	b.WriteString("credentials-file: /etc/cloudflared/credentials/credentials.json\n")
	b.WriteString("ingress:\n")
	for _, e := range entries {
		r := e.rule
		ub := renderBackend(&r.Spec.Backend, r.Namespace)
		resolvedBackend[types.NamespacedName{Namespace: r.Namespace, Name: r.Name}] = ub
		fmt.Fprintf(&b, "  - hostname: %s\n    service: %s\n", e.hostname, ub)
		if oReq := mergedOriginRequest(r, routing); oReq != "" {
			b.WriteString(oReq)
		}
	}
	switch {
	case routing != nil && routing.DefaultBackend != nil:
		fmt.Fprintf(&b, "  - service: %s\n", renderBackend(routing.DefaultBackend, ""))
		// defaultBackend inherits tunnel-level originRequest defaults; without
		// this the catch-all silently ignores originServerName / noTLSVerify /
		// httpHostHeader (#81) and SNI-sensitive upstreams (e.g. Envoy Gateway)
		// reset every connection.
		if routing.OriginRequest != nil {
			b.WriteString(renderOriginRequest(routing.OriginRequest))
		}
	default:
		b.WriteString("  - service: http_status:404\n")
	}

	sum := sha256.Sum256([]byte(b.String()))
	for k, ok := range includedSet {
		if ok {
			decisions[k] = RuleDecision{
				Status:          RuleIncluded,
				ResolvedBackend: resolvedBackend[k],
			}
		}
	}
	return AggregationResult{
		Rendered:   []byte(b.String()),
		ConfigHash: hex.EncodeToString(sum[:]),
		Decisions:  decisions,
	}
}

// renderEntry is a flattened per-(rule, hostname) unit used during the render
// phase of Aggregate. Flattening allows the specificity sort to reorder
// hostnames both within a single rule and across rules at equal priority.
type renderEntry struct {
	rule     *cloudflarev1alpha1.CloudflareTunnelRule
	hostname string
}

// renderEntryLess returns a less function for sort.SliceStable over a
// []renderEntry. Sort key: (priority desc, wildcardCount asc, labelCount desc,
// hostname asc, namespace asc, name asc).
func renderEntryLess(entries []renderEntry) func(i, j int) bool {
	return func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.rule.Spec.Priority != b.rule.Spec.Priority {
			return a.rule.Spec.Priority > b.rule.Spec.Priority
		}
		aw, al := hostnameSpecificity(a.hostname)
		bw, bl := hostnameSpecificity(b.hostname)
		if aw != bw {
			return aw < bw // fewer wildcards = more specific = earlier
		}
		if al != bl {
			return al > bl // more labels = more specific = earlier
		}
		if a.hostname != b.hostname {
			return a.hostname < b.hostname
		}
		if a.rule.Namespace != b.rule.Namespace {
			return a.rule.Namespace < b.rule.Namespace
		}
		return a.rule.Name < b.rule.Name
	}
}

// hostnameSpecificity returns a two-element tuple used to order hostnames so
// that more-specific entries render before broader wildcards. cloudflared
// evaluates ingress rules top-down with first-match semantics (equivalent to
// adyanth/cloudflare-operator issue #147): if *.example.com appears before
// api.example.com, cloudflared matches api.example.com requests to the
// wildcard and api.example.com's rule is never reached.
//
// The tuple is compared as (wildcardCount asc, labelCount desc):
//   - wildcardCount: number of '*' characters. Zero wildcards = most specific.
//   - labelCount: len(strings.Split(hostname, ".")). More labels = more
//     specific (e.g. *.api.example.com has 4 labels vs *.example.com's 3).
//
// Callers that need a fully deterministic tiebreak should also compare the
// hostname string lexicographically after this tuple.
func hostnameSpecificity(hostname string) (wildcardCount int, labelCount int) {
	wildcardCount = strings.Count(hostname, "*")
	labelCount = len(strings.Split(hostname, "."))
	return wildcardCount, labelCount
}

// earlier returns true when rule a has an earlier creationTimestamp than b,
// or, if timestamps are equal, when a's UID sorts before b's UID. UIDs are
// RFC4122-style strings, deterministic across the cluster, so lexicographic
// ordering serves as a stable (semantically arbitrary) tiebreak.
func earlier(a, b *cloudflarev1alpha1.CloudflareTunnelRule) bool {
	at, bt := a.CreationTimestamp.Time, b.CreationTimestamp.Time
	if !at.Equal(bt) {
		return at.Before(bt)
	}
	return a.UID < b.UID
}

// renderBackend produces the cloudflared "service:" value for a TunnelRuleBackend.
// ruleNamespace is used when resolving a ServiceRef with an empty Namespace field.
func renderBackend(b *cloudflarev1alpha1.TunnelRuleBackend, ruleNamespace string) string {
	switch {
	case b.ServiceRef != nil:
		ns := b.ServiceRef.Namespace
		if ns == "" {
			ns = ruleNamespace
		}
		scheme := b.ServiceRef.Scheme
		if scheme == "" {
			scheme = "http"
		}
		// Port may be int or named; named ports render as-is — the tunnel
		// controller resolves named ports via Service lookup at reconcile time.
		port := b.ServiceRef.Port.String()
		return fmt.Sprintf("%s://%s.%s.svc.cluster.local:%s", scheme, b.ServiceRef.Name, ns, port)
	case b.URL != nil:
		return *b.URL
	case b.HTTPStatus != nil:
		return fmt.Sprintf("http_status:%d", *b.HTTPStatus)
	default:
		// Defense-in-depth fallback for a malformed routing.DefaultBackend
		// that bypassed CRD CEL validation (e.g. backend with all three fields
		// nil). Rule backends cannot reach here because Aggregate filters them
		// upstream via IsExactlyOne(). 500 (rather than 404) is chosen so
		// operators see a distinct server-error signal from the catch-all 404.
		return "http_status:500"
	}
}

// mergedOriginRequest returns an indented "originRequest:" YAML block for a
// rule, merging tunnel-level routing defaults.
//
// Merge semantics: the tunnel's routing.originRequest supplies defaults for all
// fields. When the rule has its own spec.originRequest set (non-nil), the
// rule's values take complete precedence over the tunnel defaults for every
// field — including bool fields that are zero-valued. This means a rule with
// an explicit OriginRequest block can set noTLSVerify: false to override a
// tunnel-level noTLSVerify: true. If the rule's OriginRequest is nil, tunnel
// defaults are used as-is. If both are nil, the function returns "".
func mergedOriginRequest(r *cloudflarev1alpha1.CloudflareTunnelRule, routing *cloudflarev1alpha1.TunnelRoutingSpec) string {
	var def *cloudflarev1alpha1.TunnelRuleOriginRequest
	if routing != nil {
		def = routing.OriginRequest
	}
	own := r.Spec.OriginRequest
	if own == nil && def == nil {
		return ""
	}

	// Start from tunnel defaults, then let the rule override entirely.
	merge := cloudflarev1alpha1.TunnelRuleOriginRequest{}
	if def != nil {
		merge = *def
	}
	if own != nil {
		// Rule wins on all fields when it has an explicit OriginRequest block.
		merge = *own
	}
	return renderOriginRequest(&merge)
}

// renderOriginRequest formats a TunnelRuleOriginRequest as the indented
// cloudflared YAML block. Returns "" when o is nil or every field is zero, so
// the caller emits nothing — avoids a dangling "originRequest:\n" with no
// fields underneath. Used by both the per-rule merge path (mergedOriginRequest)
// and the defaultBackend arm in Aggregate (#81), which has no rule-level
// overrides and inherits tunnel defaults directly.
func renderOriginRequest(o *cloudflarev1alpha1.TunnelRuleOriginRequest) string {
	if o == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(originRequestHeader)
	if o.NoTLSVerify {
		b.WriteString("      noTLSVerify: true\n")
	}
	if o.OriginServerName != "" {
		fmt.Fprintf(&b, "      originServerName: %s\n", o.OriginServerName)
	}
	if o.ConnectTimeout != nil {
		// cloudflared accepts Go's time.Duration string format here
		// (e.g. "30s", "1m30s") for connectTimeout.
		fmt.Fprintf(&b, "      connectTimeout: %s\n", o.ConnectTimeout.Duration.String())
	}
	if o.HTTPHostHeader != "" {
		fmt.Fprintf(&b, "      httpHostHeader: %s\n", o.HTTPHostHeader)
	}
	if b.Len() == len(originRequestHeader) {
		return ""
	}
	return b.String()
}
