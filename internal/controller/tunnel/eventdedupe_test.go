/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnel

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
)

func makeDedupeObj(uid string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "obj-" + uid, Namespace: "ns", UID: types.UID(uid)}}
}

func TestEventDedupe_SuppressesIdenticalWithinTTL(t *testing.T) {
	d := newEventDedupe(1*time.Hour, 1024)
	rec := record.NewFakeRecorder(10)
	obj := makeDedupeObj("u1")

	d.emit(rec, obj, corev1.EventTypeWarning, "R", "msg")
	d.emit(rec, obj, corev1.EventTypeWarning, "R", "msg")
	d.emit(rec, obj, corev1.EventTypeWarning, "R", "msg")

	require.Len(t, rec.Events, 1, "two duplicates within TTL must be suppressed")
}

func TestEventDedupe_AllowsDifferentReason(t *testing.T) {
	d := newEventDedupe(1*time.Hour, 1024)
	rec := record.NewFakeRecorder(10)
	obj := makeDedupeObj("u1")

	d.emit(rec, obj, corev1.EventTypeWarning, "R1", "msg")
	d.emit(rec, obj, corev1.EventTypeWarning, "R2", "msg")

	require.Len(t, rec.Events, 2)
}

func TestEventDedupe_AllowsDifferentMessage(t *testing.T) {
	d := newEventDedupe(1*time.Hour, 1024)
	rec := record.NewFakeRecorder(10)
	obj := makeDedupeObj("u1")

	d.emit(rec, obj, corev1.EventTypeWarning, "R", "msg-a")
	d.emit(rec, obj, corev1.EventTypeWarning, "R", "msg-b")

	require.Len(t, rec.Events, 2)
}

func TestEventDedupe_AllowsAfterTTLExpiry(t *testing.T) {
	d := newEventDedupe(50*time.Millisecond, 1024)
	rec := record.NewFakeRecorder(10)
	obj := makeDedupeObj("u1")

	d.emit(rec, obj, corev1.EventTypeWarning, "R", "msg")
	time.Sleep(80 * time.Millisecond)
	d.emit(rec, obj, corev1.EventTypeWarning, "R", "msg")

	require.Len(t, rec.Events, 2)
}

func TestEventDedupe_PerTarget(t *testing.T) {
	d := newEventDedupe(1*time.Hour, 1024)
	rec := record.NewFakeRecorder(10)

	d.emit(rec, makeDedupeObj("u1"), corev1.EventTypeWarning, "R", "msg")
	d.emit(rec, makeDedupeObj("u2"), corev1.EventTypeWarning, "R", "msg")

	require.Len(t, rec.Events, 2, "different target UIDs must produce separate emits")
}

func TestEventDedupe_NilRecorderIsNoOp(t *testing.T) {
	d := newEventDedupe(1*time.Hour, 1024)
	obj := makeDedupeObj("u1")
	// Should not panic.
	d.emit(nil, obj, corev1.EventTypeWarning, "R", "msg")
}

func TestEventDedupe_MaxSizeEvictsOldest(t *testing.T) {
	d := newEventDedupe(1*time.Hour, 2) // tiny cap
	rec := record.NewFakeRecorder(10)

	d.emit(rec, makeDedupeObj("u1"), corev1.EventTypeWarning, "R", "msg")
	// Tiny sleep so u1's time stamp is strictly older than u2/u3 — the
	// oldest-eviction uses time.Now() granularity, which on some CI systems
	// has sub-microsecond resolution but identical adjacent calls.
	time.Sleep(1 * time.Millisecond)
	d.emit(rec, makeDedupeObj("u2"), corev1.EventTypeWarning, "R", "msg")
	time.Sleep(1 * time.Millisecond)
	d.emit(rec, makeDedupeObj("u3"), corev1.EventTypeWarning, "R", "msg") // forces u1 eviction
	d.emit(rec, makeDedupeObj("u1"), corev1.EventTypeWarning, "R", "msg") // would have been deduped if u1 still cached

	require.Len(t, rec.Events, 4, "cap-2 cache must evict u1, allowing the re-emit")
}
