package status

import (
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

func SetCondition(conditions *[]metav1.Condition, conditionType string, status metav1.ConditionStatus, reason, message string, generation int64) {
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}
	meta.SetStatusCondition(conditions, condition)
}

// SetReady sets the Ready condition AND derives Phase from the
// (status, reason) pair when phase is non-nil. Callers that don't yet
// have a Status.Phase field pass nil — the function is a strict
// superset of the previous SetReady.
func SetReady(
	conditions *[]metav1.Condition,
	phase *cloudflarev1alpha1.Phase,
	status metav1.ConditionStatus,
	reason, message string,
	generation int64,
) {
	SetCondition(conditions, cloudflarev1alpha1.ConditionTypeReady,
		status, reason, message, generation)
	if phase == nil {
		return
	}
	*phase = derivePhase(status, reason)
}

// SetPhase sets *phase to p. A nil phase pointer is a no-op (used by
// callers that do not yet have a Status.Phase field, and by tests that
// don't care about Phase).
func SetPhase(phase *cloudflarev1alpha1.Phase, p cloudflarev1alpha1.Phase) {
	if phase == nil {
		return
	}
	*phase = p
}

func derivePhase(status metav1.ConditionStatus, reason string) cloudflarev1alpha1.Phase {
	switch status {
	case metav1.ConditionTrue:
		return cloudflarev1alpha1.PhaseReady
	case metav1.ConditionFalse:
		if slices.Contains(cloudflarev1alpha1.InProgressReasons, reason) {
			return cloudflarev1alpha1.PhaseReconciling
		}
		return cloudflarev1alpha1.PhaseError
	case metav1.ConditionUnknown:
		return cloudflarev1alpha1.PhasePending
	default:
		return cloudflarev1alpha1.PhasePending
	}
}
