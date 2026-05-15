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
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// gcEmulateStripDeadOwner strips ownerReferences whose .Name == deadName from
// the tunnel CR (envtest has no garbage collector, so a deleted source's
// ownerRef would dangle and block isOrphaned). It also waits until the tunnel
// reconciler has observed the drained cache (Status.AttachedSources empty),
// bumping a metadata label each tick to force a real resourceVersion change
// (k8s does not bump rv on a no-op Update, so without this the tunnel
// reconciler would stall at defaultTunnelInterval). Returns when the dead
// owner is gone AND AttachedSources is empty.
//
// The label key uses the envtest.local/ prefix rather than cloudflare.io/ —
// this is a test-only mechanical retriggering device, not an operator
// annotation, so it must not occupy the reserved cloudflare.io namespace.
func gcEmulateStripDeadOwner(t *testing.T, ctx context.Context, c client.Client, tnKey client.ObjectKey, deadName string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := c.Get(ctx, tnKey, &tn); err != nil {
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
			if or.Name != deadName {
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
		tn.Labels["envtest.local/strip-tick"] = strconv.FormatInt(time.Now().UnixNano(), 10)
		if err := c.Update(ctx, &tn); err != nil {
			t.Logf("update err: %v (rv=%s)", err, tn.ResourceVersion)
		} else {
			t.Logf("update ok: new rv=%s", tn.ResourceVersion)
		}
		return false
	}, 45*time.Second, 250*time.Millisecond,
		"dangling "+deadName+" ownerReference must be stripped and AttachedSources drained (GC emulation)")
}

// bumpRetriggerTick stamps a benign test-only label on the tunnel CR to force
// a real resourceVersion change and generate a watch event so the tunnel
// reconciler re-runs on the next tick (k8s does not bump rv on a no-op
// Update, so without this the tunnel reconciler would stall at its long
// requeue interval). Conflict-tolerant: a stale-rv error just means the next
// tick retries with a fresh Get. The label key uses the envtest.local/ prefix
// — NOT cloudflare.io/ — because this is a test-only mechanical retriggering
// device, not an operator annotation (same rationale as
// gcEmulateStripDeadOwner's strip-tick label).
func bumpRetriggerTick(ctx context.Context, c client.Client, tn *v1alpha1.CloudflareTunnel) {
	if tn.Labels == nil {
		tn.Labels = map[string]string{}
	}
	tn.Labels["envtest.local/retrigger-tick"] = strconv.FormatInt(time.Now().UnixNano(), 10)
	_ = c.Update(ctx, tn) // conflict-tolerant: error ignored, next tick retries
}

// TestEnvtest_CascadeGC_DirectCreateNeverAcquiresControllerRef pins the
// positive form of design §7 against a real apiserver: a user-authored
// (direct-create) CloudflareTunnel CR must NEVER acquire a controller
// OwnerReference, even while an annotated Service actively attaches to it.
//
// Why this matters: needsOwnerTransfer drives TransferOwnershipIfNeeded,
// which stamps a source as the tunnel CR's Controller+BlockOwnerDeletion
// owner. If that fired for a direct-create CR, deleting the Service would
// let Kubernetes GC cascade-delete the user's tunnel — exactly the §7
// violation Task 14 closes. The isAutoCreated gate on needsOwnerTransfer
// must keep the operator from ever taking controller-ownership of a user's
// CR. (The negative form — "never self-deleted" — is covered by
// TestEnvtest_CascadeGC_DirectCreateNeverGCd; this test pins the upstream
// invariant that no controller-ref is ever acquired in the first place.)
//
// Sequence:
//  1. Create a CloudflareTunnel CR directly with the derived name
//     "cf-<ns>-direct-owns" and NO auto-created annotation (finalizer
//     present so it reconciles like a real CR).
//  2. Create an annotated Service (tunnel-name: direct-owns) that adopts it
//     via EnsureTunnelCR's find path (no owner-ref set on adopt).
//  3. Wait for the Service to appear in Status.AttachedSources.
//  4. Assert via require.Never (~10s, 250ms tick) that the tunnel CR's
//     OwnerReferences NEVER becomes non-empty AND the auto-created
//     annotation stays absent. Each tick bumps a benign retrigger label so
//     the tunnel reconciler is actively re-run throughout the window
//     (otherwise the needsOwnerTransfer path would never be exercised and
//     the test would be vacuous).
//
// Non-vacuity (mutation-verified): with needsOwnerTransfer ungated (the
// pre-Task-14 form, len(OwnerReferences)==0 && len(AttachedSources)>0), the
// reconciler promotes the attaching Service to controller-owner within a
// few reconciles → OwnerReferences becomes non-empty → require.Never trips.
// With the isAutoCreated gate intact, needsOwnerTransfer is false for the
// unannotated direct-create CR, TransferOwnershipIfNeeded is never called,
// and OwnerReferences stays empty for the full window.
func TestEnvtest_CascadeGC_DirectCreateNeverAcquiresControllerRef(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupServiceEnv(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// CloudflareZone scaffold — DNSRecord admission CEL requires has(zoneID)||has(zoneRef).
	zone := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v1alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	// Step 1: create the CloudflareTunnel CR directly — no auto-created
	// annotation. The name follows the DeriveTunnelName template so the
	// annotated Service's EnsureTunnelCR finds it via Get (adopt path).
	tnName := "cf-" + f.ns + "-direct-owns"
	tnKey := types.NamespacedName{Namespace: f.ns, Name: tnName}

	directTunnel := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tnName,
			Namespace: f.ns,
			// No cloudflare.io/auto-created annotation — direct-create path.
			Finalizers: []string{conventions.FinalizerName},
		},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name: "cf-direct-owns",
			Connector: v1alpha1.ConnectorSpec{
				Replicas:           1,
				Protocol:           "auto",
				LogLevel:           "info",
				GracePeriodSeconds: 30,
			},
		},
	}
	require.NoError(t, f.c.Create(ctx, directTunnel))

	// Step 2: annotated Service adopts the existing CR via EnsureTunnelCR's
	// find path (no owner-ref set on adopt).
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "owns-svc", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "direct-owns",
				conventions.AnnotationHostnames:  "owns.example.com",
				conventions.AnnotationZoneRef:    "example-com",
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, svc))

	// Step 3: wait for the Service to attach (appears in AttachedSources)
	// while the auto-created annotation stays absent (adopt = no backfill).
	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, tnKey, &tn); err != nil {
			return false
		}
		if tn.Annotations[conventions.AnnotationAutoCreated] == "true" {
			return false
		}
		for _, src := range tn.Status.AttachedSources {
			if src.Name == "owns-svc" {
				return true
			}
		}
		return false
	}, 45*time.Second, 250*time.Millisecond,
		"Service must attach and auto-created annotation must stay absent (adopt path)")

	// Step 4: with the Service actively attaching, the tunnel CR's
	// OwnerReferences must NEVER become non-empty and the auto-created
	// annotation must stay absent. Each tick bumps a benign retrigger label
	// so the tunnel reconciler keeps running (otherwise needsOwnerTransfer
	// would never be exercised — vacuity guard). Pre-Task-14 (ungated
	// needsOwnerTransfer) the Service is promoted to controller-owner within
	// a few reconciles and this require.Never trips.
	require.Never(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, tnKey, &tn); err != nil {
			return false // transient Get error — not the failure condition
		}
		if len(tn.OwnerReferences) > 0 {
			return true // operator took controller-ownership of a user's CR — §7 violation
		}
		if tn.Annotations[conventions.AnnotationAutoCreated] == "true" {
			return true // operator backfilled the marker — §7 violation
		}
		bumpRetriggerTick(ctx, f.c, &tn)
		return false
	}, 10*time.Second, 250*time.Millisecond,
		"direct-create tunnel must never acquire a controller OwnerReference (needsOwnerTransfer isAutoCreated-gated)")

	// Final state: no owner refs, auto-created annotation still absent.
	var finalTunnel v1alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, tnKey, &finalTunnel))
	require.Empty(t, finalTunnel.OwnerReferences,
		"direct-create tunnel must end with zero OwnerReferences")
	require.NotEqual(t, "true", finalTunnel.Annotations[conventions.AnnotationAutoCreated],
		"adopt path must not backfill the auto-created annotation")
}

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
	// kube-controller-manager). gcEmulateStripDeadOwner gates completion on
	// BOTH ownerRefs==0 AND AttachedSources==0 — isolation-safety: without
	// waiting for the ServiceSourceReconciler to drain the cache, the tunnel
	// reconciler would see AttachedSources=[solo-svc] and requeue at the 30-min
	// defaultTunnelInterval, stalling the test in strict isolation. The helper
	// bumps a metadata label each tick (envtest.local/strip-tick) to force a
	// real resourceVersion change and trigger a watch event.
	gcEmulateStripDeadOwner(t, ctx, f.c, tnKey, "solo-svc")

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

// TestEnvtest_CascadeGC_TwoTickRaceProtection pins the cascade-GC race-safety
// contract (design §3.1) against a real apiserver: a new source that attaches
// within the grace window must clear LastOrphanedAt so the tunnel does NOT
// self-delete, and ownership must transfer to the new source.
//
// Sequence:
//  1. An annotated Service ("first") creates an auto-created tunnel (race-tnl).
//  2. "first" is deleted; gcEmulateStripDeadOwner strips its ownerRef and waits
//     for AttachedSources to drain so production isOrphaned becomes true.
//  3. Production stamps LastOrphanedAt (first orphan observation).
//  4. A second annotated Service ("second") is created IMMEDIATELY after the
//     stamp is observed — within the 3-second grace window (no time.Sleep).
//     The production reconciler detects the new attach, clears LastOrphanedAt,
//     and transfers ownership to "second"; the tunnel survives.
//
// Race-window handling: the 3-second grace (PendingDeletionGrace) is generous
// relative to controller-runtime reconcile latency (~ms). Creating "second"
// immediately after observing the stamp gives the reattach + cache-populate +
// reconcile a comfortable window against self-delete. A truly lost race
// (tunnel deleted before "second" attaches) would surface as a NotFound error
// on the final Get — the test FAILS loudly, never false-passes.
//
// This test is non-vacuous: if the production reconciler's clear-on-reattach
// path (the `else if tn.Status.LastOrphanedAt != nil` branch in Reconcile)
// were removed, self-delete would win and this test would fail with the tunnel
// being NotFound on the final assertion.
func TestEnvtest_CascadeGC_TwoTickRaceProtection(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupServiceEnv(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// CloudflareZone scaffold — DNSRecord admission CEL requires has(zoneID)||has(zoneRef).
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
					conventions.AnnotationTunnelName: "race-tnl",
					conventions.AnnotationHostnames:  host,
					conventions.AnnotationZoneRef:    "example-com",
				},
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{{Port: 80}},
			},
		}
	}

	// Step 1: "first" creates the auto-created tunnel.
	first := mkSvc("first", "first.example.com")
	require.NoError(t, f.c.Create(ctx, first))

	// DeriveTunnelName template: "cf-<ns>-<tunnel-name>".
	tnName := "cf-" + f.ns + "-race-tnl"
	tnKey := types.NamespacedName{Namespace: f.ns, Name: tnName}

	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, tnKey, &tn); err != nil {
			return false
		}
		return tn.Annotations[conventions.AnnotationAutoCreated] == "true" &&
			len(tn.OwnerReferences) == 1 && tn.OwnerReferences[0].Name == "first"
	}, 45*time.Second, 250*time.Millisecond,
		"auto-created tunnel must appear owned by first")

	// Step 2: delete "first" and emulate GC — strip its ownerRef and wait for
	// AttachedSources to drain so production isOrphaned becomes true.
	require.NoError(t, f.c.Delete(ctx, first))
	gcEmulateStripDeadOwner(t, ctx, f.c, tnKey, "first")

	// Step 3: wait for production to stamp LastOrphanedAt (first orphan observation).
	// Do NOT sleep — capture the stamp time and immediately proceed to step 4.
	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, tnKey, &tn); err != nil {
			return false
		}
		return tn.Status.LastOrphanedAt != nil
	}, 30*time.Second, 250*time.Millisecond,
		"LastOrphanedAt must be stamped after first is removed and cache drains")

	// Step 4: create "second" IMMEDIATELY inside the grace window (no time.Sleep).
	// The production reconciler will observe the new attach, clear LastOrphanedAt,
	// and transfer ownership; the tunnel must NOT self-delete.
	second := mkSvc("second", "second.example.com")
	require.NoError(t, f.c.Create(ctx, second))

	// Step 5: assert the tunnel survived, LastOrphanedAt is nil, and ownership
	// transferred to "second". The tunnel must NOT be NotFound at any point after
	// "second" is created — the Finally assertion below checks existence too.
	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, tnKey, &tn); err != nil {
			// NotFound here means self-delete beat "second"'s reattach — test fails.
			return false
		}
		return tn.Status.LastOrphanedAt == nil &&
			len(tn.OwnerReferences) == 1 && tn.OwnerReferences[0].Name == "second"
	}, 45*time.Second, 250*time.Millisecond,
		"tunnel must survive, clear LastOrphanedAt, and transfer ownership to second")

	// Step 6 (hardening): assert self-delete does NOT fire for ~6s after "second"
	// exists — 2× the grace window, ensuring the clear is durable.
	require.Never(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		err := f.c.Get(ctx, tnKey, &tn)
		return apierrors.IsNotFound(err)
	}, 6*time.Second, 250*time.Millisecond,
		"tunnel must not be NotFound after second attaches (self-delete must not fire)")
}

// TestEnvtest_CascadeGC_DirectCreateNeverGCd pins the contract (design §8.2)
// that user-authored (direct-create) CloudflareTunnel CRs are NEVER
// auto-GC'd: the orphan block is skipped at `if isAutoCreated(&tn)` because
// the cloudflare.io/auto-created annotation is absent.
//
// Sequence:
//  1. Create a CloudflareTunnel CR directly with the derived name
//     "cf-<ns>-direct-tnl" and NO auto-created annotation. Add the standard
//     finalizer so it reconciles like a real CR.
//  2. Create an annotated Service (tunnel-name: direct-tnl). EnsureTunnelCR
//     finds the existing CR (adopt path) and returns it UNTOUCHED — it must
//     NOT stamp auto-created (no backfill; Task-4 contract).
//  3. Wait for the Service to appear in Status.AttachedSources.
//  4. Delete the Service.
//  5. Assert via require.Never (12s, 250ms tick) that the tunnel CR is
//     NEVER NotFound. Each tick strips any dangling ownerReference whose
//     owner is the deleted service (envtest has no garbage collector, so a
//     deleted source's ownerRef stays until explicitly removed; without this
//     strip, len(OwnerReferences)>0 would permanently block isOrphaned from
//     evaluating — the isAutoCreated gate under test would never be reached,
//     leaving the test vacuous) AND bumps a benign envtest.local/retrigger-tick
//     label so the tunnel reconciler is actively retriggered throughout the
//     window. The production orphan block re-evaluates on every tick while
//     the source is gone. Finally assert LastOrphanedAt==nil (never stamped)
//     and the auto-created annotation is still absent.
//
// Harness-fidelity note: although EnsureTunnelCR's adopt path does NOT set
// an ownerReference (the direct-create CR is returned untouched), the tunnel
// reconciler's needsOwnerTransfer path promotes the first attaching source
// (the Service) to controller-owner on a subsequent reconcile. envtest has
// no kube-controller-manager, so deleting the Service leaves a dangling
// ownerRef. The per-tick strip in require.Never emulates real-cluster GC
// (identical to the gcEmulateStripDeadOwner helper used by the T11 and T10
// tests) and enables isOrphaned to evaluate correctly.
//
// Non-vacuity (mutation-verified): if the isAutoCreated gate in the orphan
// block is forced to return true, the tunnel has no annotation guard,
// isOrphaned fires once ownerRefs are stripped and the cache drains,
// LastOrphanedAt is stamped, and after the 3s grace the tunnel self-deletes
// — require.Never trips because the tunnel is NotFound. With the gate intact
// (isAutoCreated returns false for the unannotated direct-create tunnel)
// the orphan block is never entered, LastOrphanedAt stays nil, and the
// tunnel survives the full window.
func TestEnvtest_CascadeGC_DirectCreateNeverGCd(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupServiceEnv(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// CloudflareZone scaffold — DNSRecord admission CEL requires has(zoneID)||has(zoneRef).
	zone := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v1alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	// Step 1: create the CloudflareTunnel CR directly — no auto-created annotation.
	// The name follows the DeriveTunnelName template so the annotated Service's
	// EnsureTunnelCR will find it via Get and take the adopt path (return as-is).
	tnName := "cf-" + f.ns + "-direct-tnl"
	tnKey := types.NamespacedName{Namespace: f.ns, Name: tnName}

	directTunnel := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tnName,
			Namespace: f.ns,
			// No cloudflare.io/auto-created annotation — this is the direct-create
			// path. The operator must not backfill the marker on adopt.
			Finalizers: []string{conventions.FinalizerName},
		},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name: "cf-direct",
			Connector: v1alpha1.ConnectorSpec{
				Replicas:           1,
				Protocol:           "auto",
				LogLevel:           "info",
				GracePeriodSeconds: 30,
			},
		},
	}
	require.NoError(t, f.c.Create(ctx, directTunnel))

	// Step 2: annotated Service causes EnsureTunnelCR to find the existing CR
	// (adopt path). The existing CR must NOT receive the auto-created marker.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "direct-svc", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "direct-tnl",
				conventions.AnnotationHostnames:  "direct.example.com",
				conventions.AnnotationZoneRef:    "example-com",
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, svc))

	// Step 3: wait for the Service to appear in Status.AttachedSources and
	// verify the adopt path did NOT backfill the auto-created annotation.
	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, tnKey, &tn); err != nil {
			return false
		}
		// Annotation must remain absent — adopt path must not stamp it.
		if tn.Annotations[conventions.AnnotationAutoCreated] == "true" {
			return false
		}
		for _, src := range tn.Status.AttachedSources {
			if src.Name == "direct-svc" {
				return true
			}
		}
		return false
	}, 45*time.Second, 250*time.Millisecond,
		"Service must attach and auto-created annotation must stay absent (adopt path)")

	// Step 4: delete the Service.
	require.NoError(t, f.c.Delete(ctx, svc))

	// Step 5: assert the tunnel is NEVER deleted over 12s (> 3s grace + stamp + 3s
	// requeue + drain, with margin). Each tick strips any dangling ownerRef whose
	// owner is the deleted service (envtest GC emulation — without this,
	// len(OwnerReferences)>0 would permanently block isOrphaned and the test
	// would be vacuous with respect to the isAutoCreated gate) AND bumps a benign
	// envtest.local/retrigger-tick label so the tunnel reconciler is retriggered
	// throughout the window. With the isAutoCreated gate intact the orphan block
	// is never entered (tunnel is not auto-created) — no stamp, no self-delete.
	// With the gate mutated to return true the tunnel self-deletes within the
	// window and require.Never trips. Conflict-tolerant: error ignored, next tick
	// retries with a fresh Get.
	require.Never(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, tnKey, &tn); err != nil {
			return apierrors.IsNotFound(err)
		}
		// Strip any dangling ownerReference whose owner is the now-deleted service
		// (envtest has no garbage collector, so a deleted source's ownerRef stays
		// until explicitly removed). Without this, len(OwnerReferences)>0 would
		// block isOrphaned from ever evaluating — the gate under test would never
		// be reached, leaving the test vacuous again.
		kept := tn.OwnerReferences[:0]
		for _, or := range tn.OwnerReferences {
			if or.Name != "direct-svc" {
				kept = append(kept, or)
			}
		}
		tn.OwnerReferences = kept
		// Bump a test-only label to force a real resourceVersion change and generate
		// a watch event so the tunnel reconciler re-runs this tick. The label key
		// uses the envtest.local/ prefix — NOT cloudflare.io/ — because this is a
		// test-only mechanical device, not an operator annotation.
		if tn.Labels == nil {
			tn.Labels = map[string]string{}
		}
		tn.Labels["envtest.local/retrigger-tick"] = strconv.FormatInt(time.Now().UnixNano(), 10)
		_ = f.c.Update(ctx, &tn) // conflict-tolerant: error ignored, next tick retries
		return false
	}, 12*time.Second, 250*time.Millisecond,
		"direct-create tunnel must never be auto-GC'd (isAutoCreated gate skips orphan path)")

	// Final state: LastOrphanedAt never stamped; auto-created annotation absent.
	var finalTunnel v1alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, tnKey, &finalTunnel))
	require.Nil(t, finalTunnel.Status.LastOrphanedAt,
		"direct-create tunnel must never have LastOrphanedAt stamped")
	require.NotEqual(t, "true", finalTunnel.Annotations[conventions.AnnotationAutoCreated],
		"adopt path must not backfill the auto-created annotation")
}
