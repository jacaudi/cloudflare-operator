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

package tunnelsynth

import (
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
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
		Reason:  "ClientSideClientRequired",
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
