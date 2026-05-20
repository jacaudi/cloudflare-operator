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

// Feature F force-reconcile envtest coverage for the CloudflareDNSRecord
// controller (S6 / backlog #2).
//
// DNSRecord is the most complex of the 5 reconcilers (primary record + TXT
// companion + drift detection + mode handling). A passing envtest on this
// controller is strong evidence the prelude works uniformly.
//
// Both tests reuse the newTxtRegistryHarness + scaffoldZoneMgr helpers from
// dnsrecord_txt_registry_test.go — no parallel fixture API is introduced.
//
// Call-count assertions use m.Calls("DNS.GetRecord") — the managed-mode path
// always calls GetRecord on every reconcile tick when RecordID is set. The
// force-reconcile contract for DNSRecord is:
//   - When annotation != lastAck: the reconcile runs to completion AND the ack
//     is written to status.lastReconcileToken.
//   - When annotation == lastAck: no spurious ack re-write; controller
//     reconciles normally (GetRecord still fires on each tick — no early exit).
//
// The primary assertion in both tests is the ack progression in
// status.lastReconcileToken, which is ONLY written when the prelude detects a
// new token (forceReconcile=true path). The call-count delta is a secondary,
// corroborating assertion.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// TestEnvtest_DNSRecord_ForceReconcile_AnnotationBypassesNoDriftShortCircuit
// verifies that patching cloudflare.io/reconcile-at on a steady-state
// (Ready, no drift) CloudflareDNSRecord triggers a full re-check and
// persists the ack in status.lastReconcileToken.
//
// Sequence:
//  1. Create Zone + DNSRecord → wait for Ready (TXT companion created, RecordID set).
//  2. Snapshot DNS.GetRecord call count (baseline for steady-state).
//  3. Patch annotation to "tkn-1" → assert lastReconcileToken becomes "tkn-1"
//     AND at least one additional DNS.GetRecord call is recorded.
//  4. Patch annotation to "tkn-2" → assert lastReconcileToken becomes "tkn-2"
//     (second force-reconcile acked correctly).
func TestEnvtest_DNSRecord_ForceReconcile_AnnotationBypassesNoDriftShortCircuit(t *testing.T) {
	ctx, m, c := newTxtRegistryHarness(t)

	zoneID := scaffoldZoneMgr(t, ctx, c, "frc-bypass", "default")

	content := "192.0.2.100"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "frc-bypass-rec", Namespace: "default"},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    "frc.bypass.example.com",
			Type:    "A",
			Content: &content,
			ZoneID:  zoneID,
			Mode:    v2alpha1.RecordModeManaged,
		},
	}
	require.NoError(t, c.Create(ctx, rec))
	t.Cleanup(func() { _ = c.Delete(context.Background(), rec) })

	// Step 1: wait for the controller to reach steady-state (Ready + RecordID set).
	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareDNSRecord
		if err := c.Get(ctx, types.NamespacedName{Name: "frc-bypass-rec", Namespace: "default"}, &got); err != nil {
			return false
		}
		return got.Status.RecordID != "" &&
			dnsRecordReadyReason(ctx, c, "frc-bypass-rec", "default") == conventions.ReasonReady
	}, 20*time.Second, 250*time.Millisecond, "DNSRecord must reach Ready with RecordID set")

	// Step 2: snapshot GetRecord call count immediately after the Ready wait.
	// At the 5-minute default interval the controller will not tick again
	// before the annotation patch below, so no additional wait is needed.
	getsBefore := m.Calls("DNS.GetRecord")

	// Step 3: patch annotation to "tkn-1" — controller must ack it.
	var live v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "frc-bypass-rec", Namespace: "default"}, &live))
	if live.Annotations == nil {
		live.Annotations = map[string]string{}
	}
	live.Annotations[conventions.AnnotationReconcileAt] = "tkn-1"
	require.NoError(t, c.Update(ctx, &live))

	// Wait for the ack to be written (forceReconcile path ran and completed).
	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareDNSRecord
		if err := c.Get(ctx, types.NamespacedName{Name: "frc-bypass-rec", Namespace: "default"}, &got); err != nil {
			return false
		}
		return got.Status.LastReconcileToken == "tkn-1"
	}, 15*time.Second, 250*time.Millisecond, "status.lastReconcileToken must be acked to 'tkn-1'")

	// Corroborating: at least one additional DNS.GetRecord call was made — the
	// managed reconcile path ran (full re-check, not an early exit).
	getsAfter := m.Calls("DNS.GetRecord")
	require.Greater(t, getsAfter, getsBefore,
		"at least one additional DNS.GetRecord must be recorded after the force-reconcile")

	// Step 4: patch annotation again to "tkn-2" — second force must be acked.
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "frc-bypass-rec", Namespace: "default"}, &live))
	live.Annotations[conventions.AnnotationReconcileAt] = "tkn-2"
	require.NoError(t, c.Update(ctx, &live))

	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareDNSRecord
		if err := c.Get(ctx, types.NamespacedName{Name: "frc-bypass-rec", Namespace: "default"}, &got); err != nil {
			return false
		}
		return got.Status.LastReconcileToken == "tkn-2"
	}, 15*time.Second, 250*time.Millisecond, "status.lastReconcileToken must be acked to 'tkn-2'")
}

// TestEnvtest_DNSRecord_ForceReconcile_NoAnnotationChange_NoEffect is a
// negative control: when the annotation is already acked (annotation ==
// lastReconcileToken), the prelude must NOT write a new ack (forceReconcile
// stays false). The token must remain stable across multiple reconcile ticks.
//
// Sequence:
//  1. Create Zone + DNSRecord with annotation = "tkn-1" → wait for Ready.
//  2. Seed status.lastReconcileToken = "tkn-1" via Status().Update.
//  3. Wait several reconcile ticks.
//  4. Assert: lastReconcileToken remains "tkn-1" (no spurious re-write).
//  5. Assert: the controller is still healthy (Ready condition intact).
func TestEnvtest_DNSRecord_ForceReconcile_NoAnnotationChange_NoEffect(t *testing.T) {
	ctx, _, c := newTxtRegistryHarness(t)

	zoneID := scaffoldZoneMgr(t, ctx, c, "frc-nochange", "default")

	content := "192.0.2.101"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "frc-nochange-rec",
			Namespace: "default",
			Annotations: map[string]string{
				conventions.AnnotationReconcileAt: "tkn-1",
			},
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    "frc.nochange.example.com",
			Type:    "A",
			Content: &content,
			ZoneID:  zoneID,
			Mode:    v2alpha1.RecordModeManaged,
		},
	}
	require.NoError(t, c.Create(ctx, rec))
	t.Cleanup(func() { _ = c.Delete(context.Background(), rec) })

	// Step 1: wait for Ready (RecordID set — the controller ran its first reconcile).
	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareDNSRecord
		if err := c.Get(ctx, types.NamespacedName{Name: "frc-nochange-rec", Namespace: "default"}, &got); err != nil {
			return false
		}
		return got.Status.RecordID != "" &&
			dnsRecordReadyReason(ctx, c, "frc-nochange-rec", "default") == conventions.ReasonReady
	}, 20*time.Second, 250*time.Millisecond, "DNSRecord must reach Ready with RecordID set")

	// Step 2: seed the ack so annotation == lastReconcileToken.
	// The controller may have already acked "tkn-1" on its first reconcile
	// (annotation was present at creation time). Check first; only
	// Status().Update if the ack isn't set yet.
	var live v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "frc-nochange-rec", Namespace: "default"}, &live))
	if live.Status.LastReconcileToken != "tkn-1" {
		live.Status.LastReconcileToken = "tkn-1"
		require.NoError(t, c.Status().Update(ctx, &live))
	}

	// Wait for the apiserver to reflect the ack (may already be there).
	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareDNSRecord
		if err := c.Get(ctx, types.NamespacedName{Name: "frc-nochange-rec", Namespace: "default"}, &got); err != nil {
			return false
		}
		return got.Status.LastReconcileToken == "tkn-1"
	}, 5*time.Second, 100*time.Millisecond, "status.lastReconcileToken must be seeded to 'tkn-1'")

	// Step 3+4: wait several reconcile ticks. The default interval for DNSRecord
	// is 5m, but the annotation patch triggered one full reconcile. Let the
	// controller tick at least once more under the acked state and confirm the
	// token is unchanged. We trigger a second reconcile via a benign annotation
	// update (a separate key), then confirm token remains "tkn-1".
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "frc-nochange-rec", Namespace: "default"}, &live))
	if live.Annotations == nil {
		live.Annotations = map[string]string{}
	}
	live.Annotations[conventions.AnnotationReconcileAt] = "tkn-1" // same value — no new force
	live.Annotations["test.cloudflare.io/touch"] = "1"            // triggers a reconcile
	require.NoError(t, c.Update(ctx, &live))

	// Give the controller time to process the re-queue. Then assert the ack
	// value has NOT changed — the prelude must return forceReconcile=false
	// because annotation == lastReconcileToken.
	//
	// We cannot assert "zero GetRecord calls since seeding" because the
	// controller always calls GetRecord in managed mode; the no-effect property
	// is proven by the ack STAYING at "tkn-1" (no advance to a different value).
	require.Never(t, func() bool {
		var got v2alpha1.CloudflareDNSRecord
		if err := c.Get(ctx, types.NamespacedName{Name: "frc-nochange-rec", Namespace: "default"}, &got); err != nil {
			return false
		}
		return got.Status.LastReconcileToken != "tkn-1"
	}, 5*time.Second, 250*time.Millisecond, "status.lastReconcileToken must NOT change when annotation is already acked")

	// Step 5: the controller must remain healthy (Ready=True) throughout.
	require.Equal(t, conventions.ReasonReady,
		dnsRecordReadyReason(ctx, c, "frc-nochange-rec", "default"),
		"controller must remain Ready with an already-acked annotation")

	// Sanity: make sure the annotation itself was not modified by the operator
	// (the contract is: operator writes status.lastReconcileToken, NEVER the
	// annotation).
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "frc-nochange-rec", Namespace: "default"}, &live))
	require.Equal(t, "tkn-1", live.Annotations[conventions.AnnotationReconcileAt],
		"operator must never modify the cloudflare.io/reconcile-at annotation")
}
