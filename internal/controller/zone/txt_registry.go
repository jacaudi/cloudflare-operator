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

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/reconcile"
)

// ErrTxtRegistryKeyMalformed is returned by loadCodec when the Secret's key
// data is present but not exactly 32 bytes, which is required for AES-256-GCM.
var ErrTxtRegistryKeyMalformed = errors.New("txt registry key must be exactly 32 bytes")

// txtAffix is the prefix used to derive TXT companion record names via
// cloudflare.AffixName. It identifies records written by this operator.
const txtAffix = "cf-txt"

// TxtOwnership classifies the ownership relationship between a TXT companion
// record and the CloudflareDNSRecord that queries it.
type TxtOwnership int

const (
	// TxtOwnershipMatch means the TXT record was written by this exact CR.
	TxtOwnershipMatch TxtOwnership = iota
	// TxtOwnershipForeign means the TXT record exists but belongs to a
	// different CR (different namespace/name or kind).
	TxtOwnershipForeign
	// TxtOwnershipUnrecognized means the TXT record content could not be
	// decoded by the configured codec.
	TxtOwnershipUnrecognized
)

// loadCodec resolves the codec to use for TXT companion records. When keyRef
// is nil or has an empty Name, the plaintext codec is returned. Otherwise the
// referenced Secret is fetched; if absent the error wraps ErrSecretNotFound,
// if the named key is missing it wraps ErrSecretKeyMissing, and if the key
// material is not exactly 32 bytes it wraps ErrTxtRegistryKeyMalformed.
//
// The key name inside the Secret defaults to "key" (not "token") when
// keyRef.Key is empty — the TXT key Secret uses a different default than the
// credential Secret.
func loadCodec(ctx context.Context, c client.Client, keyRef *v2alpha1.SecretReference, defaultNamespace string) (cloudflare.Codec, error) {
	if keyRef == nil || keyRef.Name == "" {
		return cloudflare.NewPlaintextCodec(), nil
	}

	ns := keyRef.Namespace
	if ns == "" {
		ns = defaultNamespace
	}
	keyName := keyRef.Key
	if keyName == "" {
		keyName = "key"
	}

	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Name: keyRef.Name, Namespace: ns}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("txt registry key Secret %s/%s: %w", ns, keyRef.Name, cloudflare.ErrSecretNotFound)
		}
		return nil, fmt.Errorf("txt registry key Secret %s/%s: %w", ns, keyRef.Name, err)
	}

	raw, ok := secret.Data[keyName]
	if !ok {
		return nil, fmt.Errorf("txt registry key Secret %s/%s missing key %q: %w", ns, keyRef.Name, keyName, cloudflare.ErrSecretKeyMissing)
	}

	if len(raw) != 32 {
		return nil, fmt.Errorf("txt registry key Secret %s/%s key %q has %d bytes: %w", ns, keyRef.Name, keyName, len(raw), ErrTxtRegistryKeyMalformed)
	}

	var key [32]byte
	copy(key[:], raw)
	return cloudflare.NewAESCodec(key), nil
}

// autoDetectingFor returns a read-side codec that can decode both plaintext
// and encrypted TXT records. It wraps the configured encoder so that existing
// records written with either format can be read back correctly.
func autoDetectingFor(encoder cloudflare.Codec) cloudflare.Codec {
	// NewAutoDetectingCodec already type-asserts internally: an aesCodec
	// enables the v1: AES branch, anything else (plaintext) yields a
	// plaintext-only read-side decoder that refuses v1: input. Passing the
	// configured encoder straight through avoids a brittle Kind() string
	// compare and keeps the read codec consistent with the write codec.
	return cloudflare.NewAutoDetectingCodec(encoder)
}

// verifyTXTOwnership decodes a TXT companion record's content using the
// supplied codec and classifies ownership relative to the caller's identity.
func verifyTXTOwnership(txtContent string, codec cloudflare.Codec, ourKind, ourNS, ourName string) TxtOwnership {
	p, err := codec.Decode(txtContent)
	if err != nil {
		return TxtOwnershipUnrecognized
	}
	if p.K == ourKind && p.NS == ourNS && p.N == ourName {
		return TxtOwnershipMatch
	}
	return TxtOwnershipForeign
}

// writeTXTCompanion creates or updates the TXT companion record for the given
// DNS record name in Cloudflare. It encodes a RegistryPayload with the
// provided identity fields and content hash, then upserts the record under the
// affixed name. The ID of the resulting TXT record is returned.
func writeTXTCompanion(
	ctx context.Context,
	dc cloudflare.DNSClient,
	zoneID, recordName, contentHash string,
	ourNS, ourName string,
	encoder cloudflare.Codec,
) (string, error) {
	txtName := cloudflare.AffixName(txtAffix, recordName)

	payload := cloudflare.RegistryPayload{
		V:  1,
		K:  "CloudflareDNSRecord",
		NS: ourNS,
		N:  ourName,
		H:  contentHash,
	}
	content, err := encoder.Encode(payload)
	if err != nil {
		return "", fmt.Errorf("writeTXTCompanion encode: %w", err)
	}

	existing, err := dc.ListRecordsByNameAndType(ctx, zoneID, txtName, "TXT")
	if err != nil {
		return "", fmt.Errorf("writeTXTCompanion list: %w", err)
	}

	if len(existing) > 0 {
		updated, err := dc.UpdateRecord(ctx, zoneID, existing[0].ID, cloudflare.DNSRecordParams{
			Type:    "TXT",
			Name:    txtName,
			Content: content,
			TTL:     1,
		})
		if err != nil {
			return "", fmt.Errorf("writeTXTCompanion update: %w", err)
		}
		return updated.ID, nil
	}

	created, err := dc.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
		Type:    "TXT",
		Name:    txtName,
		Content: content,
		TTL:     1,
	})
	if err != nil {
		return "", fmt.Errorf("writeTXTCompanion create: %w", err)
	}
	return created.ID, nil
}

// deleteTXTCompanion removes the TXT companion record with the given Cloudflare
// record ID. An empty txtRecordID is treated as a no-op. ErrRecordNotFound is
// tolerated (the record may already have been removed externally).
func deleteTXTCompanion(ctx context.Context, dc cloudflare.DNSClient, zoneID, txtRecordID string) error {
	if txtRecordID == "" {
		return nil
	}
	if err := dc.DeleteRecord(ctx, zoneID, txtRecordID); err != nil {
		if errors.Is(err, cloudflare.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("deleteTXTCompanion: %w", err)
	}
	return nil
}

// companionInputs is the parameter bag for reconcileTXTCompanion.
type companionInputs struct {
	recordName  string // rec.Spec.Name (the public hostname)
	contentHash string // sha256Hex(content)
	ourNS       string // rec.Namespace
	ourName     string // rec.Name
	storedTxtID string // rec.Status.TxtRecordID (may be stale/empty)
	encoder     cloudflare.Codec
	readCodec   cloudflare.Codec
}

// companionOutcome is the result of one companion reconcile pass.
type companionOutcome struct {
	txtRecordID string // set when the companion is ours and present
	ownershipOK bool   // true ⇒ companion is in the desired state
	failClass   string // "" | reconcile.ClassNameMiss | reconcile.ClassForeign | reconcile.ClassUndecodable | "cf-create" | "cf-update"
}

// reconcileTXTCompanion converges the TXT ownership companion against ACTUAL
// Cloudflare state every reconcile (S1 design §4.2, composed strategy):
//
//  1. ID-first: if in.storedTxtID is set, GetRecord(ID). ErrRecordNotFound ⇒
//     treat as absent (out-of-band delete — sub-bug a). Other errors propagate
//     (transient).
//  2. Fallback: ID empty / 404 ⇒ ListRecordsByNameAndType(zoneCorrectName,TXT)
//     using the post-T1 AffixName scheme (cf-txt.<host>). Returns the first
//     match or nil.
//  3. Classify via verifyTXTOwnership:
//     absent      → CreateRecord; on CF 81058 ⇒ re-list + re-classify
//     (never a hard error — sub-bug b).
//     Match       → if payload-hash drift: UpdateRecord; set TxtRecordID.
//     Foreign     → REFUSE (no write); ownershipOK=false, failClass=ClassForeign.
//     Unrecognized→ REFUSE (no write); ownershipOK=false, failClass=ClassUndecodable.
//
// Anti-hijack invariant (P5 design Q2): NEVER writes a TXT companion for a
// record this CR cannot prove it owns.
func reconcileTXTCompanion(ctx context.Context, dc cloudflare.DNSClient, zoneID string, in companionInputs) (companionOutcome, error) {
	txtName := cloudflare.AffixName(txtAffix, in.recordName)

	// Step 1+2: resolve the live companion (ID-first, name-list fallback).
	found, err := resolveCompanion(ctx, dc, zoneID, in.storedTxtID, txtName)
	if err != nil {
		return companionOutcome{}, err
	}

	desired := cloudflare.RegistryPayload{
		V: 1, K: "CloudflareDNSRecord", NS: in.ourNS, N: in.ourName, H: in.contentHash,
	}
	content, eerr := in.encoder.Encode(desired)
	if eerr != nil {
		return companionOutcome{}, fmt.Errorf("reconcileTXTCompanion encode: %w", eerr)
	}

	// Step 3: classify + converge.
	if found != nil {
		switch verifyTXTOwnership(found.Content, in.readCodec, "CloudflareDNSRecord", in.ourNS, in.ourName) {
		case TxtOwnershipMatch:
			if p, derr := in.readCodec.Decode(found.Content); derr != nil || p.H != in.contentHash {
				upd, uerr := dc.UpdateRecord(ctx, zoneID, found.ID, cloudflare.DNSRecordParams{
					Type: "TXT", Name: txtName, Content: content, TTL: 1,
				})
				if uerr != nil {
					return companionOutcome{failClass: "cf-update"}, fmt.Errorf("companion update: %w", uerr)
				}
				return companionOutcome{txtRecordID: upd.ID, ownershipOK: true}, nil
			}
			return companionOutcome{txtRecordID: found.ID, ownershipOK: true}, nil
		case TxtOwnershipForeign:
			return companionOutcome{ownershipOK: false, failClass: reconcile.ClassForeign}, nil
		default: // TxtOwnershipUnrecognized
			return companionOutcome{ownershipOK: false, failClass: reconcile.ClassUndecodable}, nil
		}
	}

	// Absent → create. CF 81058 ⇒ re-list + re-classify (never a hard error).
	created, cerr := dc.CreateRecord(ctx, zoneID, cloudflare.DNSRecordParams{
		Type: "TXT", Name: txtName, Content: content, TTL: 1,
	})
	if cerr != nil {
		if !isAlreadyExistsErr(cerr) {
			return companionOutcome{failClass: "cf-create"}, fmt.Errorf("companion create: %w", cerr)
		}
		// "Already exists" on create ⇒ another reconcile / external party
		// just created it; re-list + re-classify rather than erroring.
		list, lerr := dc.ListRecordsByNameAndType(ctx, zoneID, txtName, "TXT")
		if lerr != nil {
			return companionOutcome{}, fmt.Errorf("companion relist after exists: %w", lerr)
		}
		if len(list) == 0 {
			// CF claims the record exists but our exact-name list can't see it
			// (the original external-host failure mode pre-T1, now a name-miss
			// rather than an infinite loop).
			return companionOutcome{ownershipOK: false, failClass: reconcile.ClassNameMiss}, nil
		}
		switch verifyTXTOwnership(list[0].Content, in.readCodec, "CloudflareDNSRecord", in.ourNS, in.ourName) {
		case TxtOwnershipMatch:
			return companionOutcome{txtRecordID: list[0].ID, ownershipOK: true}, nil
		case TxtOwnershipForeign:
			return companionOutcome{ownershipOK: false, failClass: reconcile.ClassForeign}, nil
		default:
			return companionOutcome{ownershipOK: false, failClass: reconcile.ClassUndecodable}, nil
		}
	}
	return companionOutcome{txtRecordID: created.ID, ownershipOK: true}, nil
}

// resolveCompanion returns the live companion record from Cloudflare (or nil
// if absent). ID-first: if storedTxtID is set, GetRecord(ID); ErrRecordNotFound
// ⇒ fall back to name-list. Other errors propagate (transient).
func resolveCompanion(ctx context.Context, dc cloudflare.DNSClient, zoneID, storedTxtID, txtName string) (*cloudflare.DNSRecord, error) {
	if storedTxtID != "" {
		rec, err := dc.GetRecord(ctx, zoneID, storedTxtID)
		if err == nil {
			return rec, nil
		}
		if !errors.Is(err, cloudflare.ErrRecordNotFound) {
			return nil, err // transient
		}
		// Stale ID; fall through to name-list.
	}
	list, err := dc.ListRecordsByNameAndType(ctx, zoneID, txtName, "TXT")
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	r := list[0]
	return &r, nil
}

// isAlreadyExistsErr reports whether err is a Cloudflare "record already
// exists" error (81058 for TXT, 81053 for A/AAAA/CNAME). Matched on the
// documented numeric codes embedded in the wrapped error string; robust to
// error wrapping.
func isAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "81058") || strings.Contains(s, "81053")
}

// legacyAffixName reproduces the PRE-S1 AffixName scheme exactly: prefix +
// "." for apex, prefix + "-" + dash-joined-all-but-last-label + "." + last-
// label for subdomains; per-label wildcard sanitize. This is FROZEN — used
// ONLY to locate and best-effort GC the operator's own stale legacy-named
// companions written by older builds. Do NOT "fix" or simplify; its purpose
// is to reproduce historical output byte-for-byte. The current scheme is
// cloudflare.AffixName (T1, prefix-as-leftmost-label).
func legacyAffixName(prefix, name string) string {
	// sanitize maps "*" → "_wildcard", mirroring the pre-S1 sanitizeLabel
	// (unexported in the cloudflare package; inlined here to stay self-contained).
	sanitize := func(label string) string {
		if label == "*" {
			return "_wildcard"
		}
		return label
	}
	if !strings.ContainsRune(name, '.') {
		return prefix + "." + sanitize(name)
	}
	segs := strings.Split(name, ".")
	for i, s := range segs {
		segs[i] = sanitize(s)
	}
	head := strings.Join(segs[:len(segs)-1], "-")
	return prefix + "-" + head + "." + segs[len(segs)-1]
}

// gcLegacyCompanion best-effort deletes a stale legacy-named TXT companion
// IFF its decoded payload proves it is THIS record's own (kind / namespace /
// name match — design §4.4). The caller is responsible for ensuring the
// correctly-named replacement is already present before invoking this.
//
// Returns (legacyFound, err). legacyFound==true indicates that at least one
// legacy companion existed for this record (ownership-verified against our
// kind/namespace/name). The caller uses this to decide whether to stamp the
// per-CR ack (Status.LegacyCompanionGCDone). legacyFound is set as soon as
// the helper finds an ownership-verified hit, BEFORE attempting the delete;
// so both the "deleted successfully" and "delete failed" paths return
// legacyFound==true. Records that are present but do not pass ownership
// verification (foreign/undecodable) leave legacyFound==false — they are not
// ours to GC. If legacyFound==true AND err!=nil, the delete failed; the
// caller MUST NOT stamp the ack so the next reconcile retries.
//
// zoneDomain "" models the literal-Spec.ZoneID path (no CloudflareZone CR
// available to resolve the domain) — in that case we cannot construct the
// zone-appended FQDN that Cloudflare stored under, so we skip silently. This
// is the documented "literal-Spec.ZoneID legacy orphan stays" trade-off.
//
// Never deletes foreign / undecodable records.
func gcLegacyCompanion(ctx context.Context, dc cloudflare.DNSClient, zoneID, zoneDomain, recordName, ourNS, ourName string, readCodec cloudflare.Codec) (legacyFound bool, err error) {
	if zoneDomain == "" {
		return false, nil
	}
	newName := cloudflare.AffixName(txtAffix, recordName)
	legacy := legacyAffixName(txtAffix, recordName)

	// Cloudflare stores a non-zone-suffixed POST zone-appended; we try both
	// the bare legacy name (idempotent when it happens to already end in the
	// zone) and the zone-appended FQDN (the prod-incident case).
	candidates := []string{legacy, legacy + "." + zoneDomain}
	for _, cand := range candidates {
		if cand == newName {
			continue // never touch the current-scheme companion
		}
		recs, lerr := dc.ListRecordsByNameAndType(ctx, zoneID, cand, "TXT")
		if lerr != nil {
			continue // best-effort on List: skip this candidate
		}
		for _, r := range recs {
			if verifyTXTOwnership(r.Content, readCodec, "CloudflareDNSRecord", ourNS, ourName) == TxtOwnershipMatch {
				// Set legacyFound BEFORE attempting delete so that both
				// success and failure paths return legacyFound==true.
				legacyFound = true
				if derr := dc.DeleteRecord(ctx, zoneID, r.ID); derr != nil {
					return true, derr
				}
			}
		}
	}
	return legacyFound, nil
}
