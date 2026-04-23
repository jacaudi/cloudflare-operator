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
// guard (in mergedOriginRequest) compare lengths against the header, instead
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
// tunnel's spec.routing defaults.
//
// Ordering rules (spec §4.3):
//  1. Rules sorted by spec.priority desc, then metadata.name asc.
//  2. Duplicate hostnames resolved by first writer (creationTimestamp asc,
//     then UID asc); losers get RuleDuplicateHostname.
//  3. config.yaml order: included rules, then spec.routing.defaultBackend (if
//     set), then a fixed final http_status:404 catch-all.
func Aggregate(rules []cloudflarev1alpha1.CloudflareTunnelRule, routing *cloudflarev1alpha1.TunnelRoutingSpec) AggregationResult {
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

	// Render included rules and record resolved backends.
	var b strings.Builder
	b.WriteString("ingress:\n")
	for _, r := range included {
		ub := renderBackend(&r.Spec.Backend, r.Namespace)
		resolvedBackend[types.NamespacedName{Namespace: r.Namespace, Name: r.Name}] = ub
		for _, h := range r.Spec.Hostnames {
			fmt.Fprintf(&b, "  - hostname: %s\n    service: %s\n", h, ub)
			if oReq := mergedOriginRequest(r, routing); oReq != "" {
				b.WriteString(oReq)
			}
		}
	}
	if routing != nil && routing.DefaultBackend != nil {
		fmt.Fprintf(&b, "  - service: %s\n", renderBackend(routing.DefaultBackend, ""))
	}
	b.WriteString("  - service: http_status:404\n")

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

	var b strings.Builder
	b.WriteString(originRequestHeader)
	if merge.NoTLSVerify {
		b.WriteString("      noTLSVerify: true\n")
	}
	if merge.OriginServerName != "" {
		fmt.Fprintf(&b, "      originServerName: %s\n", merge.OriginServerName)
	}
	if merge.ConnectTimeout != nil {
		// cloudflared accepts Go's time.Duration string format here
		// (e.g. "30s", "1m30s") for connectTimeout.
		fmt.Fprintf(&b, "      connectTimeout: %s\n", merge.ConnectTimeout.Duration.String())
	}
	if merge.HTTPHostHeader != "" {
		fmt.Fprintf(&b, "      httpHostHeader: %s\n", merge.HTTPHostHeader)
	}
	// Empty-block suppression: if only the header was written (every merged
	// field is zero), return "" so the caller emits nothing — avoids a
	// dangling "originRequest:\n" with no fields underneath.
	if b.Len() == len(originRequestHeader) {
		return ""
	}
	return b.String()
}
