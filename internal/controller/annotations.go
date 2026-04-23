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
	"errors"
	"fmt"
	"strings"
)

// Cloudflare-operator annotation keys (v1).
//
// These form the public, stable annotation vocabulary that source controllers
// (httproute_source, service_source) consume. Once shipped, semantics do not
// change in breaking ways — new features get new keys.
const (
	AnnotationTarget             = "cloudflare.io/target"
	AnnotationZoneRef            = "cloudflare.io/zone-ref"
	AnnotationZoneRefNamespace   = "cloudflare.io/zone-ref-namespace"
	AnnotationTunnelRefNamespace = "cloudflare.io/tunnel-ref-namespace"
	AnnotationTunnelUpstream     = "cloudflare.io/tunnel-upstream"
	AnnotationHostnames          = "cloudflare.io/hostnames"
	AnnotationPort               = "cloudflare.io/port"
	AnnotationScheme             = "cloudflare.io/scheme"
	AnnotationProxied            = "cloudflare.io/proxied"
	AnnotationTTL                = "cloudflare.io/ttl"
	AnnotationAdopt              = "cloudflare.io/adopt"

	AnnotationPrefix = "cloudflare.io/"

	// AnnotationRegistryFor marks an emitted TXT-registry CloudflareDNSRecord
	// so the DNS controller does not re-emit a TXT for it.
	AnnotationRegistryFor = "cloudflare.io/registry-for"
	// AnnotationConfigHash goes on the connector Deployment's pod template
	// and the rendered ConfigMap so config changes trigger rollouts.
	AnnotationConfigHash = "cloudflare.io/config-hash"
)

// Labels applied to emitted CloudflareDNSRecord / CloudflareTunnelRule CRs
// for source-based discovery via label selectors.
const (
	LabelSourceKind      = "cloudflare.io/source-kind"
	LabelSourceNamespace = "cloudflare.io/source-namespace"
	LabelSourceName      = "cloudflare.io/source-name"
	LabelManagedBy       = "cloudflare.io/managed-by"
)

// TargetKind is the kind field of a parsed cloudflare.io/target value.
type TargetKind string

const (
	TargetKindTunnel  TargetKind = "tunnel"
	TargetKindCNAME   TargetKind = "cname"
	TargetKindAddress TargetKind = "address"
)

// TargetSpec is a parsed cloudflare.io/target annotation value.
//
//	tunnel:<name>    -> Kind=Tunnel,  Name=<name>
//	cname:<fqdn>     -> Kind=CNAME,   CNAME=<fqdn>
//	address          -> Kind=Address
type TargetSpec struct {
	Kind  TargetKind
	Name  string // populated when Kind=Tunnel
	CNAME string // populated when Kind=CNAME
}

// ErrInvalidTarget is returned by ParseTarget on malformed input.
var ErrInvalidTarget = errors.New("invalid cloudflare.io/target value")

// ParseTarget parses a cloudflare.io/target value. Kinds are matched
// case-sensitively ("tunnel", "cname", "address") — callers MUST use the
// lowercase canonical form.
func ParseTarget(raw string) (TargetSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return TargetSpec{}, fmt.Errorf("%w: empty", ErrInvalidTarget)
	}
	if raw == "address" {
		return TargetSpec{Kind: TargetKindAddress}, nil
	}
	kind, value, found := strings.Cut(raw, ":")
	if !found {
		return TargetSpec{}, fmt.Errorf("%w: expected <kind>:<value>, got %q", ErrInvalidTarget, raw)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return TargetSpec{}, fmt.Errorf("%w: empty value for kind %q", ErrInvalidTarget, kind)
	}
	switch TargetKind(kind) {
	case TargetKindTunnel:
		return TargetSpec{Kind: TargetKindTunnel, Name: value}, nil
	case TargetKindCNAME:
		return TargetSpec{Kind: TargetKindCNAME, CNAME: value}, nil
	default:
		return TargetSpec{}, fmt.Errorf("%w: unknown kind %q", ErrInvalidTarget, kind)
	}
}

// MergeCloudflareAnnotations merges parent (e.g. Gateway) and child (e.g. Route)
// annotations, keeping only the cloudflare.io/* subset. Child values override
// parent values on the same key.
func MergeCloudflareAnnotations(parent, child map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range parent {
		if strings.HasPrefix(k, AnnotationPrefix) {
			out[k] = v
		}
	}
	for k, v := range child {
		if strings.HasPrefix(k, AnnotationPrefix) {
			out[k] = v
		}
	}
	return out
}
