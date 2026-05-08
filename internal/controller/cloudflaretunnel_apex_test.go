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

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

func TestApexRecordName(t *testing.T) {
	tun := &cloudflarev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "external-edge", Namespace: "infra"},
	}
	if got := apexRecordName(tun); got != "external-edge-apex" {
		t.Errorf("apexRecordName = %q, want %q", got, "external-edge-apex")
	}
}

func TestValidateApexSpec(t *testing.T) {
	cases := []struct {
		name      string
		fqdn      string
		zoneFQDN  string
		wantErrIs error
	}{
		{"exact match", "example.com", "example.com", nil},
		{"subdomain match", "edge.example.com", "example.com", nil},
		{"deep subdomain", "a.b.c.example.com", "example.com", nil},
		{"empty name", "", "example.com", ErrApexInvalidName},
		{"malformed name", "not_a_dns_name!", "example.com", ErrApexInvalidName},
		{"unrelated zone", "edge.example.org", "example.com", ErrApexZoneMismatch},
		{"shared suffix but different zone", "evil-example.com", "example.com", ErrApexZoneMismatch},
		{"empty zone (zone not yet ready)", "edge.example.com", "", ErrApexZoneNotReady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateApexSpec(tc.fqdn, tc.zoneFQDN)
			if tc.wantErrIs == nil {
				if err != nil {
					t.Fatalf("validateApexSpec(%q,%q) = %v, want nil", tc.fqdn, tc.zoneFQDN, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErrIs) {
				t.Fatalf("validateApexSpec(%q,%q) = %v, want errors.Is(%v)", tc.fqdn, tc.zoneFQDN, err, tc.wantErrIs)
			}
		})
	}
}

// buildApexFakeClient builds a fake client preloaded with the given
// CloudflareDNSRecords and configured with the CloudflareDNSRecord and
// CloudflareTunnel status subresources so status updates round-trip.
// Status subresources aren't strictly required by the Task 2 helpers, but
// the helper signature is shared with later orchestrator tests that do
// write status.
func buildApexFakeClient(t *testing.T, recs ...*cloudflarev1alpha1.CloudflareDNSRecord) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := cloudflarev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	objs := make([]client.Object, 0, len(recs))
	for _, r := range recs {
		objs = append(objs, r)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&cloudflarev1alpha1.CloudflareDNSRecord{}, &cloudflarev1alpha1.CloudflareTunnel{}).
		WithObjects(objs...).
		Build()
}

func TestFindCollidingApexCR(t *testing.T) {
	mkRec := func(ns, name, fqdn string) *cloudflarev1alpha1.CloudflareDNSRecord {
		return &cloudflarev1alpha1.CloudflareDNSRecord{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
				Name: fqdn,
				Type: "CNAME",
			},
		}
	}

	t.Run("no records", func(t *testing.T) {
		c := buildApexFakeClient(t)
		got, err := findCollidingApexCR(context.Background(), c, "infra", "edge.example.com", "external-edge-apex")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got != nil {
			t.Fatalf("got = %v, want nil", got)
		}
	})

	t.Run("our own CR is not a collision", func(t *testing.T) {
		ours := mkRec("infra", "external-edge-apex", "edge.example.com")
		c := buildApexFakeClient(t, ours)
		got, err := findCollidingApexCR(context.Background(), c, "infra", "edge.example.com", "external-edge-apex")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got != nil {
			t.Fatalf("got = %v, want nil (our own CR is not a collision)", got.Name)
		}
	})

	t.Run("other CR with same name in same ns is a collision", func(t *testing.T) {
		other := mkRec("infra", "user-handwritten", "edge.example.com")
		c := buildApexFakeClient(t, other)
		got, err := findCollidingApexCR(context.Background(), c, "infra", "edge.example.com", "external-edge-apex")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got == nil || got.Name != "user-handwritten" {
			t.Fatalf("got = %v, want user-handwritten", got)
		}
	})

	t.Run("CR in a different namespace is not a collision", func(t *testing.T) {
		// CR-level collision is namespace-scoped; the Cloudflare-side
		// collision (different CR but same FQDN globally) is the
		// CloudflareDNSRecord controller's TXT-registry job.
		other := mkRec("other-ns", "user-handwritten", "edge.example.com")
		c := buildApexFakeClient(t, other)
		got, err := findCollidingApexCR(context.Background(), c, "infra", "edge.example.com", "external-edge-apex")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got != nil {
			t.Fatalf("got = %v, want nil (different namespace)", got.Name)
		}
	})

	t.Run("CR with different name is not a collision", func(t *testing.T) {
		other := mkRec("infra", "user-handwritten", "different.example.com")
		c := buildApexFakeClient(t, other)
		got, err := findCollidingApexCR(context.Background(), c, "infra", "edge.example.com", "external-edge-apex")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got != nil {
			t.Fatalf("got = %v, want nil (different FQDN)", got.Name)
		}
	})
}

func TestDesiredApexRecord(t *testing.T) {
	tun := &cloudflarev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "external-edge",
			Namespace: "infra",
			UID:       "tun-uid-123",
		},
		TypeMeta: metav1.TypeMeta{
			APIVersion: cloudflarev1alpha1.GroupVersion.String(),
			Kind:       "CloudflareTunnel",
		},
		Spec: cloudflarev1alpha1.CloudflareTunnelSpec{
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "cf-creds", Namespace: "cf-system"},
			ApexHostname: &cloudflarev1alpha1.ApexHostnameSpec{
				Name:    "edge.example.com",
				ZoneRef: cloudflarev1alpha1.ZoneReference{Name: "example-com", Namespace: "cf-system"},
				// Proxied unset -> default applied at construction time
			},
		},
		Status: cloudflarev1alpha1.CloudflareTunnelStatus{
			TunnelID:    "abcd-1234",
			TunnelCNAME: "abcd-1234.cfargotunnel.com",
		},
	}

	got := desiredApexRecord(tun)
	if got.Name != "external-edge-apex" {
		t.Errorf("Name = %q, want external-edge-apex", got.Name)
	}
	if got.Namespace != "infra" {
		t.Errorf("Namespace = %q, want infra", got.Namespace)
	}
	if got.Spec.Name != "edge.example.com" {
		t.Errorf("Spec.Name = %q, want edge.example.com", got.Spec.Name)
	}
	if got.Spec.Type != "CNAME" {
		t.Errorf("Spec.Type = %q, want CNAME", got.Spec.Type)
	}
	if got.Spec.Content == nil || *got.Spec.Content != "abcd-1234.cfargotunnel.com" {
		t.Errorf("Spec.Content = %v, want pointer to %q", got.Spec.Content, "abcd-1234.cfargotunnel.com")
	}
	if got.Spec.Proxied == nil || *got.Spec.Proxied != true {
		t.Errorf("Spec.Proxied = %v, want pointer to true (default)", got.Spec.Proxied)
	}
	if got.Spec.ZoneRef == nil || got.Spec.ZoneRef.Name != "example-com" || got.Spec.ZoneRef.Namespace != "cf-system" {
		t.Errorf("Spec.ZoneRef = %v, want example-com/cf-system", got.Spec.ZoneRef)
	}
	if got.Spec.SecretRef.Name != "cf-creds" || got.Spec.SecretRef.Namespace != "cf-system" {
		t.Errorf("Spec.SecretRef = %v, want cf-creds/cf-system", got.Spec.SecretRef)
	}
	// Owner-ref to the tunnel, controller=true.
	if len(got.OwnerReferences) != 1 {
		t.Fatalf("OwnerReferences len=%d, want 1", len(got.OwnerReferences))
	}
	o := got.OwnerReferences[0]
	if o.Kind != "CloudflareTunnel" || o.Name != "external-edge" || o.UID != "tun-uid-123" {
		t.Errorf("OwnerReference = %+v", o)
	}
	if o.Controller == nil || !*o.Controller {
		t.Errorf("OwnerReference.Controller = %v, want true", o.Controller)
	}
}

func TestDesiredApexRecord_ProxiedExplicitlyFalse(t *testing.T) {
	pf := false
	tun := &cloudflarev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "edge", Namespace: "infra", UID: "u"},
		TypeMeta:   metav1.TypeMeta{APIVersion: cloudflarev1alpha1.GroupVersion.String(), Kind: "CloudflareTunnel"},
		Spec: cloudflarev1alpha1.CloudflareTunnelSpec{
			SecretRef:    cloudflarev1alpha1.SecretReference{Name: "s"},
			ApexHostname: &cloudflarev1alpha1.ApexHostnameSpec{Name: "edge.example.com", ZoneRef: cloudflarev1alpha1.ZoneReference{Name: "z"}, Proxied: &pf},
		},
		Status: cloudflarev1alpha1.CloudflareTunnelStatus{TunnelCNAME: "x.cfargotunnel.com"},
	}
	got := desiredApexRecord(tun)
	if got.Spec.Proxied == nil || *got.Spec.Proxied != false {
		t.Fatalf("Spec.Proxied = %v, want pointer to false (explicit)", got.Spec.Proxied)
	}
}

func TestDeleteApexRecordIfPresent(t *testing.T) {
	tun := &cloudflarev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "external-edge", Namespace: "infra"},
	}

	t.Run("idempotent when no apex CR exists", func(t *testing.T) {
		c := buildApexFakeClient(t)
		if err := deleteApexRecordIfPresent(context.Background(), c, tun); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	})

	t.Run("deletes existing apex CR by deterministic name", func(t *testing.T) {
		ours := &cloudflarev1alpha1.CloudflareDNSRecord{
			ObjectMeta: metav1.ObjectMeta{Name: "external-edge-apex", Namespace: "infra"},
			Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
				Name: "edge.example.com", Type: "CNAME",
			},
		}
		c := buildApexFakeClient(t, ours)
		if err := deleteApexRecordIfPresent(context.Background(), c, tun); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		var got cloudflarev1alpha1.CloudflareDNSRecord
		err := c.Get(context.Background(), types.NamespacedName{Name: "external-edge-apex", Namespace: "infra"}, &got)
		if err == nil || !apierrors.IsNotFound(err) {
			t.Fatalf("apex CR still present (err=%v)", err)
		}
	})
}

// readyTunnel returns a CloudflareTunnel with TunnelID/TunnelCNAME set,
// representing a tunnel that has finished provisioning. Apex spec is
// caller's responsibility.
func readyTunnel(name, ns string) *cloudflarev1alpha1.CloudflareTunnel {
	return &cloudflarev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, UID: types.UID("uid-" + name),
		},
		TypeMeta: metav1.TypeMeta{
			APIVersion: cloudflarev1alpha1.GroupVersion.String(),
			Kind:       "CloudflareTunnel",
		},
		Spec: cloudflarev1alpha1.CloudflareTunnelSpec{
			Name:                name,
			SecretRef:           cloudflarev1alpha1.SecretReference{Name: "cf-creds"},
			GeneratedSecretName: name + "-creds",
		},
		Status: cloudflarev1alpha1.CloudflareTunnelStatus{
			TunnelID:    "tunid-" + name,
			TunnelCNAME: "tunid-" + name + ".cfargotunnel.com",
		},
	}
}

func TestReconcileApexHostname_SpecAbsent_NoOp(t *testing.T) {
	tun := readyTunnel("external-edge", "infra")
	c := buildApexFakeClient(t)

	res, err := reconcileApexHostname(context.Background(), c, tun)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.RequeueAfter != 0 || res.Requeue {
		t.Fatalf("res = %+v, want zero", res)
	}
	if tun.Status.ApexHostname != nil {
		t.Errorf("Status.ApexHostname = %+v, want nil", tun.Status.ApexHostname)
	}
	if meta.FindStatusCondition(tun.Status.Conditions, cloudflarev1alpha1.ConditionTypeApexHostnameReady) != nil {
		t.Errorf("ApexHostnameReady condition should not exist when spec is absent")
	}
}

func TestReconcileApexHostname_SpecAbsent_GC(t *testing.T) {
	tun := readyTunnel("external-edge", "infra")
	tun.Status.ApexHostname = &cloudflarev1alpha1.ApexHostnameStatus{
		Name: "edge.example.com", RecordID: "rec-123",
	}
	meta.SetStatusCondition(&tun.Status.Conditions, metav1.Condition{
		Type:   cloudflarev1alpha1.ConditionTypeApexHostnameReady,
		Status: metav1.ConditionTrue,
		Reason: cloudflarev1alpha1.ReasonReconcileSuccess,
	})

	apexCR := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "external-edge-apex", Namespace: "infra"},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			Name: "edge.example.com", Type: "CNAME",
		},
	}
	c := buildApexFakeClient(t, apexCR)

	res, err := reconcileApexHostname(context.Background(), c, tun)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("res.RequeueAfter = %v, want 0", res.RequeueAfter)
	}
	// Apex CR removed.
	var probe cloudflarev1alpha1.CloudflareDNSRecord
	getErr := c.Get(context.Background(), types.NamespacedName{Name: "external-edge-apex", Namespace: "infra"}, &probe)
	if getErr == nil || !apierrors.IsNotFound(getErr) {
		t.Errorf("apex CR not deleted: getErr=%v", getErr)
	}
	// Status cleared.
	if tun.Status.ApexHostname != nil {
		t.Errorf("Status.ApexHostname = %+v, want nil", tun.Status.ApexHostname)
	}
	// Condition removed.
	if meta.FindStatusCondition(tun.Status.Conditions, cloudflarev1alpha1.ConditionTypeApexHostnameReady) != nil {
		t.Errorf("ApexHostnameReady condition should be removed")
	}
}

// hasCondition returns true iff conditions has a Type==t entry with the
// given Status and Reason. Used by orchestrator tests to assert what
// reconcileApexHostname set.
func hasCondition(conditions []metav1.Condition, t string, s metav1.ConditionStatus, reason string) bool {
	c := meta.FindStatusCondition(conditions, t)
	return c != nil && c.Status == s && c.Reason == reason
}

func TestReconcileApexHostname_ZoneNotFound(t *testing.T) {
	tun := readyTunnel("external-edge", "infra")
	tun.Spec.ApexHostname = &cloudflarev1alpha1.ApexHostnameSpec{
		Name:    "edge.example.com",
		ZoneRef: cloudflarev1alpha1.ZoneReference{Name: "missing-zone"},
	}
	c := buildApexFakeClient(t) // no zone present

	res, err := reconcileApexHostname(context.Background(), c, tun)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.RequeueAfter != 30*time.Second {
		t.Errorf("res.RequeueAfter = %v, want 30s", res.RequeueAfter)
	}
	if !hasCondition(tun.Status.Conditions, cloudflarev1alpha1.ConditionTypeApexHostnameReady,
		metav1.ConditionFalse, cloudflarev1alpha1.ReasonZoneRefNotReady) {
		t.Errorf("expected ApexHostnameReady=False ReasonZoneRefNotReady; got %+v",
			meta.FindStatusCondition(tun.Status.Conditions, cloudflarev1alpha1.ConditionTypeApexHostnameReady))
	}
}

func TestReconcileApexHostname_ZoneNotReady(t *testing.T) {
	zone := &cloudflarev1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: "infra"},
		Spec:       cloudflarev1alpha1.CloudflareZoneSpec{Name: "example.com"},
		// No Ready condition; treat as not-ready.
	}
	tun := readyTunnel("external-edge", "infra")
	tun.Spec.ApexHostname = &cloudflarev1alpha1.ApexHostnameSpec{
		Name:    "edge.example.com",
		ZoneRef: cloudflarev1alpha1.ZoneReference{Name: "example-com"},
	}
	c := buildApexFakeClientWithZone(t, zone)

	res, err := reconcileApexHostname(context.Background(), c, tun)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.RequeueAfter != 30*time.Second {
		t.Errorf("res.RequeueAfter = %v, want 30s", res.RequeueAfter)
	}
	if !hasCondition(tun.Status.Conditions, cloudflarev1alpha1.ConditionTypeApexHostnameReady,
		metav1.ConditionFalse, cloudflarev1alpha1.ReasonZoneRefNotReady) {
		t.Errorf("expected ApexHostnameReady=False ReasonZoneRefNotReady")
	}
}

func TestReconcileApexHostname_NameNotUnderZone(t *testing.T) {
	zone := &cloudflarev1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: "infra"},
		Spec:       cloudflarev1alpha1.CloudflareZoneSpec{Name: "example.com"},
		Status: cloudflarev1alpha1.CloudflareZoneStatus{
			Conditions: []metav1.Condition{{
				Type:   cloudflarev1alpha1.ConditionTypeReady,
				Status: metav1.ConditionTrue,
				Reason: cloudflarev1alpha1.ReasonReconcileSuccess,
			}},
		},
	}
	tun := readyTunnel("external-edge", "infra")
	tun.Spec.ApexHostname = &cloudflarev1alpha1.ApexHostnameSpec{
		Name:    "edge.example.org",
		ZoneRef: cloudflarev1alpha1.ZoneReference{Name: "example-com"},
	}
	c := buildApexFakeClientWithZone(t, zone)

	res, err := reconcileApexHostname(context.Background(), c, tun)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("res.RequeueAfter = %v, want 0", res.RequeueAfter)
	}
	if !hasCondition(tun.Status.Conditions, cloudflarev1alpha1.ConditionTypeApexHostnameReady,
		metav1.ConditionFalse, cloudflarev1alpha1.ReasonInvalidSpec) {
		t.Errorf("expected ApexHostnameReady=False ReasonInvalidSpec")
	}
	// No apex CR should have been created.
	var probe cloudflarev1alpha1.CloudflareDNSRecord
	getErr := c.Get(context.Background(), types.NamespacedName{Name: "external-edge-apex", Namespace: "infra"}, &probe)
	if getErr == nil {
		t.Errorf("apex CR was created despite validation failure")
	}
}

// buildApexFakeClientWithZone is a variant of buildApexFakeClient that
// preloads a CloudflareZone alongside any DNS records.
func buildApexFakeClientWithZone(t *testing.T, zone *cloudflarev1alpha1.CloudflareZone, recs ...*cloudflarev1alpha1.CloudflareDNSRecord) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := cloudflarev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	objs := []client.Object{zone}
	for _, r := range recs {
		objs = append(objs, r)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(
			&cloudflarev1alpha1.CloudflareDNSRecord{},
			&cloudflarev1alpha1.CloudflareTunnel{},
			&cloudflarev1alpha1.CloudflareZone{},
		).
		WithObjects(objs...).
		Build()
}

func TestReconcileApexHostname_Collision(t *testing.T) {
	zone := &cloudflarev1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: "infra"},
		Spec:       cloudflarev1alpha1.CloudflareZoneSpec{Name: "example.com"},
		Status: cloudflarev1alpha1.CloudflareZoneStatus{
			Conditions: []metav1.Condition{{
				Type: cloudflarev1alpha1.ConditionTypeReady, Status: metav1.ConditionTrue,
				Reason: cloudflarev1alpha1.ReasonReconcileSuccess,
			}},
		},
	}
	hand := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "user-handwritten", Namespace: "infra"},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			Name: "edge.example.com", Type: "CNAME",
		},
	}

	tun := readyTunnel("external-edge", "infra")
	tun.Spec.ApexHostname = &cloudflarev1alpha1.ApexHostnameSpec{
		Name:    "edge.example.com",
		ZoneRef: cloudflarev1alpha1.ZoneReference{Name: "example-com"},
	}
	c := buildApexFakeClientWithZone(t, zone, hand)

	if _, err := reconcileApexHostname(context.Background(), c, tun); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !hasCondition(tun.Status.Conditions, cloudflarev1alpha1.ConditionTypeApexHostnameReady,
		metav1.ConditionFalse, cloudflarev1alpha1.ReasonRecordOwnershipConflict) {
		t.Errorf("expected ApexHostnameReady=False ReasonRecordOwnershipConflict")
	}
	// The handwritten CR must remain untouched.
	var probe cloudflarev1alpha1.CloudflareDNSRecord
	if err := c.Get(context.Background(), types.NamespacedName{Name: "user-handwritten", Namespace: "infra"}, &probe); err != nil {
		t.Errorf("handwritten CR was deleted: %v", err)
	}
	// No apex CR was created.
	var apex cloudflarev1alpha1.CloudflareDNSRecord
	getErr := c.Get(context.Background(), types.NamespacedName{Name: "external-edge-apex", Namespace: "infra"}, &apex)
	if getErr == nil {
		t.Errorf("apex CR was created despite collision")
	}
}

func TestReconcileApexHostname_HappyPath(t *testing.T) {
	zone := &cloudflarev1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: "infra"},
		Spec:       cloudflarev1alpha1.CloudflareZoneSpec{Name: "example.com"},
		Status: cloudflarev1alpha1.CloudflareZoneStatus{
			Conditions: []metav1.Condition{{
				Type: cloudflarev1alpha1.ConditionTypeReady, Status: metav1.ConditionTrue,
				Reason: cloudflarev1alpha1.ReasonReconcileSuccess,
			}},
		},
	}
	tun := readyTunnel("external-edge", "infra")
	tun.Spec.ApexHostname = &cloudflarev1alpha1.ApexHostnameSpec{
		Name:    "edge.example.com",
		ZoneRef: cloudflarev1alpha1.ZoneReference{Name: "example-com"},
	}
	c := buildApexFakeClientWithZone(t, zone)

	if _, err := reconcileApexHostname(context.Background(), c, tun); err != nil {
		t.Fatalf("err = %v", err)
	}

	// Apex CR was created.
	var apex cloudflarev1alpha1.CloudflareDNSRecord
	if err := c.Get(context.Background(), types.NamespacedName{Name: "external-edge-apex", Namespace: "infra"}, &apex); err != nil {
		t.Fatalf("apex CR not found: %v", err)
	}
	if apex.Spec.Name != "edge.example.com" {
		t.Errorf("apex.Spec.Name = %q, want edge.example.com", apex.Spec.Name)
	}
	if apex.Spec.Type != "CNAME" {
		t.Errorf("apex.Spec.Type = %q, want CNAME", apex.Spec.Type)
	}
	if apex.Spec.Content == nil || *apex.Spec.Content != tun.Status.TunnelCNAME {
		t.Errorf("apex.Spec.Content = %v, want %q", apex.Spec.Content, tun.Status.TunnelCNAME)
	}
	if apex.Spec.Proxied == nil || *apex.Spec.Proxied != true {
		t.Errorf("apex.Spec.Proxied = %v, want pointer to true", apex.Spec.Proxied)
	}
	// Owner-reffed.
	if len(apex.OwnerReferences) != 1 || apex.OwnerReferences[0].UID != tun.UID {
		t.Errorf("OwnerReferences = %+v", apex.OwnerReferences)
	}
	// Status.ApexHostname populated.
	if tun.Status.ApexHostname == nil || tun.Status.ApexHostname.Name != "edge.example.com" {
		t.Errorf("Status.ApexHostname = %+v", tun.Status.ApexHostname)
	}
	// Apex condition is False/Pending because the apex CR has no Ready=True yet.
	if !hasCondition(tun.Status.Conditions, cloudflarev1alpha1.ConditionTypeApexHostnameReady,
		metav1.ConditionFalse, cloudflarev1alpha1.ReasonApexRecordPending) {
		t.Errorf("expected ApexHostnameReady=False ReasonApexRecordPending; got %+v",
			meta.FindStatusCondition(tun.Status.Conditions, cloudflarev1alpha1.ConditionTypeApexHostnameReady))
	}
}

func TestReconcileApexHostname_HappyPath_RecordReady(t *testing.T) {
	zone := &cloudflarev1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: "infra"},
		Spec:       cloudflarev1alpha1.CloudflareZoneSpec{Name: "example.com"},
		Status: cloudflarev1alpha1.CloudflareZoneStatus{
			Conditions: []metav1.Condition{{
				Type: cloudflarev1alpha1.ConditionTypeReady, Status: metav1.ConditionTrue,
				Reason: cloudflarev1alpha1.ReasonReconcileSuccess,
			}},
		},
	}
	// Pre-existing apex CR with Ready=True (representing a steady-state reconcile).
	apexExisting := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "external-edge-apex", Namespace: "infra"},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			Name: "edge.example.com", Type: "CNAME",
		},
		Status: cloudflarev1alpha1.CloudflareDNSRecordStatus{
			RecordID: "rec-789",
			Conditions: []metav1.Condition{{
				Type: cloudflarev1alpha1.ConditionTypeReady, Status: metav1.ConditionTrue,
				Reason: cloudflarev1alpha1.ReasonReconcileSuccess,
			}},
		},
	}
	tun := readyTunnel("external-edge", "infra")
	tun.Spec.ApexHostname = &cloudflarev1alpha1.ApexHostnameSpec{
		Name:    "edge.example.com",
		ZoneRef: cloudflarev1alpha1.ZoneReference{Name: "example-com"},
	}
	c := buildApexFakeClientWithZone(t, zone, apexExisting)

	if _, err := reconcileApexHostname(context.Background(), c, tun); err != nil {
		t.Fatalf("err = %v", err)
	}
	if tun.Status.ApexHostname == nil || tun.Status.ApexHostname.RecordID != "rec-789" {
		t.Errorf("Status.ApexHostname.RecordID = %v, want rec-789", tun.Status.ApexHostname)
	}
	if !hasCondition(tun.Status.Conditions, cloudflarev1alpha1.ConditionTypeApexHostnameReady,
		metav1.ConditionTrue, cloudflarev1alpha1.ReasonReconcileSuccess) {
		t.Errorf("expected ApexHostnameReady=True ReasonReconcileSuccess")
	}
}

func TestReconcileApexHostname_RotationUpdatesContent(t *testing.T) {
	zone := &cloudflarev1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: "infra"},
		Spec:       cloudflarev1alpha1.CloudflareZoneSpec{Name: "example.com"},
		Status: cloudflarev1alpha1.CloudflareZoneStatus{
			Conditions: []metav1.Condition{{
				Type: cloudflarev1alpha1.ConditionTypeReady, Status: metav1.ConditionTrue,
				Reason: cloudflarev1alpha1.ReasonReconcileSuccess,
			}},
		},
	}
	// Apex CR exists from a prior reconcile, points at the OLD UUID.
	apexExisting := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "external-edge-apex", Namespace: "infra"},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			Name: "edge.example.com", Type: "CNAME",
			Content: strPtr("OLD-UUID.cfargotunnel.com"),
		},
	}
	tun := readyTunnel("external-edge", "infra")
	// Simulate rotation: Status.TunnelCNAME now points at the NEW UUID.
	tun.Status.TunnelID = "new-uuid"
	tun.Status.TunnelCNAME = "new-uuid.cfargotunnel.com"
	tun.Spec.ApexHostname = &cloudflarev1alpha1.ApexHostnameSpec{
		Name:    "edge.example.com",
		ZoneRef: cloudflarev1alpha1.ZoneReference{Name: "example-com"},
	}
	c := buildApexFakeClientWithZone(t, zone, apexExisting)

	if _, err := reconcileApexHostname(context.Background(), c, tun); err != nil {
		t.Fatalf("err = %v", err)
	}

	var apex cloudflarev1alpha1.CloudflareDNSRecord
	if err := c.Get(context.Background(), types.NamespacedName{Name: "external-edge-apex", Namespace: "infra"}, &apex); err != nil {
		t.Fatalf("get apex: %v", err)
	}
	if apex.Spec.Content == nil || *apex.Spec.Content != "new-uuid.cfargotunnel.com" {
		t.Errorf("apex.Spec.Content = %v, want new-uuid.cfargotunnel.com (rotation)", apex.Spec.Content)
	}
}

// TestReconcileApexHostname_TypeMetaSelfHeal verifies the orchestrator
// recovers when the tunnel's TypeMeta has been stripped (as happens on
// cached reads via controller-runtime's typed client). Without self-heal,
// the apex CR's owner-ref would have empty APIVersion/Kind.
func TestReconcileApexHostname_TypeMetaSelfHeal(t *testing.T) {
	zone := &cloudflarev1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: "infra"},
		Spec:       cloudflarev1alpha1.CloudflareZoneSpec{Name: "example.com"},
		Status: cloudflarev1alpha1.CloudflareZoneStatus{
			Conditions: []metav1.Condition{{
				Type: cloudflarev1alpha1.ConditionTypeReady, Status: metav1.ConditionTrue,
				Reason: cloudflarev1alpha1.ReasonReconcileSuccess,
			}},
		},
	}
	tun := readyTunnel("external-edge", "infra")
	// Simulate the cached-read TypeMeta strip.
	tun.TypeMeta = metav1.TypeMeta{}
	tun.Spec.ApexHostname = &cloudflarev1alpha1.ApexHostnameSpec{
		Name:    "edge.example.com",
		ZoneRef: cloudflarev1alpha1.ZoneReference{Name: "example-com"},
	}
	c := buildApexFakeClientWithZone(t, zone)

	if _, err := reconcileApexHostname(context.Background(), c, tun); err != nil {
		t.Fatalf("err = %v", err)
	}

	var apex cloudflarev1alpha1.CloudflareDNSRecord
	if err := c.Get(context.Background(), types.NamespacedName{Name: "external-edge-apex", Namespace: "infra"}, &apex); err != nil {
		t.Fatalf("apex CR not found: %v", err)
	}
	if len(apex.OwnerReferences) != 1 {
		t.Fatalf("OwnerReferences len=%d, want 1", len(apex.OwnerReferences))
	}
	o := apex.OwnerReferences[0]
	if o.APIVersion == "" {
		t.Errorf("OwnerReferences[0].APIVersion is empty; TypeMeta self-heal failed")
	}
	if o.Kind != "CloudflareTunnel" {
		t.Errorf("OwnerReferences[0].Kind = %q, want CloudflareTunnel", o.Kind)
	}
	if o.APIVersion != cloudflarev1alpha1.GroupVersion.String() {
		t.Errorf("OwnerReferences[0].APIVersion = %q, want %q",
			o.APIVersion, cloudflarev1alpha1.GroupVersion.String())
	}
}
