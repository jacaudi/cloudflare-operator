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
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// TLSRouteSourceReconciler watches TLSRoutes (gateway-api v1alpha2) attached
// via parentRefs to Gateways carrying cloudflare.io/tunnel="true". For each
// tunnel-targeted parent (only ONE is honored per design §4.2 / Q3 lock):
//   - synthesize one tcp:// IngressContribution per route hostname routed at
//     the Gateway's underlying Service;
//   - emit one CloudflareDNSRecord per route hostname (CNAME → Gateway apex,
//     the chain hop that stabilizes per-Route DNS across tunnel recreation);
//   - write `Accepted=True / TunnelAttached` AND `PartiallyInvalid=True /
//     ClientSideClientRequired` on the tunnel-targeted parent's status entry.
//
// The ClientSideClientRequired warning is ALWAYS stamped (per design §4.3):
// TLSRoute hostnames terminate outside the browser model, so they are
// reachable only via `cloudflared access tcp` or WARP — a hard fact about
// the access surface that operators need to see before pointing DNS.
//
// Differs from the HTTPRoute source reconciler only in:
//   - works on *gwv1a2.TLSRoute instead of *gwv1.HTTPRoute;
//   - tcp:// service URL instead of http(s)://;
//   - translator call: TranslateTLSRoute always returns the
//     ClientSideClientRequired warning, which is surfaced as
//     PartiallyInvalid=True.
//
// Multi-parent contract identical to HTTPRoute: other controllers' status
// entries are preserved via re-fetch + memory-merge + Update.
//
// Gateway-service discovery is annotation-only (cloudflare.io/gateway-service)
// via the shared resolveGatewayService helper — no label fallback.
//
// Stale-key sweep: uses the shared cacheTracker (attach.go) — on annotation
// change or Route delete the prior tunnel-key's cache entry is cleared. The
// tracker is initialized once via ensureTracker, called at the top of every
// Reconcile and guarded by sync.Once so concurrent reconciles on the same
// instance (MaxConcurrentReconciles > 1) cannot race to allocate competing
// trackers and lose prior-attachment state.
type TLSRouteSourceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Cache    *tunnelsynth.Cache
	Recorder record.EventRecorder

	tracker     *cacheTracker
	trackerOnce sync.Once
	dedupe      *eventDedupe // D2 event dedupe; lazy-inited inside trackerOnce.
}

// ensureTracker initializes r.tracker exactly once. Safe against concurrent
// callers (controller-runtime worker pool with MaxConcurrentReconciles > 1).
// Idempotent: tests that pre-seed r.tracker keep their fixture untouched.
func (r *TLSRouteSourceReconciler) ensureTracker() {
	r.trackerOnce.Do(func() {
		if r.tracker == nil {
			r.tracker = newCacheTracker()
		}
		if r.dedupe == nil {
			r.dedupe = newEventDedupe(0, 0)
		}
	})
}

// +kubebuilder:rbac:groups="gateway.networking.k8s.io",resources=tlsroutes,verbs=get;list;watch
// +kubebuilder:rbac:groups="gateway.networking.k8s.io",resources=tlsroutes/status,verbs=update;patch
// +kubebuilder:rbac:groups="gateway.networking.k8s.io",resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflaretunnels,verbs=get;list;watch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarednsrecords,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives one iteration of the TLSRoute-source state machine.
func (r *TLSRouteSourceReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("tlsroute", req.NamespacedName)
	r.ensureTracker()
	srcKey := tunnelsynth.SourceKey{Kind: "TLSRoute", Namespace: req.Namespace, Name: req.Name}

	var rt gwv1a2.TLSRoute
	if err := r.Get(ctx, req.NamespacedName, &rt); err != nil {
		if apierrors.IsNotFound(err) {
			// Route deleted — clear our prior cache entry via the tracker.
			if prev, ok := r.tracker.sweep(srcKey); ok {
				r.Cache.Clear(prev, srcKey)
			}
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// Find the tunnel-targeted parent (only one is honored per design §4.2 /
	// Q3 lock). Returns nil parent when no parent is tunnel-targeted —
	// silent no-op (this Route belongs to other controllers).
	parent, gw, tn, gwSvc, port, err := r.findTunnelTargetedParent(ctx, &rt)
	if err != nil {
		return reconcile.Result{}, err
	}
	if parent == nil {
		if prev, ok := r.tracker.sweep(srcKey); ok {
			r.Cache.Clear(prev, srcKey)
		}
		return reconcile.Result{}, nil
	}

	// Resolve the chain CNAME content for per-route DNSRecords. Uses the
	// shared chainContentFor helper (apex.go) which checks for a valid
	// cloudflare.io/gateway-apex annotation first, then falls back to the
	// tunnel CNAME for concrete-listener Gateways. Returns blocked=true
	// when the Gateway has only wildcard listeners and no apex annotation
	// — emitting a wildcard CNAME would cause Cloudflare error 9007.
	chainContent, apexBlocked, _ := chainContentFor(gw, tn)

	gwOrigin := tunnelsynth.GatewayOrigin{
		Hostname: chainContent,
		// tcp:// per design §4.3 — cloudflared dials the Gateway's TLS
		// listener over plain TCP (the listener terminates TLS itself).
		// Port comes from the resolved Gateway service (annotation or
		// Service's first port), NOT the listener's public-facing port.
		Service: fmt.Sprintf("tcp://%s.%s.svc.cluster.local:%d", gwSvc.Name, gwSvc.Namespace, port),
		IsTLS:   true,
	}

	// Resolve effective hostnames: TLSRoute spec.hostnames, falling back to
	// the first listener hostname when the Route declares none (Gateway-API
	// attachment semantics: an empty Route Hostnames list inherits from the
	// listener). The fallback uses the listener hostname — NOT chainContent
	// — because chainContent is the CNAME target, not the route's identity.
	listenerApex := firstListenerHostname(gw)
	hostnames := make([]string, 0, len(rt.Spec.Hostnames))
	for _, h := range rt.Spec.Hostnames {
		if h != "" {
			hostnames = append(hostnames, string(h))
		}
	}
	if len(hostnames) == 0 && listenerApex != "" {
		hostnames = []string{listenerApex}
	}

	contribs, warns := tunnelsynth.TranslateTLSRoute(&rt, hostnames, gwOrigin, tunnelsynth.Defaults{})

	tunnelKey := tunnelsynth.TunnelKey{Namespace: tn.Namespace, Name: tn.Name}

	// Annotation-change sweep: clear the prior tunnel-key if it differs.
	if prev, ok := r.tracker.swap(srcKey, tunnelKey); ok {
		r.Cache.Clear(prev, srcKey)
	}
	// Register under the current key regardless of contribs length — an
	// empty registration keeps bookkeeping symmetric on reconcile thrash.
	r.Cache.Set(tunnelKey, srcKey, contribs)

	// Surface translator warnings as Events for operator visibility.
	if r.Recorder != nil {
		for _, w := range warns {
			r.dedupe.emit(r.Recorder, &rt, corev1.EventTypeWarning, w.Reason, w.Message)
		}
	}

	// Blocked: the parent Gateway has only wildcard listeners and no valid
	// cloudflare.io/gateway-apex annotation. Emitting a wildcard CNAME
	// would cause Cloudflare error 9007; instead surface a Warning and
	// requeue after a bounded interval (no hot-loop). This check must come
	// BEFORE the deferred-emission guard because apexBlocked also implies
	// chainContent == "" — without this ordering the deferred guard would
	// fire first and return without the Warning event.
	if apexBlocked {
		if r.Recorder != nil {
			r.dedupe.emit(r.Recorder, &rt, corev1.EventTypeWarning,
				conventions.ReasonGatewayApexRequired,
				"parent Gateway listener is wildcard-only; set the cloudflare.io/gateway-apex annotation on the Gateway to publish per-route records")
		}
		if err := r.writeParentStatus(ctx, &rt, *parent, warns, len(contribs) > 0); err != nil {
			logger.V(1).Info("status write failed (best-effort; ignored)", "err", err)
		}
		return reconcile.Result{RequeueAfter: reconcilelib.DefaultRequeueAfter}, nil
	}

	// Defer DNSRecord emission until the chain content is resolved AND the
	// tunnel CNAME is populated. On the concrete-listener path chainContent
	// is tn.Status.TunnelCNAME, so chainContent=="" iff TunnelCNAME=="". The
	// second clause guards the override path. Mirrors the HTTPRoute guard.
	if chainContent == "" || tn.Status.TunnelCNAME == "" {
		logger.V(1).Info("deferring DNSRecord emission until chain content + tunnel CNAME populate",
			"tunnel", tunnelKey, "chainContent", chainContent, "tunnelCNAME", tn.Status.TunnelCNAME)
		if err := r.writeParentStatus(ctx, &rt, *parent, warns, len(contribs) > 0); err != nil {
			logger.V(1).Info("status write failed (best-effort; ignored)", "err", err)
		}
		return reconcile.Result{}, nil
	}

	// Emit per-hostname DNSRecord CRs: CNAME <hostname> → <chain-content>.
	desired := make(map[string]struct{}, len(hostnames))
	for _, h := range hostnames {
		desired[h] = struct{}{}
		if err := r.emitChainDNSRecord(ctx, &rt, h, chainContent, gw); err != nil {
			return reconcile.Result{}, fmt.Errorf("emit dns record for %q: %w", h, err)
		}
	}

	// Prune previously-emitted DNSRecord CRs whose hostname is no longer in
	// the desired set. Best-effort: a prune error logs and continues — the
	// desired records are already emitted, and any surviving orphan is retried
	// on the next reconcile. Placed strictly AFTER the emit loop on the
	// post-emit path; never reached on the deferred-emission early-return
	// above (where desired would be empty and would wrongly delete live CRs).
	pruned, perr := pruneOrphanedDNSRecords(ctx, r.Client, "TLSRoute", rt.Name, rt.Namespace, desired)
	if perr != nil {
		logger.Error(perr, "orphan-prune failed (continuing)")
	} else if len(pruned) > 0 {
		r.dedupe.emit(r.Recorder, &rt, corev1.EventTypeNormal, conventions.ReasonOrphanedDNSRecordPruned,
			fmt.Sprintf("deleted %d orphaned DNSRecord CR(s) for hostnames no longer in spec", len(pruned)))
	}

	if err := r.writeParentStatus(ctx, &rt, *parent, warns, len(contribs) > 0); err != nil {
		// Status write is best-effort per design §4.2.
		logger.V(1).Info("status write failed (best-effort; ignored)", "err", err)
	}
	return reconcile.Result{}, nil
}

// findTunnelTargetedParent walks the Route's parentRefs and returns the
// FIRST parent that is a tunnel-targeted Gateway. Mirrors the HTTPRoute
// implementation's qualification chain.
//
// A parent is "tunnel-targeted" iff:
//  1. The Gateway exists and is gettable,
//  2. cloudflare.io/tunnel="true" annotation,
//  3. The derived tunnel name is valid (DNS-1123, length ≤ 52),
//  4. The CloudflareTunnel CR for that derived name exists,
//  5. cloudflare.io/gateway-service annotation resolves to a Service.
//
// Returns (nil, …) when no parent qualifies — silent no-op for the caller.
func (r *TLSRouteSourceReconciler) findTunnelTargetedParent(
	ctx context.Context,
	rt *gwv1a2.TLSRoute,
) (*gwv1.ParentReference, *gwv1.Gateway, *v2alpha1.CloudflareTunnel, *corev1.Service, int32, error) {
	return findTunnelTargetedParentRef(ctx, r.Client, rt.Namespace, rt.Spec.ParentRefs)
}

// emitChainDNSRecord upserts the chain CloudflareDNSRecord CR for this
// TLSRoute + hostname pair via the shared SSA-based helper. chainContent
// is the resolved chain target: either the cloudflare.io/gateway-apex
// override hostname (when set on the Gateway), or the tunnel CNAME directly
// for concrete-listener Gateways. Annotation drift (cloudflare.io/adopt,
// cloudflare.io/zone-ref, etc.) propagates to the emitted CR because
// EmitDNSRecord uses SSA.
//
// cloudflare.io/* annotations are merged via inheritedAnnotations: the
// route's own value wins when set; missing values fall through to the
// parent Gateway. This implements the per-route override / per-gateway
// default pattern from design §5.
//
// Operator-edits-win: a user `kubectl edit` on the emitted CR will be
// reverted on the next reconcile.
func (r *TLSRouteSourceReconciler) emitChainDNSRecord(ctx context.Context, rt *gwv1a2.TLSRoute, hostname, chainContent string, gw *gwv1.Gateway) error {
	return EmitDNSRecord(ctx, r.Client, r.Scheme, EmitOpts{
		Owner:       rt,
		OwnerKind:   "TLSRoute",
		Hostname:    hostname,
		Content:     chainContent,
		Annotations: inheritedAnnotations(rt.GetAnnotations(), gw),
	})
}

// writeParentStatus is the parent-only status write. Touches ONLY the
// tunnel-targeted parent's entry in rt.Status.Parents; preserves every other
// parent's entry. Mirrors HTTPRouteSourceReconciler.writeParentStatus.
//
// For TLSRoute the only translator-emitted warning today is
// ClientSideClientRequired (always present). It is surfaced as
// PartiallyInvalid=True; Accepted=True/TunnelAttached when at least one
// contribution lands, Accepted=False/NoListenerHostname otherwise.
func (r *TLSRouteSourceReconciler) writeParentStatus(
	ctx context.Context,
	rt *gwv1a2.TLSRoute,
	parent gwv1.ParentReference,
	warns []tunnelsynth.TranslateWarning,
	hasContribs bool,
) error {
	// Re-fetch — the live Status may have been mutated by another controller
	// after this reconcile's initial Get.
	var live gwv1a2.TLSRoute
	if err := r.Get(ctx, types.NamespacedName{Namespace: rt.Namespace, Name: rt.Name}, &live); err != nil {
		return fmt.Errorf("re-fetch tlsroute for status write: %w", err)
	}

	// Find our existing entry (by parent identity) or append a new one.
	idx := -1
	for i := range live.Status.Parents {
		if parentRefEquals(live.Status.Parents[i].ParentRef, parent) {
			idx = i
			break
		}
	}
	if idx < 0 {
		live.Status.Parents = append(live.Status.Parents, gwv1.RouteParentStatus{
			ParentRef:      parent,
			ControllerName: tunnelControllerName,
		})
		idx = len(live.Status.Parents) - 1
	}
	live.Status.Parents[idx].ControllerName = tunnelControllerName

	// Accepted: True/TunnelAttached when contribs landed; False/
	// NoListenerHostname when not (the only way contribs is empty here is no
	// resolved hostname — the Route has no Spec.Hostnames and the Gateway's
	// listener has no hostname either).
	acceptedStatus := metav1.ConditionTrue
	acceptedReason := conventions.ReasonTunnelAttached
	acceptedMsg := ""
	if !hasContribs {
		acceptedStatus = metav1.ConditionFalse
		acceptedReason = conventions.ReasonNoListenerHostname
		acceptedMsg = "no hostname resolved for TLSRoute (Route.Spec.Hostnames empty and listener hostname empty)"
	}
	conds := reconcilelib.SetCondition(
		live.Status.Parents[idx].Conditions,
		conventions.ConditionTypeAccepted, acceptedStatus, acceptedReason, acceptedMsg,
	)

	// PartiallyInvalid: surface ClientSideClientRequired always (the
	// translator always emits this warning for TLSRoute). If the warning
	// list ever grows (e.g. SNI mismatches), the last warning wins for the
	// message; the condition status remains True as long as any warning is
	// present.
	piStatus := metav1.ConditionFalse
	piReason := conventions.ReasonTunnelAttached
	var piMsg string
	for _, w := range warns {
		piStatus = metav1.ConditionTrue
		piReason = w.Reason
		piMsg = w.Message
	}
	conds = reconcilelib.SetCondition(
		conds,
		conventions.ConditionTypePartiallyInvalid, piStatus, piReason, piMsg,
	)
	live.Status.Parents[idx].Conditions = conds

	return r.Status().Update(ctx, &live)
}

var _ reconcile.Reconciler = (*TLSRouteSourceReconciler)(nil)
