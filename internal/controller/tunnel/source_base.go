/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
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
