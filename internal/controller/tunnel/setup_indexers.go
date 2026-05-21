/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnel

import (
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// IndexKeyRouteByGatewayParent is the field-indexer key under which we store
// the "<namespace>/<name>" of each Gateway parent referenced by a route.
// gatewayToHTTPRoutes / gatewayToTLSRoutes List by this field instead of
// scanning the cluster-wide route cache on every Gateway event.
const IndexKeyRouteByGatewayParent = "spec.parentRefs.gateway"

// indexHTTPRouteByGatewayParent returns the set of "<ns>/<name>" parent keys
// for an HTTPRoute, filtered to parentRefs whose Group is the Gateway-API
// group (or nil, which defaults to it) and whose Kind is "Gateway" (or nil).
func indexHTTPRouteByGatewayParent(o client.Object) []string {
	rt, ok := o.(*gwv1.HTTPRoute)
	if !ok {
		return nil
	}
	return parentKeysOf(rt.Namespace, rt.Spec.ParentRefs)
}

// indexTLSRouteByGatewayParent mirrors indexHTTPRouteByGatewayParent for
// TLSRoute (v1alpha2 surface; reuses the v1 ParentReference type via
// CommonRouteSpec).
func indexTLSRouteByGatewayParent(o client.Object) []string {
	rt, ok := o.(*gwv1a2.TLSRoute)
	if !ok {
		return nil
	}
	return parentKeysOf(rt.Namespace, rt.Spec.ParentRefs)
}

// parentKeysOf is the shared core of the two indexers. Returns nil for
// routes with no Gateway parents (so the cache index stays small).
func parentKeysOf(routeNS string, refs []gwv1.ParentReference) []string {
	var out []string
	for _, pr := range refs {
		if pr.Kind != nil && *pr.Kind != "Gateway" {
			continue
		}
		if pr.Group != nil && *pr.Group != "gateway.networking.k8s.io" {
			continue
		}
		ns := routeNS
		if pr.Namespace != nil {
			ns = string(*pr.Namespace)
		}
		out = append(out, (types.NamespacedName{Namespace: ns, Name: string(pr.Name)}).String())
	}
	return out
}
