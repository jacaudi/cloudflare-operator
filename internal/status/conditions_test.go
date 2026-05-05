package status

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

func TestSetCondition_AddsNew(t *testing.T) {
	var conditions []metav1.Condition
	SetCondition(&conditions, "Ready", metav1.ConditionTrue, "Success", "all good", 1)

	if len(conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(conditions))
	}
	if conditions[0].Type != "Ready" {
		t.Errorf("expected type Ready, got %s", conditions[0].Type)
	}
	if conditions[0].Status != metav1.ConditionTrue {
		t.Errorf("expected status True, got %s", conditions[0].Status)
	}
	if conditions[0].Reason != "Success" {
		t.Errorf("expected reason Success, got %s", conditions[0].Reason)
	}
	if conditions[0].ObservedGeneration != 1 {
		t.Errorf("expected generation 1, got %d", conditions[0].ObservedGeneration)
	}
}

func TestSetCondition_UpdatesExisting(t *testing.T) {
	conditions := []metav1.Condition{
		{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "OldReason",
			Message:            "old message",
			LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour)),
		},
	}
	SetCondition(&conditions, "Ready", metav1.ConditionTrue, "NewReason", "new message", 2)

	if len(conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(conditions))
	}
	if conditions[0].Status != metav1.ConditionTrue {
		t.Errorf("expected status True, got %s", conditions[0].Status)
	}
	if conditions[0].Reason != "NewReason" {
		t.Errorf("expected reason NewReason, got %s", conditions[0].Reason)
	}
}

func TestSetReady(t *testing.T) {
	var conditions []metav1.Condition
	SetReady(&conditions, nil, metav1.ConditionTrue, "Success", "synced", 1)

	if len(conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(conditions))
	}
	if conditions[0].Type != "Ready" {
		t.Errorf("expected type Ready, got %s", conditions[0].Type)
	}
}

func TestDerivePhase(t *testing.T) {
	cases := []struct {
		name   string
		status metav1.ConditionStatus
		reason string
		want   cloudflarev1alpha1.Phase
	}{
		// True → always Ready, regardless of reason.
		{"true_with_success_reason", metav1.ConditionTrue, cloudflarev1alpha1.ReasonReconcileSuccess, cloudflarev1alpha1.PhaseReady},
		{"true_with_unrelated_reason", metav1.ConditionTrue, "AnythingElse", cloudflarev1alpha1.PhaseReady},

		// False + in-progress reason → Reconciling.
		{"false_reconciling", metav1.ConditionFalse, cloudflarev1alpha1.ReasonReconciling, cloudflarev1alpha1.PhaseReconciling},
		{"false_zone_ref_not_ready", metav1.ConditionFalse, cloudflarev1alpha1.ReasonZoneRefNotReady, cloudflarev1alpha1.PhaseReconciling},
		{"false_zone_pending", metav1.ConditionFalse, cloudflarev1alpha1.ReasonZonePending, cloudflarev1alpha1.PhaseReconciling},
		{"false_gateway_not_ready", metav1.ConditionFalse, cloudflarev1alpha1.ReasonGatewayAddressNotReady, cloudflarev1alpha1.PhaseReconciling},
		{"false_tunnel_not_ready", metav1.ConditionFalse, cloudflarev1alpha1.ReasonTunnelNotReady, cloudflarev1alpha1.PhaseReconciling},

		// False + error reason → Error. Includes v0.12.0 (Part 1) reasons.
		{"false_invalid_spec", metav1.ConditionFalse, cloudflarev1alpha1.ReasonInvalidSpec, cloudflarev1alpha1.PhaseError},
		{"false_remote_gone", metav1.ConditionFalse, cloudflarev1alpha1.ReasonRemoteGone, cloudflarev1alpha1.PhaseError},
		{"false_permission_denied", metav1.ConditionFalse, cloudflarev1alpha1.ReasonPermissionDenied, cloudflarev1alpha1.PhaseError},
		{"false_plan_tier_required", metav1.ConditionFalse, cloudflarev1alpha1.ReasonPlanTierRequired, cloudflarev1alpha1.PhaseError},

		// Part 2 reason — Part 2 has merged.
		{"false_secret_not_labeled", metav1.ConditionFalse, cloudflarev1alpha1.ReasonSecretNotLabeled, cloudflarev1alpha1.PhaseError},

		// False + pre-existing failure reasons → Error.
		{"false_reconcile_error", metav1.ConditionFalse, cloudflarev1alpha1.ReasonReconcileError, cloudflarev1alpha1.PhaseError},
		{"false_cloudflare_error", metav1.ConditionFalse, cloudflarev1alpha1.ReasonCloudflareError, cloudflarev1alpha1.PhaseError},
		{"false_secret_not_found", metav1.ConditionFalse, cloudflarev1alpha1.ReasonSecretNotFound, cloudflarev1alpha1.PhaseError},
		{"false_unknown_string", metav1.ConditionFalse, "TotallyUnknownReason", cloudflarev1alpha1.PhaseError},
		{"false_empty_reason", metav1.ConditionFalse, "", cloudflarev1alpha1.PhaseError},

		// Unknown → Pending.
		{"unknown", metav1.ConditionUnknown, cloudflarev1alpha1.ReasonReconciling, cloudflarev1alpha1.PhasePending},
		{"unknown_empty_reason", metav1.ConditionUnknown, "", cloudflarev1alpha1.PhasePending},

		// Default arm — bogus status value falls through to PhasePending.
		{"bogus_status", metav1.ConditionStatus("Bogus"), "Anything", cloudflarev1alpha1.PhasePending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := derivePhase(tc.status, tc.reason)
			if got != tc.want {
				t.Errorf("derivePhase(%q, %q) = %q, want %q", tc.status, tc.reason, got, tc.want)
			}
		})
	}
}

func TestSetPhase_NilPointer_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("SetPhase(nil, ...) panicked: %v", r)
		}
	}()
	SetPhase(nil, cloudflarev1alpha1.PhaseDeleting)
}

func TestSetPhase_SetsPointerValue(t *testing.T) {
	var p cloudflarev1alpha1.Phase
	SetPhase(&p, cloudflarev1alpha1.PhaseDeleting)
	if p != cloudflarev1alpha1.PhaseDeleting {
		t.Errorf("SetPhase: got %q, want %q", p, cloudflarev1alpha1.PhaseDeleting)
	}
}

func TestSetPhase_OverwritesExisting(t *testing.T) {
	p := cloudflarev1alpha1.PhaseReady
	SetPhase(&p, cloudflarev1alpha1.PhaseDeleting)
	if p != cloudflarev1alpha1.PhaseDeleting {
		t.Errorf("SetPhase did not overwrite: got %q", p)
	}
}

func TestSetReady_NilPhasePointer_OnlyWritesCondition(t *testing.T) {
	conds := []metav1.Condition{}
	SetReady(&conds, nil, metav1.ConditionTrue,
		cloudflarev1alpha1.ReasonReconcileSuccess, "ok", 1)
	got := meta.FindStatusCondition(conds, cloudflarev1alpha1.ConditionTypeReady)
	if got == nil || got.Status != metav1.ConditionTrue {
		t.Fatalf("Ready condition not set correctly")
	}
	// No panic and no observable phase write — phase is nil.
}

func TestSetReady_NonNilPhase_DerivesPhase(t *testing.T) {
	conds := []metav1.Condition{}
	var p cloudflarev1alpha1.Phase
	SetReady(&conds, &p, metav1.ConditionTrue,
		cloudflarev1alpha1.ReasonReconcileSuccess, "ok", 1)
	if p != cloudflarev1alpha1.PhaseReady {
		t.Errorf("phase = %q, want %q", p, cloudflarev1alpha1.PhaseReady)
	}
}

func TestSetReady_NonNilPhase_FalseInProgress_SetsReconciling(t *testing.T) {
	conds := []metav1.Condition{}
	var p cloudflarev1alpha1.Phase
	SetReady(&conds, &p, metav1.ConditionFalse,
		cloudflarev1alpha1.ReasonZoneRefNotReady, "waiting", 2)
	if p != cloudflarev1alpha1.PhaseReconciling {
		t.Errorf("phase = %q, want %q", p, cloudflarev1alpha1.PhaseReconciling)
	}
}

func TestSetReady_NonNilPhase_FalseError_SetsError(t *testing.T) {
	conds := []metav1.Condition{}
	var p cloudflarev1alpha1.Phase
	SetReady(&conds, &p, metav1.ConditionFalse,
		cloudflarev1alpha1.ReasonInvalidSpec, "bad spec", 3)
	if p != cloudflarev1alpha1.PhaseError {
		t.Errorf("phase = %q, want %q", p, cloudflarev1alpha1.PhaseError)
	}
}

func TestSetReady_NonNilPhase_Unknown_SetsPending(t *testing.T) {
	conds := []metav1.Condition{}
	p := cloudflarev1alpha1.PhaseReady // pre-existing
	SetReady(&conds, &p, metav1.ConditionUnknown,
		cloudflarev1alpha1.ReasonReconciling, "starting", 0)
	if p != cloudflarev1alpha1.PhasePending {
		t.Errorf("phase = %q, want %q", p, cloudflarev1alpha1.PhasePending)
	}
}
