/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package envtest_test

// Legacy-companion GC one-shot ack envtest coverage for the
// CloudflareDNSRecord controller (simplify slice 1 / finding B).
//
// gcLegacyCompanion is called (and acked) via the case cout.ownershipOK
// branch in Reconcile. Both tests use ZoneRef (not a literal ZoneID) so
// that zres.ZoneObject is non-nil and zoneDomain is populated — required for
// gcLegacyCompanion to issue any List calls (the function short-circuits on
// zoneDomain=="" per design §4.4).
//
// Harness: newTxtRegistryHarness (shared with dnsrecord_txt_registry_test.go).
// The DNS reconciler's Recorder field is nil in this harness, so event
// assertions are not used. Non-vacuity for NoAckOnError is proven by asserting
// DNS.ListRecordsByNameAndType was called at least once after the baseline
// (proving the helper ran and the ownership check matched) before asserting
// the ack is still false.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// legacyAffixNameEnvtest reproduces the PRE-S1 AffixName scheme to derive
// the exact legacy candidate names that gcLegacyCompanion will query.
// Must stay byte-for-byte identical to legacyAffixName in txt_registry.go
// (that function is unexported; this copy keeps the envtest self-contained).
func legacyAffixNameEnvtest(prefix, name string) string {
	sanitize := func(label string) string {
		if label == "*" {
			return "_wildcard"
		}
		return label
	}
	hasAnyDot := false
	for _, c := range name {
		if c == '.' {
			hasAnyDot = true
			break
		}
	}
	if !hasAnyDot {
		return prefix + "." + sanitize(name)
	}
	segs := splitByDot(name)
	for i, s := range segs {
		segs[i] = sanitize(s)
	}
	head := joinByDash(segs[:len(segs)-1])
	return prefix + "-" + head + "." + segs[len(segs)-1]
}

func splitByDot(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func joinByDash(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "-" + p
	}
	return out
}

// TestEnvtest_DNSRecord_LegacyGC_AckOnFirstSuccess verifies that
// Status.LegacyCompanionGCDone is stamped true after the first successful
// reconcile (when the mock returns no legacy candidates — the common post-S1
// case) and that subsequent reconciles do NOT issue additional
// ListRecordsByNameAndType calls for the legacy candidate names (proving the
// gate prevents repeated GC passes).
func TestEnvtest_DNSRecord_LegacyGC_AckOnFirstSuccess(t *testing.T) {
	ctx, m, c := newTxtRegistryHarness(t)

	// Create zone CR via ZoneRef path so zoneDomain is non-empty inside
	// gcLegacyCompanion (the function short-circuits on zoneDomain=="").
	zoneCRName := "lgc-ack-zone"
	_ = scaffoldZoneMgr(t, ctx, c, zoneCRName, "default")

	recordName := "host.lgc.example.com"

	// Snapshot List call count before creating the DNSRecord CR.
	listBefore := m.Calls("DNS.ListRecordsByNameAndType")

	content := "192.0.2.200"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "lgc-ack-rec", Namespace: "default"},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    recordName,
			Type:    "A",
			Content: &content,
			ZoneRef: &v2alpha1.ZoneReference{Name: zoneCRName},
			Mode:    v2alpha1.RecordModeManaged,
		},
	}
	require.NoError(t, c.Create(ctx, rec))
	t.Cleanup(func() { _ = c.Delete(context.Background(), rec) })

	// Step 1: wait for Status.LegacyCompanionGCDone to become true.
	// The first reconcile creates the main record and TXT companion, then calls
	// gcLegacyCompanion. With no legacy records seeded the helper finds empty
	// results (legacyFound=false, err=nil) → the else branch stamps the ack.
	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareDNSRecord
		if err := c.Get(ctx, types.NamespacedName{Name: "lgc-ack-rec", Namespace: "default"}, &got); err != nil {
			return false
		}
		return got.Status.LegacyCompanionGCDone
	}, 20*time.Second, 250*time.Millisecond,
		"Status.LegacyCompanionGCDone must become true after first successful reconcile")

	// Confirm the record is Ready (ownershipOK path was taken — that is the
	// branch containing the gcLegacyCompanion call).
	require.Equal(t, conventions.ReasonReady,
		dnsRecordReadyReason(ctx, c, "lgc-ack-rec", "default"),
		"DNSRecord must be Ready (confirming ownershipOK path was taken)")

	// Step 2: snapshot List count after the ack was written.
	// gcLegacyCompanion queries both legacy candidates, so at least one List
	// call must have occurred since the baseline.
	listAfterAck := m.Calls("DNS.ListRecordsByNameAndType")
	require.Greater(t, listAfterAck, listBefore,
		"gcLegacyCompanion must have issued at least one List call before stamping the ack")

	// Step 3: trigger a second reconcile via an annotation touch (mirrors the
	// S6 force-reconcile envtest pattern).
	var live v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "lgc-ack-rec", Namespace: "default"}, &live))
	if live.Annotations == nil {
		live.Annotations = map[string]string{}
	}
	live.Annotations["test.cloudflare.io/touch"] = "lgc-1"
	require.NoError(t, c.Update(ctx, &live))

	// Step 4: assert the List count does NOT increase beyond listAfterAck for
	// 5 seconds. The gate (!LegacyCompanionGCDone) must prevent gcLegacyCompanion
	// from running on all subsequent reconciles. require.Never asserts that
	// the condition never becomes true within the window.
	require.Never(t, func() bool {
		return m.Calls("DNS.ListRecordsByNameAndType") > listAfterAck
	}, 5*time.Second, 200*time.Millisecond,
		"gcLegacyCompanion must NOT issue any List calls after Status.LegacyCompanionGCDone is true")
}

// TestEnvtest_DNSRecord_LegacyGC_NoAckOnError verifies that
// Status.LegacyCompanionGCDone is NOT stamped when gcLegacyCompanion
// encounters a delete error (legacyFound=true, err!=nil path).
//
// Non-vacuity proof: we assert via require.Eventually that
// DNS.ListRecordsByNameAndType was called at least once after the baseline
// (proving the helper ran and found the seeded legacy record). If the
// controller were to stamp the ack despite the delete error, the gate would
// prevent the List call on the second reconcile — but there IS no second
// reconcile within the 5-minute default interval, so the assertion
// "List was called after baseline" is non-trivially satisfied only when the
// helper actually ran. This is then combined with asserting the ack is false.
func TestEnvtest_DNSRecord_LegacyGC_NoAckOnError(t *testing.T) {
	ctx, m, c := newTxtRegistryHarness(t)

	zoneCRName := "lgc-noack-zone"
	zoneID := scaffoldZoneMgr(t, ctx, c, zoneCRName, "default")

	recordName := "host.lgcerr.example.com"

	// Derive the legacy candidate name gcLegacyCompanion will query for
	// the bare (non-zone-appended) form. Seed a TXT record at this name
	// in the mock DNS store with the correct ownership payload so that
	// verifyTXTOwnership passes and legacyFound is set to true.
	legacyName := legacyAffixNameEnvtest("cf-txt", recordName)

	ownerPayload := cloudflare.RegistryPayload{
		V: 1, K: "CloudflareDNSRecord", NS: "default", N: "lgc-noack-rec",
	}
	ownerContent, err := cloudflare.NewPlaintextCodec().Encode(ownerPayload)
	require.NoError(t, err)

	_, err = m.DNS.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
		Type: "TXT", Name: legacyName, Content: ownerContent, TTL: 1,
	})
	require.NoError(t, err)

	// Install a persistent delete error so that EVERY reconcile tick that
	// reaches the gcLegacyCompanion delete path sees a failure. A single-shot
	// InjectError would be consumed on the first reconcile and silently succeed
	// on controller-runtime's immediate re-queue (triggered by the first
	// reconcile's status write). InjectPersistentError fires on every call
	// until cleared; ClearPersistentError is called via t.Cleanup so the
	// error is active throughout the assertion window.
	m.InjectPersistentError("DNS.DeleteRecord", errors.New("simulated delete failure"))
	t.Cleanup(func() { m.ClearPersistentError("DNS.DeleteRecord") })

	listBefore := m.Calls("DNS.ListRecordsByNameAndType")

	content := "192.0.2.201"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "lgc-noack-rec", Namespace: "default"},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    recordName,
			Type:    "A",
			Content: &content,
			ZoneRef: &v2alpha1.ZoneReference{Name: zoneCRName},
			Mode:    v2alpha1.RecordModeManaged,
		},
	}
	require.NoError(t, c.Create(ctx, rec))
	t.Cleanup(func() { _ = c.Delete(context.Background(), rec) })

	// Step 1: wait for DNS.ListRecordsByNameAndType to be called at least once
	// after the baseline — proving the helper ran and found the seeded record.
	require.Eventually(t, func() bool {
		return m.Calls("DNS.ListRecordsByNameAndType") > listBefore
	}, 20*time.Second, 250*time.Millisecond,
		"gcLegacyCompanion must have issued at least one List call (proving the helper ran)")

	// Step 2: wait for the CR to reach Ready — this confirms the ownershipOK
	// path was taken (the branch containing the gcLegacyCompanion call).
	// The delete error inside gcLegacyCompanion does NOT block Ready; the
	// controller logs the error and emits an Event without marking the record
	// degraded.
	require.Eventually(t, func() bool {
		return dnsRecordReadyReason(ctx, c, "lgc-noack-rec", "default") == conventions.ReasonReady
	}, 20*time.Second, 250*time.Millisecond,
		"DNSRecord must reach Ready (ownershipOK path taken) so gcLegacyCompanion was entered")

	// Step 3: assert the ack is still false despite the helper having run.
	// The delete error caused gcLegacyCompanion to return (legacyFound=true,
	// err!=nil); the controller must not stamp the ack in that case.
	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "lgc-noack-rec", Namespace: "default"}, &got))
	require.False(t, got.Status.LegacyCompanionGCDone,
		"Status.LegacyCompanionGCDone must remain false when gcLegacyCompanion returns a delete error")
}
