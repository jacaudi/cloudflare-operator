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

package tunnel

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Default TTL for the dedupe cache. 1h matches the Kubernetes default Event
// TTL; on suppression-window expiry, the same warning may emit again and the
// apiserver garbage-collects the prior copy on its own schedule.
const defaultEventDedupeTTL = 1 * time.Hour

// Default cap on cache entries per reconciler. 1024 keys × ~120 bytes ≈
// 125 KB worst case. Far below the controller's memory budget; the cap
// exists to prevent unbounded growth on pathological inputs (e.g. messages
// that embed a counter or timestamp).
const defaultEventDedupeMaxSize = 1024

// eventDedupe is a per-reconciler suppression cache for repeated Events.
// Source-reconcilers stamp the same TranslateWarning Event every reconcile
// pass (every 30m), producing 48 identical Events per route per day. This
// type collapses them to one emit per (UID, reason, message) per TTL.
//
// Lifecycle: live on the reconciler struct as a pointer; lazy-init via
// sync.Once in the reconciler's setup path (mirror cacheTracker). On
// process restart the cache empties; the K8s Event TTL handles cleanup of
// the now-stale duplicates already in etcd.
type eventDedupe struct {
	mu      sync.Mutex
	recent  map[string]time.Time
	ttl     time.Duration
	maxSize int
}

func newEventDedupe(ttl time.Duration, maxSize int) *eventDedupe {
	if ttl <= 0 {
		ttl = defaultEventDedupeTTL
	}
	if maxSize <= 0 {
		maxSize = defaultEventDedupeMaxSize
	}
	return &eventDedupe{
		recent:  make(map[string]time.Time),
		ttl:     ttl,
		maxSize: maxSize,
	}
}

// eventDedupeKey returns the dedupe key for an emit. Hashing isn't necessary
// at this volume — string concatenation costs less than a map lookup of a
// long string in any realistic load.
func eventDedupeKey(uid types.UID, reason, message string) string {
	return string(uid) + "|" + reason + "|" + message
}

// emit records the (target, reason, message) tuple via the supplied
// Recorder, suppressing identical emits within the dedupe window. Safe to
// call with a nil Recorder (no-op, no cache state change either). Safe to
// call concurrently.
func (d *eventDedupe) emit(recorder record.EventRecorder, target client.Object, eventType, reason, message string) {
	if recorder == nil {
		return
	}
	k := eventDedupeKey(target.GetUID(), reason, message)

	d.mu.Lock()
	now := time.Now()
	if t, ok := d.recent[k]; ok && now.Sub(t) < d.ttl {
		d.mu.Unlock()
		return
	}
	if len(d.recent) >= d.maxSize {
		// Evict the oldest entry. O(n) on cap; cap is small (1024) so this
		// is comfortably under a microsecond even on the slowest CI runners.
		var oldestKey string
		var oldestTime time.Time
		first := true
		for ek, et := range d.recent {
			if first || et.Before(oldestTime) {
				oldestKey, oldestTime, first = ek, et, false
			}
		}
		delete(d.recent, oldestKey)
	}
	d.recent[k] = now
	d.mu.Unlock()

	recorder.Event(target, eventType, reason, message)
}
