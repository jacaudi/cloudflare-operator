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
		ReasonRemoteGone,
		ReasonApplied,
		ReasonNotConfigured,
		ReasonPermissionDenied,
		ReasonPlanTierRequired,
		ReasonPartialApply,
	}
	for _, r := range reasons {
		if r == "" {
			t.Error("reason constant should not be empty")
		}
	}
}

func TestConditionTypeConstants(t *testing.T) {
	conditionTypes := []string{
		ConditionTypeSSLApplied,
		ConditionTypeSecurityApplied,
		ConditionTypePerformanceApplied,
		ConditionTypeNetworkApplied,
		ConditionTypeDNSApplied,
		ConditionTypeBotManagementApplied,
	}
	for _, c := range conditionTypes {
		if c == "" {
			t.Errorf("condition type constant should not be empty")
		}
	}
}

func TestApexHostnameConstants(t *testing.T) {
	if ConditionTypeApexHostnameReady == "" {
		t.Error("ConditionTypeApexHostnameReady should not be empty")
	}
	if ConditionTypeApexHostnameReady != "ApexHostnameReady" {
		t.Errorf("ConditionTypeApexHostnameReady = %q, want %q",
			ConditionTypeApexHostnameReady, "ApexHostnameReady")
	}
	if ReasonApexRecordPending == "" {
		t.Error("ReasonApexRecordPending should not be empty")
	}
	// ReasonApexRecordPending is an in-progress reason: derivePhase must
	// map it to PhaseReconciling, not PhaseError. Membership in
	// InProgressReasons is the contract.
	found := false
	for _, r := range InProgressReasons {
		if r == ReasonApexRecordPending {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ReasonApexRecordPending must be a member of InProgressReasons")
	}
}
