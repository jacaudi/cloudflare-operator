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

package conventions

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
)

// SafeRecorder wraps a record.EventRecorder with nil-safety so callers
// can emit Events unconditionally without guarding against a nil field.
// Used by all reconcilers to eliminate the 24× "if r.Recorder != nil"
// guard pattern.
//
// A typed-nil *SafeRecorder and a SafeRecorder wrapping a nil
// EventRecorder are both safe to call — all methods become no-ops.
type SafeRecorder struct {
	record.EventRecorder
}

// NewSafeRecorder constructs a SafeRecorder. If rec is nil, all methods
// on the returned SafeRecorder are no-ops.
func NewSafeRecorder(rec record.EventRecorder) *SafeRecorder {
	return &SafeRecorder{EventRecorder: rec}
}

// Event is nil-safe. It is a no-op when s is nil or s.EventRecorder is nil.
//
// Method promotion is NOT relied on here: if the embedded EventRecorder is
// nil and the promoted method were called, the concrete type's dispatch
// would panic. Explicit override is required.
func (s *SafeRecorder) Event(object runtime.Object, eventtype, reason, message string) {
	if s == nil || s.EventRecorder == nil {
		return
	}
	s.EventRecorder.Event(object, eventtype, reason, message)
}

// Eventf is nil-safe. It is a no-op when s is nil or s.EventRecorder is nil.
func (s *SafeRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...any) {
	if s == nil || s.EventRecorder == nil {
		return
	}
	s.EventRecorder.Eventf(object, eventtype, reason, messageFmt, args...)
}

// AnnotatedEventf is nil-safe. It is a no-op when s is nil or s.EventRecorder is nil.
func (s *SafeRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype, reason, messageFmt string, args ...any) {
	if s == nil || s.EventRecorder == nil {
		return
	}
	s.EventRecorder.AnnotatedEventf(object, annotations, eventtype, reason, messageFmt, args...)
}
