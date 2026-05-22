/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

func TestSetReady_AppendsCondition(t *testing.T) {
	var conds []metav1.Condition
	conds = SetReady(conds, metav1.ConditionTrue, conventions.ReasonReady, "all good")
	require.Len(t, conds, 1)
	require.Equal(t, conventions.ConditionTypeReady, conds[0].Type)
	require.Equal(t, metav1.ConditionTrue, conds[0].Status)
	require.Equal(t, conventions.ReasonReady, conds[0].Reason)
	require.Equal(t, "all good", conds[0].Message)
}

func TestSetReady_OverwritesSameType(t *testing.T) {
	var conds []metav1.Condition
	conds = SetReady(conds, metav1.ConditionFalse, conventions.ReasonReconciling, "in progress")
	conds = SetReady(conds, metav1.ConditionTrue, conventions.ReasonReady, "all good")
	require.Len(t, conds, 1)
	require.Equal(t, metav1.ConditionTrue, conds[0].Status)
}

func TestSetCondition_NewType(t *testing.T) {
	var conds []metav1.Condition
	conds = SetCondition(conds, "Synced", metav1.ConditionTrue, "Synced", "")
	conds = SetReady(conds, metav1.ConditionTrue, conventions.ReasonReady, "")
	require.Len(t, conds, 2)
}

func TestDerivePhase(t *testing.T) {
	cases := []struct {
		name   string
		status metav1.ConditionStatus
		reason string
		want   v2alpha1.Phase
	}{
		{"ready-true", metav1.ConditionTrue, conventions.ReasonReady, v2alpha1.PhaseReady},
		{"reconciling", metav1.ConditionFalse, conventions.ReasonReconciling, v2alpha1.PhaseReconciling},
		{"error", metav1.ConditionFalse, conventions.ReasonDegraded, v2alpha1.PhaseError},
		{"unknown", metav1.ConditionUnknown, "", v2alpha1.PhasePending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, DerivePhase(c.status, c.reason))
		})
	}
}

func TestSetUnstructuredCondition_AppendNew(t *testing.T) {
	conds := []interface{}{}
	conds = SetUnstructuredCondition(conds, "Ready", "True", "Ready", "all good")
	require.Len(t, conds, 1)
	c := conds[0].(map[string]interface{})
	require.Equal(t, "Ready", c["type"])
	require.Equal(t, "True", c["status"])
}

func TestSetUnstructuredCondition_PreservesLastTransitionTime(t *testing.T) {
	earlier := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	conds := []interface{}{
		map[string]interface{}{
			"type":               "Ready",
			"status":             "False",
			"reason":             "Reconciling",
			"message":            "in progress",
			"lastTransitionTime": earlier,
		},
	}
	// Same status+reason — LastTransitionTime preserved.
	conds = SetUnstructuredCondition(conds, "Ready", "False", "Reconciling", "still in progress")
	c := conds[0].(map[string]interface{})
	require.Equal(t, earlier, c["lastTransitionTime"], "LastTransitionTime should be preserved on no-op")
}

func TestSetUnstructuredCondition_UpdatesLastTransitionTimeOnStatusChange(t *testing.T) {
	earlier := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	conds := []interface{}{
		map[string]interface{}{
			"type":               "Ready",
			"status":             "True",
			"reason":             "Ready",
			"message":            "all good",
			"lastTransitionTime": earlier,
		},
	}
	conds = SetUnstructuredCondition(conds, "Ready", "False", "Reconciling", "spec changed")
	c := conds[0].(map[string]interface{})
	require.NotEqual(t, earlier, c["lastTransitionTime"], "LastTransitionTime should advance on status change")
}

func TestSetUnstructuredCondition_PreservesLTT_OnMessageOnlyChange(t *testing.T) {
	earlier := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	conds := []interface{}{
		map[string]interface{}{
			"type":               "Ready",
			"status":             "True",
			"reason":             "Ready",
			"message":            "old message",
			"lastTransitionTime": earlier,
		},
	}
	conds = SetUnstructuredCondition(conds, "Ready", "True", "Ready", "new message")
	c := conds[0].(map[string]interface{})
	require.Equal(t, earlier, c["lastTransitionTime"], "LTT must be preserved when only message changes")
	require.Equal(t, "new message", c["message"])
}

func TestSetUnstructuredCondition_PreservesLTT_OnReasonOnlyChange(t *testing.T) {
	earlier := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	conds := []interface{}{
		map[string]interface{}{
			"type":               "Ready",
			"status":             "True",
			"reason":             "Ready",
			"message":            "all good",
			"lastTransitionTime": earlier,
		},
	}
	conds = SetUnstructuredCondition(conds, "Ready", "True", "PartialReady", "all good")
	c := conds[0].(map[string]interface{})
	require.Equal(t, earlier, c["lastTransitionTime"], "LTT must be preserved when status is unchanged, even if reason changes (matches metav1.Condition convention)")
	require.Equal(t, "PartialReady", c["reason"], "reason must still be updated")
}

// ---------------------------------------------------------------------------
// UpdateStatusIfChanged tests
// ---------------------------------------------------------------------------

// fakeStatus is a minimal StatusEpilogue implementation used in
// UpdateStatusIfChanged unit tests. It carries only the three bookkeeping
// fields the helper operates on; the rest of the status content is
// represented by the separate "payload" string so tests can drive
// statusDiffers without coupling to a real CRD type.
type fakeStatus struct {
	lst *metav1.Time
	gen int64
	tok string
}

func (f *fakeStatus) GetLastSyncedAt() *metav1.Time  { return f.lst }
func (f *fakeStatus) SetLastSyncedAt(t *metav1.Time) { f.lst = t }
func (f *fakeStatus) GetObservedGeneration() int64   { return f.gen }
func (f *fakeStatus) SetObservedGeneration(g int64)  { f.gen = g }
func (f *fakeStatus) GetLastReconcileToken() string  { return f.tok }
func (f *fakeStatus) SetLastReconcileToken(s string) { f.tok = s }

// statusIfChangedScheme returns a scheme with corev1 + v2alpha1 registered.
func statusIfChangedScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, v2alpha1.AddToScheme(s))
	return s
}

// TestUpdateStatusIfChanged_NoChange_NoWrite: same content, force=false.
// Expect: changed=false, no Update call, in-memory LastSyncedAt +
// ObservedGeneration rolled back to snapshot values (rollback invariant).
//
// The rollback matters when mid-reconcile logic has already mutated live.lst
// or live.gen (e.g. reflectZoneStatus stamps a new LastSyncedAt before
// calling the helper). The helper must undo those mutations on the no-write
// path so the in-memory object is consistent with what is persisted.
func TestUpdateStatusIfChanged_NoChange_NoWrite(t *testing.T) {
	s := statusIfChangedScheme(t)

	snapTime := metav1.NewTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	midReconcileTime := metav1.NewTime(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))

	obj := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default", Generation: 5},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(obj).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()

	// Snapshot: bookkeeping as of reconcile-start.
	snap := &fakeStatus{lst: &snapTime, gen: 5, tok: ""}
	// Live: simulates a mid-reconcile mutation that advanced LastSyncedAt and
	// ObservedGeneration before the helper is called. The content diff predicate
	// returns false (no real change), so the helper must roll these back.
	live := &fakeStatus{lst: &midReconcileTime, gen: 99, tok: ""}

	// Caller's predicate: no content difference (only bookkeeping fields drifted).
	changed, err := UpdateStatusIfChanged(
		context.Background(), c, obj,
		live, snap,
		false, "",
		func() bool { return false },
	)

	require.NoError(t, err)
	require.False(t, changed)

	// Rollback invariant: in-memory fields must equal snapshot values, not the
	// mid-reconcile values. This is the correctness gate.
	require.Equal(t, snap.GetLastSyncedAt(), live.GetLastSyncedAt(),
		"no-write path must restore LastSyncedAt to snapshot value")
	require.Equal(t, snap.GetObservedGeneration(), live.GetObservedGeneration(),
		"no-write path must restore ObservedGeneration to snapshot value")

	// Confirm no Status().Update was called — the persisted object is unchanged.
	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	// The fake client records updates to status subresource; if Update had been
	// called, LastSyncedAt would be non-nil on the stored object.
	require.Nil(t, got.Status.LastSyncedAt, "Status().Update must NOT have been called on the no-change path")
}

// TestUpdateStatusIfChanged_ContentChange_Writes: statusDiffers() returns
// true. Expect: changed=true, Update called, LastSyncedAt and
// ObservedGeneration updated on the live object.
func TestUpdateStatusIfChanged_ContentChange_Writes(t *testing.T) {
	s := statusIfChangedScheme(t)

	obj := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default", Generation: 3},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(obj).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()

	// Snapshot: generation matches, no prior LastSyncedAt.
	snap := &fakeStatus{lst: nil, gen: 3, tok: ""}
	// Live status wired to obj.Status so Update can be verified.
	live := &obj.Status

	changed, err := UpdateStatusIfChanged(
		context.Background(), c, obj,
		live, snap,
		false, "",
		func() bool { return true }, // content differs
	)

	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, live.GetLastSyncedAt(), "LastSyncedAt must be stamped on write path")
	require.Equal(t, obj.GetGeneration(), live.GetObservedGeneration(),
		"ObservedGeneration must match obj.Generation on write path")

	// Confirm the write reached the fake store.
	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	require.NotNil(t, got.Status.LastSyncedAt, "persisted LastSyncedAt must be non-nil after write")
}

// TestUpdateStatusIfChanged_GenerationAdvances_Writes: obj.Generation >
// snapshot.ObservedGeneration. Expect: changed=true regardless of
// statusDiffers result.
func TestUpdateStatusIfChanged_GenerationAdvances_Writes(t *testing.T) {
	s := statusIfChangedScheme(t)

	obj := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default", Generation: 7},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(obj).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()

	// Snapshot has an older generation.
	snap := &fakeStatus{lst: nil, gen: 6, tok: ""}
	live := &obj.Status

	changed, err := UpdateStatusIfChanged(
		context.Background(), c, obj,
		live, snap,
		false, "",
		func() bool { return false }, // no content diff — generation alone should trigger
	)

	require.NoError(t, err)
	require.True(t, changed, "generation advance must trigger a write even when statusDiffers returns false")
	require.Equal(t, obj.GetGeneration(), live.GetObservedGeneration())
}

// TestUpdateStatusIfChanged_ForceReconcile_StampsAck: force=true, token="t1".
// Expect: SetLastReconcileToken("t1") called on live status; Update called.
//
// Design note: the helper stamps the token BEFORE evaluating the statusDiffers
// predicate. A well-written caller includes LastReconcileToken in its
// DeepEqual comparison, so the stamp causes statusDiffers to return true,
// which triggers the write. The test replicates that contract explicitly.
func TestUpdateStatusIfChanged_ForceReconcile_StampsAck(t *testing.T) {
	s := statusIfChangedScheme(t)

	obj := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default", Generation: 2},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(obj).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()

	snap := &fakeStatus{lst: nil, gen: 2, tok: ""}
	live := &fakeStatus{lst: nil, gen: 2, tok: ""}

	// The caller's predicate includes LastReconcileToken — this is the contract
	// that makes force-reconcile trigger a write.
	changed, err := UpdateStatusIfChanged(
		context.Background(), c, obj,
		live, snap,
		true, "t1",
		func() bool { return live.tok != snap.tok }, // detects the stamped token
	)

	require.NoError(t, err)
	require.True(t, changed, "force-reconcile with token change must trigger a write")
	require.Equal(t, "t1", live.tok, "SetLastReconcileToken must have been called with 't1'")
}

// TestUpdateStatusIfChanged_PropagatesUpdateError: fake client returns error
// from Status.Update. Expect: error propagated, changed=false.
func TestUpdateStatusIfChanged_PropagatesUpdateError(t *testing.T) {
	s := statusIfChangedScheme(t)

	// Object NOT registered in the fake client — Status().Update will fail.
	obj := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "missing", Namespace: "default", Generation: 1},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()

	snap := &fakeStatus{lst: nil, gen: 0, tok: ""}
	live := &obj.Status

	changed, err := UpdateStatusIfChanged(
		context.Background(), c, obj,
		live, snap,
		false, "",
		func() bool { return true }, // diff triggers write attempt
	)

	require.Error(t, err, "error from Status().Update must be propagated")
	require.False(t, changed)
}
