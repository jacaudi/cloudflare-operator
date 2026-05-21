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

import "sync"

// sourceBase holds fields shared by every source-controller (Service,
// Gateway, HTTPRoute, TLSRoute). It is embedded as a value into each
// reconciler; field access (r.tracker, r.dedupe, r.trackerOnce) is
// promoted via Go's embedded-field rules.
type sourceBase struct {
	trackerOnce sync.Once
	tracker     *cacheTracker
	dedupe      *eventDedupe
}

// ensure initializes the tracker + dedupe on first call. Idempotent under
// concurrent reconciles (MaxConcurrentReconciles > 1) via sync.Once.
// Pre-seeded tracker/dedupe (test fixtures) are preserved.
func (s *sourceBase) ensure() {
	s.trackerOnce.Do(func() {
		if s.tracker == nil {
			s.tracker = newCacheTracker()
		}
		if s.dedupe == nil {
			s.dedupe = newEventDedupe(0, 0)
		}
	})
}
