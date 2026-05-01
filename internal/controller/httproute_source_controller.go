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
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

// ErrNoGatewayAddress is returned when a target=address reconcile cannot
// proceed because the parent Gateway has no status addresses populated yet.
var ErrNoGatewayAddress = errors.New("no Gateway address populated in status")

// kindGateway is the Gateway API kind string used when filtering ParentRefs.
const kindGateway = "Gateway"

// recordTypeCNAME is the Cloudflare DNS record type string for CNAME records.
const recordTypeCNAME = "CNAME"

// HTTPRouteSourceReconciler watches HTTPRoutes that carry cloudflare.io/*
// annotations (either directly or inherited from a parent Gateway) and emits
// CloudflareDNSRecord + CloudflareTunnelRule CRs.
type HTTPRouteSourceReconciler struct {
	client.Client
	Recorder    record.EventRecorder
	TxtOwnerID  string
	AffixConfig cfclient.AffixConfig
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarednsrecords,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflaretunnelrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflaretunnels,verbs=get;list;watch
// +kubebuilder:rbac:groups=cloudflare.io,resources=cloudflarezones,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile processes a single HTTPRoute reconcile request.
func (r *HTTPRouteSourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the HTTPRoute.
	var route gwv1.HTTPRoute
	if err := r.Get(ctx, req.NamespacedName, &route); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get HTTPRoute: %w", err)
	}

	// 2. Build merged annotation set: parent Gateway annotations then Route overrides.
	ann := r.mergedAnnotations(ctx, &route)

	// 3. Check for cloudflare.io/target annotation.
	rawTarget, ok := ann[AnnotationTarget]
	if !ok || rawTarget == "" {
		return ctrl.Result{}, nil
	}

	// 4. Require TxtOwnerID.
	if r.TxtOwnerID == "" {
		r.Recorder.Event(&route, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonInvalidAnnotation,
			"TXT_OWNER_ID is not configured; skipping HTTPRoute source")
		return ctrl.Result{}, nil
	}

	// 5. Parse target annotation.
	ts, err := ParseTarget(rawTarget)
	if err != nil {
		r.Recorder.Eventf(&route, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonInvalidAnnotation,
			"invalid cloudflare.io/target %q: %v", rawTarget, err)
		return ctrl.Result{}, nil
	}

	// 6. Extract and validate hostnames from spec.Hostnames.
	hs := hostnamesFromRoute(route.Spec.Hostnames)
	if res := r.validateHostnames(&route, hs); res != nil {
		return *res, nil
	}

	// 7. Resolve backend content (tunnel CNAME / gateway address / cname).
	dnsContent, recordType, tunnelNs, result := r.resolveBackendContent(ctx, &route, ts, ann)
	if result != nil {
		return *result, nil
	}

	// 8. Determine proxied flag.
	proxied := r.resolveProxied(&route, ts, ann)

	// 9. TTL.
	ttl := ttlFromAnnotation(ann)

	// 10. Build owner references.
	route.TypeMeta = metav1.TypeMeta{
		APIVersion: "gateway.networking.k8s.io/v1",
		Kind:       "HTTPRoute",
	}
	ownerRefs := ownerRefsFor(&route)

	// 11. Source labels.
	sourceLabels := map[string]string{
		LabelSourceKind:      "HTTPRoute",
		LabelSourceNamespace: route.Namespace,
		LabelSourceName:      route.Name,
		LabelManagedBy:       "cloudflare-operator",
	}

	// 12. Emit DNS records for each hostname + companion TXT.
	for _, h := range hs {
		zone, err := resolveZoneRefFromAnnotations(ctx, r.Client, route.Namespace, ann, h)
		if err != nil {
			r.Recorder.Eventf(&route, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonNoMatchingZone,
				"zone resolution failed for hostname %q: %v", h, err)
			return ctrl.Result{}, nil
		}
		if err := r.emitDNSPair(ctx, &route, h, dnsContent, recordType, zone, proxied, ttl, sourceLabels, ownerRefs, ann); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 13. Emit or delete TunnelRule when target is tunnel.
	if ts.Kind == TargetKindTunnel {
		if err := r.reconcileTunnelRule(ctx, &route, ts, ann, hs, tunnelNs, sourceLabels, ownerRefs); err != nil {
			return ctrl.Result{}, err
		}
	}

	logger.V(1).Info("reconciled HTTPRoute source",
		"httproute", req.NamespacedName,
		"hostnames", hs,
		"target", rawTarget)

	r.Recorder.Eventf(&route, corev1.EventTypeNormal, cloudflarev1alpha1.ReasonDNSReconciled,
		"DNS records reconciled for %d hostname(s)", len(hs))

	return ctrl.Result{}, nil
}

// mergedAnnotations builds the merged cloudflare.io/* annotation set for a Route:
// parent Gateway annotations merged with the Route's own annotations (Route wins).
func (r *HTTPRouteSourceReconciler) mergedAnnotations(ctx context.Context, route *gwv1.HTTPRoute) map[string]string {
	parentAnn := r.readParentAnnotations(ctx, route)
	routeAnn := route.Annotations
	if routeAnn == nil {
		routeAnn = map[string]string{}
	}
	return MergeCloudflareAnnotations(parentAnn, routeAnn)
}

// validateHostnames checks that hs is non-empty and that all entries are valid
// DNS names. Returns a non-nil *ctrl.Result when reconcile should short-circuit.
func (r *HTTPRouteSourceReconciler) validateHostnames(route *gwv1.HTTPRoute, hs []string) *ctrl.Result {
	if len(hs) == 0 {
		r.Recorder.Event(route, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonInvalidAnnotation,
			"spec.hostnames is empty; at least one hostname is required")
		return &ctrl.Result{}
	}
	for _, h := range hs {
		if !isValidDNSName(h) {
			r.Recorder.Eventf(route, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonInvalidAnnotation,
				"hostname %q is not a valid DNS name", h)
			return &ctrl.Result{}
		}
	}
	return nil
}

// resolveBackendContent determines the DNS record content string and record type
// based on the target kind. Returns a non-nil *ctrl.Result when reconcile should
// short-circuit (tunnel not found / not ready / gateway not ready).
func (r *HTTPRouteSourceReconciler) resolveBackendContent(
	ctx context.Context,
	route *gwv1.HTTPRoute,
	ts TargetSpec,
	ann map[string]string,
) (content, recordType, tunnelNs string, result *ctrl.Result) {
	switch ts.Kind {
	case TargetKindTunnel:
		cname, ready, err := resolveTunnelCNAME(ctx, r.Client, route.Namespace, ann, ts.Name)
		if err != nil {
			r.Recorder.Eventf(route, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonTunnelNotFound,
				"cannot resolve tunnel %q: %v", ts.Name, err)
			return "", "", "", &ctrl.Result{}
		}
		if !ready {
			r.Recorder.Eventf(route, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonTunnelNotReady,
				"tunnel %q has no CNAME yet; requeuing", ts.Name)
			res := ctrl.Result{RequeueAfter: 15 * time.Second}
			return "", "", "", &res
		}
		ns := firstNonEmpty(ann[AnnotationTunnelRefNamespace], route.Namespace)
		return cname, recordTypeCNAME, ns, nil

	case TargetKindCNAME:
		return ts.CNAME, recordTypeCNAME, "", nil

	case TargetKindAddress:
		addr, err := r.firstGatewayAddress(ctx, route)
		if err != nil {
			if errors.Is(err, ErrNoGatewayAddress) {
				r.Recorder.Eventf(route, corev1.EventTypeWarning,
					cloudflarev1alpha1.ReasonGatewayAddressNotReady,
					"parent Gateway has no addresses yet; requeuing")
				res := ctrl.Result{RequeueAfter: 15 * time.Second}
				return "", "", "", &res
			}
			res := ctrl.Result{}
			r.Recorder.Eventf(route, corev1.EventTypeWarning,
				cloudflarev1alpha1.ReasonGatewayAddressNotReady,
				"resolve gateway address: %v", err)
			return "", "", "", &res
		}
		return addr, inferAddressRecordType(addr), "", nil
	}
	return "", recordTypeCNAME, "", nil
}

// resolveProxied determines the proxied flag. Default is true for all target
// kinds. For non-tunnel targets the annotation may override the default.
// Tunnel targets always force true regardless of the annotation.
func (r *HTTPRouteSourceReconciler) resolveProxied(
	route *gwv1.HTTPRoute,
	ts TargetSpec,
	ann map[string]string,
) bool {
	proxied := true
	if ts.Kind != TargetKindTunnel {
		// Non-tunnel: allow the annotation to override the default.
		if raw, ok := ann[AnnotationProxied]; ok && raw != "" {
			v, parseErr := strconv.ParseBool(raw)
			if parseErr != nil {
				r.Recorder.Eventf(route, corev1.EventTypeWarning, cloudflarev1alpha1.ReasonInvalidAnnotation,
					"invalid cloudflare.io/proxied value %q; ignoring and using default", raw)
			} else {
				proxied = v
			}
		}
	}
	// Tunnels MUST be proxied — cannot be turned off via annotation.
	if ts.Kind == TargetKindTunnel {
		proxied = true
	}
	return proxied
}

// reconcileTunnelRule emits a TunnelRule when tunnel-upstream is set, or deletes
// any orphan rule when it is absent.
func (r *HTTPRouteSourceReconciler) reconcileTunnelRule(
	ctx context.Context,
	route *gwv1.HTTPRoute,
	ts TargetSpec,
	ann map[string]string,
	hs []string,
	tunnelNs string,
	sourceLabels map[string]string,
	ownerRefs []metav1.OwnerReference,
) error {
	upstream := ann[AnnotationTunnelUpstream]
	ruleName := fmt.Sprintf("httproute-%s-%s", route.Namespace, route.Name)

	if upstream != "" {
		rule := &cloudflarev1alpha1.CloudflareTunnelRule{
			ObjectMeta: metav1.ObjectMeta{
				Name:            ruleName,
				Namespace:       route.Namespace,
				Labels:          sourceLabels,
				OwnerReferences: ownerRefs,
			},
			Spec: cloudflarev1alpha1.CloudflareTunnelRuleSpec{
				TunnelRef: cloudflarev1alpha1.TunnelReference{
					Name:      ts.Name,
					Namespace: tunnelNs,
				},
				Hostnames: hs,
				Backend: cloudflarev1alpha1.TunnelRuleBackend{
					URL: strPtr(upstream),
				},
				Priority: 100,
				SourceRef: &cloudflarev1alpha1.TunnelRuleSourceRef{
					APIVersion: "gateway.networking.k8s.io/v1",
					Kind:       "HTTPRoute",
					Namespace:  route.Namespace,
					Name:       route.Name,
					UID:        string(route.UID),
				},
			},
		}
		if err := upsertTunnelRule(ctx, r.Client, rule); err != nil {
			return fmt.Errorf("upsert TunnelRule: %w", err)
		}
		return nil
	}

	// No upstream set — delete any orphan rule. NotFound is expected (idempotent).
	orphan := &cloudflarev1alpha1.CloudflareTunnelRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ruleName,
			Namespace: route.Namespace,
		},
	}
	if err := r.Delete(ctx, orphan); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete orphan rule: %w", err)
	}
	return nil
}

// hostnamesFromRoute converts a slice of gwv1.Hostname values to plain strings.
func hostnamesFromRoute(hs []gwv1.Hostname) []string {
	out := make([]string, 0, len(hs))
	for _, h := range hs {
		if s := strings.TrimSpace(string(h)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// inferAddressRecordType returns "A" for IPv4 addresses, "AAAA" for IPv6,
// and "CNAME" for anything that looks like a hostname.
func inferAddressRecordType(addr string) string {
	// IPv6 addresses contain ":".
	if strings.Contains(addr, ":") {
		return "AAAA"
	}
	// IPv4: four dot-separated numeric octets.
	parts := strings.Split(addr, ".")
	if len(parts) == 4 {
		allNumeric := true
		for _, p := range parts {
			for _, ch := range p {
				if ch < '0' || ch > '9' {
					allNumeric = false
					break
				}
			}
			if !allNumeric {
				break
			}
		}
		if allNumeric {
			return "A"
		}
	}
	return recordTypeCNAME
}

// emitDNSPair creates or updates the DNS record and its companion TXT
// registry record for a single hostname.
func (r *HTTPRouteSourceReconciler) emitDNSPair(
	ctx context.Context,
	route *gwv1.HTTPRoute,
	hostname string,
	content string,
	recordType string,
	zone *cloudflarev1alpha1.CloudflareZone,
	proxied bool,
	ttl int,
	labels map[string]string,
	ownerRefs []metav1.OwnerReference,
	sourceAnnotations map[string]string,
) error {
	crName := capCRName(fmt.Sprintf("httproute-%s-%s-%s", route.Namespace, route.Name, sanitizeDNSForCRName(hostname)))
	// Propagate cloudflare.io/adopt from the source object to the emitted CR so
	// the DNS controller's registry decision can honour it.
	var dnsRecordAnnotations map[string]string
	if sourceAnnotations[AnnotationAdopt] == AnnotationValueTrue {
		dnsRecordAnnotations = map[string]string{AnnotationAdopt: AnnotationValueTrue}
	}
	dnsRecord := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:            crName,
			Namespace:       route.Namespace,
			Labels:          labels,
			Annotations:     dnsRecordAnnotations,
			OwnerReferences: ownerRefs,
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			Name:      hostname,
			Type:      recordType,
			Content:   strPtr(content),
			Proxied:   boolPtr(proxied),
			TTL:       ttl,
			SecretRef: cloudflarev1alpha1.SecretReference{
				Name:      zone.Spec.SecretRef.Name,
				Namespace: zone.Namespace,
			},
			ZoneRef: &cloudflarev1alpha1.ZoneReference{
				Name:      zone.Name,
				Namespace: zone.Namespace,
			},
		},
	}
	if err := upsertDNSRecord(ctx, r.Client, dnsRecord); err != nil {
		return fmt.Errorf("upsert DNS record for %q: %w", hostname, err)
	}

	txtFQDN := cfclient.AffixName(hostname, recordType, r.AffixConfig)
	txtContent := cfclient.EncodeRegistryPayload(cfclient.RegistryPayload{
		Owner:           r.TxtOwnerID,
		SourceKind:      "HTTPRoute",
		SourceNamespace: route.Namespace,
		SourceName:      route.Name,
	})
	txtCRName := capCRName(fmt.Sprintf("httproute-%s-%s-%s-txt", route.Namespace, route.Name, sanitizeDNSForCRName(hostname)))
	txtRecord := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      txtCRName,
			Namespace: route.Namespace,
			Labels:    labels,
			Annotations: map[string]string{
				AnnotationRegistryFor: hostname,
			},
			OwnerReferences: ownerRefs,
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			Name:      txtFQDN,
			Type:      "TXT",
			Content:   strPtr(txtContent),
			TTL:       120,
			SecretRef: cloudflarev1alpha1.SecretReference{
				Name:      zone.Spec.SecretRef.Name,
				Namespace: zone.Namespace,
			},
			ZoneRef: &cloudflarev1alpha1.ZoneReference{
				Name:      zone.Name,
				Namespace: zone.Namespace,
			},
		},
	}
	if err := upsertDNSRecord(ctx, r.Client, txtRecord); err != nil {
		return fmt.Errorf("upsert TXT record for %q: %w", hostname, err)
	}
	return nil
}

// readParentAnnotations walks route.Spec.ParentRefs and returns the cloudflare.io/*
// annotation subset from the first parent Gateway that carries any cloudflare.io/* key.
// Cross-namespace parents (p.Namespace != nil) are resolved using the referenced namespace.
func (r *HTTPRouteSourceReconciler) readParentAnnotations(ctx context.Context, route *gwv1.HTTPRoute) map[string]string {
	for _, p := range route.Spec.ParentRefs {
		// Only consider Gateway parents.
		if p.Kind != nil && string(*p.Kind) != kindGateway {
			continue
		}
		// Resolve namespace.
		ns := route.Namespace
		if p.Namespace != nil && string(*p.Namespace) != "" {
			ns = string(*p.Namespace)
		}

		var gw gwv1.Gateway
		if err := r.Get(ctx, types.NamespacedName{Name: string(p.Name), Namespace: ns}, &gw); err != nil {
			continue
		}
		// Check if the Gateway carries any cloudflare.io/* annotations.
		for k := range gw.Annotations {
			if strings.HasPrefix(k, AnnotationPrefix) {
				subset := map[string]string{}
				for k2, v2 := range gw.Annotations {
					if strings.HasPrefix(k2, AnnotationPrefix) {
						subset[k2] = v2
					}
				}
				return subset
			}
		}
	}
	return nil
}

// firstGatewayAddress iterates all parent Gateways in route.Spec.ParentRefs
// and returns the first address found across all ready Gateways.
// Get errors and Gateways with empty Status.Addresses are skipped (continued),
// matching the plan semantics for multi-parent fallback.
// Returns ErrNoGatewayAddress when no parent Gateway has addresses populated yet.
func (r *HTTPRouteSourceReconciler) firstGatewayAddress(ctx context.Context, route *gwv1.HTTPRoute) (string, error) {
	for _, p := range route.Spec.ParentRefs {
		if p.Kind != nil && string(*p.Kind) != kindGateway {
			continue
		}
		ns := route.Namespace
		if p.Namespace != nil && string(*p.Namespace) != "" {
			ns = string(*p.Namespace)
		}
		var gw gwv1.Gateway
		if err := r.Get(ctx, types.NamespacedName{Name: string(p.Name), Namespace: ns}, &gw); err != nil {
			// Skip unresolvable parents; another parent may have addresses.
			continue
		}
		if len(gw.Status.Addresses) == 0 {
			// This Gateway has no addresses yet; try the next parent.
			continue
		}
		return gw.Status.Addresses[0].Value, nil
	}
	return "", fmt.Errorf("%w", ErrNoGatewayAddress)
}

// mapGatewayToRoutes returns reconcile requests for all HTTPRoutes that
// reference the updated Gateway as a parent. Routes are listed CLUSTER-WIDE so
// that Routes in different namespaces are enqueued promptly.
func (r *HTTPRouteSourceReconciler) mapGatewayToRoutes(ctx context.Context, obj client.Object) []reconcile.Request {
	gw, ok := obj.(*gwv1.Gateway)
	if !ok {
		return nil
	}
	var list gwv1.HTTPRouteList
	if err := r.List(ctx, &list); err != nil { // cluster-wide
		return nil
	}
	out := make([]reconcile.Request, 0)
	for i := range list.Items {
		route := &list.Items[i]
		for _, p := range route.Spec.ParentRefs {
			if p.Kind != nil && string(*p.Kind) != kindGateway {
				continue
			}
			ns := route.Namespace
			if p.Namespace != nil && string(*p.Namespace) != "" {
				ns = string(*p.Namespace)
			}
			if ns == gw.Namespace && string(p.Name) == gw.Name {
				out = append(out, reconcile.Request{
					NamespacedName: types.NamespacedName{Namespace: route.Namespace, Name: route.Name},
				})
				break
			}
		}
	}
	return out
}

// mapTunnelToRoutes returns reconcile requests for all HTTPRoutes that reference
// the updated CloudflareTunnel. Routes are listed CLUSTER-WIDE so that
// cross-namespace references are enqueued promptly.
//
// Namespace resolution: if a Route carries cloudflare.io/tunnel-ref-namespace,
// that value is used as the tunnel namespace; otherwise the Route's own namespace
// is assumed.
func (r *HTTPRouteSourceReconciler) mapTunnelToRoutes(ctx context.Context, obj client.Object) []reconcile.Request {
	tun, ok := obj.(*cloudflarev1alpha1.CloudflareTunnel)
	if !ok {
		return nil
	}
	var list gwv1.HTTPRouteList
	if err := r.List(ctx, &list); err != nil { // cluster-wide
		return nil
	}
	out := make([]reconcile.Request, 0)
	for i := range list.Items {
		route := &list.Items[i]
		// Use mergedAnnotations so Routes that inherit cloudflare.io/* from a
		// parent Gateway are also enqueued when the target tunnel changes.
		ann := r.mergedAnnotations(ctx, route)
		if ann[AnnotationTarget] != "tunnel:"+tun.Name {
			continue
		}
		refNs := ann[AnnotationTunnelRefNamespace]
		if refNs == "" {
			refNs = route.Namespace
		}
		if refNs != tun.Namespace {
			continue
		}
		out = append(out, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: route.Namespace, Name: route.Name},
		})
	}
	return out
}

// SetupWithManager registers the HTTPRouteSourceReconciler with the manager.
func (r *HTTPRouteSourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gwv1.HTTPRoute{}).
		Watches(
			&gwv1.Gateway{},
			handler.EnqueueRequestsFromMapFunc(r.mapGatewayToRoutes),
		).
		Watches(
			&cloudflarev1alpha1.CloudflareTunnel{},
			handler.EnqueueRequestsFromMapFunc(r.mapTunnelToRoutes),
		).
		Complete(r)
}
