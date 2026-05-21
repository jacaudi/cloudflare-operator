/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnelsynth

import (
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// TranslateTLSRoute converts a TLSRoute attached to a tunnel-targeted Gateway
// into tcp:// ingress contributions, one per provided hostname. Pure function;
// no K8s client, no logger.
//
// Always surfaces a ClientSideClientRequired warning: TLSRoute traffic
// terminates outside the browser model, so the resulting hostnames are
// reachable only via `cloudflared access tcp` or WARP. Callers thread the
// warning into the source object's status so users understand the access
// surface before pointing DNS at the tunnel.
//
// The hostnames parameter is explicit (rather than read off the TLSRoute
// spec) because Gateway-attached TLSRoutes inherit their hostnames from the
// listener when their own list is empty — the reconciler resolves that
// before calling the translator.
func TranslateTLSRoute(_ *gwv1a2.TLSRoute, hostnames []string, gw GatewayOrigin, defaults Defaults) ([]IngressContribution, []TranslateWarning) {
	warns := []TranslateWarning{{
		Reason:  conventions.ReasonClientSideClientRequired,
		Message: "TLSRoute hostnames are reachable only via cloudflared access tcp or WARP",
	}}
	contribs := make([]IngressContribution, 0, len(hostnames))
	for _, h := range hostnames {
		contribs = append(contribs, IngressContribution{
			Hostname:         h,
			Service:          gw.Service, // expected to be tcp://… per caller
			NoTLSVerify:      copyBoolPtr(defaults.NoTLSVerifyDefault),
			OriginServerName: copyStringPtr(defaults.OriginServerNameDefault),
		})
	}
	return contribs, warns
}
