package reconcile

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
		want   Phase
	}{
		{"ready-true", metav1.ConditionTrue, conventions.ReasonReady, PhaseReady},
		{"reconciling", metav1.ConditionFalse, conventions.ReasonReconciling, PhaseReconciling},
		{"error", metav1.ConditionFalse, conventions.ReasonDegraded, PhaseError},
		{"unknown", metav1.ConditionUnknown, "", PhasePending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, DerivePhase(c.status, c.reason))
		})
	}
}
