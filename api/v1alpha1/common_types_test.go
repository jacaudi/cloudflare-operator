// api/v1alpha1/common_types_test.go
package v1alpha1

import (
	"testing"
)

func TestConditionConstants(t *testing.T) {
	if ConditionTypeReady == "" {
		t.Error("ConditionTypeReady should not be empty")
	}
}

func TestReasonConstants(t *testing.T) {
	reasons := []string{
		ReasonReconciling,
		ReasonReconcileSuccess,
		ReasonReconcileError,
		ReasonCloudflareError,
		ReasonSecretNotFound,
		ReasonInvalidSpec,
		ReasonDeletingResource,
		ReasonIPResolutionError,
	}
	for _, r := range reasons {
		if r == "" {
			t.Error("reason constant should not be empty")
		}
	}
}
