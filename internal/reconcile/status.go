// Package reconcile holds Foundation-owned helpers shared by the bootstrap
// reconciler and the zone / tunnel controllers introduced in specs 2 and 3.
package reconcile

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// Phase is a coarse-grained status summary derived from the Ready condition.
type Phase string

const (
	PhaseReady       Phase = "Ready"
	PhaseReconciling Phase = "Reconciling"
	PhaseError       Phase = "Error"
	PhasePending     Phase = "Pending"
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

// DerivePhase maps (Ready.Status, Ready.Reason) to a Phase enum.
func DerivePhase(status metav1.ConditionStatus, reason string) Phase {
	switch status {
	case metav1.ConditionTrue:
		return PhaseReady
	case metav1.ConditionFalse:
		if _, ok := inProgressReasons[reason]; ok {
			return PhaseReconciling
		}
		return PhaseError
	default:
		return PhasePending
	}
}
