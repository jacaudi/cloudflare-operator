/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

// Package tunnelsynth holds the shared types + cache that source reconcilers
// (Service, Gateway, HTTPRoute, TLSRoute) write into and the tunnel reconciler
// reads from. Lives in its own package to avoid an import cycle with
// internal/controller/tunnel.
package tunnelsynth

// TunnelKey identifies a CloudflareTunnel CR.
type TunnelKey struct {
	Namespace string
	Name      string
}

// SourceKey identifies a source object contributing ingress entries.
// Kind is one of: Service, Gateway, HTTPRoute, TLSRoute.
type SourceKey struct {
	Kind      string
	Namespace string
	Name      string
}

// IngressContribution is one ingress entry contributed by a source.
// It is the source-side intermediate that the resolver consumes to produce
// the final cloudflare-SDK-facing ingress list.
type IngressContribution struct {
	// Hostname is the public FQDN (required for non-catch-all entries).
	Hostname string
	// Path is an optional path regex applied within the hostname.
	Path string
	// Service is the cloudflared service URL (e.g. http://svc.ns.svc.cluster.local:80).
	Service string
	// NoTLSVerify reflects cloudflare.io/no-tls-verify; nil means inherit.
	NoTLSVerify *bool
	// OriginServerName reflects cloudflare.io/origin-server-name; nil means inherit.
	OriginServerName *string
}

// ContributionWithSource is what Snapshot returns — IngressContribution plus
// its originating source so consumers can break hostname ties deterministically.
type ContributionWithSource struct {
	IngressContribution
	Source SourceKey
}
