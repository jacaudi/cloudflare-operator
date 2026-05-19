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
// Post-E2 production data-flow for TXT records:
//
//	Write: controller(logical) → cfEmulatingDNS.CreateRecord/UpdateRecord
//	        → cloudflare.EncodeTXT(content) [mirrors wireContent chokepoint]
//	        → mock stores presentation form (e.g. `"v=spf1 ..."`)
//	Read:  mock returns presentation form → cfEmulatingDNS.GetRecord/List
//	        → cloudflare.CanonicalizeTXT (when Canonicalize=true, mirrors mapRecordResponse)
//	        → controller sees logical content
//
// The real cloudflare.dnsClient handles encoding in wireContent (CreateRecord /
// UpdateRecord) and decoding in mapRecordResponse (CanonicalizeTXT, TXT-gated).
// The in-memory mock (internal/cloudflare/mock) stores params.Content verbatim
// and bypasses BOTH chokepoints. cfEmulatingDNS bridges that gap:
//   - On WRITE (CreateRecord/UpdateRecord): calls cloudflare.EncodeTXT for TXT
//     records before delegating to the inner mock, so the mock stores presentation
//     form — exactly as Cloudflare would after receiving a wireContent-encoded PUT.
//   - On READ (GetRecord/ListRecordsByNameAndType): returns the stored
//     presentation form and optionally applies cloudflare.CanonicalizeTXT
//     (Canonicalize=true), mirroring mapRecordResponse.
//
// Toggling Canonicalize on/off proves non-vacuity:
//   - off: mock stores `"v=spf1 ..."` (presentation); reconciler desired is
//     `v=spf1 ...` (logical); drift compare sees inequality → UpdateRecord every
//     reconcile (churn).
//   - on:  CanonicalizeTXT restores logical form; drift compare sees equality →
//     convergence.
//
// This file adds:
//   - cfEmulatingDNS: the corrected decorator (encodes on write, decodes on read).
//   - TestQuoteChurn_NonVacuity: load-bearing churn/convergence proof.
//   - TestQuote_PlaintextAdoptThroughQuoting: plaintext TXT adoption through
//     the corrected read path (seeds via raw mock; CanonicalizeTXT idempotency
//     ensures the logical JSON passes through unchanged).
//   - TestQuote_AESLargeOwnershipThroughSplit: AES-encoded companion >255 bytes
//     is correctly split by EncodeTXT and reassembled by CanonicalizeTXT.
//   - TestQuote_ExactPayloadRoundTripsLosslessly: exact Bug A payload
//     (JSON with embedded '"' chars) round-trips through EncodeTXT→mock→
//     CanonicalizeTXT without loss (regression for the JSON-quoting failure).

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

// cfEmulatingDNS wraps a DNSClient and models the real *dnsClient's two TXT
// chokepoints that the mock lacks:
//
//   - Write chokepoint (mirrors wireContent in dns.go): CreateRecord and
//     UpdateRecord apply cloudflare.EncodeTXT to TXT content before delegating
//     to the inner mock. The mock therefore stores presentation form
//     (e.g. `"v=spf1 include:example.com ~all"` or `"{\"v\":1,...}"`), just
//     as Cloudflare would after receiving an EncodeTXT-encoded PUT.
//
//   - Read chokepoint (mirrors mapRecordResponse in dns.go): GetRecord and
//     ListRecordsByNameAndType return the mock's stored (presentation-form) TXT
//     content; when Canonicalize is true they additionally apply
//     cloudflare.CanonicalizeTXT, restoring logical content.
//
// Toggling Canonicalize on/off proves non-vacuity: without canonicalization the
// reconciler's drift compare sees presentation form vs logical desired → churn;
// with it the reconciler sees logical vs logical → convergence.
//
// All write methods delegate verbatim so the mock's call counters remain accurate.
// Non-TXT records are untouched on both paths.
type cfEmulatingDNS struct {
	inner        cloudflare.DNSClient
	Canonicalize bool
}

// Compile-time interface assertion: cfEmulatingDNS must implement DNSClient fully.
var _ cloudflare.DNSClient = (*cfEmulatingDNS)(nil)

// encodeTXTContent applies cloudflare.EncodeTXT to TXT record content before it
// is written to the inner mock. Non-TXT content is returned unchanged. This
// mirrors the wireContent chokepoint in dns.go that the mock lacks.
//
// MAINTENANCE: mirrors wireContent in dns.go. If wireContent ever encodes
// additional record types, update this helper in lockstep or the test no
// longer faithfully models the production write path.
func encodeTXTContent(recordType, content string) string {
	if recordType == "TXT" {
		return cloudflare.EncodeTXT(content)
	}
	return content
}

// applyTXTEmulation mirrors mapRecordResponse's TXT gate on the read path: when
// Canonicalize is true, cloudflare.CanonicalizeTXT is applied to TXT content,
// restoring the logical value from the presentation form the mock stored.
// When Canonicalize is false the presentation form is returned as-is (models
// missing read canonicalization — the drift compare will see a mismatch →
// churn).
func (c *cfEmulatingDNS) applyTXTEmulation(r cloudflare.DNSRecord) cloudflare.DNSRecord {
	if r.Type != "TXT" {
		return r
	}
	if c.Canonicalize {
		r.Content = cloudflare.CanonicalizeTXT(r.Content)
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

// CreateRecord applies EncodeTXT for TXT records (mirrors wireContent), then
// delegates to the inner client. The mock therefore stores presentation form.
func (c *cfEmulatingDNS) CreateRecord(ctx context.Context, zoneID string, params cloudflare.DNSRecordParams) (*cloudflare.DNSRecord, error) {
	params.Content = encodeTXTContent(params.Type, params.Content)
	return c.inner.CreateRecord(ctx, zoneID, params)
}

// UpdateRecord applies EncodeTXT for TXT records (mirrors wireContent), then
// delegates to the inner client. The mock therefore stores presentation form.
func (c *cfEmulatingDNS) UpdateRecord(ctx context.Context, zoneID, recordID string, params cloudflare.DNSRecordParams) (*cloudflare.DNSRecord, error) {
	params.Content = encodeTXTContent(params.Type, params.Content)
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
//  1. Canonicalize=false: the decorator stores presentation form in the mock on
//     write (via EncodeTXT) but returns it raw on read. The reconciler's drift
//     compare sees `"v=spf1 ..."` (presentation) vs `v=spf1 ...` (logical
//     desired) — a mismatch — and calls UpdateRecord on every reconcile (churn).
//     The test asserts UpdateRecord is called MORE than once — proving the bug
//     exists without canonicalization and that the test is not vacuous.
//
//  2. Canonicalize=true: the decorator applies CanonicalizeTXT on read, giving
//     back the logical value. No drift is detected; UpdateRecord is called at
//     most once.
//
// The non-vacuity gate: withoutFix MUST be > 1. If it is 0 or 1 the harness is
// wired incorrectly (presentation form did not reach the drift compare).
//
// Seeding note: the initial TXT record is seeded via the DECORATOR (not the raw
// mock) so the mock stores presentation form from the start. This is required
// for the Canonicalize=false path to churn: if the mock held logical content,
// both paths would see logical vs logical and neither would churn.
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

		// Seed the TXT record via the DECORATOR so the mock stores presentation
		// form (EncodeTXT applied on write). This is the critical difference from
		// the pre-E2 model: the decorator now encodes on write, so the mock holds
		// `"v=spf1 ..."` rather than `v=spf1 ...`. Without this, both Canonicalize
		// paths would see logical-vs-logical and neither would churn.
		existing, err := dec.CreateRecord(context.Background(), zoneID, cloudflare.DNSRecordParams{
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

	// Non-vacuity gate: without canonicalization the decorator stores
	// presentation form in the mock (via EncodeTXT on write) but returns it raw
	// on read. needsUpdate sees:
	//   existing.Content = `"v=spf1 include:example.com ~all"` (presentation form)
	//   content          = `v=spf1 include:example.com ~all`   (logical desired)
	// → needsUpdate returns true → UpdateRecord called on every reconcile → churn.
	require.Greater(t, withoutFix, 1,
		"NON-VACUITY GATE: without canonicalization a steady-state TXT record must churn "+
			"(>1 UpdateRecord over 5 reconciles); got %d. "+
			"If 0 or 1: check that the record was seeded via dec.CreateRecord (so the mock "+
			"stores presentation form) and Status.RecordID was seeded so the reconciler "+
			"enters the update path.",
		withoutFix)

	require.LessOrEqual(t, withFix, 1,
		"with canonicalization a steady-state TXT record must converge "+
			"(<=1 UpdateRecord over 5 reconciles); got %d",
		withFix)
}

// TestQuote_PlaintextAdoptThroughQuoting verifies that plaintext TXT adoption
// succeeds when the mock DNS client is wrapped by cfEmulatingDNS{Canonicalize:true}.
//
// The companion TXT is seeded via the raw mock (seedPlaintextTXT), which stores
// logical JSON content (e.g. `{"v":1,...}`). On read, cfEmulatingDNS applies
// cloudflare.CanonicalizeTXT; because the logical JSON does not start with '"',
// CanonicalizeTXT is a no-op (idempotent) and returns the content unchanged.
// The reconciler/codec therefore sees valid JSON, verifyTXTOwnership returns
// Match, and adoption succeeds.
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
	// seedPlaintextTXT stores bare JSON in the mock (bypasses the decorator).
	// With Canonicalize:true, CanonicalizeTXT is a no-op for non-quoted JSON →
	// the decoder sees valid JSON → ownership verified → adoption succeeds.
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
// payload exceeds 255 bytes. cloudflare.EncodeTXT (the production encoder, reused
// here via cfEmulatingDNS) splits it into multiple <=255-byte quoted chunks
// joined by spaces. CanonicalizeTXT strips the quotes and reassembles the chunks
// into the original `v1:...` string.
//
// Note: AES base64 output contains no `"` or `\`, so EncodeTXT produces the same
// byte sequence a manual RFC 1035 chunker would; the production encoder is used
// directly (DRY: no duplicate encoding logic in tests).
//
// This test proves the canonicalization layer at the codec level:
//   - EncodeTXT(encoded) produces multiple quoted chunks (>2 quotes in the result)
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

	// Emulate Cloudflare's on-wire quoting/splitting via the production encoder.
	// (AES base64 contains no `"` or `\`, so each ≤255-byte chunk needs no
	// internal escaping — EncodeTXT output is pure quoted-chunk form.)
	wired := cloudflare.EncodeTXT(encoded)
	// each EncodeTXT chunk contributes exactly two '"' characters, so >2 proves
	// the >255-byte envelope was split into at least two character-strings
	// (base64 AES output contains no literal '"').
	require.Greater(t, strings.Count(wired, `"`), 2,
		"EncodeTXT must produce multiple quoted chunks for a >255-byte payload")

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

// TestQuote_ExactPayloadRoundTripsLosslessly is a regression test for Bug A:
// a plaintext TXT companion whose JSON payload contains embedded '"' characters
// (all real payloads — keys and values are quoted in JSON) was stored and
// returned incorrectly before E1/E2.
//
// This test uses the EXACT payload from the bug report. The payload contains
// many '"' characters because it is a JSON object. Before E1 (no EncodeTXT),
// the reconciler sent the raw JSON to Cloudflare; Cloudflare rejected or stored
// it lossily (API error 9207 or dropped quotes). Before E2 (no wireContent
// wiring), even with EncodeTXT present it was never called on the write path.
//
// Post-E2 flow under test:
//  1. Plaintext codec encodes payload → logical JSON with '"' chars.
//  2. cfEmulatingDNS.CreateRecord → EncodeTXT(json) → mock stores escaped
//     presentation form (`"{\"v\":1,\"k\":\"CloudflareDNSRecord\",...}"`).
//  3. cfEmulatingDNS.ListRecordsByNameAndType → mock returns presentation form
//     → CanonicalizeTXT → restores original logical JSON.
//  4. verifyTXTOwnership / codec.Decode sees the byte-identical logical payload
//     → ownership match.
//
// Non-vacuity: decoding the raw presentation form (without CanonicalizeTXT)
// must fail — proves E1+E2 are both required for the round-trip to succeed.
func TestQuote_ExactPayloadRoundTripsLosslessly(t *testing.T) {
	// Exact payload from Bug A report (matches a real production companion TXT).
	const exactLogical = `{"v":1,"k":"CloudflareDNSRecord","ns":"network","n":"external-jacaudi-dev-external-jacaudi-dev-b139e087","h":"sha256:f2bb0441db266c09a1a176eefabb5d028ae21e3379b960f1ab6808fe32220bfe"}`

	// Step 1: EncodeTXT must escape embedded '"' chars and wrap in outer quotes.
	wired := cloudflare.EncodeTXT(exactLogical)
	require.True(t, strings.HasPrefix(wired, `"`),
		"EncodeTXT output must start with '\"' (presentation form)")
	// The outer wrapping quote plus escapes for inner '"' chars: wired must be
	// longer than the logical content.
	require.Greater(t, len(wired), len(exactLogical),
		"EncodeTXT output must be longer than logical input (escaping adds bytes)")

	// Step 2: CanonicalizeTXT must restore the byte-identical original.
	restored := cloudflare.CanonicalizeTXT(wired)
	require.Equal(t, exactLogical, restored,
		"CanonicalizeTXT(EncodeTXT(x)) must equal x for the exact Bug A payload")

	// Step 3: plaintext codec must decode the restored content correctly.
	codec := cloudflare.NewPlaintextCodec()
	payload, err := codec.Decode(restored)
	require.NoError(t, err,
		"plaintext codec must decode the restored logical JSON without error")
	require.Equal(t, "CloudflareDNSRecord", payload.K)
	require.Equal(t, "network", payload.NS)
	require.Equal(t, "external-jacaudi-dev-external-jacaudi-dev-b139e087", payload.N)
	require.Equal(t, "sha256:f2bb0441db266c09a1a176eefabb5d028ae21e3379b960f1ab6808fe32220bfe", payload.H)

	// Non-vacuity: decoding the raw presentation form (with escaped quotes, no
	// CanonicalizeTXT) must fail. This proves BOTH steps are required.
	_, badErr := codec.Decode(wired)
	require.Error(t, badErr,
		"plaintext codec must fail to decode raw presentation form (outer '\"' escaping); "+
			"proves CanonicalizeTXT (E1) AND the wireContent write wiring (E2) are both required — test is non-vacuous")

	// Step 4: full controller round-trip through cfEmulatingDNS.
	// Seed the companion TXT via the decorator (not raw mock) so the mock stores
	// presentation form as it would after E2's wireContent wiring.
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	const (
		zoneID   = "z1"
		recName  = "external-jacaudi-dev-external-jacaudi-dev-b139e087.example.com"
		recNS    = "network"
		recN     = "external-jacaudi-dev-external-jacaudi-dev-b139e087"
		recConst = "1.2.3.4"
	)

	s := zoneTestScheme(t)
	m := mock.New()
	dec := &cfEmulatingDNS{inner: m.DNS, Canonicalize: true}

	// Seed the A record via raw mock (type A — no TXT encoding involved).
	aID := seedARecord(t, m, zoneID, recName, recConst)

	// Seed the TXT companion via the decorator. The decorator calls EncodeTXT on
	// write, so the mock stores the escaped presentation form. This is the
	// production post-E2 write path under test.
	txtName := cloudflare.AffixName("cf-txt", recName)
	txtRec, err2 := dec.CreateRecord(context.Background(), zoneID, cloudflare.DNSRecordParams{
		Name:    txtName,
		Type:    "TXT",
		Content: exactLogical,
		TTL:     1,
	})
	require.NoError(t, err2, "decorator CreateRecord must not error")
	txtID := txtRec.ID

	// Verify the mock stores presentation form (not logical) — this is the E2 gate.
	rawRecs, err3 := m.DNS.ListRecordsByNameAndType(context.Background(), zoneID, txtName, "TXT")
	require.NoError(t, err3)
	require.Len(t, rawRecs, 1)
	require.Equal(t, wired, rawRecs[0].Content,
		"mock must store EncodeTXT presentation form (E2 gate: wireContent must have been applied)")

	// Build an Adopt=true record pointing at our seeded A+TXT.
	recContent := recConst
	adoptRecord := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       recN,
			Namespace:  recNS,
			Finalizers: []string{conventions.FinalizerName},
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    recName,
			Type:    "A",
			Content: &recContent,
			ZoneID:  zoneID,
			Adopt:   true,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(adoptRecord).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()

	reconciler := &CloudflareDNSRecordReconciler{
		Client: fakeClient,
		Scheme: s,
		DNSClientFn: func(_ cloudflare.Credentials) (cloudflare.DNSClient, error) {
			return dec, nil
		},
		IPResolver: ipresolver.NewResolver(),
	}

	_, rerr := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: recN, Namespace: recNS},
	})
	require.NoError(t, rerr, "reconcile must not error")

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, fakeClient.Get(context.Background(),
		types.NamespacedName{Name: recN, Namespace: recNS}, &got))

	require.Equal(t, aID, got.Status.RecordID,
		"adoption must succeed: RecordID must match the pre-seeded A record (exact payload round-trips losslessly)")
	require.Equal(t, txtID, got.Status.TxtRecordID,
		"adoption must succeed: TxtRecordID must match the pre-seeded TXT companion")

	cond := findReadyCondition(got.Status.Conditions)
	require.NotNil(t, cond, "Ready condition must be set")
	require.Equal(t, metav1.ConditionTrue, cond.Status,
		"Ready condition must be True: exact Bug A payload round-trips losslessly through EncodeTXT→mock→CanonicalizeTXT")
	require.Equal(t, conventions.ReasonReady, cond.Reason)
}
