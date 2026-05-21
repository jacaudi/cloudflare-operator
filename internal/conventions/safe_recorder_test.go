/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package conventions_test

import (
	"testing"
	"time"

	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
)

// TestSafeRecorder_NilRecorder_NoOp asserts that a typed-nil *SafeRecorder
// calling Event/Eventf/AnnotatedEventf does not panic.
func TestSafeRecorder_NilRecorder_NoOp(t *testing.T) {
	var sr *conventions.SafeRecorder // nil
	sr.Eventf(nil, "Normal", "Test", "msg")
	sr.Event(nil, "Normal", "Test", "msg")
	sr.AnnotatedEventf(nil, nil, "Normal", "Test", "msg")
}

// TestSafeRecorder_WrapsNonNil asserts that a non-nil SafeRecorder forwards
// calls to the underlying EventRecorder correctly.
func TestSafeRecorder_WrapsNonNil(t *testing.T) {
	fake := record.NewFakeRecorder(10)
	sr := conventions.NewSafeRecorder(fake)
	sr.Eventf(&corev1.Pod{}, "Normal", "Test", "msg %d", 42)
	select {
	case ev := <-fake.Events:
		require.Contains(t, ev, "Test")
		require.Contains(t, ev, "42")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected event not emitted")
	}
}

// TestSafeRecorder_EmbeddedNilEventRecorder_NoOp asserts that a non-nil
// SafeRecorder wrapping a nil EventRecorder is also a no-op (no panic).
func TestSafeRecorder_EmbeddedNilEventRecorder_NoOp(t *testing.T) {
	sr := &conventions.SafeRecorder{EventRecorder: nil}
	sr.Eventf(nil, "Normal", "Test", "msg") // must not panic
}
