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

// dnsrecord_quote_integration_test.go — Bug A non-vacuity proof.
//
// Cloudflare returns TXT record content in RFC 1035 presentation form:
// whitespace-separated double-quoted character-strings, with strings longer
// than 255 bytes split automatically (e.g. `"foo" "bar"`). The real
// cloudflare.dnsClient handles this via CanonicalizeTXT inside mapRecordResponse.
// The in-memory mock (internal/cloudflare/mock) implements cloudflare.DNSClient
// directly and stores/returns logical content with no quoting, so existing
// controller tests never exercise the quoting path.
//
// This file adds:
//   - cfEmulatingDNS: a decorator that wraps the mock DNS client and emulates
//     Cloudflare's on-wire quoting for TXT reads. Toggling Canonicalize on/off
//     proves non-vacuity.
//   - TestQuoteChurn_NonVacuity: the load-bearing proof that WITHOUT
//     canonicalization a steady-state TXT record churns (UpdateRecord called
//     every reconcile), and WITH it the record converges (at most one write).
//   - TestQuote_PlaintextAdoptThroughQuoting: plaintext TXT adoption succeeds
//     when ownership verification sees emulated-quoted content.
//   - TestQuote_AESLargeOwnershipThroughSplit: AES-encoded companion whose
//     v1: envelope exceeds 255 bytes is correctly split by cfEmulatingDNS and
//     reassembled by CanonicalizeTXT, proving the canonicalization layer works.

import (
	"context"
	"strings"
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

// --- Decorator ---

// cfEmulatingDNS wraps a DNSClient and emulates Cloudflare's TXT wire form on
// reads: stored TXT content is returned wrapped in quotes and split into
// <=255-byte character-strings. When Canonicalize is true it then applies
// cloudflare.CanonicalizeTXT, mirroring the real client's mapRecordResponse.
// Toggling Canonicalize proves non-vacuity (off => churn, on => convergence).
//
// Only the two TXT-returning read methods (GetRecord and ListRecordsByNameAndType)
// are intercepted; all write methods delegate verbatim so the mock's call
// counters remain accurate.
type cfEmulatingDNS struct {
	inner        cloudflare.DNSClient
	Canonicalize bool
}

// Compile-time interface assertion: cfEmulatingDNS must implement DNSClient fully.
var _ cloudflare.DNSClient = (*cfEmulatingDNS)(nil)

// cfWire converts logical TXT content to Cloudflare's RFC 1035 presentation
// form: the content is split into <=255-logical-byte chunks, each wrapped in
// double quotes, joined with spaces. Double-quote and backslash characters
// within each chunk are escaped as \" and \\ respectively (RFC 1035 §5.1),
// so that CanonicalizeTXT can correctly decode the result.
//
// Note on chunk size: cfWire splits on the LOGICAL (pre-escape) content at
// 255 bytes. RFC 1035 §3.3's 255-octet limit strictly applies to the on-wire
// ESCAPED character-string, which may be longer than its logical content when
// embedded '"' or '\' are escaped. This is intentionally simplified: the
// operator's only >255-byte TXT payloads are base64 AES v1: envelopes and
// JSON, which contain no '"' or '\' to escape, so a logical-255 chunk equals
// the on-wire escaped length exactly and cfWire faithfully emulates
// Cloudflare's split for the payloads under test.
//
// The \X (non-digit backslash escape) form is used for embedded '"' and '\'
// rather than the RFC 1035 \DDD decimal form because it is simpler to emit
// and is exactly what cloudflare.CanonicalizeTXT decodes at its
// non-digit-escape path (see txt_canonical.go). Both \X and \DDD are valid
// RFC 1035 §5.1 escapes.
func cfWire(content string) string {
	if content == "" {
		return `""`
	}
	var b strings.Builder
	for i := 0; i < len(content); i += 255 { // chunk the LOGICAL content at 255 bytes (see note in cfWire doc)
		end := i + 255
		if end > len(content) {
			end = len(content)
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('"')
		for _, ch := range []byte(content[i:end]) {
			if ch == '"' || ch == '\\' {
				b.WriteByte('\\')
			}
			b.WriteByte(ch)
		}
		b.WriteByte('"')
	}
	return b.String()
}

// applyTXTEmulation wraps a single DNSRecord's Content in Cloudflare
// presentation form, then (optionally) applies CanonicalizeTXT.
func (c *cfEmulatingDNS) applyTXTEmulation(r cloudflare.DNSRecord) cloudflare.DNSRecord {
	if r.Type != "TXT" {
		return r
	}
	wire := cfWire(r.Content)
	if c.Canonicalize {
		r.Content = cloudflare.CanonicalizeTXT(wire)
	} else {
		r.Content = wire
	}
	return r
}

// GetRecord delegates to the inner client and applies TXT emulation to the result.
func (c *cfEmulatingDNS) GetRecord(ctx context.Context, zoneID, recordID string) (*cloudflare.DNSRecord, error) {
	r, err := c.inner.GetRecord(ctx, zoneID, recordID)
	if err != nil {
		return nil, err
	}
	out := c.applyTXTEmulation(*r)
	return &out, nil
}

// ListRecordsByNameAndType delegates to the inner client and applies TXT
// emulation to all returned records.
func (c *cfEmulatingDNS) ListRecordsByNameAndType(ctx context.Context, zoneID, name, recordType string) ([]cloudflare.DNSRecord, error) {
	recs, err := c.inner.ListRecordsByNameAndType(ctx, zoneID, name, recordType)
	if err != nil {
		return nil, err
	}
	out := make([]cloudflare.DNSRecord, len(recs))
	for i, r := range recs {
		out[i] = c.applyTXTEmulation(r)
	}
	return out, nil
}

// CreateRecord delegates verbatim to the inner client (no TXT emulation on writes).
func (c *cfEmulatingDNS) CreateRecord(ctx context.Context, zoneID string, params cloudflare.DNSRecordParams) (*cloudflare.DNSRecord, error) {
	return c.inner.CreateRecord(ctx, zoneID, params)
}

// UpdateRecord delegates verbatim to the inner client (no TXT emulation on writes).
func (c *cfEmulatingDNS) UpdateRecord(ctx context.Context, zoneID, recordID string, params cloudflare.DNSRecordParams) (*cloudflare.DNSRecord, error) {
	return c.inner.UpdateRecord(ctx, zoneID, recordID, params)
}

// DeleteRecord delegates verbatim to the inner client (no TXT emulation).
func (c *cfEmulatingDNS) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	return c.inner.DeleteRecord(ctx, zoneID, recordID)
}

// --- Tests ---

// TestQuoteChurn_NonVacuity is the LOAD-BEARING non-vacuity proof for Bug A.
//
// It runs the reconciler 5 times against a steady-state TXT CloudflareDNSRecord
// (desired content == stored content) under two conditions:
//
//  1. Canonicalize=false: the decorator returns quoted/split content from CF
//     reads, while the reconciler compares against the logical desired value.
//     The mismatch triggers UpdateRecord on every reconcile (churn). The test
//     asserts UpdateRecord is called MORE than once — proving the bug exists
//     without canonicalization and that the test is not vacuous.
//
//  2. Canonicalize=true: the decorator applies CanonicalizeTXT after quoting,
//     giving back the logical value. No drift is detected; UpdateRecord is
//     called at most once.
//
// The non-vacuity gate: withoutFix MUST be > 1. If it is 0 or 1 the harness
// is wired incorrectly (quoting did not reach the drift compare).
func TestQuoteChurn_NonVacuity(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	const (
		zoneID  = "z1"
		recName = "txt.example.com"
		desired = "v=spf1 include:example.com ~all"
	)

	run := func(canonicalize bool) int {
		s := zoneTestScheme(t)
		m := mock.New()
		dec := &cfEmulatingDNS{inner: m.DNS, Canonicalize: canonicalize}

		desiredContent := desired
		rec := &v2alpha1.CloudflareDNSRecord{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "txtrec",
				Namespace:  "default",
				Finalizers: []string{conventions.FinalizerName},
			},
			Spec: v2alpha1.CloudflareDNSRecordSpec{
				Name:    recName,
				Type:    "TXT",
				Content: &desiredContent,
				ZoneID:  zoneID,
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(rec).
			WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
			Build()

		// Seed the mock with the TXT record already at the desired content.
		// True steady state: the ONLY possible UpdateRecord calls are drift-driven
		// (caused by the decorator's quoting mis-matching the logical desired value).
		existing, err := m.DNS.CreateRecord(context.Background(), zoneID, cloudflare.DNSRecordParams{
			Name: recName, Type: "TXT", Content: desired, TTL: 1,
		})
		require.NoError(t, err)

		// Persist Status.RecordID so the reconciler enters the update/drift path
		// immediately (skipping the create branch entirely).
		rec.Status.RecordID = existing.ID
		rec.Status.CurrentContent = desired
		require.NoError(t, fakeClient.Status().Update(context.Background(), rec))

		// We construct the reconciler inline rather than calling newDNSReconciler
		// because newDNSReconciler hardcodes m.DNS as the DNS client and exposes
		// no DNSClient injection seam, so it cannot be reused to inject the
		// cfEmulatingDNS decorator. MAINTENANCE: this struct literal mirrors
		// newDNSReconciler's configuration; if newDNSReconciler gains or changes
		// fields, this literal must be kept in sync or this test will silently
		// exercise a different reconciler config than the rest of the suite.
		reconciler := &CloudflareDNSRecordReconciler{
			Client: fakeClient,
			Scheme: s,
			DNSClientFn: func(_ cloudflare.Credentials) (cloudflare.DNSClient, error) {
				return dec, nil
			},
			IPResolver: ipresolver.NewResolver(),
		}

		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "txtrec", Namespace: "default"}}
		for i := 0; i < 5; i++ {
			_, rerr := reconciler.Reconcile(context.Background(), req)
			require.NoError(t, rerr, "reconcile %d must not error", i)
		}

		calls := m.Calls("DNS.UpdateRecord")
		t.Logf("canonicalize=%v => DNS.UpdateRecord calls: %d", canonicalize, calls)
		return calls
	}

	withoutFix := run(false)
	withFix := run(true)

	// Non-vacuity gate: without canonicalization the decorator's quoting causes
	// needsUpdate to see:
	//   existing.Content = `"v=spf1 include:example.com ~all"` (quoted)
	//   content          = `v=spf1 include:example.com ~all`   (logical)
	// → needsUpdate returns true → UpdateRecord called on every reconcile → churn.
	require.Greater(t, withoutFix, 1,
		"NON-VACUITY GATE: without canonicalization a steady-state TXT record must churn "+
			"(>1 UpdateRecord over 5 reconciles); got %d. "+
			"If 0 or 1: check that cfEmulatingDNS.GetRecord wraps TXT content and "+
			"Status.RecordID was seeded so the reconciler enters the update path.",
		withoutFix)

	require.LessOrEqual(t, withFix, 1,
		"with canonicalization a steady-state TXT record must converge "+
			"(<=1 UpdateRecord over 5 reconciles); got %d",
		withFix)
}

// TestQuote_PlaintextAdoptThroughQuoting verifies that plaintext TXT adoption
// succeeds when the mock DNS client is wrapped by cfEmulatingDNS{Canonicalize:true}.
//
// The decorator emulates Cloudflare returning the companion TXT in quoted form
// (e.g. `"{"v":1,...}"`). With CanonicalizeTXT applied, the reconciler decodes
// the logical JSON correctly, verifyTXTOwnership returns Match, and adoption
// succeeds. Status.RecordID and TxtRecordID are both set.
func TestQuote_PlaintextAdoptThroughQuoting(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	const zoneID = "z1"
	s := zoneTestScheme(t)
	m := mock.New()
	dec := &cfEmulatingDNS{inner: m.DNS, Canonicalize: true}

	// Build an Adopt=true record with the finalizer already set (adoptRec
	// is the helper from dnsrecord_controller_test.go). Default type is A.
	rec := adoptRec("rec", "default", zoneID)

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()

	// Pre-seed the A record and a plaintext TXT companion encoding our identity.
	// seedPlaintextTXT stores bare JSON in the mock; cfEmulatingDNS will wrap
	// it in quotes on read. With Canonicalize:true, CanonicalizeTXT strips the
	// quotes → the decoder sees valid JSON → ownership verified → adoption succeeds.
	//
	// Seed values must match adoptRec's expected record: name test.example.com,
	// content 1.1.1.1, NS default, N rec — confirm against adoptRec in
	// dnsrecord_controller_test.go if it changes.
	aID := seedARecord(t, m, zoneID, "test.example.com", "1.1.1.1")
	txtID := seedPlaintextTXT(t, m, zoneID, "test.example.com", "default", "rec", "")

	reconciler := &CloudflareDNSRecordReconciler{
		Client: fakeClient,
		Scheme: s,
		DNSClientFn: func(_ cloudflare.Credentials) (cloudflare.DNSClient, error) {
			return dec, nil
		},
		IPResolver: ipresolver.NewResolver(),
	}

	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"},
	})
	require.NoError(t, err)

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, fakeClient.Get(context.Background(),
		types.NamespacedName{Name: "rec", Namespace: "default"}, &got))

	require.Equal(t, aID, got.Status.RecordID,
		"adoption must succeed: RecordID must be set to the pre-existing A record")
	require.Equal(t, txtID, got.Status.TxtRecordID,
		"adoption must succeed: TxtRecordID must be set to the companion TXT record")
	require.Equal(t, "cf-txt", got.Status.TxtAffix,
		"TxtAffix must be cf-txt after successful adoption")

	cond := findReadyCondition(got.Status.Conditions)
	require.NotNil(t, cond, "Ready condition must be set")
	require.Equal(t, metav1.ConditionTrue, cond.Status,
		"Ready condition must be True after successful adoption through quoting")
	require.Equal(t, conventions.ReasonReady, cond.Reason,
		"Reason must be ReasonReady after successful adoption through quoting")
}

// TestQuote_AESLargeOwnershipThroughSplit verifies that an AES-encoded TXT
// companion whose v1: envelope exceeds 255 bytes is correctly handled when
// cfEmulatingDNS emulates Cloudflare's quoting/splitting.
//
// The AES-encoded content (`v1:<nonce>:<ciphertext>`) for a sufficiently large
// payload exceeds 255 bytes. cfEmulatingDNS.cfWire splits it into multiple
// <=255-byte quoted chunks joined by spaces. CanonicalizeTXT strips the quotes
// and reassembles the chunks into the original `v1:...` string.
//
// This test proves the canonicalization layer at the codec level:
//   - cfWire(encoded) produces multiple quoted chunks (>2 quotes in the result)
//   - CanonicalizeTXT(wired) == encoded  (round-trip identity)
//   - autoDetectingCodec.Decode(reassembled) succeeds and returns correct ownership
//   - autoDetectingCodec.Decode(wired) fails with ErrUnrecognizedCodec
//     (proves canonicalization is required — test is non-vacuous)
func TestQuote_AESLargeOwnershipThroughSplit(t *testing.T) {
	// Build a 32-byte key and AES codec.
	var key [32]byte
	copy(key[:], "test-key-for-quote-integration!!")
	aesCodec := cloudflare.NewAESCodec(key)

	// Use long namespace/name to push the AES payload past 255 bytes.
	// A v1: envelope for a ~60-byte JSON body is ~125 bytes; adding an 80-char
	// namespace reliably pushes the encrypted ciphertext past 255 bytes.
	longNS := strings.Repeat("x", 80)
	longN := strings.Repeat("y", 80)

	encoded, err := aesCodec.Encode(cloudflare.RegistryPayload{
		V: 1, K: "CloudflareDNSRecord",
		NS: longNS, N: longN,
		H: sha256HexFor("1.1.1.1"),
	})
	require.NoError(t, err)
	require.Greater(t, len(encoded), 255,
		"AES payload must exceed 255 bytes to exercise the split path (got %d bytes)", len(encoded))
	t.Logf("AES TXT payload length: %d bytes (>255 ✓)", len(encoded))

	// Emulate Cloudflare's on-wire quoting/splitting.
	wired := cfWire(encoded)
	// each cfWire chunk contributes exactly two '"' characters, so >2 proves the
	// >255-byte envelope was split into at least two character-strings (base64
	// AES output contains no literal '"').
	require.Greater(t, strings.Count(wired, `"`), 2,
		"cfWire must produce multiple quoted chunks for a >255-byte payload")

	// Load-bearing: CanonicalizeTXT must reassemble the split chunks back to
	// the original v1: envelope.
	reassembled := cloudflare.CanonicalizeTXT(wired)
	require.Equal(t, encoded, reassembled,
		"CanonicalizeTXT must reassemble split chunks back to the original v1: envelope")

	// The auto-detecting codec must decode the reassembled content correctly.
	autoCodec := cloudflare.NewAutoDetectingCodec(aesCodec)
	payload, derr := autoCodec.Decode(reassembled)
	require.NoError(t, derr,
		"auto-detecting codec must decode the reassembled AES payload without error")
	require.Equal(t, longNS, payload.NS,
		"decoded namespace must match the original (ownership verification would pass)")
	require.Equal(t, longN, payload.N,
		"decoded name must match the original (ownership verification would pass)")

	// Non-vacuity: without canonicalization (raw wire form with quotes), the
	// decoder fails because the leading `"` breaks the `v1:` prefix check.
	_, badErr := autoCodec.Decode(wired)
	require.Error(t, badErr,
		"decoder must fail on raw wire form (with leading quote); "+
			"this proves CanonicalizeTXT is required — the test is non-vacuous")
}
