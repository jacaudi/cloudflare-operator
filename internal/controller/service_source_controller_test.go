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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

const testRecordTypeCNAME = "CNAME"

// ---- test infrastructure ----------------------------------------------------

func svcTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := cloudflarev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add v1alpha1: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	return s
}

func buildSvcReconciler(s *runtime.Scheme, ownerID string, objs ...client.Object) (*ServiceSourceReconciler, *record.FakeRecorder) {
	recorder := record.NewFakeRecorder(32)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(
			&cloudflarev1alpha1.CloudflareDNSRecord{},
			&cloudflarev1alpha1.CloudflareTunnelRule{},
			&cloudflarev1alpha1.CloudflareTunnel{},
		).
		Build()
	return &ServiceSourceReconciler{
		Client:     c,
		Recorder:   recorder,
		TxtOwnerID: ownerID,
	}, recorder
}

// newSvc returns a Service with the given annotations, one HTTP port.
func newSvc(name, ns string, ann map[string]string) *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			UID:         types.UID(name + "-uid"),
			Annotations: ann,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 8080, Protocol: corev1.ProtocolTCP},
			},
		},
	}
}

// newSvcMultiPort returns a Service with multiple named ports.
func newSvcMultiPort(name, ns string, ann map[string]string, ports []corev1.ServicePort) *corev1.Service {
	svc := newSvc(name, ns, ann)
	svc.Spec.Ports = ports
	return svc
}

// newZone returns a CloudflareZone ready for use in tests.
func newZone(name, ns, zoneName, secretRef string) *cloudflarev1alpha1.CloudflareZone {
	return &cloudflarev1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: cloudflarev1alpha1.CloudflareZoneSpec{
			Name:      zoneName,
			SecretRef: cloudflarev1alpha1.SecretReference{Name: secretRef},
		},
	}
}

// newReadyTunnel returns a CloudflareTunnel with TunnelCNAME set.
func newReadyTunnel(name, ns string) *cloudflarev1alpha1.CloudflareTunnel {
	return &cloudflarev1alpha1.CloudflareTunnel{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "cloudflare.io/v1alpha1",
			Kind:       "CloudflareTunnel",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: cloudflarev1alpha1.CloudflareTunnelSpec{
			Name:                name,
			SecretRef:           cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
			GeneratedSecretName: name + "-credentials",
		},
		Status: cloudflarev1alpha1.CloudflareTunnelStatus{
			TunnelCNAME: "test-tunnel-id.cfargotunnel.com",
			TunnelID:    "test-tunnel-id",
		},
	}
}

func req(ns, name string) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
}

// expectEvent drains the recorder channel and returns events.
func drainEvents(recorder *record.FakeRecorder) []string {
	var events []string
	for {
		select {
		case e := <-recorder.Events:
			events = append(events, e)
		default:
			return events
		}
	}
}

// ---- TestServiceSource_MissingAnnotationIgnored ----------------------------------------

func TestServiceSource_MissingAnnotationIgnored(t *testing.T) {
	s := svcTestScheme(t)
	svc := newSvc("my-svc", "apps", nil)
	r, rec := buildSvcReconciler(s, "owner1", svc)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No target annotation → no emissions.
	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) != 0 {
		t.Errorf("expected 0 DNS records, got %d", len(records.Items))
	}
	// No events should be emitted.
	evts := drainEvents(rec)
	if len(evts) != 0 {
		t.Errorf("expected no events, got %v", evts)
	}
}

// ---- TestServiceSource_MissingTxtOwnerID_Warns ------------------------------

func TestServiceSource_MissingTxtOwnerID_Warns(t *testing.T) {
	s := svcTestScheme(t)
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
	})
	r, rec := buildSvcReconciler(s, "" /* empty TxtOwnerID */, svc)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	if len(evts) == 0 {
		t.Error("expected a warning event for missing TxtOwnerID")
	}
	// No emissions.
	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) != 0 {
		t.Errorf("expected 0 DNS records, got %d", len(records.Items))
	}
}

// ---- TestServiceSource_TargetAddressOnService_Rejected ----------------------

func TestServiceSource_TargetAddressOnService_Rejected(t *testing.T) {
	s := svcTestScheme(t)
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "address",
		AnnotationHostnames: "foo.example.com",
	})
	r, rec := buildSvcReconciler(s, "owner1", svc)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	found := false
	for _, e := range evts {
		if strings.Contains(e, string(cloudflarev1alpha1.ReasonInvalidAnnotation)) || strings.Contains(e, "Warning") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning event for address target on Service; got %v", evts)
	}
	// No emissions.
	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) != 0 {
		t.Errorf("expected 0 DNS records, got %d", len(records.Items))
	}
}

// ---- TestServiceSource_MissingHostnames_Rejected ----------------------------

func TestServiceSource_MissingHostnames_Rejected(t *testing.T) {
	s := svcTestScheme(t)
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget: "tunnel:home",
		// no AnnotationHostnames
	})
	r, rec := buildSvcReconciler(s, "owner1", svc)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	if len(evts) == 0 {
		t.Error("expected a warning event for missing hostnames")
	}
	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) != 0 {
		t.Errorf("expected 0 DNS records, got %d", len(records.Items))
	}
}

// ---- TestServiceSource_InvalidHostname_Rejected -----------------------------

func TestServiceSource_InvalidHostname_Rejected(t *testing.T) {
	s := svcTestScheme(t)
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "bad_name.example.com",
	})
	r, rec := buildSvcReconciler(s, "owner1", svc)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	if len(evts) == 0 {
		t.Error("expected a warning event for invalid hostname")
	}
	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) != 0 {
		t.Errorf("expected 0 DNS records, got %d", len(records.Items))
	}
}

// ---- TestServiceSource_NoPorts ----------------------------------------------

func TestServiceSource_NoPorts(t *testing.T) {
	s := svcTestScheme(t)
	svc := newSvcMultiPort("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
	}, []corev1.ServicePort{}) // zero ports
	r, rec := buildSvcReconciler(s, "owner1", svc)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	if len(evts) == 0 {
		t.Error("expected a warning event for no ports")
	}
	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) != 0 {
		t.Errorf("expected 0 DNS records, got %d", len(records.Items))
	}
}

// ---- TestServiceSource_BackendPortByName ------------------------------------

func TestServiceSource_BackendPortByName(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	svc := newSvcMultiPort("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
		AnnotationPort:      "http",
	}, []corev1.ServicePort{
		{Name: "http", Port: 8080, Protocol: corev1.ProtocolTCP},
		{Name: "metrics", Port: 9090, Protocol: corev1.ProtocolTCP},
	})
	r, _ := buildSvcReconciler(s, "owner1", svc, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 1 {
		t.Fatalf("expected 1 tunnel rule, got %d", len(rules.Items))
	}
	ref := rules.Items[0].Spec.Backend.ServiceRef
	if ref == nil {
		t.Fatal("expected ServiceRef to be set")
	}
	if ref.Port.IntValue() != 8080 {
		t.Errorf("expected port 8080, got %v", ref.Port)
	}
}

// ---- TestServiceSource_BackendPortByNumber ----------------------------------

func TestServiceSource_BackendPortByNumber(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	svc := newSvcMultiPort("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
		AnnotationPort:      "9090",
	}, []corev1.ServicePort{
		{Name: "http", Port: 8080, Protocol: corev1.ProtocolTCP},
		{Name: "metrics", Port: 9090, Protocol: corev1.ProtocolTCP},
	})
	r, _ := buildSvcReconciler(s, "owner1", svc, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 1 {
		t.Fatalf("expected 1 tunnel rule, got %d", len(rules.Items))
	}
	ref := rules.Items[0].Spec.Backend.ServiceRef
	if ref == nil {
		t.Fatal("expected ServiceRef to be set")
	}
	if ref.Port.IntValue() != 9090 {
		t.Errorf("expected port 9090, got %v", ref.Port)
	}
}

// ---- TestServiceSource_BackendPortNotFound ----------------------------------

func TestServiceSource_BackendPortNotFound(t *testing.T) {
	s := svcTestScheme(t)
	svc := newSvcMultiPort("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
		AnnotationPort:      "nonexistent",
	}, []corev1.ServicePort{
		{Name: "http", Port: 8080, Protocol: corev1.ProtocolTCP},
	})
	r, rec := buildSvcReconciler(s, "owner1", svc)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	if len(evts) == 0 {
		t.Error("expected a warning event for port not found")
	}
	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) != 0 {
		t.Errorf("expected 0 DNS records, got %d", len(records.Items))
	}
}

// ---- TestServiceSource_CNAMETarget_NoRule -----------------------------------

func TestServiceSource_CNAMETarget_NoRule(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "cname:external.example.net",
		AnnotationHostnames: "foo.example.com",
	})
	r, _ := buildSvcReconciler(s, "owner1", svc, zone)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list DNS records: %v", err)
	}
	// Expect 1 CNAME record + 1 TXT registry record = 2 total.
	if len(records.Items) != 2 {
		t.Fatalf("expected 2 DNS records (CNAME + TXT), got %d", len(records.Items))
	}

	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 0 {
		t.Errorf("expected 0 TunnelRules for cname target, got %d", len(rules.Items))
	}
}

// ---- TestServiceSource_ProxiedOverride_RespectedForCNAME -------------------

func TestServiceSource_ProxiedOverride_RespectedForCNAME(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "cname:external.example.net",
		AnnotationHostnames: "foo.example.com",
		AnnotationProxied:   "false",
	})
	r, _ := buildSvcReconciler(s, "owner1", svc, zone)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, rec := range records.Items {
		if rec.Spec.Type == testRecordTypeCNAME {
			if rec.Spec.Proxied == nil || *rec.Spec.Proxied != false {
				t.Errorf("expected Proxied=false for CNAME record, got %v", rec.Spec.Proxied)
			}
		}
	}
}

// ---- TestServiceSource_ProxiedForcedForTunnel -------------------------------

func TestServiceSource_ProxiedForcedForTunnel(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
		AnnotationProxied:   "false", // should be ignored/overridden
	})
	r, _ := buildSvcReconciler(s, "owner1", svc, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, rec := range records.Items {
		if rec.Spec.Type == testRecordTypeCNAME {
			if rec.Spec.Proxied == nil || *rec.Spec.Proxied != true {
				t.Errorf("expected Proxied=true forced for tunnel target, got %v", rec.Spec.Proxied)
			}
		}
	}
}

// ---- TestServiceSource_TTLAnnotation_Respected ------------------------------

func TestServiceSource_TTLAnnotation_Respected(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
		AnnotationTTL:       "300",
	})
	r, _ := buildSvcReconciler(s, "owner1", svc, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, rec := range records.Items {
		if rec.Spec.Type == testRecordTypeCNAME {
			if rec.Spec.TTL != 300 {
				t.Errorf("expected TTL=300, got %d", rec.Spec.TTL)
			}
		}
	}
}

// ---- TestServiceSource_SourceLabelsOnEmittedCRs ----------------------------

func TestServiceSource_SourceLabelsOnEmittedCRs(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
	})
	r, _ := buildSvcReconciler(s, "owner1", svc, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list DNS records: %v", err)
	}
	for _, rec := range records.Items {
		checkSourceLabels(t, rec.Labels, "Service", "apps", "my-svc")
	}

	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	for _, rule := range rules.Items {
		checkSourceLabels(t, rule.Labels, "Service", "apps", "my-svc")
	}
}

// checkSourceLabels verifies the 4 standard source labels are present and that
// no extra labels exist (pattern #6).
func checkSourceLabels(t *testing.T, labels map[string]string, kind, ns, name string) {
	t.Helper()
	if labels[LabelSourceKind] != kind {
		t.Errorf("LabelSourceKind: expected %q, got %q", kind, labels[LabelSourceKind])
	}
	if labels[LabelSourceNamespace] != ns {
		t.Errorf("LabelSourceNamespace: expected %q, got %q", ns, labels[LabelSourceNamespace])
	}
	if labels[LabelSourceName] != name {
		t.Errorf("LabelSourceName: expected %q, got %q", name, labels[LabelSourceName])
	}
	if labels[LabelManagedBy] != "cloudflare-operator" {
		t.Errorf("LabelManagedBy: expected cloudflare-operator, got %q", labels[LabelManagedBy])
	}
	if len(labels) != 4 {
		t.Errorf("expected exactly 4 labels, got %d: %v", len(labels), labels)
	}
}

// ---- TestServiceSource_OwnerRefCascade -------------------------------------

func TestServiceSource_OwnerRefCascade(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
	})
	r, _ := buildSvcReconciler(s, "owner1", svc, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list DNS records: %v", err)
	}
	for _, rec := range records.Items {
		assertOwnerRef(t, rec.OwnerReferences, "my-svc", types.UID("my-svc-uid"))
	}

	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	for _, rule := range rules.Items {
		assertOwnerRef(t, rule.OwnerReferences, "my-svc", types.UID("my-svc-uid"))
	}
}

func assertOwnerRef(t *testing.T, refs []metav1.OwnerReference, wantName string, wantUID types.UID) {
	t.Helper()
	if len(refs) == 0 {
		t.Fatal("expected at least one OwnerReference, got none")
	}
	ref := refs[0]
	if ref.Name != wantName {
		t.Errorf("OwnerRef.Name: expected %q, got %q", wantName, ref.Name)
	}
	if ref.UID != wantUID {
		t.Errorf("OwnerRef.UID: expected %q, got %q", wantUID, ref.UID)
	}
	if ref.Controller == nil || !*ref.Controller {
		t.Error("OwnerRef.Controller should be true")
	}
	if ref.BlockOwnerDeletion == nil || !*ref.BlockOwnerDeletion {
		t.Error("OwnerRef.BlockOwnerDeletion should be true")
	}
}

// ---- TestServiceSource_UpsertExistingRecord ---------------------------------

func TestServiceSource_UpsertExistingRecord(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
	})

	// Pre-create a DNS record at the expected name with wrong content.
	existingRecord := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc-apps-my-svc-foo-example-com",
			Namespace: "apps",
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			Name:      "foo.example.com",
			Type:      "CNAME",
			Content:   strPtr("old-content.example.com"),
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
			ZoneRef:   &cloudflarev1alpha1.ZoneReference{Name: "example-com"},
		},
	}
	r, _ := buildSvcReconciler(s, "owner1", svc, zone, tunnel, existingRecord)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Re-fetch the record and verify it was updated.
	var updated cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(context.Background(),
		types.NamespacedName{Namespace: "apps", Name: "svc-apps-my-svc-foo-example-com"},
		&updated); err != nil {
		t.Fatalf("get updated record: %v", err)
	}
	if updated.Spec.Content != nil && *updated.Spec.Content == "old-content.example.com" {
		t.Error("expected record content to be updated, but it still has old value")
	}
}

// ---- TestServiceSource_MultipleHostnames_EmitsOneRule ----------------------

func TestServiceSource_MultipleHostnames_EmitsOneRule(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com,bar.example.com",
	})
	r, _ := buildSvcReconciler(s, "owner1", svc, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 1 {
		t.Fatalf("expected 1 TunnelRule for 2 hostnames, got %d", len(rules.Items))
	}
	rule := rules.Items[0]
	if len(rule.Spec.Hostnames) != 2 {
		t.Errorf("expected rule.Spec.Hostnames to have 2 entries, got %v", rule.Spec.Hostnames)
	}
}

// ---- TestServiceSource_WildcardHostname_Sanitized ---------------------------

func TestServiceSource_WildcardHostname_Sanitized(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "*.apps.example.com",
	})
	r, _ := buildSvcReconciler(s, "owner1", svc, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	// All emitted CRs should have "wild" in the name and no asterisks or dots.
	found := false
	for _, rec := range records.Items {
		for _, ch := range rec.Name {
			if ch == '*' || ch == '.' {
				t.Errorf("CR name %q contains invalid character %q", rec.Name, ch)
			}
		}
		if strings.Contains(rec.Name, "wild") {
			found = true
		}
	}
	if !found && len(records.Items) > 0 {
		t.Errorf("expected at least one CR name containing 'wild', got %v",
			func() []string {
				names := make([]string, len(records.Items))
				for i, r := range records.Items {
					names[i] = r.Name
				}
				return names
			}())
	}
}

// ---- TestServiceSource_ZoneRef_ExplicitWins ---------------------------------

func TestServiceSource_ZoneRef_ExplicitWins(t *testing.T) {
	s := svcTestScheme(t)
	// Two zones: explicit "my-zone" and a longer-suffix match would be "example-com".
	explicitZone := newZone("my-zone", "apps", "example.com", "cf-secret-explicit")
	otherZone := newZone("other-zone", "default", "example.com", "cf-secret-other")
	tunnel := newReadyTunnel("home", "apps")
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
		AnnotationZoneRef:   "my-zone",
	})
	r, _ := buildSvcReconciler(s, "owner1", svc, explicitZone, otherZone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, rec := range records.Items {
		if rec.Spec.ZoneRef != nil && rec.Spec.ZoneRef.Name != "my-zone" {
			t.Errorf("expected ZoneRef my-zone, got %q", rec.Spec.ZoneRef.Name)
		}
	}
}

// ---- TestServiceSource_ZoneRef_FallsBackToLongestSuffix --------------------

func TestServiceSource_ZoneRef_FallsBackToLongestSuffix(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
		// no AnnotationZoneRef
	})
	r, _ := buildSvcReconciler(s, "owner1", svc, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) == 0 {
		t.Fatal("expected DNS records to be emitted")
	}
}

// ---- TestServiceSource_ZoneNotFound_Warns -----------------------------------

func TestServiceSource_ZoneNotFound_Warns(t *testing.T) {
	s := svcTestScheme(t)
	// No zone registered for example.com.
	tunnel := newReadyTunnel("home", "apps")
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
	})
	r, rec := buildSvcReconciler(s, "owner1", svc, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	if len(evts) == 0 {
		t.Error("expected a warning event for zone not found")
	}
	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) != 0 {
		t.Errorf("expected 0 DNS records, got %d", len(records.Items))
	}
}

// ---- TestServiceSource_TunnelNotFound_Warns ---------------------------------

func TestServiceSource_TunnelNotFound_Warns(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	// No tunnel registered.
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
	})
	r, rec := buildSvcReconciler(s, "owner1", svc, zone)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	evts := drainEvents(rec)
	found := false
	for _, e := range evts {
		if strings.Contains(e, string(cloudflarev1alpha1.ReasonTunnelNotFound)) || strings.Contains(e, "Warning") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected TunnelNotFound warning; got %v", evts)
	}
	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records.Items) != 0 {
		t.Errorf("expected 0 DNS records, got %d", len(records.Items))
	}
}

// ---- TestServiceSource_TunnelNotReady_Requeue ------------------------------

func TestServiceSource_TunnelNotReady_Requeue(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	// Tunnel exists but no CNAME yet.
	tunnel := &cloudflarev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "apps"},
		Spec: cloudflarev1alpha1.CloudflareTunnelSpec{
			Name:                "home",
			SecretRef:           cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
			GeneratedSecretName: "home-credentials",
		},
		// Status.TunnelCNAME intentionally empty.
	}
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
	})
	r, rec := buildSvcReconciler(s, "owner1", svc, zone, tunnel)

	result, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should requeue.
	if result.RequeueAfter == 0 {
		t.Error("expected requeue when tunnel not ready")
	}
	evts := drainEvents(rec)
	if len(evts) == 0 {
		t.Error("expected a warning event for tunnel not ready")
	}
}

// ---- TestServiceSource_TunnelRefCrossNamespace ------------------------------

func TestServiceSource_TunnelRefCrossNamespace(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	// Tunnel lives in "network" namespace.
	tunnel := &cloudflarev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "network"},
		Spec: cloudflarev1alpha1.CloudflareTunnelSpec{
			Name:                "home",
			SecretRef:           cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
			GeneratedSecretName: "home-credentials",
		},
		Status: cloudflarev1alpha1.CloudflareTunnelStatus{
			TunnelCNAME: "test-id.cfargotunnel.com",
		},
	}
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:             "tunnel:home",
		AnnotationHostnames:          "foo.example.com",
		AnnotationTunnelRefNamespace: "network",
	})
	r, _ := buildSvcReconciler(s, "owner1", svc, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules.Items))
	}
	tunnelRef := rules.Items[0].Spec.TunnelRef
	if tunnelRef.Namespace != "network" {
		t.Errorf("expected TunnelRef.Namespace=network, got %q", tunnelRef.Namespace)
	}
	if tunnelRef.Name != "home" {
		t.Errorf("expected TunnelRef.Name=home, got %q", tunnelRef.Name)
	}
}

// ---- TestServiceSource_ErrNoServicePorts_SentinelWrapped -------------------

func TestServiceSource_ErrNoServicePorts_SentinelWrapped(t *testing.T) {
	svc := newSvcMultiPort("my-svc", "apps", nil, []corev1.ServicePort{})
	r := &ServiceSourceReconciler{}
	_, err := r.resolveServiceBackend(svc)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrNoServicePorts) {
		t.Errorf("expected ErrNoServicePorts, got %v", err)
	}
}

// ---- TestServiceSource_ErrPortNotFound_SentinelWrapped ---------------------

func TestServiceSource_ErrPortNotFound_SentinelWrapped(t *testing.T) {
	svc := newSvcMultiPort("my-svc", "apps", map[string]string{
		AnnotationPort: "nonexistent",
	}, []corev1.ServicePort{
		{Name: "http", Port: 8080, Protocol: corev1.ProtocolTCP},
	})
	r := &ServiceSourceReconciler{}
	_, err := r.resolveServiceBackend(svc)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrPortNotFound) {
		t.Errorf("expected ErrPortNotFound, got %v", err)
	}
}

// ---- TestServiceSource_EmitDNSAndRule ------------------
// This is the "golden path" test from the plan.

func TestServiceSource_EmitDNSAndRule(t *testing.T) {
	s := svcTestScheme(t)
	zone := newZone("example-com", "apps", "example.com", "cf-secret")
	tunnel := newReadyTunnel("home", "apps")
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:    "tunnel:home",
		AnnotationHostnames: "foo.example.com",
	})
	r, _ := buildSvcReconciler(s, "owner1", svc, zone, tunnel)

	_, err := r.Reconcile(context.Background(), req("apps", "my-svc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expect 1 CNAME + 1 TXT = 2 DNS records.
	var records cloudflarev1alpha1.CloudflareDNSRecordList
	if err := r.List(context.Background(), &records); err != nil {
		t.Fatalf("list DNS records: %v", err)
	}
	if len(records.Items) != 2 {
		t.Fatalf("expected 2 DNS records (CNAME + TXT), got %d", len(records.Items))
	}

	// Expect 1 TunnelRule.
	var rules cloudflarev1alpha1.CloudflareTunnelRuleList
	if err := r.List(context.Background(), &rules); err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules.Items) != 1 {
		t.Fatalf("expected 1 TunnelRule, got %d", len(rules.Items))
	}
	rule := rules.Items[0]
	if rule.Spec.TunnelRef.Name != "home" {
		t.Errorf("expected TunnelRef.Name=home, got %q", rule.Spec.TunnelRef.Name)
	}
	if rule.Spec.Priority != 100 {
		t.Errorf("expected Priority=100, got %d", rule.Spec.Priority)
	}
}

// ---- TestServiceSource_ServiceNotFound_NoError ------------------------------

func TestServiceSource_ServiceNotFound_NoError(t *testing.T) {
	s := svcTestScheme(t)
	r, _ := buildSvcReconciler(s, "owner1") // no objects pre-loaded

	_, err := r.Reconcile(context.Background(), req("apps", "missing-svc"))
	if err != nil {
		t.Fatalf("expected no error for missing service, got: %v", err)
	}
}

// ---- TestServiceSource_MapTunnelToServices_CrossNamespace ------------------
// TDD: write the failing test first, then fix the mapper.

func TestServiceSource_MapTunnelToServices_CrossNamespace(t *testing.T) {
	s := svcTestScheme(t)
	// Service in "apps" references a tunnel in "network" via annotation.
	svc := newSvc("my-svc", "apps", map[string]string{
		AnnotationTarget:             "tunnel:home",
		AnnotationTunnelRefNamespace: "network",
	})
	tun := newReadyTunnel("home", "network")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(svc, tun).
		Build()
	r := &ServiceSourceReconciler{Client: c}

	reqs := r.mapTunnelToServices(context.Background(), tun)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d: %v", len(reqs), reqs)
	}
	if reqs[0].Namespace != "apps" || reqs[0].Name != "my-svc" {
		t.Errorf("expected {apps/my-svc}, got %v", reqs[0])
	}
}

// ---- TestServiceSource_MapTunnelToServices_SameNamespace -------------------
// Lock the existing same-namespace behavior: a Service with no tunnel-ref-namespace
// annotation should match a tunnel in the same namespace.

func TestServiceSource_MapTunnelToServices_SameNamespace(t *testing.T) {
	s := svcTestScheme(t)
	// Service in "network" with no tunnel-ref-namespace → should match tunnel network/home.
	svc := newSvc("my-svc", "network", map[string]string{
		AnnotationTarget: "tunnel:home",
		// no AnnotationTunnelRefNamespace
	})
	tun := newReadyTunnel("home", "network")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(svc, tun).
		Build()
	r := &ServiceSourceReconciler{Client: c}

	reqs := r.mapTunnelToServices(context.Background(), tun)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d: %v", len(reqs), reqs)
	}
	if reqs[0].Namespace != "network" || reqs[0].Name != "my-svc" {
		t.Errorf("expected {network/my-svc}, got %v", reqs[0])
	}
}
