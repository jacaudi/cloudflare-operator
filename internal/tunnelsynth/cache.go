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

import "sync"

// Cache is the in-memory per-tunnel ingress-contribution registry.
//
// Concurrency model: many readers (one snapshot per tunnel reconcile) versus
// many writers (one Set/Clear per source reconcile), so the store is guarded
// by an RWMutex. Snapshot returns a deep copy — callers may freely mutate the
// returned slice and its elements without affecting cache state.
type Cache struct {
	mu sync.RWMutex
	// store[tunnelKey][sourceKey] = the source's current contribution set.
	store map[TunnelKey]map[SourceKey][]IngressContribution
}

// NewCache returns an empty Cache.
func NewCache() *Cache {
	return &Cache{store: map[TunnelKey]map[SourceKey][]IngressContribution{}}
}

// Set replaces the contribution set for (tunnel, source) atomically. Pass an
// empty slice to clear without removing the source key — use Clear() to drop it.
func (c *Cache) Set(tk TunnelKey, src SourceKey, contributions []IngressContribution) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tunnel, ok := c.store[tk]
	if !ok {
		tunnel = map[SourceKey][]IngressContribution{}
		c.store[tk] = tunnel
	}
	// Deep-copy in so callers can mutate their slice without affecting cache.
	out := make([]IngressContribution, len(contributions))
	copy(out, contributions)
	tunnel[src] = out
}

// Clear removes a source's contribution set entirely. The tunnel entry itself
// is dropped from the outer map when its last source disappears.
func (c *Cache) Clear(tk TunnelKey, src SourceKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tunnel, ok := c.store[tk]; ok {
		delete(tunnel, src)
		if len(tunnel) == 0 {
			delete(c.store, tk)
		}
	}
}

// Snapshot returns a deep copy of every contribution attached to a tunnel,
// each tagged with its source. Order is not specified — callers must sort if
// they need determinism. The returned slice and its pointer-typed fields are
// independent copies, so mutating them does not affect cache state.
func (c *Cache) Snapshot(tk TunnelKey) []ContributionWithSource {
	c.mu.RLock()
	defer c.mu.RUnlock()
	tunnel, ok := c.store[tk]
	if !ok {
		return nil
	}
	var out []ContributionWithSource
	for src, contribs := range tunnel {
		for _, ic := range contribs {
			cp := ic
			if ic.NoTLSVerify != nil {
				v := *ic.NoTLSVerify
				cp.NoTLSVerify = &v
			}
			if ic.OriginServerName != nil {
				v := *ic.OriginServerName
				cp.OriginServerName = &v
			}
			if ic.CAPoolPath != nil {
				v := *ic.CAPoolPath
				cp.CAPoolPath = &v
			}
			out = append(out, ContributionWithSource{IngressContribution: cp, Source: src})
		}
	}
	return out
}

// AttachedSources lists every source currently contributing to a tunnel.
// The returned slice is unordered; sort by (Kind, Namespace, Name) if a
// deterministic order is required.
func (c *Cache) AttachedSources(tk TunnelKey) []SourceKey {
	c.mu.RLock()
	defer c.mu.RUnlock()
	tunnel, ok := c.store[tk]
	if !ok {
		return nil
	}
	out := make([]SourceKey, 0, len(tunnel))
	for s := range tunnel {
		out = append(out, s)
	}
	return out
}
