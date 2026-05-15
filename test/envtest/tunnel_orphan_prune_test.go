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

package envtest_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// TestEnvtest_OrphanPrune_RemovesDNSRecordWhenHostnameDropped pins Design E3
// (orphan prune) against a real apiserver. A Service emitting two
// CloudflareDNSRecord CRs (a.example.com, b.example.com) has its hostnames
// annotation narrowed to just a.example.com; the next source-reconcile pass
// must prune the now-orphaned b.example.com CR while leaving a.example.com
// untouched.
//
// Gotchas pre-empted (per project Phase 3 execution lessons):
//   - DNSRecord admission has CEL "has(zoneID) || has(zoneRef)": the fixture
//     creates a CloudflareZone CR and sets cloudflare.io/zone-ref on the
//     Service so every emitted DNSRecord carries a valid zoneRef.
//   - Tunnel Status.TunnelCNAME must populate before emission can advance past
//     the source reconciler's deferred-emission guard; the fixture waits on it.
//   - setupServiceEnv is reused without modification — Service + Tunnel
//     reconcilers share one tunnelsynth.Cache, identical to the sibling
//     Service envtests.
func TestEnvtest_OrphanPrune_RemovesDNSRecordWhenHostnameDropped(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupServiceEnv(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Zone CR — admission requires has(zoneRef); the Service carries zone-ref,
	// so every emitted DNSRecord inherits it and passes CEL validation.
	zone := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v1alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	// Annotated Service emitting TWO hostnames.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "a.example.com,b.example.com",
				conventions.AnnotationZoneRef:    "example-com",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, svc))

	// Wait for the tunnel CR + Status.TunnelCNAME (deferred-emission flow).
	expectedTunnel := "cf-" + f.ns + "-payments"
	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: expectedTunnel}, &tn); err != nil {
			return false
		}
		return tn.Status.TunnelCNAME != ""
	}, 30*time.Second, 250*time.Millisecond, "tunnel Status.TunnelCNAME populated")

	// Both DNSRecord CRs must be emitted before we narrow the annotation.
	require.Eventually(t, func() bool {
		return dnsRecordExists(ctx, t, f.c, f.ns, "a.example.com") &&
			dnsRecordExists(ctx, t, f.c, f.ns, "b.example.com")
	}, 30*time.Second, 250*time.Millisecond, "both a.example.com and b.example.com DNSRecord CRs emitted")

	// Narrow the hostnames annotation: drop b.example.com.
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: "svc"}, svc))
	svc.Annotations[conventions.AnnotationHostnames] = "a.example.com"
	require.NoError(t, f.c.Update(ctx, svc))

	// The next reconcile pass must prune the orphaned b.example.com CR while
	// keeping a.example.com. Before the orphan-prune wiring this assertion
	// fails: the b.example.com CR persists because nothing deletes it.
	require.Eventually(t, func() bool {
		return !dnsRecordExists(ctx, t, f.c, f.ns, "b.example.com")
	}, 30*time.Second, 250*time.Millisecond,
		"orphaned DNSRecord for b.example.com must be pruned after it leaves the hostnames annotation")

	// a.example.com must survive — it's still in the desired set.
	require.True(t, dnsRecordExists(ctx, t, f.c, f.ns, "a.example.com"),
		"DNSRecord for a.example.com must survive (still in the desired set)")
}

// TestEnvtest_OrphanPrune_RespectsLabelScope pins the label-scope contract of
// pruneOrphanedDNSRecords against a real apiserver: pruning triggered by one
// Service (which lists CRs by its own three source-identity labels) must NEVER
// touch a CR owned by a different Service, even when both live in the same
// namespace and reference the same zone.
func TestEnvtest_OrphanPrune_RespectsLabelScope(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupServiceEnv(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	zone := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v1alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	// Service ONE emits one.example.com.
	svc1 := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc-one", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "one.example.com",
				conventions.AnnotationZoneRef:    "example-com",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, svc1))

	// Service TWO emits two.example.com (same tunnel, same zone, same ns).
	svc2 := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc-two", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "two.example.com",
				conventions.AnnotationZoneRef:    "example-com",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, svc2))

	expectedTunnel := "cf-" + f.ns + "-payments"
	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: expectedTunnel}, &tn); err != nil {
			return false
		}
		return tn.Status.TunnelCNAME != ""
	}, 30*time.Second, 250*time.Millisecond, "tunnel Status.TunnelCNAME populated")

	// Both CRs emitted.
	require.Eventually(t, func() bool {
		return dnsRecordExists(ctx, t, f.c, f.ns, "one.example.com") &&
			dnsRecordExists(ctx, t, f.c, f.ns, "two.example.com")
	}, 30*time.Second, 250*time.Millisecond, "both Services' DNSRecord CRs emitted")

	// Opt svc-one OUT entirely (tunnel=false). Its reconcile path no longer
	// emits and is not the prune path; but more importantly, mutate svc-two so
	// IT prunes — drop its only hostname so svc-two's prune pass runs with an
	// empty desired set. svc-one's CR must survive: it carries svc-one's
	// source labels, invisible to svc-two's label-scoped prune.
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: "svc-two"}, svc2))
	svc2.Annotations[conventions.AnnotationHostnames] = "two-renamed.example.com"
	require.NoError(t, f.c.Update(ctx, svc2))

	// svc-two's old CR (two.example.com) is pruned; the new one
	// (two-renamed.example.com) is emitted.
	require.Eventually(t, func() bool {
		return !dnsRecordExists(ctx, t, f.c, f.ns, "two.example.com") &&
			dnsRecordExists(ctx, t, f.c, f.ns, "two-renamed.example.com")
	}, 30*time.Second, 250*time.Millisecond,
		"svc-two's old hostname CR pruned and renamed CR emitted")

	// svc-one's CR MUST survive the svc-two prune — label scope isolates them.
	require.True(t, dnsRecordExists(ctx, t, f.c, f.ns, "one.example.com"),
		"svc-one's DNSRecord must survive svc-two's prune (label-scoped, not cross-source)")
}

// dnsRecordExists reports whether a CloudflareDNSRecord with the given
// Spec.Name currently exists in the namespace. A record mid-deletion (non-nil
// DeletionTimestamp) is treated as already gone so the prune assertions don't
// race the finalizer/GC.
func dnsRecordExists(ctx context.Context, t *testing.T, c client.Client, ns, hostname string) bool {
	t.Helper()
	var list v1alpha1.CloudflareDNSRecordList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return false
	}
	for i := range list.Items {
		dr := &list.Items[i]
		if dr.Spec.Name == hostname && dr.DeletionTimestamp == nil {
			return true
		}
	}
	return false
}
