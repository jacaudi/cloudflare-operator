package status

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	SetReady(&conditions, metav1.ConditionTrue, "Success", "synced", 1)

	if len(conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(conditions))
	}
	if conditions[0].Type != "Ready" {
		t.Errorf("expected type Ready, got %s", conditions[0].Type)
	}
}
