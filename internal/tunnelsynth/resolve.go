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

package tunnelsynth

import (
	"sort"

	cf "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

// ResolveOpts carries reconciler-supplied options for Resolve.
type ResolveOpts struct {
	// CatchAllService is the cloudflared "fall-through" service URL
	// (e.g. "http_status:404" or "http://default.svc.cluster.local:80"). The
	// resolver appends this as the last ingress entry. Required (non-empty).
	CatchAllService string
}

// Conflict reports one hostname that was contributed by multiple sources and
// the loser. Hostname-conflict resolution picks the lowest
// (Kind, Namespace, Name) lexicographic source as the winner; remaining
// contributions for that hostname are dropped from the ingress list and
// returned here so the reconciler can write DuplicateHostname conditions on
// the loser objects.
type Conflict struct {
	Hostname string
	Winner   SourceKey
	Loser    SourceKey
}

// Resolve merges a tunnel's contribution snapshot into a final
// cf.TunnelConfig. Returns conflicts so the reconciler can write
// DuplicateHostname conditions on loser source objects.
//
// Determinism rules:
//   - Winners are emitted sorted by (Hostname, Path) ascending so the wire
//     output is byte-stable.
//   - On hostname conflict (same Hostname across sources), the lowest
//     (Kind, Namespace, Name) lexicographic source wins. Other contributions
//     for that hostname are dropped from the output and reported as Conflict
//     entries with Winner+Loser set.
//   - The catch-all is appended last; its Service is ResolveOpts.CatchAllService.
//   - Conflicts are sorted by Hostname ascending.
func Resolve(contribs []ContributionWithSource, opts ResolveOpts) (cf.TunnelConfig, []Conflict) {
	// Group by hostname; within each group, keep the lowest-source winner.
	type group struct {
		winner ContributionWithSource
		losers []ContributionWithSource
	}
	groups := map[string]*group{}
	for _, c := range contribs {
		g, ok := groups[c.Hostname]
		if !ok {
			groups[c.Hostname] = &group{winner: c}
			continue
		}
		if sourceLess(c.Source, g.winner.Source) {
			g.losers = append(g.losers, g.winner)
			g.winner = c
		} else {
			g.losers = append(g.losers, c)
		}
	}

	// Emit winners; collect conflicts.
	winners := make([]ContributionWithSource, 0, len(groups))
	var conflicts []Conflict
	for _, g := range groups {
		winners = append(winners, g.winner)
		for _, l := range g.losers {
			conflicts = append(conflicts, Conflict{
				Hostname: g.winner.Hostname,
				Winner:   g.winner.Source,
				Loser:    l.Source,
			})
		}
	}
	sort.Slice(winners, func(i, j int) bool {
		if winners[i].Hostname != winners[j].Hostname {
			return winners[i].Hostname < winners[j].Hostname
		}
		return winners[i].Path < winners[j].Path
	})
	sort.Slice(conflicts, func(i, j int) bool {
		return conflicts[i].Hostname < conflicts[j].Hostname
	})

	cfg := cf.TunnelConfig{}
	for _, w := range winners {
		entry := cf.IngressEntry{
			Hostname: w.Hostname,
			Path:     w.Path,
			Service:  w.Service,
		}
		if w.NoTLSVerify != nil || w.OriginServerName != nil {
			or := &cf.IngressOriginRequest{}
			if w.NoTLSVerify != nil {
				v := *w.NoTLSVerify
				or.NoTLSVerify = &v
			}
			if w.OriginServerName != nil {
				v := *w.OriginServerName
				or.OriginServerName = &v
			}
			entry.OriginRequest = or
		}
		cfg.Ingress = append(cfg.Ingress, entry)
	}
	cfg.Ingress = append(cfg.Ingress, cf.IngressEntry{Service: opts.CatchAllService})
	return cfg, conflicts
}

// sourceLess orders SourceKeys lexicographically by (Kind, Namespace, Name).
// Used to break hostname conflicts deterministically.
func sourceLess(a, b SourceKey) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.Namespace != b.Namespace {
		return a.Namespace < b.Namespace
	}
	return a.Name < b.Name
}
