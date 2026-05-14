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

// Package zone contains the four reconcilers that make up the zone bundle:
// CloudflareZone, CloudflareZoneConfig, CloudflareDNSRecord, CloudflareRuleset.
package zone

import (
	"context"
	stderrors "errors"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/reconcile"
)

// defaultZoneInterval is the fallback requeue interval when Spec.Interval is unset.
const defaultZoneInterval = 30 * time.Minute

// CloudflareZoneReconciler drives the lifecycle of a CloudflareZone CR:
// credentials → create-or-adopt the zone on Cloudflare → reflect status →
// activation poke while pending → delete (with optional zone removal).
type CloudflareZoneReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Recorder is wired by the manager setup (T18). T14 does not currently
	// emit events; future reasons may.
	Recorder record.EventRecorder

	// ZoneClientFn returns a Cloudflare ZoneClient for the resolved credentials.
	// Tests inject an in-memory mock; production wires NewZoneClientFromCF.
	ZoneClientFn func(cloudflare.Credentials) (cloudflare.ZoneClient, error)

	// CFClientFn returns the raw cloudflare-go wrapper, needed to drain the
	// zone hold before DeleteZone on the DeletionPolicyDelete path. Optional;
	// when nil, the hold-drain step is skipped (best-effort).
	CFClientFn func(cloudflare.Credentials) (*cloudflare.Client, error)
}

// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarezones,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarezones/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarezones/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives one iteration of the CloudflareZone state machine.
func (r *CloudflareZoneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cloudflarezone", req.NamespacedName)

	var z v1alpha1.CloudflareZone
	if err := r.Get(ctx, req.NamespacedName, &z); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !z.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &z)
	}

	if reconcile.EnsureFinalizer(&z, conventions.FinalizerName) {
		if err := r.Update(ctx, &z); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	creds, halt, err := reconcile.LoadCredentialsHierarchical(ctx, r.Client, z.Spec.Cloudflare, z.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if halt != nil {
		return reconcile.HaltCredentialsUnavailable(ctx, r.Client, &z, &z.Status.Conditions, &z.Status.Phase, halt)
	}

	// Snapshot status before reconcile work so the trailing Status().Update
	// can be skipped when nothing material changed. reflectZoneStatus stamps
	// LastSyncedAt + ObservedGeneration each pass; we mask those for the
	// comparison below.
	originalStatus := z.Status.DeepCopy()

	zc, err := r.ZoneClientFn(creds)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Create-or-adopt: if we have no ZoneID, look one up by name, else create.
	if z.Status.ZoneID == "" {
		existing, lerr := zc.ListZonesByName(ctx, creds.AccountID, z.Spec.Name)
		if lerr != nil {
			return ctrl.Result{}, lerr
		}
		if len(existing) > 0 {
			z.Status.ZoneID = existing[0].ID
			logger.Info("adopted existing zone", "zoneID", z.Status.ZoneID, "name", z.Spec.Name)
		} else {
			created, cerr := zc.CreateZone(ctx, creds.AccountID, cloudflare.ZoneParams{
				Name: z.Spec.Name,
				Type: z.Spec.Type,
			})
			if cerr != nil {
				return ctrl.Result{}, cerr
			}
			z.Status.ZoneID = created.ID
			logger.Info("created zone", "zoneID", z.Status.ZoneID, "name", z.Spec.Name)
		}
	}

	got, err := zc.GetZone(ctx, z.Status.ZoneID)
	if err != nil {
		if stderrors.Is(err, cloudflare.ErrZoneNotFound) {
			// Zone disappeared from Cloudflare — clear and requeue to recreate/adopt.
			z.Status.ZoneID = ""
			if uerr := r.Status().Update(ctx, &z); uerr != nil {
				return ctrl.Result{}, uerr
			}
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	// Reflect observed state.
	now := metav1.Now()
	reflectZoneStatus(&z, got, now)

	switch got.Status {
	case v1alpha1.ZoneStatusActive:
		z.Status.Conditions = reconcile.SetCondition(z.Status.Conditions,
			conventions.ConditionTypeReady, metav1.ConditionTrue,
			conventions.ReasonZoneActivated, "zone is active")
		z.Status.Phase = v1alpha1.PhaseReady
	case v1alpha1.ZoneStatusPending, v1alpha1.ZoneStatusInitializing:
		// Pending / initializing path:
		//
		// Cloudflare's TriggerActivationCheck is documented as asynchronous —
		// it queues an NS check and returns immediately, with the zone
		// flipping to active only when the check actually runs (minutes to
		// hours later). For production traffic we'd unconditionally surface
		// ZoneActivating here.
		//
		// However, the in-memory mock used in unit tests flips status
		// synchronously inside TriggerActivationCheck. To make the reconciler
		// observe that flip in the same pass (and to handle the rare case
		// where real Cloudflare does return active quickly), we re-Get and
		// re-reflect the full snapshot before falling back to the Reconciling
		// branch. Against real Cloudflare this re-Get almost always shows
		// pending again, so the cost is one extra API call per reconcile of a
		// pending zone.
		//
		// We re-run the *full* reflect (not just Status.Status) so that
		// observed fields like NameServers and ActivatedOn stay consistent
		// with the persisted Status string — otherwise we'd write
		// Status=active with stale NameServers/ActivatedOn from the pre-poke
		// snapshot.
		if terr := zc.TriggerActivationCheck(ctx, z.Status.ZoneID); terr != nil {
			logger.Info("activation check poke failed (continuing)", "err", terr.Error())
		} else if refreshed, gerr := zc.GetZone(ctx, z.Status.ZoneID); gerr == nil && refreshed != nil {
			reflectZoneStatus(&z, refreshed, now)
		} else if gerr != nil {
			logger.V(1).Info("activation refresh GetZone failed", "err", gerr.Error())
		}
		if z.Status.Status == v1alpha1.ZoneStatusActive {
			z.Status.Conditions = reconcile.SetCondition(z.Status.Conditions,
				conventions.ConditionTypeReady, metav1.ConditionTrue,
				conventions.ReasonZoneActivated, "zone is active")
			z.Status.Phase = v1alpha1.PhaseReady
		} else {
			z.Status.Conditions = reconcile.SetCondition(z.Status.Conditions,
				conventions.ConditionTypeReady, metav1.ConditionFalse,
				conventions.ReasonZoneActivating, "awaiting NS delegation")
			z.Status.Phase = v1alpha1.PhaseReconciling
		}
	default:
		z.Status.Conditions = reconcile.SetCondition(z.Status.Conditions,
			conventions.ConditionTypeReady, metav1.ConditionUnknown,
			conventions.ReasonReconciling, "zone status: "+got.Status)
		z.Status.Phase = v1alpha1.PhasePending
	}

	candidate := z.Status.DeepCopy()
	candidate.LastSyncedAt = originalStatus.LastSyncedAt
	candidate.ObservedGeneration = originalStatus.ObservedGeneration
	if z.Generation != originalStatus.ObservedGeneration || !equality.Semantic.DeepEqual(originalStatus, candidate) {
		if err := r.Status().Update(ctx, &z); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		// No material change: roll back the LastSyncedAt + ObservedGeneration
		// stamps reflectZoneStatus applied so the in-memory status mirrors what
		// is in etcd.
		z.Status.LastSyncedAt = originalStatus.LastSyncedAt
		z.Status.ObservedGeneration = originalStatus.ObservedGeneration
	}

	interval := defaultZoneInterval
	if z.Spec.Interval != nil && z.Spec.Interval.Duration > 0 {
		interval = z.Spec.Interval.Duration
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

// reconcileDelete handles the deletion path: optionally remove the zone on
// Cloudflare (DeletionPolicyDelete), then drop the finalizer. The CR is
// already deletion-marked so no status update is attempted.
func (r *CloudflareZoneReconciler) reconcileDelete(ctx context.Context, z *v1alpha1.CloudflareZone) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if z.Status.ZoneID != "" && z.Spec.DeletionPolicy == v1alpha1.DeletionPolicyDelete {
		creds, halt, err := reconcile.LoadCredentialsHierarchical(ctx, r.Client, z.Spec.Cloudflare, z.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		if halt != nil {
			// Cannot delete on CF side without creds — leave the finalizer in
			// place and requeue so the user can correct the credential ref.
			return *halt, nil
		}

		zc, err := r.ZoneClientFn(creds)
		if err != nil {
			return ctrl.Result{}, err
		}

		// Best-effort: drain any hold before delete.
		//
		// Test gap (deferred): unit-test coverage of this branch is not
		// exercised by TestZone_DeleteWithDelete_RemovesZone because that
		// fixture sets CFClientFn=nil. Adding coverage requires either (a)
		// injecting a DrainZoneHold stub (refactor: a new DrainHoldFn field
		// on the reconciler), or (b) wiring a real *cloudflare.Client backed
		// by httptest. Tracked for a future task; the behaviour is itself
		// best-effort and the error is logged-not-returned, so leaving the
		// branch uncovered is bounded.
		if r.CFClientFn != nil {
			if cf, cerr := r.CFClientFn(creds); cerr == nil && cf != nil {
				if derr := cloudflare.DrainZoneHold(ctx, cf.CF(), z.Status.ZoneID); derr != nil {
					logger.Info("zone hold drain failed (continuing)", "err", derr.Error())
				}
			}
		}

		if derr := reconcile.WrapDeleteErr(zc.DeleteZone(ctx, z.Status.ZoneID)); derr != nil {
			return ctrl.Result{}, derr
		}
		logger.Info("deleted zone on Cloudflare", "zoneID", z.Status.ZoneID)
	}

	if reconcile.RemoveFinalizer(z, conventions.FinalizerName) {
		if err := r.Update(ctx, z); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// reflectZoneStatus copies observed fields from a cloudflare.Zone into the
// CR's status. The caller is responsible for the subsequent r.Status().Update.
// Passing the `now` timestamp in (rather than calling metav1.Now() inside)
// keeps LastSyncedAt consistent across multiple reflect calls within the same
// reconcile pass.
func reflectZoneStatus(z *v1alpha1.CloudflareZone, observed *cloudflare.Zone, now metav1.Time) {
	z.Status.Status = observed.Status
	z.Status.NameServers = observed.NameServers
	z.Status.OriginalNameServers = observed.OriginalNameServers
	z.Status.OriginalRegistrar = observed.OriginalRegistrar
	z.Status.ActivatedOn = nil
	if observed.ActivatedOn != nil {
		t := metav1.NewTime(*observed.ActivatedOn)
		z.Status.ActivatedOn = &t
	}
	z.Status.LastSyncedAt = &now
	z.Status.ObservedGeneration = z.Generation
}
