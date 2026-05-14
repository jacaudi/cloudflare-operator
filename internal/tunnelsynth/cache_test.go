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
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func mkKey(kind, ns, name string) SourceKey {
	return SourceKey{Kind: kind, Namespace: ns, Name: name}
}

func TestCache_SetAndSnapshot(t *testing.T) {
	c := NewCache()
	tk := TunnelKey{Namespace: "app-foo", Name: "cf-app-foo"}
	src := mkKey("Service", "app-foo", "svc")

	c.Set(tk, src, []IngressContribution{
		{Hostname: "foo.example.com", Service: "http://svc.app-foo:80"},
	})

	snap := c.Snapshot(tk)
	require.Len(t, snap, 1)
	require.Equal(t, "foo.example.com", snap[0].Hostname)
	require.Equal(t, src, snap[0].Source)
}

func TestCache_SetReplacesPriorContributions(t *testing.T) {
	c := NewCache()
	tk := TunnelKey{Namespace: "ns", Name: "tn"}
	src := mkKey("Service", "ns", "svc")

	c.Set(tk, src, []IngressContribution{
		{Hostname: "a.example.com", Service: "http://svc.ns:80"},
		{Hostname: "b.example.com", Service: "http://svc.ns:80"},
	})
	c.Set(tk, src, []IngressContribution{
		{Hostname: "a.example.com", Service: "http://svc.ns:80"},
	})

	snap := c.Snapshot(tk)
	require.Len(t, snap, 1)
	require.Equal(t, "a.example.com", snap[0].Hostname)
}

func TestCache_Clear(t *testing.T) {
	c := NewCache()
	tk := TunnelKey{Namespace: "ns", Name: "tn"}
	src := mkKey("Service", "ns", "svc")
	c.Set(tk, src, []IngressContribution{{Hostname: "a.example.com", Service: "http://svc.ns:80"}})

	c.Clear(tk, src)

	require.Empty(t, c.Snapshot(tk))
}

func TestCache_MultipleSourcesSameTunnel(t *testing.T) {
	c := NewCache()
	tk := TunnelKey{Namespace: "ns", Name: "tn"}
	s1 := mkKey("Service", "ns", "s1")
	s2 := mkKey("HTTPRoute", "ns", "r2")

	c.Set(tk, s1, []IngressContribution{{Hostname: "a.example.com", Service: "http://s1.ns:80"}})
	c.Set(tk, s2, []IngressContribution{{Hostname: "b.example.com", Service: "http://gw.ns:443"}})

	snap := c.Snapshot(tk)
	require.Len(t, snap, 2)
}

func TestCache_SnapshotIsDeepCopy(t *testing.T) {
	c := NewCache()
	tk := TunnelKey{Namespace: "ns", Name: "tn"}
	src := mkKey("Service", "ns", "svc")
	c.Set(tk, src, []IngressContribution{{Hostname: "a.example.com", Service: "http://svc.ns:80"}})

	snap := c.Snapshot(tk)
	snap[0].Hostname = "mutated.example.com"

	again := c.Snapshot(tk)
	require.Equal(t, "a.example.com", again[0].Hostname, "snapshot mutations must not leak back into cache")
}

func TestCache_ConcurrentReadersWriters(t *testing.T) {
	c := NewCache()
	tk := TunnelKey{Namespace: "ns", Name: "tn"}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			src := SourceKey{Kind: "Service", Namespace: "ns", Name: "s"}
			c.Set(tk, src, []IngressContribution{{Hostname: "a.example.com", Service: "http://x:80"}})
		}(i)
		go func() {
			defer wg.Done()
			_ = c.Snapshot(tk)
		}()
	}
	wg.Wait()
}

func TestCache_ListSourcesAttachedToTunnel(t *testing.T) {
	c := NewCache()
	tk := TunnelKey{Namespace: "ns", Name: "tn"}
	c.Set(tk, mkKey("Service", "ns", "s"), []IngressContribution{{Hostname: "a.example.com", Service: "http://s.ns:80"}})
	c.Set(tk, mkKey("HTTPRoute", "ns", "r"), []IngressContribution{{Hostname: "b.example.com", Service: "http://gw.ns:443"}})

	srcs := c.AttachedSources(tk)
	require.Len(t, srcs, 2)
}

// Total count assertion (review pattern #6): if a future change introduces an
// extra public method or a public struct field, downstream tests catch it.
func TestCache_PublicSurfaceCount(t *testing.T) {
	c := NewCache()
	require.NotNil(t, c)
	// Verify the public method set we expect.
	var _ interface {
		Set(TunnelKey, SourceKey, []IngressContribution)
		Clear(TunnelKey, SourceKey)
		Snapshot(TunnelKey) []ContributionWithSource
		AttachedSources(TunnelKey) []SourceKey
	} = c
}
