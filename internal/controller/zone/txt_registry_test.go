/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package zone

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
)

// zoneScheme returns a runtime.Scheme with corev1 and v2alpha1 registered.
func zoneScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, v2alpha1.AddToScheme(s))
	return s
}

// --- loadCodec tests ---

func TestLoadCodec_EmptyKeyName_DefaultsToKey(t *testing.T) {
	s := zoneScheme(t)
	var raw [32]byte
	for i := range raw {
		raw[i] = byte(i)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "txt-key", Namespace: "default"},
		Data:       map[string][]byte{"key": raw[:]},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(secret).Build()
	ref := &v2alpha1.SecretReference{Name: "txt-key"} // Key intentionally empty
	codec, err := loadCodec(context.Background(), c, ref, "default")
	require.NoError(t, err)
	require.Equal(t, "aes-gcm", codec.Kind())
}

func TestLoadCodec_NoKey_ReturnsPlaintext(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(zoneScheme(t)).Build()

	codec, err := loadCodec(ctx, c, nil, "default")
	require.NoError(t, err)
	require.Equal(t, "plaintext", codec.Kind())
}

func TestLoadCodec_ValidKey_ReturnsAESGCM(t *testing.T) {
	ctx := context.Background()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "txt-key", Namespace: "default"},
		Data:       map[string][]byte{"key": key},
	}
	c := fake.NewClientBuilder().WithScheme(zoneScheme(t)).WithObjects(secret).Build()

	ref := &v2alpha1.SecretReference{Name: "txt-key", Key: "key"}
	codec, err := loadCodec(ctx, c, ref, "default")
	require.NoError(t, err)
	require.Equal(t, "aes-gcm", codec.Kind())
}

func TestLoadCodec_MissingSecret_Errors(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(zoneScheme(t)).Build()

	ref := &v2alpha1.SecretReference{Name: "does-not-exist", Key: "key"}
	_, err := loadCodec(ctx, c, ref, "default")
	require.Error(t, err)
	require.ErrorIs(t, err, cloudflare.ErrSecretNotFound)
}

func TestLoadCodec_WrongKeyLength_Errors(t *testing.T) {
	ctx := context.Background()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "txt-key", Namespace: "default"},
		Data:       map[string][]byte{"key": []byte("tooshort")}, // 8 bytes, not 32
	}
	c := fake.NewClientBuilder().WithScheme(zoneScheme(t)).WithObjects(secret).Build()

	ref := &v2alpha1.SecretReference{Name: "txt-key", Key: "key"}
	_, err := loadCodec(ctx, c, ref, "default")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrTxtRegistryKeyMalformed)
}

func TestLoadCodec_MissingKeyInSecret_Errors(t *testing.T) {
	ctx := context.Background()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "txt-key", Namespace: "default"},
		Data:       map[string][]byte{"other-key": []byte("value")}, // "key" not present
	}
	c := fake.NewClientBuilder().WithScheme(zoneScheme(t)).WithObjects(secret).Build()

	ref := &v2alpha1.SecretReference{Name: "txt-key", Key: "key"}
	_, err := loadCodec(ctx, c, ref, "default")
	require.Error(t, err)
	require.ErrorIs(t, err, cloudflare.ErrSecretKeyMissing)
}

// --- autoDetectingFor tests ---

func TestAutoDetectingFor_AESEncoder_ReadsAESAndPlaintext(t *testing.T) {
	var k [32]byte
	for i := range k {
		k[i] = byte(i)
	}
	enc := cloudflare.NewAESCodec(k) // aes-gcm encoder
	rd := autoDetectingFor(enc)
	// AES-written content round-trips:
	aesContent, err := enc.Encode(cloudflare.RegistryPayload{V: 1, K: "CloudflareDNSRecord", NS: "ns", N: "n"})
	require.NoError(t, err)
	got, err := rd.Decode(aesContent)
	require.NoError(t, err)
	require.Equal(t, "n", got.N)
	// plaintext content also still decodes via the dispatcher:
	pt, _ := cloudflare.NewPlaintextCodec().Encode(cloudflare.RegistryPayload{V: 1, K: "CloudflareDNSRecord", NS: "ns", N: "p"})
	gp, err := rd.Decode(pt)
	require.NoError(t, err)
	require.Equal(t, "p", gp.N)
}

func TestAutoDetectingFor_PlaintextEncoder_RefusesEncrypted(t *testing.T) {
	rd := autoDetectingFor(cloudflare.NewPlaintextCodec()) // no key
	// plaintext decodes:
	pt, _ := cloudflare.NewPlaintextCodec().Encode(cloudflare.RegistryPayload{V: 1, K: "CloudflareDNSRecord", NS: "ns", N: "p"})
	gp, err := rd.Decode(pt)
	require.NoError(t, err)
	require.Equal(t, "p", gp.N)
	// v1: encrypted input with no key configured → ErrUnrecognizedCodec:
	_, err = rd.Decode("v1:AAAA:BBBB")
	require.ErrorIs(t, err, cloudflare.ErrUnrecognizedCodec)
}

// --- loadCodec tests ---

func TestVerifyTXTOwnership_MatchOurUID(t *testing.T) {
	codec := cloudflare.NewPlaintextCodec()
	encoded, err := codec.Encode(cloudflare.RegistryPayload{
		V: 1, K: "CloudflareDNSRecord", NS: "ns", N: "rec1",
	})
	require.NoError(t, err)

	result := verifyTXTOwnership(encoded, codec, "CloudflareDNSRecord", "ns", "rec1")
	require.Equal(t, TxtOwnershipMatch, result)
}

func TestVerifyTXTOwnership_Foreign(t *testing.T) {
	codec := cloudflare.NewPlaintextCodec()
	encoded, err := codec.Encode(cloudflare.RegistryPayload{
		V: 1, K: "CloudflareDNSRecord", NS: "other-ns", N: "other-rec",
	})
	require.NoError(t, err)

	result := verifyTXTOwnership(encoded, codec, "CloudflareDNSRecord", "ns", "rec1")
	require.Equal(t, TxtOwnershipForeign, result)
}

func TestVerifyTXTOwnership_Unparseable(t *testing.T) {
	codec := cloudflare.NewPlaintextCodec()
	result := verifyTXTOwnership("gibberish", codec, "CloudflareDNSRecord", "ns", "rec1")
	require.Equal(t, TxtOwnershipUnrecognized, result)
}

// --- writeTXTCompanion / deleteTXTCompanion tests using the mock ---

func TestWriteTXTCompanion_Create(t *testing.T) {
	ctx := context.Background()
	m := mock.New()
	encoder := cloudflare.NewPlaintextCodec()

	id, err := writeTXTCompanion(ctx, m.DNS, "zone1", "app.example.com", "", "default", "rec1", encoder)
	require.NoError(t, err)
	require.NotEmpty(t, id)

	// The TXT record should now exist in the mock under the affixed name.
	txtName := cloudflare.AffixName(txtAffix, "app.example.com")
	recs, err := m.DNS.ListRecordsByNameAndType(ctx, "zone1", txtName, "TXT")
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, id, recs[0].ID)
}

func TestWriteTXTCompanion_Update(t *testing.T) {
	ctx := context.Background()
	m := mock.New()
	encoder := cloudflare.NewPlaintextCodec()

	// Pre-seed an existing TXT record.
	txtName := cloudflare.AffixName(txtAffix, "app.example.com")
	existing, err := m.DNS.CreateRecord(ctx, "zone1", cloudflare.DNSRecordParams{
		Type: "TXT", Name: txtName, Content: "old-content",
	})
	require.NoError(t, err)

	id, err := writeTXTCompanion(ctx, m.DNS, "zone1", "app.example.com", "newhash", "default", "rec1", encoder)
	require.NoError(t, err)
	require.Equal(t, existing.ID, id) // should update in-place

	recs, err := m.DNS.ListRecordsByNameAndType(ctx, "zone1", txtName, "TXT")
	require.NoError(t, err)
	require.Len(t, recs, 1)
	// Content should have changed.
	require.NotEqual(t, "old-content", recs[0].Content)
}

func TestDeleteTXTCompanion_EmptyID_Noop(t *testing.T) {
	ctx := context.Background()
	m := mock.New()

	err := deleteTXTCompanion(ctx, m.DNS, "zone1", "")
	require.NoError(t, err)
}

func TestDeleteTXTCompanion_NotFound_Tolerated(t *testing.T) {
	ctx := context.Background()
	m := mock.New()

	err := deleteTXTCompanion(ctx, m.DNS, "zone1", "nonexistent-id")
	require.NoError(t, err)
}

func TestDeleteTXTCompanion_Deletes(t *testing.T) {
	ctx := context.Background()
	m := mock.New()
	encoder := cloudflare.NewPlaintextCodec()

	id, err := writeTXTCompanion(ctx, m.DNS, "zone1", "app.example.com", "", "default", "rec1", encoder)
	require.NoError(t, err)

	err = deleteTXTCompanion(ctx, m.DNS, "zone1", id)
	require.NoError(t, err)

	txtName := cloudflare.AffixName(txtAffix, "app.example.com")
	recs, err := m.DNS.ListRecordsByNameAndType(ctx, "zone1", txtName, "TXT")
	require.NoError(t, err)
	require.Empty(t, recs)
}

// --- gcLegacyCompanion tests ---

// seedLegacyRecord creates a TXT record under the legacy naming scheme for
// the given recordName in the mock DNS store, encoded with the given codec
// as owned by ourNS/ourName. Returns the record ID.
func seedLegacyRecord(t *testing.T, ctx context.Context, dns cloudflare.DNSClient, zoneID, recordName, ourNS, ourName string, codec cloudflare.Codec) string {
	t.Helper()
	legacyName := legacyAffixName(txtAffix, recordName)
	payload := cloudflare.RegistryPayload{V: 1, K: "CloudflareDNSRecord", NS: ourNS, N: ourName}
	content, err := codec.Encode(payload)
	require.NoError(t, err)
	rec, err := dns.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
		Type:    "TXT",
		Name:    legacyName,
		Content: content,
	})
	require.NoError(t, err)
	return rec.ID
}

// TestGcLegacyCompanion_NoCandidates: when no legacy-named records exist at
// either candidate name, gcLegacyCompanion returns (false, nil).
func TestGcLegacyCompanion_NoCandidates(t *testing.T) {
	ctx := context.Background()
	m := mock.New()
	readCodec := cloudflare.NewPlaintextCodec()

	legacyFound, err := gcLegacyCompanion(ctx, m.DNS, "zone1", "example.com", "app.example.com", "default", "rec1", readCodec)
	require.NoError(t, err)
	require.False(t, legacyFound)
}

// TestGcLegacyCompanion_LegacyFoundAndDeleted: when a legacy-named record
// exists and passes ownership verification, gcLegacyCompanion deletes it and
// returns (true, nil).
func TestGcLegacyCompanion_LegacyFoundAndDeleted(t *testing.T) {
	ctx := context.Background()
	m := mock.New()
	codec := cloudflare.NewPlaintextCodec()

	// Seed a legacy-named companion owned by our CR.
	legacyID := seedLegacyRecord(t, ctx, m.DNS, "zone1", "app.example.com", "default", "rec1", codec)

	legacyFound, err := gcLegacyCompanion(ctx, m.DNS, "zone1", "example.com", "app.example.com", "default", "rec1", codec)
	require.NoError(t, err)
	require.True(t, legacyFound)

	// The legacy record must have been deleted.
	legacyName := legacyAffixName(txtAffix, "app.example.com")
	recs, listErr := m.DNS.ListRecordsByNameAndType(ctx, "zone1", legacyName, "TXT")
	require.NoError(t, listErr)
	require.Empty(t, recs, "legacy record %s must have been deleted", legacyID)
}

// TestGcLegacyCompanion_DeleteError: when a legacy-named record exists,
// passes ownership verification, but DeleteRecord returns an error,
// gcLegacyCompanion returns (true, err) — legacyFound is still true even
// though the delete failed, so the caller knows not to stamp the ack.
func TestGcLegacyCompanion_DeleteError(t *testing.T) {
	ctx := context.Background()
	m := mock.New()
	codec := cloudflare.NewPlaintextCodec()

	// Seed a legacy-named companion owned by our CR.
	seedLegacyRecord(t, ctx, m.DNS, "zone1", "app.example.com", "default", "rec1", codec)

	// Inject a delete error so the GC delete fails.
	deleteErr := errors.New("CF 429: rate limited")
	m.InjectError("DNS.DeleteRecord", deleteErr)

	legacyFound, err := gcLegacyCompanion(ctx, m.DNS, "zone1", "example.com", "app.example.com", "default", "rec1", codec)
	require.ErrorIs(t, err, deleteErr)
	require.True(t, legacyFound, "legacyFound must be true even when delete fails")
}
