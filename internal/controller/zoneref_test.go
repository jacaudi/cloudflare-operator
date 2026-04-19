package controller

import (
	"context"
	"testing"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newDNSRecord returns a minimal CloudflareDNSRecord satisfying zoneReferencer
// for ResolveZoneID tests. Spec.Name/Type are populated only to keep the object
// valid enough for round-tripping through the fake client if needed.
func newDNSRecord(namespace, zoneID string, zoneRef *cloudflarev1alpha1.ZoneReference) *cloudflarev1alpha1.CloudflareDNSRecord {
	return &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: namespace},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			Name:    "example.com",
			Type:    "A",
			ZoneID:  zoneID,
			ZoneRef: zoneRef,
		},
	}
}

func TestResolveZoneID_DirectZoneID(t *testing.T) {
	s := testScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	zoneID, err := ResolveZoneID(context.Background(), fakeClient, newDNSRecord("default", "zone-abc", nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if zoneID != "zone-abc" {
		t.Errorf("expected zone-abc, got %s", zoneID)
	}
}

func TestResolveZoneID_ZoneRefResolvesFromStatus(t *testing.T) {
	s := testScheme(t)

	zone := &cloudflarev1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-zone",
			Namespace: "default",
		},
		Spec: cloudflarev1alpha1.CloudflareZoneSpec{
			Name:      "example.com",
			AccountID: "acct-1",
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
		},
		Status: cloudflarev1alpha1.CloudflareZoneStatus{
			ZoneID: "resolved-zone-id",
			Status: "active",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(zone).
		WithStatusSubresource(zone).
		Build()

	// Set status after creation (fake client needs this)
	zone.Status.ZoneID = "resolved-zone-id"
	zone.Status.Status = "active"
	if err := fakeClient.Status().Update(context.Background(), zone); err != nil {
		t.Fatalf("failed to update status: %v", err)
	}

	ref := &cloudflarev1alpha1.ZoneReference{Name: "my-zone"}
	zoneID, err := ResolveZoneID(context.Background(), fakeClient, newDNSRecord("default", "", ref))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if zoneID != "resolved-zone-id" {
		t.Errorf("expected resolved-zone-id, got %s", zoneID)
	}
}

func TestResolveZoneID_ZoneRefNoStatusZoneID(t *testing.T) {
	s := testScheme(t)

	zone := &cloudflarev1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pending-zone",
			Namespace: "default",
		},
		Spec: cloudflarev1alpha1.CloudflareZoneSpec{
			Name:      "pending.com",
			AccountID: "acct-1",
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
		},
		Status: cloudflarev1alpha1.CloudflareZoneStatus{
			Status: "pending",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(zone).
		Build()

	ref := &cloudflarev1alpha1.ZoneReference{Name: "pending-zone"}
	_, err := ResolveZoneID(context.Background(), fakeClient, newDNSRecord("default", "", ref))
	if err == nil {
		t.Fatal("expected error for zone with no status.zoneID")
	}
}

func TestResolveZoneID_ZoneRefNotFound(t *testing.T) {
	s := testScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	ref := &cloudflarev1alpha1.ZoneReference{Name: "nonexistent-zone"}
	_, err := ResolveZoneID(context.Background(), fakeClient, newDNSRecord("default", "", ref))
	if err == nil {
		t.Fatal("expected error for non-existent CloudflareZone")
	}
}

func TestResolveZoneID_NeitherProvided(t *testing.T) {
	s := testScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	_, err := ResolveZoneID(context.Background(), fakeClient, newDNSRecord("default", "", nil))
	if err == nil {
		t.Fatal("expected error when neither zoneID nor zoneRef provided")
	}
}

func TestResolveZoneID_BothProvided_ZoneIDTakesPrecedence(t *testing.T) {
	s := testScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	ref := &cloudflarev1alpha1.ZoneReference{Name: "my-zone"}
	zoneID, err := ResolveZoneID(context.Background(), fakeClient, newDNSRecord("default", "direct-id", ref))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if zoneID != "direct-id" {
		t.Errorf("expected direct-id to take precedence, got %s", zoneID)
	}
}
