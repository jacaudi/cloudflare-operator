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

	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
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

	gcLegacyCompanion(context.Background(), m.DNS, zoneID, zoneDomain, host,
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

	gcLegacyCompanion(context.Background(), m.DNS, zoneID, zoneDomain, host,
		"network", "rec1", cloudflare.NewAutoDetectingCodec(enc))

	got, _ := m.DNS.ListRecordsByNameAndType(context.Background(), zoneID, oldName, "TXT")
	require.Len(t, got, 1, "foreign legacy companion must NOT be deleted")
}

func TestGCLegacyCompanion_NoopWhenZoneDomainEmpty(t *testing.T) {
	m := mock.New()
	enc := cloudflare.NewPlaintextCodec()
	// zoneDomain "" models the literal-Spec.ZoneID path: must skip silently.
	gcLegacyCompanion(context.Background(), m.DNS, "z1", "", "external.jacaudi.dev",
		"network", "rec1", cloudflare.NewAutoDetectingCodec(enc))
	require.Equal(t, 0, m.Calls("DNS.DeleteRecord"), "must not call DeleteRecord with empty zone domain")
}
