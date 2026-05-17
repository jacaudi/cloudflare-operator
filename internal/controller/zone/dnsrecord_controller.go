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
	"crypto/sha256"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/ipresolver"
	"github.com/jacaudi/cloudflare-operator/internal/reconcile"
)

// defaultDNSRecordInterval matches the apiserver default on Spec.Interval
// (`+kubebuilder:default="5m"`); used when admission isn't in the loop (unit
// tests with the fake client).
const defaultDNSRecordInterval = 5 * time.Minute

// CloudflareDNSRecordReconciler drives the lifecycle of a CloudflareDNSRecord
// CR: credentials → resolve zone → resolve content (with optional DynamicIP)
// → create / adopt / update / delete on Cloudflare → reflect status.
//
// TXT companion registry is active: the reconciler builds a codec per
// reconcile from CloudflareOperator.spec.cloudflare.txtRegistryKeySecretRef
// (plaintext default; AES-256-GCM when a key Secret is configured),
// Spec.Adopt is TXT-ownership-verified (no silent backfill — see design
// §5.4), and Spec.Mode=Observe makes the reconciler read-only.
type CloudflareDNSRecordReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Recorder is wired by the manager setup (T18). Nil is tolerated; event
	// emission no-ops without a recorder.
	Recorder record.EventRecorder
	// DNSClientFn returns a Cloudflare DNSClient for the resolved credentials.
	// Tests inject an in-memory mock; production wires NewDNSClientFromCF.
	DNSClientFn func(cloudflare.Credentials) (cloudflare.DNSClient, error)
	// IPResolver resolves the external IP for records with Spec.DynamicIP=true.
	IPResolver *ipresolver.Resolver
}

// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarednsrecords,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarednsrecords/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarednsrecords/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives one iteration of the CloudflareDNSRecord state machine.
func (r *CloudflareDNSRecordReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cloudflarednsrecord", req.NamespacedName)

	var rec v2alpha1.CloudflareDNSRecord
	if err := r.Get(ctx, req.NamespacedName, &rec); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !rec.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &rec)
	}

	if reconcile.EnsureFinalizer(&rec, conventions.FinalizerName) {
		if err := r.Update(ctx, &rec); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	creds, halt, err := reconcile.LoadCredentialsHierarchical(ctx, r.Client, rec.Spec.Cloudflare, rec.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if halt != nil {
		return reconcile.HaltCredentialsUnavailable(ctx, r.Client, &rec, &rec.Status.Conditions, &rec.Status.Phase, halt)
	}

	// Fetch the CloudflareOperator singleton to resolve the TXT-registry key.
	// NotFound is treated as no key configured (bootstrap creates it eventually).
	// Any other Get error is hard-returned so the reconciler retries.
	var op v2alpha1.CloudflareOperator
	if err := r.Get(ctx, types.NamespacedName{Name: v2alpha1.CloudflareOperatorSingletonName}, &op); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("get CloudflareOperator/cluster: %w", err)
		}
		// Singleton absent: treat as no TXT key configured (plaintext mode).
	}

	encoder, cerr := loadCodec(ctx, r.Client, op.Spec.Cloudflare.TxtRegistryKeySecretRef, rec.Namespace)
	if cerr != nil {
		rec.Status.Conditions = reconcile.SetReady(rec.Status.Conditions, metav1.ConditionFalse,
			conventions.ReasonTxtRegistryKeyUnavailable, cerr.Error())
		rec.Status.Phase = reconcile.DerivePhase(metav1.ConditionFalse, conventions.ReasonTxtRegistryKeyUnavailable)
		if uerr := r.Status().Update(ctx, &rec); uerr != nil {
			return ctrl.Result{}, uerr
		}
		return ctrl.Result{RequeueAfter: reconcile.DefaultRequeueAfter}, nil
	}
	readCodec := autoDetectingFor(encoder)

	// Snapshot status so the trailing Status().Update can be skipped when
	// nothing material changed; LastSyncedAt/ObservedGeneration are masked.
	originalStatus := rec.Status.DeepCopy()

	dc, err := r.DNSClientFn(creds)
	if err != nil {
		return ctrl.Result{}, err
	}

	zres, err := reconcile.ResolveZoneID(ctx, r.Client, reconcile.ZoneRefInputs{
		ZoneID: rec.Spec.ZoneID, ZoneRef: rec.Spec.ZoneRef,
	}, rec.Namespace)
	if err != nil {
		if stderrors.Is(err, reconcile.ErrZoneRefNotFound) {
			return r.haltDependency(ctx, &rec, err.Error())
		}
		return ctrl.Result{}, err
	}
	if zres.ZoneID == "" {
		return r.haltDependency(ctx, &rec, "zoneRef target has no status.zoneID yet")
	}
	zoneID := zres.ZoneID

	// Observe-mode early-exit: read CF state, populate Status, return without
	// any mutating calls. Spec.Adopt is a no-op in this mode. Direct
	// Status().Update + return — does NOT route through the terminal DeepEqual
	// gate because that gate uses originalStatus captured before we had the
	// zone ID (and it manages LastSyncedAt / ObservedGeneration which observe
	// mode deliberately leaves untouched).
	if !reconcile.ShouldMutate(string(rec.Spec.Mode)) {
		observed, oerr := dc.ListRecordsByNameAndType(ctx, zoneID, rec.Spec.Name, rec.Spec.Type)
		if oerr != nil {
			return ctrl.Result{}, fmt.Errorf("observe: list record: %w", oerr)
		}
		if len(observed) > 0 {
			rec.Status.RecordID = observed[0].ID
			rec.Status.CurrentContent = observed[0].Content
		} else {
			rec.Status.RecordID = ""
			rec.Status.CurrentContent = ""
		}

		txtName := cloudflare.AffixName(txtAffix, rec.Spec.Name)
		txtRecs, terr := dc.ListRecordsByNameAndType(ctx, zoneID, txtName, "TXT")
		if terr != nil {
			return ctrl.Result{}, fmt.Errorf("observe: list TXT: %w", terr)
		}
		if len(txtRecs) > 0 {
			obs := &v2alpha1.ObservedTXTPayload{}
			if payload, derr := readCodec.Decode(txtRecs[0].Content); derr != nil {
				obs.RawContent = txtRecs[0].Content
				obs.Codec = "unrecognized"
			} else {
				obs.Version = payload.V
				obs.Kind = payload.K
				obs.Namespace = payload.NS
				obs.Name = payload.N
				obs.ContentHash = payload.H
				obs.Codec = cloudflare.CodecKindFor(txtRecs[0].Content)
			}
			rec.Status.ObservedTXT = obs
		} else {
			rec.Status.ObservedTXT = nil
		}

		rec.Status.Conditions = reconcile.SetReady(rec.Status.Conditions, metav1.ConditionTrue,
			conventions.ReasonObserving, "Spec.Mode=Observe; operator is reading but not mutating")
		rec.Status.Phase = reconcile.DerivePhase(metav1.ConditionTrue, conventions.ReasonObserving)
		if uerr := r.Status().Update(ctx, &rec); uerr != nil {
			return ctrl.Result{}, uerr
		}
		return ctrl.Result{RequeueAfter: reconcile.ResolveInterval(rec.Spec.Interval, defaultDNSRecordInterval)}, nil
	}

	content, err := r.resolveContent(ctx, &rec)
	if err != nil {
		rec.Status.Conditions = reconcile.SetReady(rec.Status.Conditions, metav1.ConditionFalse,
			conventions.ReasonDegraded, err.Error())
		rec.Status.Phase = reconcile.DerivePhase(metav1.ConditionFalse, conventions.ReasonDegraded)
		if uerr := r.Status().Update(ctx, &rec); uerr != nil {
			return ctrl.Result{}, uerr
		}
		return ctrl.Result{RequeueAfter: reconcile.DefaultRequeueAfter}, nil
	}

	// Adopt branch: TXT-verified takeover by (name, type) match. Ownership is
	// confirmed via a companion TXT record before adoption proceeds. If no
	// companion exists, or the companion claims a foreign owner, adoption is
	// refused and the reconciler halts (Ready=False). NEVER auto-writes a TXT
	// for a pre-existing record (design §2 Q2 — the load-bearing safety
	// property). Pre-feature records follow the §5.4 migration procedure.
	// Adopted IDs flow into the update branch below to converge any drift.
	if rec.Spec.Adopt && rec.Status.RecordID == "" {
		list, lerr := dc.ListRecordsByNameAndType(ctx, zoneID, rec.Spec.Name, rec.Spec.Type)
		if lerr != nil {
			return ctrl.Result{}, fmt.Errorf("list records for adopt: %w", lerr)
		}
		if len(list) > 0 {
			txtName := cloudflare.AffixName(txtAffix, rec.Spec.Name)
			txtRecs, terr := dc.ListRecordsByNameAndType(ctx, zoneID, txtName, "TXT")
			if terr != nil {
				return ctrl.Result{}, fmt.Errorf("list TXT companion for adopt: %w", terr)
			}
			if len(txtRecs) == 0 {
				// No TXT companion — refuse adoption. DO NOT create a TXT.
				msg := "record exists but has no TXT companion; adoption refused (no silent backfill). " +
					"See docs/plans/2026-05-14-txt-registry-design.md §5.4 migration procedure."
				rec.Status.Conditions = reconcile.SetReady(rec.Status.Conditions, metav1.ConditionFalse,
					conventions.ReasonAdoptRefusedNoTXT, msg)
				rec.Status.Phase = reconcile.DerivePhase(metav1.ConditionFalse, conventions.ReasonAdoptRefusedNoTXT)
				if uerr := r.Status().Update(ctx, &rec); uerr != nil {
					return ctrl.Result{}, uerr
				}
				return ctrl.Result{RequeueAfter: reconcile.DefaultRequeueAfter}, nil
			}
			switch verifyTXTOwnership(txtRecs[0].Content, readCodec, "CloudflareDNSRecord", rec.Namespace, rec.Name) {
			case TxtOwnershipMatch:
				// TXT companion confirms this CR owns the record — adopt it.
				rec.Status.RecordID = list[0].ID
				rec.Status.TxtRecordID = txtRecs[0].ID
				rec.Status.TxtAffix = txtAffix
				logger.Info("adopted existing DNS record with TXT verification",
					"recordID", list[0].ID, "txtRecordID", txtRecs[0].ID,
					"name", rec.Spec.Name, "type", rec.Spec.Type)
				if r.Recorder != nil {
					r.Recorder.Eventf(&rec, corev1.EventTypeNormal, conventions.ReasonAdoptedExistingRecord,
						"adopted existing %s record for %s (id=%s)", rec.Spec.Type, rec.Spec.Name, list[0].ID)
				}
				// Fall through to the normal update/sync path below.
			case TxtOwnershipForeign:
				// TXT companion claims a different owner — refuse adoption.
				msg := "TXT companion claims a different owner; refusing adoption"
				rec.Status.Conditions = reconcile.SetReady(rec.Status.Conditions, metav1.ConditionFalse,
					conventions.ReasonAdoptRefusedForeign, msg)
				rec.Status.Phase = reconcile.DerivePhase(metav1.ConditionFalse, conventions.ReasonAdoptRefusedForeign)
				if uerr := r.Status().Update(ctx, &rec); uerr != nil {
					return ctrl.Result{}, uerr
				}
				return ctrl.Result{RequeueAfter: reconcile.DefaultRequeueAfter}, nil
			default: // TxtOwnershipUnrecognized
				// TXT content is not decodable — refuse conservatively.
				msg := "TXT companion content not decodable; refusing adoption (see design §5.4)"
				rec.Status.Conditions = reconcile.SetReady(rec.Status.Conditions, metav1.ConditionFalse,
					conventions.ReasonAdoptRefusedNoTXT, msg)
				rec.Status.Phase = reconcile.DerivePhase(metav1.ConditionFalse, conventions.ReasonAdoptRefusedNoTXT)
				if uerr := r.Status().Update(ctx, &rec); uerr != nil {
					return ctrl.Result{}, uerr
				}
				return ctrl.Result{RequeueAfter: reconcile.DefaultRequeueAfter}, nil
			}
		}
	}

	params := buildParams(&rec, content)

	if rec.Status.RecordID == "" {
		// Create path.
		created, cerr := dc.CreateRecord(ctx, zoneID, params)
		if cerr != nil {
			return ctrl.Result{}, fmt.Errorf("create record: %w", cerr)
		}
		rec.Status.RecordID = created.ID
		rec.Status.CurrentContent = created.Content
		logger.Info("created DNS record", "recordID", created.ID)

		// Pair: write TXT companion for the newly created record.
		contentHash := sha256Hex(content)
		txtID, terr := writeTXTCompanion(ctx, dc, zoneID, rec.Spec.Name, contentHash, rec.Namespace, rec.Name, encoder)
		if terr != nil {
			// Partial failure: DNS record created successfully, but TXT companion
			// write failed. Emit a Warning Event and continue so the terminal
			// status path still runs. The next reconcile re-enters the update
			// path (RecordID is now set) and the TxtRecordID-empty guard there
			// retries the companion write even without content drift.
			logger.Error(terr, "TXT companion write failed after DNS create; will retry next reconcile")
			if r.Recorder != nil {
				r.Recorder.Eventf(&rec, corev1.EventTypeWarning, conventions.ReasonTxtRegistryWriteFailed,
					"DNS record created but TXT companion write failed: %s", terr.Error())
			}
			// Do NOT set rec.Status.TxtRecordID — the update-path retry guard
			// (TxtRecordID == "") will call writeTXTCompanion on the next reconcile.
		} else {
			rec.Status.TxtRecordID = txtID
			rec.Status.TxtAffix = txtAffix
		}
	} else {
		// Update path: confirm existence, then converge drift.
		existing, gerr := dc.GetRecord(ctx, zoneID, rec.Status.RecordID)
		if gerr != nil {
			if stderrors.Is(gerr, cloudflare.ErrRecordNotFound) {
				logger.Info("record not found on Cloudflare; clearing RecordID and requeueing", "recordID", rec.Status.RecordID)
				rec.Status.RecordID = ""
				if uerr := r.Status().Update(ctx, &rec); uerr != nil {
					return ctrl.Result{}, uerr
				}
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, fmt.Errorf("get record: %w", gerr)
		}
		if needsUpdate(existing, &rec.Spec, content) {
			updated, uerr := dc.UpdateRecord(ctx, zoneID, rec.Status.RecordID, params)
			if uerr != nil {
				return ctrl.Result{}, fmt.Errorf("update record: %w", uerr)
			}
			rec.Status.CurrentContent = updated.Content
			logger.Info("updated DNS record", "recordID", updated.ID)
			if r.Recorder != nil {
				r.Recorder.Eventf(&rec, corev1.EventTypeNormal, conventions.ReasonDriftDetected,
					"corrected drift on %s record %s", rec.Spec.Type, rec.Spec.Name)
			}

			// Pair: refresh TXT companion hash to match the newly written content.
			contentHash := sha256Hex(content)
			txtID, terr := writeTXTCompanion(ctx, dc, zoneID, rec.Spec.Name, contentHash, rec.Namespace, rec.Name, encoder)
			if terr != nil {
				// Partial failure: DNS record updated successfully, but TXT
				// companion refresh failed. Emit a Warning Event and continue.
				logger.Error(terr, "TXT companion refresh failed after DNS update; will retry next reconcile")
				if r.Recorder != nil {
					r.Recorder.Eventf(&rec, corev1.EventTypeWarning, conventions.ReasonTxtRegistryWriteFailed,
						"DNS record updated but TXT companion refresh failed: %s", terr.Error())
				}
			} else {
				rec.Status.TxtRecordID = txtID
				rec.Status.TxtAffix = txtAffix
			}
		} else {
			rec.Status.CurrentContent = existing.Content
			// Retry guard: the main record is in sync (no content drift) but
			// TxtRecordID is empty — this means a prior create or update wrote
			// the main record successfully but the TXT companion write failed.
			// This arm is only reachable for a record THIS operator created or
			// adopt-matched (adopt-refused outcomes return before reaching here;
			// observe mode early-exits before the create/update block). Write
			// the missing companion now. Same partial-failure handling: emit a
			// Warning Event and continue; the next reconcile will retry again.
			if rec.Status.TxtRecordID == "" {
				contentHash := sha256Hex(content)
				txtID, terr := writeTXTCompanion(ctx, dc, zoneID, rec.Spec.Name, contentHash, rec.Namespace, rec.Name, encoder)
				if terr != nil {
					logger.Error(terr, "TXT companion retry failed (no-drift path); will retry next reconcile")
					if r.Recorder != nil {
						r.Recorder.Eventf(&rec, corev1.EventTypeWarning, conventions.ReasonTxtRegistryWriteFailed,
							"TXT companion retry failed (no content drift): %s", terr.Error())
					}
				} else {
					rec.Status.TxtRecordID = txtID
					rec.Status.TxtAffix = txtAffix
				}
			}
		}
	}

	rec.Status.Conditions = reconcile.SetReady(rec.Status.Conditions, metav1.ConditionTrue,
		conventions.ReasonReady, "DNS record synced")
	rec.Status.Phase = reconcile.DerivePhase(metav1.ConditionTrue, conventions.ReasonReady)

	candidate := rec.Status.DeepCopy()
	candidate.LastSyncedAt = originalStatus.LastSyncedAt
	candidate.ObservedGeneration = originalStatus.ObservedGeneration
	if rec.Generation != originalStatus.ObservedGeneration || !equality.Semantic.DeepEqual(originalStatus, candidate) {
		now := metav1.Now()
		rec.Status.LastSyncedAt = &now
		rec.Status.ObservedGeneration = rec.Generation
		if err := r.Status().Update(ctx, &rec); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{RequeueAfter: reconcile.ResolveInterval(rec.Spec.Interval, defaultDNSRecordInterval)}, nil
}

// haltDependency persists a DependencyMissing Ready=False with the given
// message and requeues. Used when zone resolution can't proceed because the
// referenced CloudflareZone isn't ready yet.
func (r *CloudflareDNSRecordReconciler) haltDependency(ctx context.Context, rec *v2alpha1.CloudflareDNSRecord, msg string) (ctrl.Result, error) {
	return reconcile.HaltDependency(ctx, r.Client, rec, &rec.Status.Conditions, &rec.Status.Phase, msg, reconcile.DefaultRequeueAfter)
}

// reconcileDelete handles the deletion path: best-effort remove the record on
// Cloudflare (NotFound is treated as success via WrapDeleteErr), then drop the
// finalizer.
func (r *CloudflareDNSRecordReconciler) reconcileDelete(ctx context.Context, rec *v2alpha1.CloudflareDNSRecord) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Observe mode never wrote anything to Cloudflare; drop the finalizer
	// immediately without any CF calls.
	if !reconcile.ShouldMutate(string(rec.Spec.Mode)) {
		if reconcile.RemoveFinalizer(rec, conventions.FinalizerName) {
			if err := r.Update(ctx, rec); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if rec.Status.RecordID != "" {
		creds, halt, err := reconcile.LoadCredentialsHierarchical(ctx, r.Client, rec.Spec.Cloudflare, rec.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		if halt != nil {
			// No creds: leave finalizer in place so the user can correct the
			// credential ref.
			return *halt, nil
		}

		dc, err := r.DNSClientFn(creds)
		if err != nil {
			return ctrl.Result{}, err
		}

		zres, err := reconcile.ResolveZoneID(ctx, r.Client, reconcile.ZoneRefInputs{
			ZoneID: rec.Spec.ZoneID, ZoneRef: rec.Spec.ZoneRef,
		}, rec.Namespace)
		if err != nil {
			// If the zone reference has been removed, we can't talk to CF for
			// this record; leave the finalizer for manual recovery.
			if stderrors.Is(err, reconcile.ErrZoneRefNotFound) {
				return ctrl.Result{RequeueAfter: reconcile.DefaultRequeueAfter}, nil
			}
			return ctrl.Result{}, err
		}
		if zres.ZoneID == "" {
			return ctrl.Result{RequeueAfter: reconcile.DefaultRequeueAfter}, nil
		}

		// Best-effort: delete the TXT companion before the main record. An orphan
		// TXT is harmless, so failures only log and never block finalizer removal.
		if rec.Status.TxtRecordID != "" {
			if derr := deleteTXTCompanion(ctx, dc, zres.ZoneID, rec.Status.TxtRecordID); derr != nil {
				logger.Error(derr, "TXT companion delete failed; leaving orphan (harmless) and continuing",
					"txtRecordID", rec.Status.TxtRecordID)
			}
		}

		if derr := reconcile.WrapDeleteErr(dc.DeleteRecord(ctx, zres.ZoneID, rec.Status.RecordID)); derr != nil {
			return ctrl.Result{}, derr
		}
		logger.Info("deleted DNS record on Cloudflare", "recordID", rec.Status.RecordID)
	}

	if reconcile.RemoveFinalizer(rec, conventions.FinalizerName) {
		if err := r.Update(ctx, rec); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// resolveContent computes the canonical record content for the desired state:
//   - DynamicIP: external IP via r.IPResolver (A only; admission CEL gates type).
//   - SRV: empty (content lives in Data; populated by buildParams).
//   - default: *Spec.Content.
func (r *CloudflareDNSRecordReconciler) resolveContent(ctx context.Context, rec *v2alpha1.CloudflareDNSRecord) (string, error) {
	if rec.Spec.DynamicIP {
		if rec.Spec.Type != v2alpha1.DNSRecordTypeA {
			return "", fmt.Errorf("dynamicIP is only valid for type A records")
		}
		if r.IPResolver == nil {
			return "", fmt.Errorf("no IP resolver configured")
		}
		return r.IPResolver.GetExternalIP(ctx)
	}
	if rec.Spec.Type == v2alpha1.DNSRecordTypeSRV {
		// SRV content is computed from SRVData via the Data map in buildParams.
		return "", nil
	}
	if rec.Spec.Content == nil {
		return "", fmt.Errorf("content is required when dynamicIP is false")
	}
	return *rec.Spec.Content, nil
}

// buildParams maps a CR's desired state to cloudflare.DNSRecordParams.
// SRV records carry their structured fields in Data; other types use Content.
func buildParams(rec *v2alpha1.CloudflareDNSRecord, content string) cloudflare.DNSRecordParams {
	params := cloudflare.DNSRecordParams{
		Name:    rec.Spec.Name,
		Type:    rec.Spec.Type,
		Content: content,
		TTL:     rec.Spec.TTL,
		Proxied: rec.Spec.Proxied,
	}
	if rec.Spec.Priority != nil {
		params.Priority = rec.Spec.Priority
	}
	if rec.Spec.SRVData != nil {
		params.Data = map[string]any{
			"service":  rec.Spec.SRVData.Service,
			"proto":    rec.Spec.SRVData.Proto,
			"name":     rec.Spec.Name,
			"priority": rec.Spec.SRVData.Priority,
			"weight":   rec.Spec.SRVData.Weight,
			"port":     rec.Spec.SRVData.Port,
			"target":   rec.Spec.SRVData.Target,
		}
	}
	return params
}

// needsUpdate reports whether the observed record diverges from the desired
// spec. SRV records skip the top-level Content comparison because their
// content is computed server-side from Data; instead they get a per-field
// comparison against the structured SRVData.
func needsUpdate(observed *cloudflare.DNSRecord, spec *v2alpha1.CloudflareDNSRecordSpec, content string) bool {
	if observed.Name != spec.Name {
		return true
	}
	// SRV records: compare structured Data fields and short-circuit out
	// before the Content branch (their Content is server-computed).
	if spec.Type == v2alpha1.DNSRecordTypeSRV && spec.SRVData != nil {
		if srvDriftDetected(observed.Data, spec.SRVData) {
			return true
		}
	}
	if spec.Type != v2alpha1.DNSRecordTypeSRV && observed.Content != content {
		return true
	}
	if spec.TTL > 0 && observed.TTL != spec.TTL {
		return true
	}
	if spec.Proxied != nil && observed.Proxied != *spec.Proxied {
		return true
	}
	// MX/URI priority drift (top-level). SRV priority lives in Data and is
	// compared inside srvDriftDetected above.
	if spec.Priority != nil {
		if observed.Priority == nil || *observed.Priority != *spec.Priority {
			return true
		}
	}
	return false
}

// srvDriftDetected compares an observed Cloudflare SRV record's Data map
// against the operator-side structured SRVData. Returns true if any
// user-controlled field differs. Number fields may come back from the SDK
// as float64 (JSON-decoded) — normalize before comparing. The "name" key
// mirrors rec.Spec.Name and is already validated by the top-level Name
// comparison in needsUpdate; it is excluded here.
func srvDriftDetected(observed map[string]any, spec *v2alpha1.SRVData) bool {
	if observed == nil {
		// Observed has no Data — either freshly created or missing fields.
		// Treat as drift to force a re-PUT and converge upstream state.
		return true
	}
	if observed["service"] != spec.Service ||
		observed["proto"] != spec.Proto ||
		observed["target"] != spec.Target {
		return true
	}
	if intField(observed["priority"]) != spec.Priority ||
		intField(observed["weight"]) != spec.Weight ||
		intField(observed["port"]) != spec.Port {
		return true
	}
	return false
}

// sha256Hex returns the sha256 hex digest of s, prefixed with "sha256:".
// It is used to compute the content hash stored in TXT companion records.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// intField normalizes any-typed numeric values (float64 from JSON, int from
// direct map literals) to int for comparison against operator-side int spec
// fields.
func intField(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	}
	return 0
}
