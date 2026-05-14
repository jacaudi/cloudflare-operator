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

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
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
type HTTPRouteSourceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Cache    *tunnelsynth.Cache
	Recorder record.EventRecorder

	tracker *cacheTracker
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
	if r.tracker == nil {
		r.tracker = newCacheTracker()
	}
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

	// Gateway apex (the CNAME chain hop). Pick the first non-empty listener
	// hostname — the chain pattern only needs one stable apex per Gateway;
	// cloudflared dispatches by Host header so the apex is a routing
	// landmark, not a per-listener pivot.
	gwApex := firstListenerHostname(gw)

	gwOrigin := tunnelsynth.GatewayOrigin{
		Hostname: gwApex,
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
			r.Recorder.Eventf(&rt, corev1.EventTypeWarning, w.Reason, "%s", w.Message)
		}
	}

	// Defer DNSRecord emission until the tunnel CR populates Status.TunnelCNAME
	// AND we have a Gateway apex. The DNSRecord written here is the CHAIN
	// hop (<route-hostname> → <gateway-apex>); the apex → tunnel-CNAME hop
	// is written by the GatewaySourceReconciler. Without an apex we can't
	// build the chain; without a tunnel CNAME the apex itself won't resolve
	// yet. Status write still happens so operators see progress.
	if gwApex == "" || tn.Status.TunnelCNAME == "" {
		logger.V(1).Info("deferring DNSRecord emission until Gateway apex + tunnel CNAME populate",
			"tunnel", tunnelKey, "gwApex", gwApex, "tunnelCNAME", tn.Status.TunnelCNAME)
		if err := r.writeParentStatus(ctx, &rt, *parent, warns, len(contribs) > 0); err != nil {
			logger.V(1).Info("status write failed (best-effort; ignored)", "err", err)
		}
		return reconcile.Result{}, nil
	}

	// Emit per-Route DNSRecord CRs: CNAME <hostname> → <gateway-apex>.
	for _, h := range rt.Spec.Hostnames {
		if err := r.emitChainDNSRecord(ctx, &rt, string(h), gwApex); err != nil {
			return reconcile.Result{}, fmt.Errorf("emit dns record for %q: %w", h, err)
		}
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
) (*gwv1.ParentReference, *gwv1.Gateway, *v1alpha1.CloudflareTunnel, *corev1.Service, int32, error) {
	for i := range rt.Spec.ParentRefs {
		pr := rt.Spec.ParentRefs[i]
		gwNS := rt.Namespace
		if pr.Namespace != nil {
			gwNS = string(*pr.Namespace)
		}
		var gw gwv1.Gateway
		if err := r.Get(ctx, types.NamespacedName{Namespace: gwNS, Name: string(pr.Name)}, &gw); err != nil {
			// Get failures here are treated as "not this parent" — a
			// missing or RBAC-denied parent shouldn't fail the whole
			// reconcile.
			continue
		}
		enabled, _ := conventions.ParseTruthy(gw.Annotations[conventions.AnnotationTunnel])
		if !enabled {
			continue
		}
		derived, err := DeriveTunnelName(gwNS, gw.Annotations[conventions.AnnotationTunnelName])
		if err != nil {
			// Bad annotation on the parent Gateway — the Gateway controller
			// surfaces NameTooLong / InvalidName on the Gateway itself. Skip
			// the parent here.
			continue
		}
		var tn v1alpha1.CloudflareTunnel
		if err := r.Get(ctx, types.NamespacedName{Namespace: gwNS, Name: derived}, &tn); err != nil {
			continue
		}
		gwSvc, port, err := resolveGatewayService(ctx, r.Client, &gw)
		if err != nil {
			// Gateway controller surfaces GatewayServiceUnspecified /
			// GatewayServiceUnresolved on the Gateway. Skip the parent.
			continue
		}
		return &pr, &gw, &tn, gwSvc, port, nil
	}
	return nil, nil, nil, nil, 0, nil
}

// firstListenerHostname returns the first non-empty hostname from the
// Gateway's listeners. Used as the CNAME chain hop target.
func firstListenerHostname(gw *gwv1.Gateway) string {
	for _, l := range gw.Spec.Listeners {
		if l.Hostname != nil && *l.Hostname != "" {
			return string(*l.Hostname)
		}
	}
	return ""
}

// emitChainDNSRecord creates (idempotently) a CloudflareDNSRecord CR for one
// Route hostname pointing at the Gateway apex (the CNAME chain's middle hop).
//
// Per design §4.2: <route-hostname> → <gateway-apex-hostname> → <tunnel-CNAME>.
// The intermediate stabilizes per-Route DNS even if the tunnel CR is
// recreated.
//
// Per spec 2 contract:
//   - spec.zoneRef.name (resolved by the zone reconciler) when
//     cloudflare.io/zone-ref is set on the Route — never spec.zoneID
//   - spec.type = CNAME
//   - spec.name = hostname
//   - spec.content = gateway apex (caller guards non-empty)
//   - spec.adopt threaded from cloudflare.io/adopt
//
// Uses emittedDNSRecordName (T10) for collision-safe CR naming.
func (r *HTTPRouteSourceReconciler) emitChainDNSRecord(ctx context.Context, rt *gwv1.HTTPRoute, hostname, gwApex string) error {
	content := gwApex // copy so we can take its address (Spec.Content is *string)
	dr := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      emittedDNSRecordName(rt.Name, hostname),
			Namespace: rt.Namespace,
		},
		Spec: v1alpha1.CloudflareDNSRecordSpec{
			Type:    "CNAME",
			Name:    hostname,
			Content: &content,
		},
	}
	reconcilelib.StampSourceLabels(dr, "HTTPRoute", rt.Name, rt.Namespace)
	if err := reconcilelib.SetControllerOwner(rt, dr, r.Scheme); err != nil {
		return err
	}
	if zr := rt.Annotations[conventions.AnnotationZoneRef]; zr != "" {
		dr.Spec.ZoneRef = &v1alpha1.ZoneReference{Name: zr, Namespace: rt.Namespace}
	}
	if adopt, _ := conventions.ParseTruthy(rt.Annotations[conventions.AnnotationAdopt]); adopt {
		dr.Spec.Adopt = true
	}
	if err := r.Create(ctx, dr); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
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
// approach noted in the T12 corrections — preferred over SSA here for
// testability with the fake client.
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
		// Fallback: no contribs and no IncompatibleFilters warn — unusual,
		// but mark Accepted=False with IncompatibleFilters so operators see
		// "nothing landed" instead of a misleading TunnelAttached.
		acceptedStatus = metav1.ConditionFalse
		acceptedReason = conventions.ReasonIncompatibleFilters
		acceptedMsg = "all rules dropped during translation"
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
