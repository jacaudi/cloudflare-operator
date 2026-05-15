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

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// TestEnvtest_CascadeGC_OwnerTransfer pins the cascade-GC owner-transfer
// contract (design §8.2) against a real apiserver: two annotated Services
// share one auto-created CloudflareTunnel; deleting the Service that owns the
// tunnel transfers controller-ownership to the remaining Service, and the
// tunnel CR survives (it is NOT self-deleted because >=1 source still attaches).
//
// Harness-fidelity notes (this is an envtest, not a real cluster):
//
//   - Derived tunnel name. EnsureTunnelCR names the CR via
//     DeriveTunnelName(ns, tunnel-name) => "cf-<ns>-<tunnel-name>", not the
//     bare annotation value. Every sibling envtest Gets "cf-<ns>-<name>";
//     this test mirrors that.
//
//   - Nondeterministic initial owner. EnsureTunnelCR is find-or-create with
//     no lexical preference on CREATE — whichever Service's reconcile wins
//     the create race becomes the owner. The test therefore captures the
//     actual owner instead of assuming a-svc, then deletes THAT owner and
//     asserts ownership moves to the other Service.
//
//   - No garbage collector in envtest. envtest starts only kube-apiserver +
//     etcd (suite_test.go uses a bare envtest.Environment{}); there is no
//     kube-controller-manager, so deleting the owner Service does NOT
//     cascade-remove its ownerReference from the tunnel CR the way a real
//     cluster's GC would. needsOwnerTransfer keys off len(OwnerReferences)==0,
//     so the test explicitly strips the deleted owner's ownerReference to
//     emulate real-cluster GC. That strip is a Tunnel CR write, which itself
//     retriggers the Tunnel reconciler (it Watches For(&CloudflareTunnel{})):
//     needsOwnerTransfer then fires and TransferOwnershipIfNeeded promotes the
//     lex-smallest LIVE remaining source (the surviving Service; the deleted
//     one is skipped as NotFound by TransferOwnershipIfNeeded's live-Get).
//
// setupServiceEnv is reused unmodified beyond its shared short-grace hook
// (the owner-transfer path does not depend on the grace window — that's the
// needsOwnerTransfer path, no LastOrphanedAt — but the short grace is
// established here as shared infra for the sibling cascade-GC envtests).
func TestEnvtest_CascadeGC_OwnerTransfer(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupServiceEnv(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// CloudflareZone — DNSRecord admission CEL requires has(zoneID)||has(zoneRef);
	// the Services carry cloudflare.io/zone-ref so emitted DNSRecords pass
	// admission. Spec shape mirrors the sibling orphan-prune envtest verbatim.
	zone := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v1alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	mkSvc := func(name, host string) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: f.ns,
				Annotations: map[string]string{
					conventions.AnnotationTunnel:     "true",
					conventions.AnnotationTunnelName: "shared-tnl",
					conventions.AnnotationHostnames:  host,
					conventions.AnnotationZoneRef:    "example-com",
				},
			},
			Spec: corev1.ServiceSpec{
				Type:  corev1.ServiceTypeClusterIP,
				Ports: []corev1.ServicePort{{Port: 80}},
			},
		}
	}
	svcA := mkSvc("a-svc", "a.example.com")
	svcB := mkSvc("b-svc", "b.example.com")
	require.NoError(t, f.c.Create(ctx, svcA))
	require.NoError(t, f.c.Create(ctx, svcB))

	// DeriveTunnelName template: "cf-<ns>-<tunnel-name>".
	tunnelName := "cf-" + f.ns + "-shared-tnl"
	tunnelKey := types.NamespacedName{Namespace: f.ns, Name: tunnelName}

	// The auto-created tunnel appears, carries the auto-created marker, and is
	// owned by exactly one of the two Services (create-race winner). Capture
	// which one actually became owner — ordering is not guaranteed.
	var initialOwner string
	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, tunnelKey, &tn); err != nil {
			return false
		}
		if tn.Annotations[conventions.AnnotationAutoCreated] != "true" {
			return false
		}
		if len(tn.OwnerReferences) != 1 {
			return false
		}
		owner := tn.OwnerReferences[0].Name
		if owner != "a-svc" && owner != "b-svc" {
			return false
		}
		initialOwner = owner
		return true
	}, 45*time.Second, 250*time.Millisecond,
		"tunnel must appear auto-created and owned by exactly one of {a-svc,b-svc}")

	// Identify the surviving Service (the one we do NOT delete).
	survivor := "b-svc"
	if initialOwner == "b-svc" {
		survivor = "a-svc"
	}
	doomed := initialOwner

	// Delete the owner Service.
	var ownerSvc corev1.Service
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: doomed}, &ownerSvc))
	require.NoError(t, f.c.Delete(ctx, &ownerSvc))

	// Emulate real-cluster GC (absent in envtest): strip the deleted owner's
	// dangling ownerReference from the tunnel CR. This both satisfies
	// needsOwnerTransfer (len(OwnerReferences)==0) and — being a Tunnel CR
	// write — retriggers the Tunnel reconciler. TransferOwnershipIfNeeded then
	// promotes the surviving Service (the deleted one is skipped: its live Get
	// returns NotFound). Conflict-tolerant: a stale ResourceVersion just means
	// the next poll iteration retries with a fresh Get.
	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, tunnelKey, &tn); err != nil {
			return false
		}
		// Already transferred to the survivor — done.
		if len(tn.OwnerReferences) == 1 && tn.OwnerReferences[0].Name == survivor {
			return true
		}
		// Still carrying the doomed owner's dangling ref — strip it
		// (GC emulation) and let the next iteration observe the transfer.
		if len(tn.OwnerReferences) == 1 && tn.OwnerReferences[0].Name == doomed {
			kept := tn.OwnerReferences[:0]
			for _, or := range tn.OwnerReferences {
				if or.Name != doomed {
					kept = append(kept, or)
				}
			}
			tn.OwnerReferences = kept
			_ = f.c.Update(ctx, &tn) // conflict => retried next tick
		}
		return false
	}, 45*time.Second, 250*time.Millisecond,
		"ownership must transfer to the surviving Service after the owner is deleted")

	// The tunnel must still exist — owner-transfer keeps an auto-created tunnel
	// alive while >=1 source attaches; it is NOT cascade-GC'd here.
	var final v1alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, tunnelKey, &final),
		"tunnel must persist after owner-transfer (>=1 source still attaches)")
	require.Len(t, final.OwnerReferences, 1)
	require.Equal(t, survivor, final.OwnerReferences[0].Name,
		"surviving Service must be the new controller-owner")
}
