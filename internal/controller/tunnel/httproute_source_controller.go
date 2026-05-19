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
	"sort"
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

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// tunnelControllerName is the Gateway-API controllerName under which the
// tunnel controller writes route parent-status entries. Per Gateway API
// conventions a controller-name is a vendored DNS-prefixed string; this is
// the operator's published identity for HTTPRoute / TLSRoute status writes.
const tunnelControllerName gwv1.GatewayController = "cloudflare.io/tunnel-controller"

// HTTPRouteSourceReconciler watches HTTPRoutes attached (via parentRefs) to
// Gateways carrying cloudflare.io/tunnel="true". For each tunnel-targeted
// parent (only ONE is honored per design §4.2 / Q3 lock):
//   - synthesize one IngressContribution per (hostname × rule) routed at the
//     Gateway's underlying Service;
//   - emit one CloudflareDNSRecord per route hostname (CNAME → Gateway apex,
//     the chain hop that stabilizes per-Route DNS across tunnel recreation);
//   - write `Accepted` / `PartiallyInvalid` on the tunnel-targeted parent's
//     status entry ONLY.
//
// Multi-parent contract (§4.2 / Q3 lock): other parents are untouched. Other
// controllers' status entries on the Route are preserved via memory-merge
// (re-fetch + replace-only-our-entry + Update). HTTPRoutes never auto-create
// CloudflareTunnel CRs — the Gateway is the trigger; this controller resolves
// the tunnel by lookup against the parent Gateway's annotations.
//
// Gateway-service discovery is annotation-only (`cloudflare.io/gateway-service`).
// No label fallback — every Gateway implementation exposes its listener
// Service under a different convention, so explicit annotation is the only
// reliable contract.
//
// Stale-key sweep: uses the shared cacheTracker (attach.go). On annotation
// change or Route delete the prior tunnel-key's cache entry is cleared.
// The tracker is initialized once via ensureTracker, called at the top of
// every Reconcile and guarded by sync.Once so concurrent reconciles on the
// same instance (MaxConcurrentReconciles > 1) cannot race to allocate
// competing trackers and lose prior-attachment state.
type HTTPRouteSourceReconciler struct {
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
func (r *HTTPRouteSourceReconciler) ensureTracker() {
	r.trackerOnce.Do(func() {
		if r.tracker == nil {
			r.tracker = newCacheTracker()
		}
		if r.dedupe == nil {
			r.dedupe = newEventDedupe(0, 0)
		}
	})
}

// +kubebuilder:rbac:groups="gateway.networking.k8s.io",resources=httproutes,verbs=get;list;watch
// +kubebuilder:rbac:groups="gateway.networking.k8s.io",resources=httproutes/status,verbs=update;patch
// +kubebuilder:rbac:groups="gateway.networking.k8s.io",resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflaretunnels,verbs=get;list;watch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarednsrecords,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives one iteration of the HTTPRoute-source state machine.
func (r *HTTPRouteSourceReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("httproute", req.NamespacedName)
	r.ensureTracker()
	srcKey := tunnelsynth.SourceKey{Kind: "HTTPRoute", Namespace: req.Namespace, Name: req.Name}

	var rt gwv1.HTTPRoute
	if err := r.Get(ctx, req.NamespacedName, &rt); err != nil {
		if apierrors.IsNotFound(err) {
			// Route deleted — clear our prior cache entry. The cacheTracker
			// knows which tunnel-key this source last attached to so we
			// don't have to re-parse the (now-missing) Route to find it.
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
		// Sweep any prior attachment — the Route was previously tunnel-
		// targeted, then its parents changed. Don't leak cache entries.
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
		Service:  fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", gwSvc.Name, gwSvc.Namespace, port),
	}
	contribs, warns := tunnelsynth.TranslateHTTPRoute(&rt, gwOrigin, tunnelsynth.Defaults{})

	tunnelKey := tunnelsynth.TunnelKey{Namespace: tn.Namespace, Name: tn.Name}

	// Annotation-change sweep: if the Route was previously attached to a
	// different tunnel-key (e.g. parent Gateway swapped tunnel-name), clear
	// the prior entry.
	if prev, ok := r.tracker.swap(srcKey, tunnelKey); ok {
		r.Cache.Clear(prev, srcKey)
	}
	// Register under the current key regardless of contribs length — an
	// empty registration keeps bookkeeping symmetric when every rule is
	// dropped (e.g. all rules carry incompatible filters) without
	// surrendering the sweep contract.
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
	// second clause guards the override path. Without a resolved content we
	// can't build the chain; without a tunnel CNAME the apex itself won't
	// resolve yet. Status write still happens so operators see progress.
	if chainContent == "" || tn.Status.TunnelCNAME == "" {
		logger.V(1).Info("deferring DNSRecord emission until chain content + tunnel CNAME populate",
			"tunnel", tunnelKey, "chainContent", chainContent, "tunnelCNAME", tn.Status.TunnelCNAME)
		if err := r.writeParentStatus(ctx, &rt, *parent, warns, len(contribs) > 0); err != nil {
			logger.V(1).Info("status write failed (best-effort; ignored)", "err", err)
		}
		return reconcile.Result{}, nil
	}

	// Emit per-Route DNSRecord CRs: CNAME <hostname> → <chain-content>.
	desired := make(map[string]struct{}, len(rt.Spec.Hostnames))
	for _, h := range rt.Spec.Hostnames {
		desired[string(h)] = struct{}{}
		if err := r.emitChainDNSRecord(ctx, &rt, string(h), chainContent, gw); err != nil {
			return reconcile.Result{}, fmt.Errorf("emit dns record for %q: %w", h, err)
		}
	}

	// Prune previously-emitted DNSRecord CRs whose hostname is no longer in
	// the desired set. Best-effort: a prune error logs and continues — the
	// desired records are already emitted, and any surviving orphan is retried
	// on the next reconcile. Placed strictly AFTER the emit loop on the
	// post-emit path; never reached on the deferred-emission early-return
	// above (where desired would be empty and would wrongly delete live CRs).
	pruned, perr := pruneOrphanedDNSRecords(ctx, r.Client, "HTTPRoute", rt.Name, rt.Namespace, desired)
	if perr != nil {
		logger.Error(perr, "orphan-prune failed (continuing)")
	} else if len(pruned) > 0 {
		r.dedupe.emit(r.Recorder, &rt, corev1.EventTypeNormal, conventions.ReasonOrphanedDNSRecordPruned,
			fmt.Sprintf("deleted %d orphaned DNSRecord CR(s) for hostnames no longer in spec", len(pruned)))
	}

	if err := r.writeParentStatus(ctx, &rt, *parent, warns, len(contribs) > 0); err != nil {
		// Status write is best-effort per design §4.2 ("best-effort Accepted
		// / PartiallyInvalid status writes on Routes as a courtesy"). Don't
		// fail the reconcile on a status write race — the next event
		// triggers another pass.
		logger.V(1).Info("status write failed (best-effort; ignored)", "err", err)
	}
	return reconcile.Result{}, nil
}

// findTunnelTargetedParent walks the Route's parentRefs and returns the
// FIRST parent that is a tunnel-targeted Gateway. Per design §4.2 / Q3 lock,
// only one parent should be tunnel-targeted; if multiple are present the
// first wins (the design is silent on multi-tunnel-targeted-parents and we
// don't hallucinate behavior here).
//
// A parent is "tunnel-targeted" iff:
//  1. The Gateway object exists and is gettable,
//  2. cloudflare.io/tunnel="true" annotation,
//  3. The derived tunnel name is valid (DNS-1123, length ≤ 52),
//  4. The CloudflareTunnel CR for that derived name exists,
//  5. cloudflare.io/gateway-service annotation resolves to a Service.
//
// Returns (nil, …) when no parent qualifies — caller treats as a silent
// no-op.
func (r *HTTPRouteSourceReconciler) findTunnelTargetedParent(
	ctx context.Context,
	rt *gwv1.HTTPRoute,
) (*gwv1.ParentReference, *gwv1.Gateway, *v2alpha1.CloudflareTunnel, *corev1.Service, int32, error) {
	return findTunnelTargetedParentRef(ctx, r.Client, rt.Namespace, rt.Spec.ParentRefs)
}

// firstListenerHostname returns the lexicographically-smallest non-empty
// hostname across the Gateway's listeners; used as the TLSRoute
// route-identity fallback when TLSRoute.Spec.Hostnames is empty. Apiserver
// listener order is not stable across reconciles, so we sort to guarantee
// lexicographic determinism — otherwise two reconciles of the same Gateway
// could pick different hostnames when multiple listeners declare hostnames.
func firstListenerHostname(gw *gwv1.Gateway) string {
	hosts := make([]string, 0, len(gw.Spec.Listeners))
	for _, l := range gw.Spec.Listeners {
		if l.Hostname == nil || *l.Hostname == "" {
			continue
		}
		hosts = append(hosts, string(*l.Hostname))
	}
	if len(hosts) == 0 {
		return ""
	}
	sort.Strings(hosts)
	return hosts[0]
}

// emitChainDNSRecord upserts the chain CloudflareDNSRecord CR for this
// HTTPRoute + hostname pair via the shared SSA-based helper. chainContent
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
func (r *HTTPRouteSourceReconciler) emitChainDNSRecord(ctx context.Context, rt *gwv1.HTTPRoute, hostname, chainContent string, gw *gwv1.Gateway) error {
	return EmitDNSRecord(ctx, r.Client, r.Scheme, EmitOpts{
		Owner:       rt,
		OwnerKind:   "HTTPRoute",
		Hostname:    hostname,
		Content:     chainContent,
		Annotations: inheritedAnnotations(rt.GetAnnotations(), gw),
	})
}

// writeParentStatus is the parent-only status write. Touches ONLY the
// tunnel-targeted parent's entry in rt.Status.Parents; preserves every other
// parent's entry (which may have been written by other controllers — Gateway
// controllers, mesh controllers, etc.).
//
// Implementation: re-fetch the live Route, merge our entry into a copy of
// its Status.Parents, then full Update. Re-fetching guards against the
// stale-spec-from-cache pitfall (the Reconcile caller may have an
// arbitrarily-old copy). This is the "merge-in-memory then full Update"
// approach — preferred over SSA here for testability with the fake client.
//
// Identity match for parent entries: same Name AND same Namespace pointer
// content (both nil, or both non-nil with equal strings). Section / Port
// are ignored — a Route may pin one parent ref per controller without
// ambiguity in practice.
//
// Returns any error from the final Status().Update so the caller can log it.
func (r *HTTPRouteSourceReconciler) writeParentStatus(
	ctx context.Context,
	rt *gwv1.HTTPRoute,
	parent gwv1.ParentReference,
	warns []tunnelsynth.TranslateWarning,
	hasContribs bool,
) error {
	// Re-fetch — the live Status may have been mutated by another controller
	// after this reconcile's initial Get.
	var live gwv1.HTTPRoute
	if err := r.Get(ctx, types.NamespacedName{Namespace: rt.Namespace, Name: rt.Name}, &live); err != nil {
		return fmt.Errorf("re-fetch httproute for status write: %w", err)
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

	// Decide Accepted reason / status. Default Accepted=True / TunnelAttached.
	// Translator warnings override:
	//   - IncompatibleFilters → Accepted=False, Reason=IncompatibleFilters
	//   - UnsupportedValue   → Accepted=True (rule kept), but PartiallyInvalid=True
	//     gets stamped separately below.
	// If we produced zero contribs (every rule rejected), Accepted=False.
	acceptedStatus := metav1.ConditionTrue
	acceptedReason := conventions.ReasonTunnelAttached
	acceptedMsg := ""
	for _, w := range warns {
		if w.Reason == conventions.ReasonIncompatibleFilters {
			acceptedStatus = metav1.ConditionFalse
			acceptedReason = w.Reason
			acceptedMsg = w.Message
			break
		}
	}
	if !hasContribs && acceptedStatus == metav1.ConditionTrue {
		acceptedStatus = metav1.ConditionFalse
		if len(rt.Spec.Hostnames) == 0 {
			// No hostnames on the route → nothing could route, but no rule
			// was "dropped". Mirror TLSRoute's accurate NoListenerHostname.
			acceptedReason = conventions.ReasonNoListenerHostname
			acceptedMsg = "no hostname resolved for HTTPRoute (Route.Spec.Hostnames empty)"
		} else {
			// Hostnames present but every rule produced no contribution
			// (e.g. all rules filtered) — "nothing landed".
			acceptedReason = conventions.ReasonIncompatibleFilters
			acceptedMsg = "all rules dropped during translation"
		}
	}
	conds := reconcilelib.SetCondition(
		live.Status.Parents[idx].Conditions,
		conventions.ConditionTypeAccepted, acceptedStatus, acceptedReason, acceptedMsg,
	)

	// Stamp PartiallyInvalid for UnsupportedValue warnings (header / query /
	// weighted backends). One condition aggregates them — last wins for the
	// message. Cleared (status=False) when no such warnings remain.
	partiallyInvalid := false
	var partialMsg string
	for _, w := range warns {
		if w.Reason == conventions.ReasonUnsupportedValue {
			partiallyInvalid = true
			partialMsg = w.Message
		}
	}
	piStatus := metav1.ConditionFalse
	piReason := conventions.ReasonTunnelAttached
	if partiallyInvalid {
		piStatus = metav1.ConditionTrue
		piReason = conventions.ReasonUnsupportedValue
	}
	conds = reconcilelib.SetCondition(
		conds,
		conventions.ConditionTypePartiallyInvalid, piStatus, piReason, partialMsg,
	)
	live.Status.Parents[idx].Conditions = conds

	return r.Status().Update(ctx, &live)
}

// parentRefEquals matches two ParentReferences on Name + Namespace. Section
// / Port are intentionally ignored — for the tunnel controller's purposes a
// (Gateway, listener) pair is identified by (name, namespace) alone; the
// design doesn't address per-section attachment.
func parentRefEquals(a, b gwv1.ParentReference) bool {
	if a.Name != b.Name {
		return false
	}
	switch {
	case a.Namespace == nil && b.Namespace == nil:
		return true
	case a.Namespace != nil && b.Namespace != nil:
		return *a.Namespace == *b.Namespace
	default:
		return false
	}
}

var _ reconcile.Reconciler = (*HTTPRouteSourceReconciler)(nil)
