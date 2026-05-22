/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

// Package reconcile holds Foundation-owned helpers shared by the bootstrap
// reconciler and the zone / tunnel controllers introduced in specs 2 and 3.
package reconcile

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// inProgressReasons are reasons that mean "still working" rather than "broken".
var inProgressReasons = map[string]struct{}{
	conventions.ReasonReconciling: {},
}

// SetReady sets the Ready condition on a slice and returns the (possibly
// reallocated) slice.
func SetReady(conds []metav1.Condition, status metav1.ConditionStatus, reason, msg string) []metav1.Condition {
	return SetCondition(conds, conventions.ConditionTypeReady, status, reason, msg)
}

// FindReady returns a pointer to the Ready condition in conds, or nil if none
// is present. The returned pointer points into the original slice; callers must
// not modify it after the slice is mutated.
func FindReady(conds []metav1.Condition) *metav1.Condition {
	return meta.FindStatusCondition(conds, conventions.ConditionTypeReady)
}

// SetCondition upserts a condition by Type. The LastTransitionTime is only
// updated when the Status actually changes.
func SetCondition(conds []metav1.Condition, condType string, status metav1.ConditionStatus, reason, msg string) []metav1.Condition {
	now := metav1.Now()
	for i := range conds {
		if conds[i].Type == condType {
			if conds[i].Status != status {
				conds[i].LastTransitionTime = now
			}
			conds[i].Status = status
			conds[i].Reason = reason
			conds[i].Message = msg
			return conds
		}
	}
	return append(conds, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		LastTransitionTime: now,
	})
}

// DerivePhase maps (Ready.Status, Ready.Reason) to a v2alpha1.Phase enum.
func DerivePhase(status metav1.ConditionStatus, reason string) v2alpha1.Phase {
	switch status {
	case metav1.ConditionTrue:
		return v2alpha1.PhaseReady
	case metav1.ConditionFalse:
		if _, ok := inProgressReasons[reason]; ok {
			return v2alpha1.PhaseReconciling
		}
		return v2alpha1.PhaseError
	default:
		return v2alpha1.PhasePending
	}
}

// StatusEpilogue is the interface UpdateStatusIfChanged[T] uses to read
// and write the three bookkeeping fields every controller stamps in its
// terminal status epilogue: LastSyncedAt, ObservedGeneration, and the
// Feature F (S6) LastReconcileToken ack.
type StatusEpilogue interface {
	GetLastSyncedAt() *metav1.Time
	SetLastSyncedAt(*metav1.Time)
	GetObservedGeneration() int64
	SetObservedGeneration(int64)
	GetLastReconcileToken() string
	SetLastReconcileToken(string)
}

// UpdateStatusIfChanged is the unified terminal status-write epilogue helper
// used by all 5 CRD reconcilers (Zone, ZoneConfig, DNSRecord, Ruleset, Tunnel).
// It is the authoritative implementation of the epilogue that was previously
// duplicated — with a subtle drift — across each reconciler.
//
// The helper performs the following in order:
//
//  1. If forceReconcile is true, stamps forceToken into
//     objStatus.LastReconcileToken (Feature F / S6 ack). This stamp happens
//     BEFORE the change predicate is evaluated so that a caller whose
//     statusDiffers callback includes LastReconcileToken in its DeepEqual will
//     detect the change and trigger a write — which is the intended contract.
//     CALLER CONTRACT: when forceReconcile may be true, the statusDiffers
//     callback MUST include LastReconcileToken in its diff comparison;
//     otherwise this function takes the no-write branch and the stamped ack
//     is silently dropped (the in-memory token survives until the next
//     Get() overwrites it).
//
//  2. Evaluates the change predicate: a write is needed when either
//     obj.GetGeneration() differs from snapshot.GetObservedGeneration() (spec
//     changed since the last reconcile) OR the caller-supplied statusDiffers
//     callback returns true (content other than the masked bookkeeping fields
//     changed, including a freshly stamped LastReconcileToken).
//
//  3. No-write branch: restores the in-memory LastSyncedAt and
//     ObservedGeneration to the snapshot's values. This is the zone-controller
//     rollback semantic promoted to a universal invariant: when this function
//     returns without writing, the in-memory obj carries no LastSyncedAt or
//     ObservedGeneration changes that weren't already in the snapshot.
//
//  4. Write branch: stamps LastSyncedAt = metav1.Now() and ObservedGeneration
//     = obj.GetGeneration(), then calls c.Status().Update(ctx, obj).
//
// The caller supplies statusDiffers as a closure that compares the *current*
// live status content against the snapshot taken at reconcile-start, masking
// LastSyncedAt and ObservedGeneration from the comparison (those fields are
// managed exclusively by this helper). A typical implementation uses
// equality.Semantic.DeepEqual on a masked copy of the two Status structs.
func UpdateStatusIfChanged[T client.Object](
	ctx context.Context,
	c client.Client,
	obj T,
	objStatus, snapshot StatusEpilogue,
	forceReconcile bool,
	forceToken string,
	statusDiffers func() bool,
) (changed bool, err error) {
	// 1. Stamp the Feature F ack before the predicate so that the caller's
	//    statusDiffers callback can detect the changed token and trigger a write.
	if forceReconcile {
		objStatus.SetLastReconcileToken(forceToken)
	}

	// 2. Change predicate: write when generation advanced OR content differs.
	if obj.GetGeneration() == snapshot.GetObservedGeneration() && !statusDiffers() {
		// 3. No-write path: restore in-memory bookkeeping to snapshot values.
		objStatus.SetLastSyncedAt(snapshot.GetLastSyncedAt())
		objStatus.SetObservedGeneration(snapshot.GetObservedGeneration())
		return false, nil
	}

	// 4. Write path: stamp now + generation, then persist.
	now := metav1.Now()
	objStatus.SetLastSyncedAt(&now)
	objStatus.SetObservedGeneration(obj.GetGeneration())
	if err := c.Status().Update(ctx, obj); err != nil {
		return false, err
	}
	return true, nil
}

// SetUnstructuredCondition is the unstructured-slice equivalent of SetCondition.
// It upserts by `type` and only updates `lastTransitionTime` when `status` changes
// (matches metav1.Condition convention used by SetCondition).
// Used by callers that operate on *unstructured.Unstructured for domain-agnostic code paths.
func SetUnstructuredCondition(conds []interface{}, condType, status, reason, msg string) []interface{} {
	now := metav1.Now().UTC().Format(time.RFC3339)
	newC := map[string]interface{}{
		"type":               condType,
		"status":             status,
		"reason":             reason,
		"message":            msg,
		"lastTransitionTime": now,
	}
	for i, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] == condType {
			// Preserve LastTransitionTime when status is unchanged (matches K8s
			// metav1.Condition convention used by SetCondition).
			if m["status"] == status {
				newC["lastTransitionTime"] = m["lastTransitionTime"]
			}
			conds[i] = newC
			return conds
		}
	}
	return append(conds, newC)
}
