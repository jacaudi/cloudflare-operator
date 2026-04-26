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
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

// RegistryAction is the verdict returned by RegistryDecide.
type RegistryAction int

const (
	// RegistryActionCreate: no existing DNS record found — proceed to create.
	RegistryActionCreate RegistryAction = iota
	// RegistryActionReconcile: existing record is owned by us — reconcile content drift.
	RegistryActionReconcile
	// RegistryActionAdopt: our TXT exists but the DNS record is gone — re-create and claim.
	RegistryActionAdopt
	// RegistryActionAdoptOrphan: existing DNS record has no TXT and cloudflare.io/adopt is set.
	RegistryActionAdoptOrphan
	// RegistryActionRefuseForeignOwner: existing TXT is owned by someone else — do not touch.
	RegistryActionRefuseForeignOwner
	// RegistryActionRefuseOrphan: existing record has no TXT and adopt is not opted in.
	RegistryActionRefuseOrphan
)

// String returns a human-readable name for the action (used in log/event messages).
func (a RegistryAction) String() string {
	switch a {
	case RegistryActionCreate:
		return "Create"
	case RegistryActionReconcile:
		return "Reconcile"
	case RegistryActionAdopt:
		return "Adopt"
	case RegistryActionAdoptOrphan:
		return "AdoptOrphan"
	case RegistryActionRefuseForeignOwner:
		return "RefuseForeignOwner"
	case RegistryActionRefuseOrphan:
		return "RefuseOrphan"
	default:
		return fmt.Sprintf("unknown(%d)", int(a))
	}
}

// Sentinel errors returned by RegistryDecide for classifiable failure verdicts.
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
	TxtImportDecryptKeys [][]byte
}

// applyRegistryDecision executes the full §5.4 decision table for a single
// reconcile pass. It looks up the companion TXT, calls RegistryDecide, then
// either refuses (returns refused=true with the error) or writes the companion
// TXT and falls through (returns refused=false, zero result, nil error).
//
// The caller (reconcileRecord) continues with normal DNS-record create/update
// only when refused=false is returned.
func (r *CloudflareDNSRecordReconciler) applyRegistryDecision(
	ctx context.Context,
	dnsRecord *cloudflarev1alpha1.CloudflareDNSRecord,
	dnsClient cfclient.DNSClient,
	zoneID string,
	existing *cfclient.DNSRecord,
) (refused bool, result ctrl.Result, err error) {
	logger := log.FromContext(ctx)
	adoptOptIn := dnsRecord.GetAnnotations()[AnnotationAdopt] == "true"
	affixedName := cfclient.AffixName(dnsRecord.Spec.Name, dnsRecord.Spec.Type, r.Registry.AffixConfig)

	// Look up the companion TXT.
	var existingTXT *cfclient.RegistryPayload
	txtRecords, listErr := dnsClient.ListRecordsByNameAndType(ctx, zoneID, affixedName, "TXT")
	if listErr != nil {
		return true, ctrl.Result{}, fmt.Errorf("list companion TXT: %w", listErr)
	}
	if len(txtRecords) > 0 {
		raw := txtRecords[0].Content
		decoded, decErr := cfclient.DecryptPayload(raw, r.Registry.TxtImportDecryptKeys)
		if decErr != nil {
			failReconcile(ctx, r.Client, dnsRecord, &dnsRecord.Status.Conditions, //nolint:errcheck
				cloudflarev1alpha1.ReasonTxtDecryptFailed, decErr, 5*time.Minute)
			return true, ctrl.Result{RequeueAfter: 5 * time.Minute}, decErr
		}
		parsed, parseErr := cfclient.DecodeRegistryPayload(decoded)
		if parseErr == nil {
			existingTXT = &parsed
		}
	}

	action, decideErr := RegistryDecide(r.Registry.TxtOwnerID, existingTXT, existing, adoptOptIn)
	logger.Info("registry decision", "action", action.String(), "record", dnsRecord.Spec.Name)

	switch action {
	case RegistryActionRefuseForeignOwner, RegistryActionRefuseOrphan:
		return true, ctrl.Result{RequeueAfter: 5 * time.Minute}, decideErr

	case RegistryActionAdopt, RegistryActionAdoptOrphan:
		r.Recorder.Event(dnsRecord, corev1.EventTypeNormal, cloudflarev1alpha1.ReasonRecordAdopted,
			fmt.Sprintf("Adopted DNS record %s — writing TXT ownership", dnsRecord.Spec.Name))
		if writeErr := r.writeRegistryTXT(ctx, dnsRecord, dnsClient, zoneID); writeErr != nil {
			if !errors.Is(writeErr, ErrSourceLabelsMissing) {
				return true, ctrl.Result{}, fmt.Errorf("write registry TXT (adopt): %w", writeErr)
			}
			logger.Info("skipped companion TXT write: source labels missing", "record", dnsRecord.Spec.Name)
		}

	case RegistryActionCreate, RegistryActionReconcile:
		if writeErr := r.writeRegistryTXT(ctx, dnsRecord, dnsClient, zoneID); writeErr != nil {
			if !errors.Is(writeErr, ErrSourceLabelsMissing) {
				return true, ctrl.Result{}, fmt.Errorf("write registry TXT: %w", writeErr)
			}
			logger.Info("skipped companion TXT write: source labels missing", "record", dnsRecord.Spec.Name)
		}
	}

	return false, ctrl.Result{}, nil
}

// RegistryDecide applies the §5.4 decision table and returns the action the
// controller should take for the current reconcile pass.
//
// Parameters:
//   - ownerID: the TxtOwnerID from RegistryConfig (caller guarantees non-empty)
//   - existingTXT: decoded payload from the companion TXT record, nil if absent
//   - existing: the current DNS record at name+type, nil if absent
//   - adoptOptIn: true when the CloudflareDNSRecord carries cloudflare.io/adopt="true"
//
// The error return is non-nil only for the two refuse verdicts; it always wraps
// one of ErrForeignTXTOwner or ErrTXTRegistryGap so callers can use errors.Is.
func RegistryDecide(
	ownerID string,
	existingTXT *cfclient.RegistryPayload,
	existing *cfclient.DNSRecord,
	adoptOptIn bool,
) (RegistryAction, error) {
	switch {
	case existing == nil && existingTXT == nil:
		// Nothing at all — clean create.
		return RegistryActionCreate, nil

	case existing == nil && existingTXT != nil:
		// Our TXT is present but the DNS record disappeared — re-create and reclaim.
		return RegistryActionAdopt, nil

	case existing != nil && existingTXT == nil:
		// DNS record exists but no ownership TXT.
		if adoptOptIn {
			return RegistryActionAdoptOrphan, nil
		}
		return RegistryActionRefuseOrphan, fmt.Errorf("%w: %s", ErrTXTRegistryGap, existing.ID)

	default:
		// Both exist — check ownership.
		// Comparison is intentionally case-sensitive (owner tokens are canonical strings).
		if existingTXT.Owner == ownerID {
			return RegistryActionReconcile, nil
		}
		return RegistryActionRefuseForeignOwner,
			fmt.Errorf("%w: record %s is owned by %q", ErrForeignTXTOwner, existing.ID, existingTXT.Owner)
	}
}
