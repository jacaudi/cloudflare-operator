/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package envtest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/controller/tunnel"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// TestEnvtest_CrossNamespaceZoneRef is a regression-lock for Task 1 of the
// tunnel-crossns-connector feature. It calls tunnel.EmitDNSRecord directly
// against the real envtest apiserver and asserts that the emitted
// CloudflareDNSRecord carries Spec.ZoneRef.Namespace == "zone-ns" (the
// annotation value) rather than the source owner's namespace "ns".
//
// This test is expected to PASS immediately because Task 1's emit.go change is
// already committed. Its purpose is to catch any future regression that reverts
// the cloudflare.io/zone-ref-namespace override in emit.go.
//
// Non-vacuity: reverting the `zoneNS = opts.Annotations[...ZoneRefNamespace]`
// hunk in emit.go causes the emitted record to carry ZoneRef.Namespace == "ns"
// (the owner's namespace) instead of "zone-ns", failing the final require.Equal.
func TestEnvtest_CrossNamespaceZoneRef(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}

	ctx := context.Background()

	// Ensure the source-owner namespace "ns" exists. Other envtest tests always
	// use shortUniqueNamespace, so "ns" is unclaimed — but guard with
	// IsAlreadyExists for resilience (tests share a single apiserver process).
	if err := sharedClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "ns"},
	}); err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}

	// Ensure the zone namespace "zone-ns" exists. The ZoneRef string is purely
	// advisory at admission time (no referential integrity CEL on DNSRecord), but
	// creating the namespace ensures that if admission ever gains that check the
	// test still passes without scaffolding changes.
	if err := sharedClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "zone-ns"},
	}); err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}

	// CloudflareZone in "zone-ns" — mirrors the zone-ref annotation value
	// "example-com". Pattern matches existing envtest zone fixtures verbatim.
	zone := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: "zone-ns"},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	if err := sharedClient.Create(ctx, zone); err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
	t.Cleanup(func() { _ = sharedClient.Delete(ctx, zone) })

	// Source owner: a Service in "ns". Must be Created so the apiserver assigns
	// a UID; SetControllerOwner (called inside EmitDNSRecord) requires a non-zero
	// UID to stamp the owner reference on the emitted CR.
	svc := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	if err := sharedClient.Create(ctx, svc); err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
	// Re-Get so svc.UID is populated from the apiserver (Create mutates in place,
	// but guard against the IsAlreadyExists path by re-fetching).
	require.NoError(t, sharedClient.Get(ctx, client.ObjectKeyFromObject(svc), svc))
	t.Cleanup(func() { _ = sharedClient.Delete(ctx, svc) })

	// Emit a CloudflareDNSRecord with both zone-ref and zone-ref-namespace set.
	// The annotation value for zone-ref-namespace ("zone-ns") is intentionally
	// different from the owner namespace ("ns") so the assertion below proves
	// the cross-ns override is honoured, not the same-namespace fallback.
	require.NoError(t, tunnel.EmitDNSRecord(ctx, sharedClient, sharedScheme, tunnel.EmitOpts{
		Owner:     svc,
		OwnerKind: "Service",
		Hostname:  "crossns.example.com",
		Content:   "tunnel.cfargotunnel.com",
		Annotations: map[string]string{
			conventions.AnnotationZoneRef:          "example-com",
			conventions.AnnotationZoneRefNamespace: "zone-ns",
		},
	}))

	// Look up the emitted record by listing in the owner's namespace and matching
	// on Spec.Name. emittedDNSRecordName is unexported in package tunnel, so we
	// use List + filter — the same pattern used by all other envtest emission tests.
	var list v2alpha1.CloudflareDNSRecordList
	require.NoError(t, sharedClient.List(ctx, &list, client.InNamespace("ns")))

	var found *v2alpha1.CloudflareDNSRecord
	for i := range list.Items {
		if list.Items[i].Spec.Name == "crossns.example.com" {
			found = &list.Items[i]
			break
		}
	}
	require.NotNil(t, found, "expected emitted CloudflareDNSRecord for crossns.example.com in namespace ns")

	// Core regression-lock assertions: ZoneRef.Name and ZoneRef.Namespace must
	// reflect the annotations, not the owner namespace. A revert of emit.go's
	// zone-ref-namespace hunk would set ZoneRef.Namespace = "ns" (owner ns),
	// causing the last assertion to fail.
	require.NotNil(t, found.Spec.ZoneRef)
	require.Equal(t, "example-com", found.Spec.ZoneRef.Name)
	require.Equal(t, "zone-ns", found.Spec.ZoneRef.Namespace,
		"ZoneRef.Namespace must be the annotation value (zone-ns), not the owner namespace (ns)")
}
