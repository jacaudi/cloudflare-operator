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

package bootstrap

import "errors"

// Config is the meta-operator's runtime configuration, parsed from flags/env
// by cmd/manager and consumed by MetaReconciler to render the zone/tunnel
// controller Deployments. It replaces the removed CloudflareOperator CR.
type Config struct {
	OperatorNamespace string
	OperatorImage     string
	MetricsAddress    string
	HealthAddress     string
	LeaderElection    bool

	ZoneEnabled  bool
	ZoneReplicas int32
	ZoneLogLevel string

	TunnelEnabled  bool
	TunnelReplicas int32
	TunnelLogLevel string

	// Credential Secret coordinates propagated (as valueFrom.secretKeyRef)
	// onto the spawned controller Deployments. The chart sets these from
	// values.credentials.{existingSecret,tokenKey,accountIDKey}.
	CredentialsSecretName   string
	CredentialsTokenKey     string
	CredentialsAccountIDKey string
}

// Validate enforces the tunnel-requires-zone invariant that was previously a
// CloudflareOperator CEL rule. Meta mode treats a non-nil return as fatal.
// errors.New (not fmt.Errorf) — no format args, else staticcheck S1028 fails
// the §8.5 lint gate.
func (c Config) Validate() error {
	if c.TunnelEnabled && !c.ZoneEnabled {
		return errors.New("controllers.tunnel.enabled requires controllers.zone.enabled")
	}
	return nil
}
