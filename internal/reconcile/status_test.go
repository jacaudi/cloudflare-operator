/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package reconcile

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
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
		want   v1alpha1.Phase
	}{
		{"ready-true", metav1.ConditionTrue, conventions.ReasonReady, v1alpha1.PhaseReady},
		{"reconciling", metav1.ConditionFalse, conventions.ReasonReconciling, v1alpha1.PhaseReconciling},
		{"error", metav1.ConditionFalse, conventions.ReasonDegraded, v1alpha1.PhaseError},
		{"unknown", metav1.ConditionUnknown, "", v1alpha1.PhasePending},
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
