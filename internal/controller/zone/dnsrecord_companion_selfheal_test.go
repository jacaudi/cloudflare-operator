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

package zone

// S1 — TXT companion self-heal regression tests.
//
// These tests cover reconcileTXTCompanion's three load-bearing invariants:
//   1. CreatesWhenAbsent: the composed reconciler creates the companion at
//      the zone-correct name (post-T1 AffixName scheme: cf-txt.<host>).
//   2. RecreatesWhenStoredIDDeletedOOB: a populated-but-stale storedTxtID is
//      not trusted — GetRecord(ID) 404 ⇒ fall back to list-by-name ⇒ create
//      a fresh companion (closes sub-bug #1a).
//   3. ForeignRefusesNoWrite: a foreign companion is REFUSED (no write) —
//      preserves the P5 design Q2 anti-hijack safety invariant.
//
// The 81058/81053-as-relist branch is exercised at the reconciler level in
// the cfEmulatingDNS-decorated tests (S1.T6 TestExternalJacaudiLoop_Regression).
// The bare in-memory mock does not surface CF's exact API-code strings, so
// reaching that branch from a pure unit test is not faithful.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/ipresolver"
)

func TestReconcileTXTCompanion_CreatesWhenAbsent(t *testing.T) {
	m := mock.New()
	enc := cloudflare.NewPlaintextCodec()
	out, err := reconcileTXTCompanion(context.Background(), m.DNS, "z1",
		companionInputs{
			recordName:  "external.jacaudi.dev",
			contentHash: "sha256:abc",
			ourNS:       "network",
			ourName:     "rec1",
			storedTxtID: "",
			encoder:     enc,
			readCodec:   cloudflare.NewAutoDetectingCodec(enc),
		})
	require.NoError(t, err)
	require.True(t, out.ownershipOK)
	require.NotEmpty(t, out.txtRecordID)

	got, lerr := m.DNS.ListRecordsByNameAndType(context.Background(), "z1",
		cloudflare.AffixName(txtAffix, "external.jacaudi.dev"), "TXT")
	require.NoError(t, lerr)
	require.Len(t, got, 1, "companion must be created at the zone-correct name")
}

func TestReconcileTXTCompanion_RecreatesWhenStoredIDDeletedOOB(t *testing.T) {
	m := mock.New()
	enc := cloudflare.NewPlaintextCodec()

	// First reconcile creates the companion.
	out1, err := reconcileTXTCompanion(context.Background(), m.DNS, "z1", companionInputs{
		recordName: "external.jacaudi.dev", contentHash: "sha256:abc",
		ourNS: "network", ourName: "rec1", storedTxtID: "",
		encoder: enc, readCodec: cloudflare.NewAutoDetectingCodec(enc),
	})
	require.NoError(t, err)
	require.True(t, out1.ownershipOK)
	require.NotEmpty(t, out1.txtRecordID)

	// Out-of-band delete the companion, but keep the (now stale) stored ID.
	require.NoError(t, m.DNS.DeleteRecord(context.Background(), "z1", out1.txtRecordID))

	// Sub-bug (a): with a populated-but-stale stored ID we MUST recreate.
	out2, err := reconcileTXTCompanion(context.Background(), m.DNS, "z1", companionInputs{
		recordName: "external.jacaudi.dev", contentHash: "sha256:abc",
		ourNS: "network", ourName: "rec1", storedTxtID: out1.txtRecordID,
		encoder: enc, readCodec: cloudflare.NewAutoDetectingCodec(enc),
	})
	require.NoError(t, err)
	require.True(t, out2.ownershipOK)
	require.NotEmpty(t, out2.txtRecordID)
	require.NotEqual(t, out1.txtRecordID, out2.txtRecordID, "must recreate, not return the dead ID")
}

func TestReconcileTXTCompanion_ForeignRefusesNoWrite(t *testing.T) {
	m := mock.New()
	enc := cloudflare.NewPlaintextCodec()
	txtName := cloudflare.AffixName(txtAffix, "external.jacaudi.dev")

	// Seed a foreign-owner companion at the zone-correct name.
	foreign, ferr := enc.Encode(cloudflare.RegistryPayload{
		V: 1, K: "CloudflareDNSRecord", NS: "other", N: "someoneelse", H: "sha256:zzz",
	})
	require.NoError(t, ferr)
	_, cerr := m.DNS.CreateRecord(context.Background(), "z1", cloudflare.DNSRecordParams{
		Name: txtName, Type: "TXT", Content: foreign, TTL: 1,
	})
	require.NoError(t, cerr)
	before := m.Calls("DNS.UpdateRecord") + m.Calls("DNS.CreateRecord")

	out, rerr := reconcileTXTCompanion(context.Background(), m.DNS, "z1", companionInputs{
		recordName: "external.jacaudi.dev", contentHash: "sha256:abc",
		ourNS: "network", ourName: "rec1", storedTxtID: "",
		encoder: enc, readCodec: cloudflare.NewAutoDetectingCodec(enc),
	})
	require.NoError(t, rerr, "foreign companion is a refusal, not a hard error")
	require.False(t, out.ownershipOK)
	require.Equal(t, "foreign", out.failClass)
	after := m.Calls("DNS.UpdateRecord") + m.Calls("DNS.CreateRecord")
	require.Equal(t, before, after, "MUST NOT write over a foreign companion (anti-hijack)")
}

func TestGCLegacyCompanion_DeletesProvablyOwnOnly(t *testing.T) {
	m := mock.New()
	enc := cloudflare.NewPlaintextCodec()
	const zoneID, zoneDomain, host = "z1", "jacaudi.dev", "external.jacaudi.dev"

	// Seed a legacy-named companion (old AffixName scheme) as Cloudflare
	// would store a non-zone-suffixed POST: "<oldname>.<zone>".
	oldName := legacyAffixName(txtAffix, host) + "." + zoneDomain
	ours, err := enc.Encode(cloudflare.RegistryPayload{
		V: 1, K: "CloudflareDNSRecord", NS: "network", N: "rec1", H: "sha256:abc",
	})
	require.NoError(t, err)
	_, cerr := m.DNS.CreateRecord(context.Background(), zoneID, cloudflare.DNSRecordParams{
		Name: oldName, Type: "TXT", Content: ours, TTL: 1,
	})
	require.NoError(t, cerr)

	_, _ = gcLegacyCompanion(context.Background(), m.DNS, zoneID, zoneDomain, host,
		"network", "rec1", cloudflare.NewAutoDetectingCodec(enc))

	got, _ := m.DNS.ListRecordsByNameAndType(context.Background(), zoneID, oldName, "TXT")
	require.Empty(t, got, "provably-own legacy companion must be deleted")
}

func TestGCLegacyCompanion_LeavesForeignAndUndecodable(t *testing.T) {
	m := mock.New()
	enc := cloudflare.NewPlaintextCodec()
	const zoneID, zoneDomain, host = "z1", "jacaudi.dev", "external.jacaudi.dev"
	oldName := legacyAffixName(txtAffix, host) + "." + zoneDomain

	foreign, err := enc.Encode(cloudflare.RegistryPayload{
		V: 1, K: "CloudflareDNSRecord", NS: "other", N: "notus", H: "x",
	})
	require.NoError(t, err)
	_, cerr := m.DNS.CreateRecord(context.Background(), zoneID, cloudflare.DNSRecordParams{
		Name: oldName, Type: "TXT", Content: foreign, TTL: 1,
	})
	require.NoError(t, cerr)

	_, _ = gcLegacyCompanion(context.Background(), m.DNS, zoneID, zoneDomain, host,
		"network", "rec1", cloudflare.NewAutoDetectingCodec(enc))

	got, _ := m.DNS.ListRecordsByNameAndType(context.Background(), zoneID, oldName, "TXT")
	require.Len(t, got, 1, "foreign legacy companion must NOT be deleted")
}

func TestGCLegacyCompanion_NoopWhenZoneDomainEmpty(t *testing.T) {
	m := mock.New()
	enc := cloudflare.NewPlaintextCodec()
	// zoneDomain "" models the literal-Spec.ZoneID path: must skip silently.
	_, _ = gcLegacyCompanion(context.Background(), m.DNS, "z1", "", "external.jacaudi.dev",
		"network", "rec1", cloudflare.NewAutoDetectingCodec(enc))
	require.Equal(t, 0, m.Calls("DNS.DeleteRecord"), "must not call DeleteRecord with empty zone domain")
}

// TestReconcile_CompanionSelfHealsAndGatesReady — steady-state Managed record
// whose companion was deleted out-of-band while status.TxtRecordID stayed
// populated, no primary drift. Pre-S1 this looped forever (sub-bug a). Post-
// S1 the composed companion reconcile re-validates the stored ID, sees 404,
// falls through to a name-list (empty), and creates a fresh companion.
func TestReconcile_CompanionSelfHealsAndGatesReady(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	const zoneID, host = "z1", "external.jacaudi.dev"
	s := zoneTestScheme(t)
	m := mock.New()

	primaryID := seedARecord(t, m, zoneID, host, "1.2.3.4")
	content := "1.2.3.4"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ext", Namespace: "network",
			Finalizers: []string{conventions.FinalizerName},
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name: host, Type: "A", Content: &content, ZoneID: zoneID,
		},
	}
	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).Build()
	rec.Status.RecordID = primaryID
	rec.Status.CurrentContent = content
	rec.Status.TxtRecordID = "dead-companion-id" // populated but stale
	require.NoError(t, fc.Status().Update(context.Background(), rec))

	r := &CloudflareDNSRecordReconciler{
		Client: fc, Scheme: s,
		DNSClientFn: func(_ cloudflare.Credentials) (cloudflare.DNSClient, error) { return m.DNS, nil },
		IPResolver:  ipresolver.NewResolver(),
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "ext", Namespace: "network"}}
	for i := 0; i < 3; i++ {
		_, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err, "reconcile %d", i)
	}

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, fc.Get(context.Background(), req.NamespacedName, &got))
	require.NotEmpty(t, got.Status.TxtRecordID, "companion must be (re)created")
	require.NotEqual(t, "dead-companion-id", got.Status.TxtRecordID, "stale ID must be replaced")
	comp, _ := m.DNS.ListRecordsByNameAndType(context.Background(), zoneID,
		cloudflare.AffixName(txtAffix, host), "TXT")
	require.Len(t, comp, 1, "exactly one zone-correct companion; no 81058 loop")
	cond := findReadyCondition(got.Status.Conditions)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionTrue, cond.Status, "Ready must be True once companion healthy")
}

// TestReconcile_ForeignCompanionGatesReadyFalse — foreign-owner companion
// pre-exists at the zone-correct name. The reconciler must REFUSE (no write
// over the foreign), leave the primary record intact, and set Ready=False
// with reason ReasonOwnershipCompanionFailed (sub-bug c).
func TestReconcile_ForeignCompanionGatesReadyFalse(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	const zoneID, host = "z1", "external.jacaudi.dev"
	s := zoneTestScheme(t)
	m := mock.New()
	primaryID := seedARecord(t, m, zoneID, host, "1.2.3.4")
	enc := cloudflare.NewPlaintextCodec()
	foreign, err := enc.Encode(cloudflare.RegistryPayload{
		V: 1, K: "CloudflareDNSRecord", NS: "other", N: "notus", H: "x",
	})
	require.NoError(t, err)
	_, cerr := m.DNS.CreateRecord(context.Background(), zoneID, cloudflare.DNSRecordParams{
		Name: cloudflare.AffixName(txtAffix, host), Type: "TXT", Content: foreign, TTL: 1,
	})
	require.NoError(t, cerr)
	content := "1.2.3.4"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "ext", Namespace: "network",
			Finalizers: []string{conventions.FinalizerName}},
		Spec: v2alpha1.CloudflareDNSRecordSpec{Name: host, Type: "A", Content: &content, ZoneID: zoneID},
	}
	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).Build()
	rec.Status.RecordID = primaryID
	rec.Status.CurrentContent = content
	require.NoError(t, fc.Status().Update(context.Background(), rec))
	r := &CloudflareDNSRecordReconciler{
		Client: fc, Scheme: s,
		DNSClientFn: func(_ cloudflare.Credentials) (cloudflare.DNSClient, error) { return m.DNS, nil },
		IPResolver:  ipresolver.NewResolver(),
	}
	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ext", Namespace: "network"}})
	require.NoError(t, err)
	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, fc.Get(context.Background(),
		types.NamespacedName{Name: "ext", Namespace: "network"}, &got))
	require.Equal(t, primaryID, got.Status.RecordID, "primary record untouched")
	cond := findReadyCondition(got.Status.Conditions)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionFalse, cond.Status, "Ready must be False on foreign companion")
	require.Equal(t, conventions.ReasonOwnershipCompanionFailed, cond.Reason)
}

// TestExternalJacaudiLoop_Regression reproduces the live prod loop and proves
// the S1 fix is load-bearing. Setup: zone "jacaudi.dev", hostname
// "external.jacaudi.dev" (hostname ends in the zone), primary A record seeded,
// no TxtRecordID. The cfEmulatingDNS decorator models CF's relative-name
// zone-append so a companion name that does NOT end in the zone is stored
// zone-appended and the operator's exact-name list cannot find it (the exact
// production failure pre-S1).
//
// With the SHIPPED scheme (cf-txt.<hostname>) the companion name already ends
// in the zone → zone-append is a no-op → exact-list finds it → convergence:
// at most ONE companion CreateRecord, then steady-state.
//
// Mutation non-vacuity check: see the instructions in the plan — temporarily
// revert AffixName to the legacy scheme; this test MUST fail (the companion
// gets zone-appended, list misses, and Create is attempted again). Restore
// AffixName; this test MUST pass. Do NOT commit the reverted state.
func TestExternalJacaudiLoop_Regression(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	const zoneID, zoneDomain, host = "z1", "jacaudi.dev", "external.jacaudi.dev"
	s := zoneTestScheme(t)
	m := mock.New()
	dec := &cfEmulatingDNS{inner: m.DNS, Canonicalize: true, ZoneDomain: zoneDomain}

	primaryID := seedARecord(t, m, zoneID, host, "1.2.3.4")
	content := "1.2.3.4"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ext", Namespace: "network",
			Finalizers: []string{conventions.FinalizerName},
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name: host, Type: "A", Content: &content, ZoneID: zoneID,
		},
	}
	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).Build()
	rec.Status.RecordID = primaryID
	rec.Status.CurrentContent = content
	require.NoError(t, fc.Status().Update(context.Background(), rec))
	r := &CloudflareDNSRecordReconciler{
		Client: fc, Scheme: s,
		DNSClientFn: func(_ cloudflare.Credentials) (cloudflare.DNSClient, error) { return dec, nil },
		IPResolver:  ipresolver.NewResolver(),
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "ext", Namespace: "network"}}
	for i := 0; i < 5; i++ {
		_, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err, "reconcile %d must not error", i)
	}

	// Key convergence assertions:
	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, fc.Get(context.Background(), req.NamespacedName, &got))
	require.NotEmpty(t, got.Status.TxtRecordID, "companion txtRecordID must be set")
	cond := findReadyCondition(got.Status.Conditions)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionTrue, cond.Status, "Ready=True after convergence")

	// LOAD-BEARING: the companion must be findable by the exact AffixName query
	// (the name the operator uses to list-by-name). With the shipped scheme the
	// companion name ends in the zone → zoneAppend is a no-op → stored under the
	// exact queried name → list finds it. With the legacy scheme the companion
	// name does NOT end in the zone → zoneAppend fires on write → stored under
	// name+"."+zone → list queries the un-appended form → empty → assertion FAILS.
	// This is the exact production failure that S1 fixes; this assertion is the
	// mutation discriminator.
	companionName := cloudflare.AffixName(txtAffix, host)
	listedRecs, lerr := m.DNS.ListRecordsByNameAndType(context.Background(), zoneID, companionName, "TXT")
	require.NoError(t, lerr)
	require.Len(t, listedRecs, 1,
		"companion must be findable by exact AffixName query %q (shipped scheme: zoneAppend is a no-op; "+
			"legacy scheme: zoneAppend fires on write → companion stored zone-appended → list misses → FAIL). "+
			"This is the production failure S1 fixes.",
		companionName)
}
