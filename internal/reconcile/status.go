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

// Package reconcile holds Foundation-owned helpers shared by the bootstrap
// reconciler and the zone / tunnel controllers introduced in specs 2 and 3.
package reconcile

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
