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
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// TestEnvtest_CascadeGC_LastSourceSelfDelete pins the cascade-GC self-delete
// contract (design §8.2) against a real apiserver: a single annotated Service
// creates an auto-created CloudflareTunnel; the Service is deleted; after the
// (harness-overridden, 3-second) grace window the tunnel CR self-deletes — the
// production reconciler stamps LastOrphanedAt, drains via the finalizer path,
// and the CR disappears.
//
// Harness-fidelity notes inherited from TestEnvtest_CascadeGC_OwnerTransfer:
//
//   - Derived tunnel name: "cf-<ns>-<tunnel-name>" (same inline template).
//
//   - No garbage collector in envtest. Deleting the only Service leaves a
//     dangling ownerReference on the tunnel CR. isOrphaned requires
//     len(OwnerReferences)==0 && len(AttachedSources)==0, so the test emulates
//     real-cluster GC by stripping the dead owner's ref via the same
//     conflict-tolerant Eventually loop Task 10 established. The strip only
//     removes that ownerRef — stamping LastOrphanedAt, respecting the grace
//     window, emitting the Warning, and self-deleting are all production code.
//
//   - Short grace. setupServiceEnv sets PendingDeletionGrace to 3s on the tunnel
//     reconciler, so the two-tick orphan window is ~3s, not the default 60s.
func TestEnvtest_CascadeGC_LastSourceSelfDelete(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupServiceEnv(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// CloudflareZone scaffold — DNSRecord admission CEL requires has(zoneID)||has(zoneRef);
	// the Service carries cloudflare.io/zone-ref so emitted DNSRecords pass admission.
	zone := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v1alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	// A single Service — the only source for this tunnel.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "solo-svc", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "solo-tnl",
				conventions.AnnotationHostnames:  "solo.example.com",
				conventions.AnnotationZoneRef:    "example-com",
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, svc))

	// DeriveTunnelName template: "cf-<ns>-<tunnel-name>".
	tnName := "cf-" + f.ns + "-solo-tnl"
	tnKey := types.NamespacedName{Namespace: f.ns, Name: tnName}

	// Wait for the auto-created tunnel owned by solo-svc.
	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, tnKey, &tn); err != nil {
			return false
		}
		return tn.Annotations[conventions.AnnotationAutoCreated] == "true" &&
			len(tn.OwnerReferences) == 1 && tn.OwnerReferences[0].Name == "solo-svc"
	}, 45*time.Second, 250*time.Millisecond,
		"auto-created tunnel must appear owned by solo-svc")

	// Delete the only source.
	require.NoError(t, f.c.Delete(ctx, svc))

	// Emulate real-cluster GC: strip the dangling solo-svc ownerReference from
	// the tunnel CR so the production isOrphaned path can fire (envtest has no
	// kube-controller-manager). This is a Tunnel CR write, which retriggers the
	// Tunnel reconciler; observeAttachedSources will also have emptied
	// AttachedSources once the Service-source reconciler processes the deletion.
	// The strip only removes the dead owner's ref — the production reconciler is
	// responsible for stamping LastOrphanedAt, waiting the grace window, and
	// self-deleting. Conflict-tolerant: stale ResourceVersion just retries.
	//
	// Isolation-safety: we gate completion on BOTH ownerRefs==0 AND observed
	// AttachedSources==0. Without this, if the tunnel reconciler fires before the
	// ServiceSourceReconciler clears the deleted Service from the shared cache,
	// observeAttachedSources still sees solo-svc → isOrphaned is false → the
	// reconciler requeues at defaultTunnelInterval (30m) → the test stalls in
	// strict isolation.
	//
	// The strip loop bumps a test-only label on every iteration to produce a
	// real k8s write (real rv change → real watch event → tunnel reconciler
	// re-runs). k8s 1.30+ skips rv bumps for no-op writes, so a plain Update
	// with only the already-stripped ownerRefs would be a no-op after the first
	// strip. The label value changes on every tick; the tunnel reconciler
	// ignores labels, so this is purely a mechanical retriggering device.
	// The tunnel reconciler re-running each tick is what eventually catches the
	// moment the ServiceSourceReconciler has drained the cache, so isOrphaned
	// can fire without depending on the 30-min defaultTunnelInterval requeue.
	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, tnKey, &tn); err != nil {
			return false // tunnel not yet visible or already gone
		}
		t.Logf("strip loop: ownerRefs=%d attachedSources=%v rv=%s", len(tn.OwnerReferences), tn.Status.AttachedSources, tn.ResourceVersion)
		// Both the dangling ownerRef is gone AND observed sources have drained —
		// isOrphaned can now fire; strip is complete.
		if len(tn.OwnerReferences) == 0 && len(tn.Status.AttachedSources) == 0 {
			return true
		}
		// Strip the dead owner's ref from ownerReferences (GC emulation).
		kept := tn.OwnerReferences[:0]
		for _, or := range tn.OwnerReferences {
			if or.Name != "solo-svc" {
				kept = append(kept, or)
			}
		}
		tn.OwnerReferences = kept
		// Bump a test-only label so every iteration is a real (non-no-op) write.
		// Without this, k8s returns the same rv for identical content, producing
		// no watch event and leaving the tunnel reconciler stuck on its 30-min
		// requeue. The tunnel reconciler ignores this label entirely.
		if tn.Labels == nil {
			tn.Labels = map[string]string{}
		}
		tn.Labels["cloudflare.io/test-strip-tick"] = strconv.FormatInt(time.Now().UnixNano(), 10)
		if err := f.c.Update(ctx, &tn); err != nil {
			t.Logf("update err: %v (rv=%s)", err, tn.ResourceVersion)
		} else {
			t.Logf("update ok: new rv=%s", tn.ResourceVersion)
		}
		return false
	}, 45*time.Second, 250*time.Millisecond,
		"dangling solo-svc ownerReference must be stripped and AttachedSources drained (GC emulation)")

	// Production stamps LastOrphanedAt on the first orphan observation
	// (len(OwnerReferences)==0 && len(AttachedSources)==0 && auto-created==true).
	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, tnKey, &tn); err != nil {
			return false
		}
		return tn.Status.LastOrphanedAt != nil
	}, 30*time.Second, 250*time.Millisecond,
		"LastOrphanedAt must be stamped on first orphan observation")

	// After the 3-second grace elapses the production reconciler emits Warning
	// ReasonTerminalNoSources, sets Ready=False, calls r.Delete (which sets a
	// DeletionTimestamp on the CR), then reconcileDelete drains via the mock
	// TunnelClient and drops the finalizer. The CR vanishes entirely.
	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		err := f.c.Get(ctx, tnKey, &tn)
		return apierrors.IsNotFound(err)
	}, 45*time.Second, 250*time.Millisecond,
		"auto-created tunnel must self-delete after grace window + drain")
}
