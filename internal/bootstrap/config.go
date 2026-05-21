/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
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

	// TunnelConnectorResourcesJSON is an opaque JSON-encoded
	// corev1.ResourceRequirements seeded as DefaultConnector.Resources on
	// auto-created CloudflareTunnel CRs spawned by the tunnel controller.
	// Empty = unset (no requests/limits — unchanged behavior). Kept opaque
	// here; parsed/validated in cmd/manager (fail-loud).
	TunnelConnectorResourcesJSON string

	// TunnelConnectorImageJSON is an opaque JSON-encoded v2alpha1.ConnectorImage
	// ({repository?,tag?}) layered over the compile-time cloudflared pin
	// (per-axis, via tunnel.ResolveImage) to form Options.DefaultImage. Empty
	// = unset (use the compile-time const). Parsed/validated in cmd/manager.
	TunnelConnectorImageJSON string
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
