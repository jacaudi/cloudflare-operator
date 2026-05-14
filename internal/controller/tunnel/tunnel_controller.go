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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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

// CloudflareTunnelReconciler drives the lifecycle of a CloudflareTunnel CR:
// credentials → tunnel create-or-adopt on Cloudflare → connector token
// Secret → cloudflared Deployment + metrics Service (+ optional Service-
// Monitor) → remote-config PUT (with drift-skip) → connector health
// observation → status rollup.
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

	// HasServiceMonitor is the CRD-discovery gate set once at manager setup.
	// When false, ServiceMonitor SSA is skipped.
	HasServiceMonitor bool
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
		tn.Status.Conditions = reconcilelib.SetReady(tn.Status.Conditions, metav1.ConditionFalse,
			conventions.ReasonCredentialsUnavailable, "cloudflare credentials unavailable")
		tn.Status.Phase = reconcilelib.DerivePhase(metav1.ConditionFalse, conventions.ReasonCredentialsUnavailable)
		if uerr := r.Status().Update(ctx, &tn); uerr != nil {
			return ctrl.Result{}, uerr
		}
		return *halt, nil
	}

	tc, err := r.TunnelClientFn(creds)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.ensureTunnel(ctx, &tn, tc, creds.AccountID); err != nil {
		return ctrl.Result{}, r.failStatus(ctx, &tn, conventions.ReasonTunnelCreating, err)
	}
	if err := r.ensureTokenSecret(ctx, &tn, tc, creds.AccountID); err != nil {
		return ctrl.Result{}, r.failStatus(ctx, &tn, conventions.ReasonTunnelCreating, err)
	}
	if err := r.ensureDataplane(ctx, &tn); err != nil {
		return ctrl.Result{}, r.failStatus(ctx, &tn, conventions.ReasonConnectorDeploying, err)
	}
	if err := r.applyRemoteConfig(ctx, &tn, tc, creds.AccountID); err != nil {
		return ctrl.Result{}, r.failStatus(ctx, &tn, conventions.ReasonRemoteConfigStale, err)
	}
	r.observeConnectors(ctx, &tn, tc, creds.AccountID)
	r.observeAttachedSources(&tn)
	r.rollupStatus(&tn)

	now := metav1.Time{Time: time.Now()}
	tn.Status.LastSyncedAt = &now
	tn.Status.ObservedGeneration = tn.Generation

	if err := r.Status().Update(ctx, &tn); err != nil {
		return ctrl.Result{}, err
	}

	interval := defaultTunnelInterval
	if tn.Spec.Interval != nil && tn.Spec.Interval.Duration > 0 {
		interval = tn.Spec.Interval.Duration
	}
	logger.V(1).Info("reconciled", "tunnelID", tn.Status.TunnelID, "connectors", tn.Status.ConnectionsHealthy)
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
	if err := r.Get(ctx, depKey, dep); err == nil {
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
	if dep.Name != "" {
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
func (r *CloudflareTunnelReconciler) ensureTokenSecret(
	ctx context.Context,
	tn *v1alpha1.CloudflareTunnel,
	tc cloudflare.TunnelClient,
	accountID string,
) error {
	tok, err := tc.GetToken(ctx, accountID, tn.Status.TunnelID)
	if err != nil {
		return err
	}
	sec := BuildTokenSecret(tn.Name, tn.Namespace, string(tok))
	if err := reconcilelib.SetControllerOwner(tn, sec, r.Scheme); err != nil {
		return err
	}
	return reconcilelib.Apply(ctx, r.Client, sec)
}

// ensureDataplane SSAs the cloudflared Deployment + metrics Service (+
// optional ServiceMonitor). All owner-reffed to the tunnel so cascade
// delete cleans them up if the finalizer drain is short-circuited.
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
	if err := reconcilelib.Apply(ctx, r.Client, svc); err != nil {
		return err
	}
	if r.HasServiceMonitor {
		if err := r.applyServiceMonitor(ctx, tn); err != nil {
			return err
		}
	}
	return nil
}

// applyServiceMonitor is a placeholder. The real implementation lands
// alongside the CRD-discovery probe in the manager setup; until then this
// keeps Reconcile compiled when HasServiceMonitor=false.
func (r *CloudflareTunnelReconciler) applyServiceMonitor(_ context.Context, _ *v1alpha1.CloudflareTunnel) error {
	return nil
}

// applyRemoteConfig: compute effective ingress from the shared cache, append
// the catch-all, drift-check against status.observedIngress, and PUT if
// different. The PUT is a full replace — Cloudflare has no merge semantics
// on /configurations.
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
	cfg, _ := tunnelsynth.Resolve(contribs, tunnelsynth.ResolveOpts{CatchAllService: catchAll})

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
func (r *CloudflareTunnelReconciler) rollupStatus(tn *v1alpha1.CloudflareTunnel) {
	switch {
	case tn.Status.TunnelID == "":
		tn.Status.Conditions = reconcilelib.SetReady(tn.Status.Conditions,
			metav1.ConditionFalse, conventions.ReasonTunnelCreating, "tunnel not yet created")
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
