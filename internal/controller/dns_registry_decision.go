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

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

// RegistryAction is the verdict returned by RegistryDecide.
// Using string constants gives self-documenting log/event output without a
// separate String() method.
type RegistryAction string

const (
	// RegistryActionCreate: no existing DNS record found — proceed to create.
	RegistryActionCreate RegistryAction = "Create"
	// RegistryActionReconcile: existing record is owned by us — reconcile content drift.
	RegistryActionReconcile RegistryAction = "Reconcile"
	// RegistryActionAdopt: existing TXT is owned by a listed import-owner — rewrite TXT and claim.
	RegistryActionAdopt RegistryAction = "Adopt"
	// RegistryActionAdoptOrphan: existing DNS record has no TXT and cloudflare.io/adopt is set.
	RegistryActionAdoptOrphan RegistryAction = "AdoptOrphan"
	// RegistryActionRefuseForeignOwner: existing TXT is owned by someone else — do not touch.
	RegistryActionRefuseForeignOwner RegistryAction = "RefuseForeignOwner"
	// RegistryActionRefuseNoTXT: existing record has no TXT and adopt is not opted in.
	RegistryActionRefuseNoTXT RegistryAction = "RefuseNoTXT"
)

// Sentinel errors returned for classifiable failure verdicts.
// Callers MUST use errors.Is for comparison — never compare error strings.
var (
	// ErrForeignTXTOwner indicates a companion TXT record exists whose owner
	// field does not match TxtOwnerID. The operator refuses to overwrite.
	ErrForeignTXTOwner = errors.New("record exists with foreign TXT owner")

	// ErrTXTRegistryGap indicates a DNS record exists at the target name+type
	// but has no companion ownership TXT, and the CR has not opted in to adopt.
	ErrTXTRegistryGap = errors.New("record exists without ownership TXT and source is not opted in to adopt")

	// ErrSourceLabelsMissing is returned by writeRegistryTXT when the managed
	// CloudflareDNSRecord is missing the cloudflare.io/source-* labels needed
	// to build the registry payload.
	ErrSourceLabelsMissing = errors.New("managed CR is missing cloudflare.io/source-* labels")
)

// RegistryConfig holds TXT-registry configuration for the DNS record controller.
// The zero value is valid and disables registry behaviour entirely
// (TxtOwnerID == "" short-circuits all registry logic).
type RegistryConfig struct {
	// TxtOwnerID is the ownership token written into companion TXT records.
	// Required to enable registry behaviour. When empty, all registry checks
	// are skipped and the controller behaves identically to pre-registry releases.
	TxtOwnerID string

	// TxtImportOwners enumerates prior controllers (e.g. "external-dns") whose
	// TXT records this operator may adopt. A TXT whose payload.Owner appears in
	// this list, and whose payload is otherwise valid, results in
	// RegistryActionAdopt — this controller rewrites the TXT to claim the record.
	TxtImportOwners []string // restore — plan §11.3

	// AffixConfig controls companion-TXT name derivation.
	// The zero value matches external-dns's default affix scheme.
	AffixConfig cfclient.AffixConfig

	// TxtEncryptAESKey is an optional 32-byte AES-256 key.  When non-nil,
	// companion TXT payloads are AES-CBC encrypted before writing.
	// When nil (the default), payloads are written as plaintext.
	TxtEncryptAESKey []byte

	// TxtImportDecryptKeys is a list of AES-256 keys tried in order when
	// decoding an existing companion TXT. Supports key rotation: include the
	// old key here and the new key in TxtEncryptAESKey.
	// When empty/nil, only plaintext incoming TXTs can be read. Encrypted TXT
	// records will fail to decrypt and route to RefuseForeignOwner.
	TxtImportDecryptKeys [][]byte
}

// applyRegistryDecision executes the full §11.3 decision table for a single
// reconcile pass. It looks up the companion TXT, calls RegistryDecide, then
// either refuses (returns refused=true with the error) or writes the companion
// TXT (for adopt paths) and falls through (returns refused=false, zero result,
// nil error).
//
// For RegistryActionCreate and RegistryActionReconcile, companion TXT is NOT
// written here — it is written by the caller AFTER the main record succeeds.
// For RegistryActionAdopt and RegistryActionAdoptOrphan, the TXT rewrite IS
// performed here (this is the reclaim path).
//
// The caller (reconcileRecord) continues with normal DNS-record create/update
// only when refused=false is returned. The returned action indicates the
// registry verdict so the caller can post-write TXT on Create paths.
func (r *CloudflareDNSRecordReconciler) applyRegistryDecision(
	ctx context.Context,
	dnsRecord *cloudflarev1alpha1.CloudflareDNSRecord,
	dnsClient cfclient.DNSClient,
	zoneID string,
	existing *cfclient.DNSRecord,
) (refused bool, action RegistryAction, result ctrl.Result, err error) {
	logger := log.FromContext(ctx)
	adoptOptIn := dnsRecord.GetAnnotations()[AnnotationAdopt] == "true"
	affixedName := cfclient.AffixName(dnsRecord.Spec.Name, dnsRecord.Spec.Type, r.Registry.AffixConfig)

	// Look up the raw companion TXT content.
	var existingTXTContent string
	txtRecords, listErr := dnsClient.ListRecordsByNameAndType(ctx, zoneID, affixedName, "TXT")
	if listErr != nil {
		return true, "", ctrl.Result{}, fmt.Errorf("list companion TXT: %w", listErr)
	}
	if len(txtRecords) > 0 {
		existingTXTContent = txtRecords[0].Content
	}

	verdict := RegistryDecide(r.Registry, existing, existingTXTContent, adoptOptIn)
	logger.Info("registry decision", "action", verdict, "record", dnsRecord.Spec.Name)

	switch verdict {
	case RegistryActionRefuseForeignOwner:
		refuseErr := fmt.Errorf("%w: %s", ErrForeignTXTOwner, dnsRecord.Spec.Name)
		failReconcile(ctx, r.Client, dnsRecord, &dnsRecord.Status.Conditions, //nolint:errcheck
			cloudflarev1alpha1.ReasonRecordOwnershipConflict, refuseErr, 5*time.Minute)
		return true, verdict, ctrl.Result{RequeueAfter: 5 * time.Minute}, refuseErr

	case RegistryActionRefuseNoTXT:
		refuseErr := fmt.Errorf("%w: %s", ErrTXTRegistryGap, dnsRecord.Spec.Name)
		failReconcile(ctx, r.Client, dnsRecord, &dnsRecord.Status.Conditions, //nolint:errcheck
			cloudflarev1alpha1.ReasonTxtRegistryGap, refuseErr, 5*time.Minute)
		return true, verdict, ctrl.Result{RequeueAfter: 5 * time.Minute}, refuseErr

	case RegistryActionAdopt, RegistryActionAdoptOrphan:
		r.Recorder.Event(dnsRecord, corev1.EventTypeNormal, cloudflarev1alpha1.ReasonRecordAdopted,
			fmt.Sprintf("Adopted DNS record %s — writing TXT ownership", dnsRecord.Spec.Name))
		if writeErr := r.writeRegistryTXT(ctx, dnsRecord, dnsClient, zoneID); writeErr != nil {
			if !errors.Is(writeErr, ErrSourceLabelsMissing) {
				return true, verdict, ctrl.Result{}, fmt.Errorf("write registry TXT (adopt): %w", writeErr)
			}
			logger.Info("skipped companion TXT write: source labels missing", "record", dnsRecord.Spec.Name)
		}

		// RegistryActionCreate and RegistryActionReconcile: companion TXT is written
		// AFTER the main record succeeds (in reconcileRecord), not here.
	}

	return false, verdict, ctrl.Result{}, nil
}

// RegistryDecide applies the §11.3 decision table and returns the action the
// controller should take for the current reconcile pass.
//
// Parameters:
//   - cfg: full RegistryConfig (used for TxtOwnerID, TxtImportOwners, TxtImportDecryptKeys)
//   - existing: the current DNS record at name+type, nil if absent
//   - existingTXTContent: raw wire content from the companion TXT record, "" if absent
//   - adoptOptIn: true when the CloudflareDNSRecord carries cloudflare.io/adopt="true"
//
// DecryptPayload errors and DecodeRegistryPayload errors both route to
// RegistryActionRefuseForeignOwner. This conservatively conflates
// "foreign-controller wrote an encrypted blob with a key we don't hold" with
// "TXT corruption" — both cases require human intervention and should not
// block adoption candidates.
func RegistryDecide(
	cfg RegistryConfig,
	existing *cfclient.DNSRecord,
	existingTXTContent string,
	adoptOptIn bool,
) RegistryAction {
	if existing == nil {
		return RegistryActionCreate
	}
	if existingTXTContent == "" {
		if adoptOptIn {
			return RegistryActionAdoptOrphan
		}
		return RegistryActionRefuseNoTXT
	}
	plain, err := cfclient.DecryptPayload(existingTXTContent, cfg.TxtImportDecryptKeys)
	if err != nil {
		return RegistryActionRefuseForeignOwner
	}
	payload, err := cfclient.DecodeRegistryPayload(plain)
	if err != nil {
		return RegistryActionRefuseForeignOwner
	}
	// Comparison is intentionally case-sensitive (owner tokens are canonical strings).
	if payload.Owner == cfg.TxtOwnerID {
		return RegistryActionReconcile
	}
	if slices.Contains(cfg.TxtImportOwners, payload.Owner) {
		return RegistryActionAdopt
	}
	return RegistryActionRefuseForeignOwner
}
