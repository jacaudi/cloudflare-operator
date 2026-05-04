package v1alpha1

import (
	"slices"
	"testing"
)

func TestInProgressReasons_KnownInProgressMembers(t *testing.T) {
	wantIn := []string{
		ReasonReconciling,
		ReasonZoneRefNotReady,
		ReasonZonePending,
		ReasonGatewayAddressNotReady,
		ReasonTunnelNotReady,
	}
	for _, r := range wantIn {
		if !slices.Contains(InProgressReasons, r) {
			t.Errorf("InProgressReasons missing in-progress reason %q", r)
		}
	}
}

func TestInProgressReasons_ErrorReasonsAreNotMembers(t *testing.T) {
	wantOut := []string{
		ReasonInvalidSpec,
		ReasonRemoteGone,
		ReasonPermissionDenied,
		ReasonPlanTierRequired,
		ReasonSecretNotLabeled, // Part 2 reason; assumes Part 2 has merged.
		ReasonReconcileError,
		ReasonCloudflareError,
		ReasonSecretNotFound,
	}
	for _, r := range wantOut {
		if slices.Contains(InProgressReasons, r) {
			t.Errorf("InProgressReasons unexpectedly contains error reason %q", r)
		}
	}
}

func TestPhase_ConstantValues(t *testing.T) {
	cases := map[Phase]string{
		PhasePending:     "Pending",
		PhaseReconciling: "Reconciling",
		PhaseReady:       "Ready",
		PhaseDeleting:    "Deleting",
		PhaseError:       "Error",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("Phase constant string mismatch: got %q want %q", string(got), want)
		}
	}
}
