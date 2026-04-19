package status

import (
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

func SetReady(conditions *[]metav1.Condition, status metav1.ConditionStatus, reason, message string, generation int64) {
	SetCondition(conditions, cloudflarev1alpha1.ConditionTypeReady, status, reason, message, generation)
}
