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

package tunnel

import (
	"context"
	"fmt"
	"reflect"
	"time"

	appsv1 "k8s.io/api/apps/v1"
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
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cloudflare "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// defaultTunnelInterval is the fallback requeue interval when Spec.Interval
// is unset. Mirrors the zone reconciler's defaultZoneInterval.
const defaultTunnelInterval = 30 * time.Minute

// drainRequeueInterval is the polling interval between finalizer-drain steps
// (scale Deployment to 0 → wait for Pods gone). Short enough to keep deletion
// snappy, long enough not to thrash the API server.
const drainRequeueInterval = 2 * time.Second

// pendingDeletionGrace is the two-tick confirmation window for cascade-GC
// self-delete. The first reconcile observing isOrphaned() stamps
// Status.LastOrphanedAt = now and requeues after this window; a later
// reconcile still observing isOrphaned past the window self-deletes the
// CR. Mitigates the source-attach-during-grace race (design §3.1). 60s is
// generous vs. controller-runtime watch propagation (single-digit seconds).
const pendingDeletionGrace = 60 * time.Second

// CloudflareTunnelReconciler drives the lifecycle of a CloudflareTunnel CR:
// credentials → tunnel create-or-adopt on Cloudflare → connector token
// Secret → cloudflared Deployment + metrics Service → remote-config PUT
// (with drift-skip) → connector health observation → status rollup.
//
// All status writes are persisted by the OUTER Reconcile function only;
// inner helpers mutate the in-memory CR and return errors. The single
// exception is failStatus, which performs a defensive Status().Update on
// the early-exit path.
type CloudflareTunnelReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// TunnelClientFn returns a Cloudflare TunnelClient for the resolved
	// credentials. Tests inject an in-memory mock; production wires
	// cloudflare.NewTunnelClientFromCF via a *cfgo.Client built per
	// resolved creds.
	TunnelClientFn func(cloudflare.Credentials) (cloudflare.TunnelClient, error)

	// Cache is the shared ingress-contribution cache written by the source
	// reconcilers (Service / Gateway / HTTPRoute / TLSRoute) and read here.
	Cache *tunnelsynth.Cache

	// DefaultImage is the operator's compile-time pinned cloudflared image.
	// Used as the default for spec.connector.image's unset half.
	DefaultImage string

	// PendingDeletionGrace overrides the cascade-GC two-tick grace window.
	// Zero (the default) means use the pendingDeletionGrace constant (60s).
	// Operator/test hook — envtests set a short value to keep runtime sane.
	PendingDeletionGrace time.Duration
}

// gracePeriod returns the cascade-GC two-tick confirmation window: the
// PendingDeletionGrace override when set to a positive value, otherwise the
// pendingDeletionGrace constant (60s). Centralizing the fallback keeps the
// orphan-state block below reading a single value per reconcile.
func (r *CloudflareTunnelReconciler) gracePeriod() time.Duration {
	if r.PendingDeletionGrace > 0 {
		return r.PendingDeletionGrace
	}
	return pendingDeletionGrace
}

// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflaretunnels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflaretunnels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflaretunnels/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives one iteration of the CloudflareTunnel state machine.
func (r *CloudflareTunnelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cloudflaretunnel", req.NamespacedName)

	var tn v1alpha1.CloudflareTunnel
	if err := r.Get(ctx, req.NamespacedName, &tn); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !tn.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &tn)
	}

	if reconcilelib.EnsureFinalizer(&tn, conventions.FinalizerName) {
		if err := r.Update(ctx, &tn); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	creds, halt, err := reconcilelib.LoadCredentialsHierarchical(ctx, r.Client, tn.Spec.Cloudflare, tn.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if halt != nil {
		return reconcilelib.HaltCredentialsUnavailable(ctx, r.Client, &tn, &tn.Status.Conditions, &tn.Status.Phase, halt)
	}

	// Owner-transfer (design §4.1 step 5): if the original owner was deleted
	// but >=1 attaching source remains, promote the lex-smallest live
	// candidate to controller-owner. Runs early so all subsequent reconcile
	// work sees a valid OwnerReference. Successful transfer requeues
	// immediately for a fresh, ownership-consistent view.
	if needsOwnerTransfer(&tn) {
		transferred, err := TransferOwnershipIfNeeded(ctx, r.Client, r.Scheme, &tn, r.Recorder)
		if err != nil {
			return ctrl.Result{}, err
		}
		if transferred {
			return ctrl.Result{Requeue: true}, nil
		}
		// No live candidates this pass (all NotFound / terminating). Fall
		// through; orphan-state management (later in this function) handles
		// the case where AttachedSources stabilizes as empty.
	}

	// Snapshot status before reconcile work so the trailing Status().Update
	// can be skipped when nothing material changed. Avoids apiserver/watcher
	// churn from stamping LastSyncedAt = time.Now() every pass.
	originalStatus := tn.Status.DeepCopy()

	tc, err := r.TunnelClientFn(creds)
	if err != nil {
		return ctrl.Result{}, err
	}

	priorTunnelID := tn.Status.TunnelID
	if err := r.ensureTunnel(ctx, &tn, tc, creds.AccountID); err != nil {
		return ctrl.Result{}, r.failStatus(ctx, &tn, conventions.ReasonTunnelCreating, err)
	}
	if priorTunnelID == "" && tn.Status.TunnelID != "" && r.Recorder != nil {
		// First-time create-or-adopt transition. Emit a single Event so
		// operators can correlate the cluster object with the Cloudflare-side
		// tunnel id without tailing logs.
		r.Recorder.Eventf(&tn, corev1.EventTypeNormal, conventions.ReasonTunnelCreated,
			"tunnel %q created (id=%s)", tn.Spec.Name, tn.Status.TunnelID)
	}
	if err := r.ensureTokenSecret(ctx, &tn, tc, creds.AccountID); err != nil {
		return ctrl.Result{}, r.failStatus(ctx, &tn, conventions.ReasonTunnelCreating, err)
	}
	if err := r.ensureDataplane(ctx, &tn); err != nil {
		return ctrl.Result{}, r.failStatus(ctx, &tn, conventions.ReasonConnectorDeploying, err)
	}
	// Observe the Deployment's Available condition before applying remote
	// config so the rollup can gate Ready=True on dataplane readiness
	// (design §8 step 9). A degraded Deployment must not report Ready=True
	// even if connectors have lingered from a previous successful rollout.
	depAvailable := r.isDeploymentAvailable(ctx, &tn)
	if err := r.applyRemoteConfig(ctx, &tn, tc, creds.AccountID); err != nil {
		return ctrl.Result{}, r.failStatus(ctx, &tn, conventions.ReasonRemoteConfigStale, err)
	}
	r.observeConnectors(ctx, &tn, tc, creds.AccountID)
	r.observeAttachedSources(&tn)
	r.rollupStatus(&tn, depAvailable)

	// pendingRequeueAfter, when >0, overrides the default interval at the
	// trailing return (used by the cascade-GC grace window).
	var pendingRequeueAfter time.Duration

	// Orphan-state management (design §4.1 step 10). Only auto-created CRs are
	// eligible for cascade-GC; direct-create CRs are never auto-removed.
	if isAutoCreated(&tn) {
		grace := r.gracePeriod()
		if isOrphaned(&tn) {
			switch {
			case tn.Status.LastOrphanedAt == nil:
				// First orphan observation: stamp now. The change-detection
				// gate below persists it (LastOrphanedAt is not masked).
				// Requeue after the grace window for the confirmation tick.
				now := metav1.Now()
				tn.Status.LastOrphanedAt = &now
				pendingRequeueAfter = grace
			case time.Since(tn.Status.LastOrphanedAt.Time) >= grace:
				// Two-tick confirmed: still orphaned past the grace window.
				if r.Recorder != nil {
					r.Recorder.Eventf(&tn, corev1.EventTypeWarning, conventions.ReasonTerminalNoSources,
						"no remaining sources after %s; self-deleting", grace)
				}
				tn.Status.Conditions = reconcilelib.SetReady(tn.Status.Conditions, metav1.ConditionFalse,
					conventions.ReasonTerminalNoSources, "auto-created tunnel has no remaining sources")
				// Best-effort terminal status so observers see the final
				// Ready=False+TerminalNoSources transition before deletion.
				_ = r.Status().Update(ctx, &tn)
				if err := r.Delete(ctx, &tn); err != nil {
					return ctrl.Result{}, fmt.Errorf("self-delete: %w", err)
				}
				return ctrl.Result{}, nil
			default:
				// Within grace: requeue when the remaining window elapses.
				pendingRequeueAfter = grace - time.Since(tn.Status.LastOrphanedAt.Time)
			}
		} else if tn.Status.LastOrphanedAt != nil {
			// State moved away from orphaned (source attached / owner
			// promoted): clear the stamp (persisted by the gate below).
			tn.Status.LastOrphanedAt = nil
		}
	}

	// Only persist status when something material changed or spec generation
	// advanced. Mask LastSyncedAt and ObservedGeneration from the comparison
	// since they would otherwise force a write every pass.
	candidate := tn.Status.DeepCopy()
	candidate.LastSyncedAt = originalStatus.LastSyncedAt
	candidate.ObservedGeneration = originalStatus.ObservedGeneration
	if tn.Generation != originalStatus.ObservedGeneration || !equality.Semantic.DeepEqual(originalStatus, candidate) {
		now := metav1.Time{Time: time.Now()}
		tn.Status.LastSyncedAt = &now
		tn.Status.ObservedGeneration = tn.Generation
		if err := r.Status().Update(ctx, &tn); err != nil {
			return ctrl.Result{}, err
		}
	}

	interval := defaultTunnelInterval
	if tn.Spec.Interval != nil && tn.Spec.Interval.Duration > 0 {
		interval = tn.Spec.Interval.Duration
	}
	logger.V(1).Info("reconciled", "tunnelID", tn.Status.TunnelID, "connectors", tn.Status.ConnectionsHealthy)
	if pendingRequeueAfter > 0 && pendingRequeueAfter < interval {
		return ctrl.Result{RequeueAfter: pendingRequeueAfter}, nil
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

// reconcileDelete runs the finalizer drain sequence (design §8.1):
//  1. Scale the cloudflared Deployment to 0 replicas.
//  2. Wait until observed Pod count is zero (requeue).
//  3. DELETE /cfd_tunnel/{id}/connections.
//  4. DELETE /cfd_tunnel/{id}.
//  5. Best-effort delete the operator-owned Secret + Service + Deployment.
//  6. Remove the finalizer.
//
// Steps 3 + 4 route through reconcilelib.WrapDeleteErr so a Cloudflare 404
// (already-deleted state) collapses to nil rather than stranding the
// finalizer.
func (r *CloudflareTunnelReconciler) reconcileDelete(ctx context.Context, tn *v1alpha1.CloudflareTunnel) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1+2. Scale Deployment to 0, wait for Pods gone.
	dep := &appsv1.Deployment{}
	depKey := types.NamespacedName{Name: dataplaneName(tn), Namespace: tn.Namespace}
	depFound := false
	if err := r.Get(ctx, depKey, dep); err == nil {
		depFound = true
		zero := int32(0)
		if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 0 {
			dep.Spec.Replicas = &zero
			if uerr := r.Update(ctx, dep); uerr != nil {
				return ctrl.Result{}, uerr
			}
			return ctrl.Result{RequeueAfter: drainRequeueInterval}, nil
		}
		if dep.Status.Replicas > 0 {
			return ctrl.Result{RequeueAfter: drainRequeueInterval}, nil
		}
	} else if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	// Credentials are required for the Cloudflare-side delete. If they're
	// unavailable, halt without removing the finalizer so the user can fix
	// the credential ref.
	if tn.Status.TunnelID != "" {
		creds, halt, err := reconcilelib.LoadCredentialsHierarchical(ctx, r.Client, tn.Spec.Cloudflare, tn.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		if halt != nil {
			logger.Info("credentials unavailable on delete; leaving finalizer in place")
			return *halt, nil
		}
		tc, err := r.TunnelClientFn(creds)
		if err != nil {
			return ctrl.Result{}, err
		}

		// 3. DeleteConnections — wraps 404 to nil so a tunnel that already
		// dropped its connectors does not block the finalizer.
		if derr := reconcilelib.WrapDeleteErr(tc.DeleteConnections(ctx, creds.AccountID, tn.Status.TunnelID)); derr != nil {
			return ctrl.Result{}, fmt.Errorf("delete connections: %w", derr)
		}
		// 4. DeleteTunnel — same dual-sentinel handling.
		if derr := reconcilelib.WrapDeleteErr(tc.DeleteTunnel(ctx, creds.AccountID, tn.Status.TunnelID)); derr != nil {
			return ctrl.Result{}, fmt.Errorf("delete tunnel: %w", derr)
		}
	}

	// 5. Delete operator-owned dataplane resources (best-effort; errors are
	// logged but do not block the finalizer drop, mirroring the zone
	// bundle's approach to owned-children cleanup).
	if depFound {
		_ = client.IgnoreNotFound(r.Delete(ctx, dep))
	}
	_ = client.IgnoreNotFound(r.Delete(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: tokenSecretName(tn), Namespace: tn.Namespace},
	}))
	_ = client.IgnoreNotFound(r.Delete(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: metricsServiceName(tn), Namespace: tn.Namespace},
	}))

	// 6. Remove finalizer.
	if reconcilelib.RemoveFinalizer(tn, conventions.FinalizerName) {
		if err := r.Update(ctx, tn); err != nil {
			return ctrl.Result{}, err
		}
		if r.Recorder != nil {
			// Drain succeeded and the finalizer was just released. Surface a
			// terminal Event so operators see the drain completed cleanly
			// rather than guessing from the CR's absence.
			r.Recorder.Eventf(tn, corev1.EventTypeNormal, "TunnelDeleted",
				"tunnel %q drained and removed from Cloudflare", tn.Spec.Name)
		}
	}
	return ctrl.Result{}, nil
}

// ensureTunnel: create-or-adopt by ID-first, name-second. Mirrors the zone
// reconciler's flow.
func (r *CloudflareTunnelReconciler) ensureTunnel(
	ctx context.Context,
	tn *v1alpha1.CloudflareTunnel,
	tc cloudflare.TunnelClient,
	accountID string,
) error {
	if tn.Status.TunnelID != "" {
		got, err := tc.GetTunnel(ctx, accountID, tn.Status.TunnelID)
		if err == nil && got != nil {
			tn.Status.TunnelCNAME = got.ID + ".cfargotunnel.com"
			return nil
		}
		// Fallthrough on error: try list-by-name then create.
	}
	list, err := tc.ListTunnelsByName(ctx, accountID, tn.Spec.Name)
	if err != nil {
		return err
	}
	if len(list) > 0 {
		tn.Status.TunnelID = list[0].ID
		tn.Status.TunnelCNAME = list[0].ID + ".cfargotunnel.com"
		return nil
	}
	created, err := tc.CreateTunnel(ctx, accountID, cloudflare.CreateTunnelParams{Name: tn.Spec.Name})
	if err != nil {
		return err
	}
	tn.Status.TunnelID = created.ID
	tn.Status.TunnelCNAME = created.ID + ".cfargotunnel.com"
	return nil
}

// ensureTokenSecret fetches the connector-join token and SSAs it into a
// stable Secret name. The token is opaque and must not be logged.
//
// Idempotency: connector tokens are bound to the Cloudflare TunnelID and are
// long-lived. To avoid burning a GetToken API call + Secret SSA on every
// reconcile, we first check whether a Secret already exists, has a non-empty
// token, and is annotated with the current TunnelID. If so, we skip both the
// API fetch and the SSA. The Secret is re-fetched when missing, when the
// token field is empty, or when the annotated TunnelID drifts (tunnel
// rotation).
func (r *CloudflareTunnelReconciler) ensureTokenSecret(
	ctx context.Context,
	tn *v1alpha1.CloudflareTunnel,
	tc cloudflare.TunnelClient,
	accountID string,
) error {
	secName := TokenSecretName(tn.Name)
	var existing corev1.Secret
	getErr := r.Get(ctx, types.NamespacedName{Namespace: tn.Namespace, Name: secName}, &existing)
	if getErr == nil {
		if len(existing.Data["token"]) > 0 && existing.Annotations[annotationTokenTunnelID] == tn.Status.TunnelID {
			return nil
		}
	} else if !apierrors.IsNotFound(getErr) {
		return getErr
	}

	tok, err := tc.GetToken(ctx, accountID, tn.Status.TunnelID)
	if err != nil {
		return err
	}
	sec := BuildTokenSecret(tn.Name, tn.Namespace, string(tok), tn.Status.TunnelID)
	if err := reconcilelib.SetControllerOwner(tn, sec, r.Scheme); err != nil {
		return err
	}
	return reconcilelib.Apply(ctx, r.Client, sec)
}

// ensureDataplane SSAs the cloudflared Deployment + metrics Service. All
// owner-reffed to the tunnel so cascade delete cleans them up if the
// finalizer drain is short-circuited.
func (r *CloudflareTunnelReconciler) ensureDataplane(ctx context.Context, tn *v1alpha1.CloudflareTunnel) error {
	dep := BuildDeployment(tn, r.resolvedDefaultImage())
	if err := reconcilelib.SetControllerOwner(tn, dep, r.Scheme); err != nil {
		return err
	}
	if err := reconcilelib.Apply(ctx, r.Client, dep); err != nil {
		return err
	}
	svc := BuildMetricsService(tn.Name, tn.Namespace)
	if err := reconcilelib.SetControllerOwner(tn, svc, r.Scheme); err != nil {
		return err
	}
	return reconcilelib.Apply(ctx, r.Client, svc)
}

// applyRemoteConfig: compute effective ingress from the shared cache, append
// the catch-all, detect out-of-band drift (live config vs observed → a single
// DriftDetected Warning Event, detection only), then diff against
// status.observedIngress and PUT if different. The PUT is a full replace —
// Cloudflare has no merge semantics on /configurations.
func (r *CloudflareTunnelReconciler) applyRemoteConfig(
	ctx context.Context,
	tn *v1alpha1.CloudflareTunnel,
	tc cloudflare.TunnelClient,
	accountID string,
) error {
	contribs := r.Cache.Snapshot(tunnelsynth.TunnelKey{Namespace: tn.Namespace, Name: tn.Name})
	catchAll := "http_status:404"
	if tn.Spec.Routing != nil && tn.Spec.Routing.Fallback != nil {
		if tn.Spec.Routing.Fallback.URL != nil {
			catchAll = *tn.Spec.Routing.Fallback.URL
		} else if tn.Spec.Routing.Fallback.HTTPStatus != nil {
			catchAll = fmt.Sprintf("http_status:%d", *tn.Spec.Routing.Fallback.HTTPStatus)
		}
	}
	cfg, conflicts := tunnelsynth.Resolve(contribs, tunnelsynth.ResolveOpts{CatchAllService: catchAll})
	if len(conflicts) > 0 {
		logger := log.FromContext(ctx)
		for _, conflict := range conflicts {
			logger.V(1).Info("hostname contributed by multiple sources; winner chosen lexicographically",
				"hostname", conflict.Hostname,
				"winnerKind", conflict.Winner.Kind,
				"winnerName", conflict.Winner.Name,
				"winnerNamespace", conflict.Winner.Namespace,
				"loserKind", conflict.Loser.Kind,
				"loserName", conflict.Loser.Name,
				"loserNamespace", conflict.Loser.Namespace,
			)
			// Acceptance §12.9 — stamp a DuplicateHostname Event on the loser
			// source object so users see the conflict on the object they own,
			// not just on the tunnel CR. Best-effort: a NotFound (loser
			// deleted between cache-write and conflict-emit) is swallowed.
			if err := r.emitDuplicateHostnameEvent(ctx, conflict); err != nil && !apierrors.IsNotFound(err) {
				logger.V(1).Info("emit DuplicateHostname event failed", "err", err.Error())
			}
		}
	}

	// Out-of-band drift detection (Design E2): ask Cloudflare for the live
	// tunnel config and compare it to what we last observed/pushed. A
	// divergence means someone edited the config via the dashboard or
	// another tool between reconciles. We surface this as a single
	// DriftDetected Warning Event for operator visibility — detection only;
	// the operator does NOT force a re-push here (the existing PUT path
	// below already restores operator-desired config whenever the
	// operator's own inputs changed). Best-effort: a GetConfiguration error
	// must not fail the reconcile.
	//
	// Guarded on a populated baseline: before the first PUT there is no
	// observed config to drift from, and an empty live config vs a nil
	// ObservedIngress would otherwise reflect.DeepEqual-mismatch and emit a
	// spurious DriftDetected on the very first reconcile.
	//
	// The comparison is order-sensitive by design: it reuses
	// snapshotFromConfig so it shares the exact basis the PUT-skip below
	// (line ~435) already trusts — diverging here would make drift and
	// PUT-skip disagree. This assumes Cloudflare preserves ingress order
	// (cloudflared ingress is semantically ordered: the catch-all is last).
	// If DriftDetected ever becomes chronically noisy, server-side ingress
	// reordering is the first thing to check here.
	if tn.Status.TunnelID != "" && len(tn.Status.ObservedIngress) > 0 {
		if live, gerr := tc.GetConfiguration(ctx, accountID, tn.Status.TunnelID); gerr != nil {
			log.FromContext(ctx).V(1).Info("GetConfiguration drift-check failed (continuing)", "err", gerr.Error())
		} else if live != nil && !reflect.DeepEqual(snapshotFromConfig(live.Config), tn.Status.ObservedIngress) {
			if r.Recorder != nil {
				r.Recorder.Eventf(tn, corev1.EventTypeWarning, conventions.ReasonDriftDetected,
					"live tunnel configuration differs from operator-observed config (out-of-band edit detected)")
			}
		}
	}

	wantSnap := snapshotFromConfig(cfg)
	if reflect.DeepEqual(wantSnap, tn.Status.ObservedIngress) {
		return nil
	}
	if _, err := tc.PutConfiguration(ctx, accountID, tn.Status.TunnelID, cfg); err != nil {
		return err
	}
	tn.Status.ObservedIngress = wantSnap
	return nil
}

// emitDuplicateHostnameEvent stamps a Warning Event on the loser source
// object naming the winner. Best-effort — the caller swallows IsNotFound
// (loser deleted between cache-write and emit) and logs anything else at
// V(1) without failing the reconcile.
func (r *CloudflareTunnelReconciler) emitDuplicateHostnameEvent(ctx context.Context, c tunnelsynth.Conflict) error {
	if r.Recorder == nil {
		return nil
	}
	loser, err := r.fetchSource(ctx, c.Loser)
	if err != nil {
		return err
	}
	r.Recorder.Eventf(loser, corev1.EventTypeWarning, conventions.ReasonDuplicateHostname,
		"hostname %q already claimed by %s %s/%s",
		c.Hostname, c.Winner.Kind, c.Winner.Namespace, c.Winner.Name)
	return nil
}

// fetchSource resolves a tunnelsynth.SourceKey to its concrete typed object
// so the EventRecorder can attach Events to the right kind. Returns the
// concrete pointer (Service / Gateway / HTTPRoute / TLSRoute) and any Get
// error verbatim (IsNotFound is the caller's responsibility).
func (r *CloudflareTunnelReconciler) fetchSource(ctx context.Context, src tunnelsynth.SourceKey) (client.Object, error) {
	key := types.NamespacedName{Namespace: src.Namespace, Name: src.Name}
	switch src.Kind {
	case "Service":
		var s corev1.Service
		if err := r.Get(ctx, key, &s); err != nil {
			return nil, err
		}
		return &s, nil
	case "Gateway":
		var g gwv1.Gateway
		if err := r.Get(ctx, key, &g); err != nil {
			return nil, err
		}
		return &g, nil
	case "HTTPRoute":
		var h gwv1.HTTPRoute
		if err := r.Get(ctx, key, &h); err != nil {
			return nil, err
		}
		return &h, nil
	case "TLSRoute":
		var t gwv1a2.TLSRoute
		if err := r.Get(ctx, key, &t); err != nil {
			return nil, err
		}
		return &t, nil
	}
	return nil, fmt.Errorf("unknown source kind %q", src.Kind)
}

// snapshotFromConfig converts a cf.TunnelConfig into the status-side
// IngressEntrySnapshot list used for drift detection.
func snapshotFromConfig(cfg cloudflare.TunnelConfig) []v1alpha1.IngressEntrySnapshot {
	out := make([]v1alpha1.IngressEntrySnapshot, 0, len(cfg.Ingress))
	for _, e := range cfg.Ingress {
		out = append(out, v1alpha1.IngressEntrySnapshot{Hostname: e.Hostname, Path: e.Path, Service: e.Service})
	}
	return out
}

// observeConnectors populates ConnectionsHealthy from the Cloudflare API.
// Errors are swallowed (logged at V(1)) — connector observation is best-
// effort and must not block the reconcile.
func (r *CloudflareTunnelReconciler) observeConnectors(
	ctx context.Context,
	tn *v1alpha1.CloudflareTunnel,
	tc cloudflare.TunnelClient,
	accountID string,
) {
	if tn.Status.TunnelID == "" {
		return
	}
	conns, err := tc.ListConnections(ctx, accountID, tn.Status.TunnelID)
	if err != nil {
		log.FromContext(ctx).V(1).Info("ListConnections failed (continuing)", "err", err.Error())
		return
	}
	// #nosec G115 — connector count is bounded (<=4 per cloudflared replica,
	// <=25 replicas per CRD validation) and fits int32 trivially.
	tn.Status.ConnectionsHealthy = int32(len(conns))
}

// observeAttachedSources mirrors the in-memory cache's source list into
// status.attachedSources. Sorted by Cache.AttachedSources (which returns in
// insertion order); for determinism we sort here.
func (r *CloudflareTunnelReconciler) observeAttachedSources(tn *v1alpha1.CloudflareTunnel) {
	srcs := r.Cache.AttachedSources(tunnelsynth.TunnelKey{Namespace: tn.Namespace, Name: tn.Name})
	out := make([]v1alpha1.AttachedSource, 0, len(srcs))
	for _, s := range srcs {
		out = append(out, v1alpha1.AttachedSource{Kind: s.Kind, Name: s.Name, Namespace: s.Namespace})
	}
	tn.Status.AttachedSources = out
}

// rollupStatus derives the Ready / ConnectorReady / RemoteConfigApplied
// conditions plus Phase from the observed state. Inner-mutate only — the
// outer Reconcile persists.
//
// Per design §8 step 9, Ready=True requires all of:
//   - tunnel created (TunnelID set),
//   - dataplane Deployment Available (depAvailable),
//   - at least one connector connected (ConnectionsHealthy > 0),
//   - remote-config applied (set by the rollup whenever the preceding gates
//     hold or the dataplane / connector gates are the only obstacle).
func (r *CloudflareTunnelReconciler) rollupStatus(tn *v1alpha1.CloudflareTunnel, depAvailable bool) {
	switch {
	case tn.Status.TunnelID == "":
		tn.Status.Conditions = reconcilelib.SetReady(tn.Status.Conditions,
			metav1.ConditionFalse, conventions.ReasonTunnelCreating, "tunnel not yet created")
	case !depAvailable:
		tn.Status.Conditions = reconcilelib.SetReady(tn.Status.Conditions,
			metav1.ConditionFalse, conventions.ReasonConnectorDeploying, "cloudflared Deployment not yet Available")
		tn.Status.Conditions = reconcilelib.SetCondition(tn.Status.Conditions,
			conventions.ConditionTypeRemoteConfigApplied, metav1.ConditionTrue,
			conventions.ReasonRemoteConfigApplied, "")
	case tn.Status.ConnectionsHealthy == 0:
		tn.Status.Conditions = reconcilelib.SetReady(tn.Status.Conditions,
			metav1.ConditionFalse, conventions.ReasonNoConnectors, "no active connectors yet")
		tn.Status.Conditions = reconcilelib.SetCondition(tn.Status.Conditions,
			conventions.ConditionTypeRemoteConfigApplied, metav1.ConditionTrue,
			conventions.ReasonRemoteConfigApplied, "")
	default:
		tn.Status.Conditions = reconcilelib.SetReady(tn.Status.Conditions,
			metav1.ConditionTrue, conventions.ReasonReady, "")
		tn.Status.Conditions = reconcilelib.SetCondition(tn.Status.Conditions,
			conventions.ConditionTypeConnectorReady, metav1.ConditionTrue,
			conventions.ReasonConnectorReady, "")
		tn.Status.Conditions = reconcilelib.SetCondition(tn.Status.Conditions,
			conventions.ConditionTypeRemoteConfigApplied, metav1.ConditionTrue,
			conventions.ReasonRemoteConfigApplied, "")
	}
	readyStatus, readyReason := readyFromConditions(tn.Status.Conditions)
	tn.Status.Phase = reconcilelib.DerivePhase(readyStatus, readyReason)
}

// isDeploymentAvailable reads the operator-managed cloudflared Deployment's
// Status.Conditions and reports whether DeploymentAvailable=True. Returns
// false when the Deployment doesn't exist yet, hasn't reported a status, or
// the Get fails — a transient client error must not flip Ready in either
// direction; the next reconcile re-checks.
func (r *CloudflareTunnelReconciler) isDeploymentAvailable(ctx context.Context, tn *v1alpha1.CloudflareTunnel) bool {
	var dep appsv1.Deployment
	key := types.NamespacedName{Name: dataplaneName(tn), Namespace: tn.Namespace}
	if err := r.Get(ctx, key, &dep); err != nil {
		return false
	}
	for _, c := range dep.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// readyFromConditions extracts the Ready condition's status + reason. Used
// to feed reconcilelib.DerivePhase, which expects them as separate inputs.
func readyFromConditions(conds []metav1.Condition) (metav1.ConditionStatus, string) {
	for _, c := range conds {
		if c.Type == conventions.ConditionTypeReady {
			return c.Status, c.Reason
		}
	}
	return metav1.ConditionUnknown, ""
}

// failStatus stamps a Ready=False condition with the given reason + error
// message and persists. Returns the wrapped error so the outer Reconcile
// loop requeues. Defensive — the main flow's helpers normally only mutate
// in-memory and let the outer Status().Update persist, but on early-exit
// errors we want the failure reason visible to operators immediately.
func (r *CloudflareTunnelReconciler) failStatus(ctx context.Context, tn *v1alpha1.CloudflareTunnel, reason string, err error) error {
	tn.Status.Conditions = reconcilelib.SetReady(tn.Status.Conditions, metav1.ConditionFalse, reason, err.Error())
	tn.Status.Phase = reconcilelib.DerivePhase(metav1.ConditionFalse, reason)
	_ = r.Status().Update(ctx, tn)
	return err
}

// resolvedDefaultImage returns the reconciler's configured DefaultImage,
// falling back to the package-level pin if the field is empty (e.g.
// constructed via zero-value in a test). Keeps the production wire-up
// declarative and the tests honest.
func (r *CloudflareTunnelReconciler) resolvedDefaultImage() string {
	if r.DefaultImage != "" {
		return r.DefaultImage
	}
	return DefaultCloudflaredImage
}

// Compile-time guard: CloudflareTunnelReconciler implements reconcile.Reconciler.
var _ reconcile.Reconciler = (*CloudflareTunnelReconciler)(nil)
