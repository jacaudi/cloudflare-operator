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
	stderrors "errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
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
// TXT companion registry is deferred this phase: there is no Codec on the
// struct, no companion-TXT writes, and Spec.Adopt is bare-takeover (any
// matching (name, type) record is adopted without ownership verification).
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

	var rec v1alpha1.CloudflareDNSRecord
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
		rec.Status.Conditions = reconcile.SetReady(rec.Status.Conditions, metav1.ConditionFalse,
			conventions.ReasonCredentialsUnavailable, "cloudflare credentials unavailable")
		rec.Status.Phase = reconcile.DerivePhase(metav1.ConditionFalse, conventions.ReasonCredentialsUnavailable)
		if uerr := r.Status().Update(ctx, &rec); uerr != nil {
			return ctrl.Result{}, uerr
		}
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
		if stderrors.Is(err, reconcile.ErrZoneRefNotFound) {
			return r.haltDependency(ctx, &rec, err.Error())
		}
		return ctrl.Result{}, err
	}
	if zres.ZoneID == "" {
		return r.haltDependency(ctx, &rec, "zoneRef target has no status.zoneID yet")
	}
	zoneID := zres.ZoneID

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

	// Adopt branch: bare takeover by (name, type) match. No ownership
	// verification (TXT registry deferred). Adopted ID flows into the update
	// branch below to converge content/TTL/proxied drift in the same pass.
	if rec.Spec.Adopt && rec.Status.RecordID == "" {
		list, lerr := dc.ListRecordsByNameAndType(ctx, zoneID, rec.Spec.Name, rec.Spec.Type)
		if lerr != nil {
			return ctrl.Result{}, fmt.Errorf("list records for adopt: %w", lerr)
		}
		if len(list) > 0 {
			rec.Status.RecordID = list[0].ID
			logger.Info("adopted existing DNS record", "recordID", list[0].ID, "name", rec.Spec.Name, "type", rec.Spec.Type)
			if r.Recorder != nil {
				r.Recorder.Eventf(&rec, corev1.EventTypeNormal, conventions.ReasonAdoptedExistingRecord,
					"adopted existing %s record for %s (id=%s)", rec.Spec.Type, rec.Spec.Name, list[0].ID)
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
		} else {
			rec.Status.CurrentContent = existing.Content
		}
	}

	rec.Status.Conditions = reconcile.SetReady(rec.Status.Conditions, metav1.ConditionTrue,
		conventions.ReasonReady, "DNS record synced")
	rec.Status.Phase = reconcile.DerivePhase(metav1.ConditionTrue, conventions.ReasonReady)
	now := metav1.Now()
	rec.Status.LastSyncedAt = &now
	rec.Status.ObservedGeneration = rec.Generation

	if err := r.Status().Update(ctx, &rec); err != nil {
		return ctrl.Result{}, err
	}

	interval := defaultDNSRecordInterval
	if rec.Spec.Interval != nil && rec.Spec.Interval.Duration > 0 {
		interval = rec.Spec.Interval.Duration
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

// haltDependency persists a DependencyMissing Ready=False with the given
// message and requeues. Used when zone resolution can't proceed because the
// referenced CloudflareZone isn't ready yet.
func (r *CloudflareDNSRecordReconciler) haltDependency(ctx context.Context, rec *v1alpha1.CloudflareDNSRecord, msg string) (ctrl.Result, error) {
	rec.Status.Conditions = reconcile.SetReady(rec.Status.Conditions, metav1.ConditionFalse,
		conventions.ReasonDependencyMissing, msg)
	rec.Status.Phase = reconcile.DerivePhase(metav1.ConditionFalse, conventions.ReasonDependencyMissing)
	if err := r.Status().Update(ctx, rec); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: reconcile.DefaultRequeueAfter}, nil
}

// reconcileDelete handles the deletion path: best-effort remove the record on
// Cloudflare (NotFound is treated as success via WrapDeleteErr), then drop the
// finalizer.
func (r *CloudflareDNSRecordReconciler) reconcileDelete(ctx context.Context, rec *v1alpha1.CloudflareDNSRecord) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

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
func (r *CloudflareDNSRecordReconciler) resolveContent(ctx context.Context, rec *v1alpha1.CloudflareDNSRecord) (string, error) {
	if rec.Spec.DynamicIP {
		if rec.Spec.Type != v1alpha1.DNSRecordTypeA {
			return "", fmt.Errorf("dynamicIP is only valid for type A records")
		}
		if r.IPResolver == nil {
			return "", fmt.Errorf("no IP resolver configured")
		}
		return r.IPResolver.GetExternalIP(ctx)
	}
	if rec.Spec.Type == v1alpha1.DNSRecordTypeSRV {
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
func buildParams(rec *v1alpha1.CloudflareDNSRecord, content string) cloudflare.DNSRecordParams {
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
func needsUpdate(observed *cloudflare.DNSRecord, spec *v1alpha1.CloudflareDNSRecordSpec, content string) bool {
	if observed.Name != spec.Name {
		return true
	}
	// SRV records: compare structured Data fields and short-circuit out
	// before the Content branch (their Content is server-computed).
	if spec.Type == v1alpha1.DNSRecordTypeSRV && spec.SRVData != nil {
		if srvDriftDetected(observed.Data, spec.SRVData) {
			return true
		}
	}
	if spec.Type != v1alpha1.DNSRecordTypeSRV && observed.Content != content {
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
func srvDriftDetected(observed map[string]any, spec *v1alpha1.SRVData) bool {
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
