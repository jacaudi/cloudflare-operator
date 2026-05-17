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

package v2alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CloudflareTunnelSpec defines the desired state of a Cloudflare Tunnel.
type CloudflareTunnelSpec struct {
	// Name is the tunnel name in Cloudflare. Immutable after create — the
	// Cloudflare API treats config_src as write-once; renames would orphan
	// the cloudflared credential Secret and DNS targets. Capped at 52
	// characters so derived resource names (cloudflared-<tunnel-name>) fit
	// the 63-character DNS-1123 label limit.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=52
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.name is immutable"
	Name string `json:"name"`

	// Connector configures the operator-managed cloudflared Deployment.
	// +kubebuilder:validation:Required
	Connector ConnectorSpec `json:"connector"`

	// Cloudflare overrides the operator-level credential + accountID.
	// Per Foundation §5: credential and accountID inherited or overridden as
	// a unit. When unset, the operator-level default applies.
	// +optional
	Cloudflare *CloudflareCredentialRef `json:"cloudflare,omitempty"`

	// Interval is the reconciliation interval. Default 30m.
	// +kubebuilder:default="30m"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`

	// Routing configures tunnel-wide originRequest defaults + the catch-all
	// default backend. The catch-all is auto-appended by the reconciler;
	// users only override it here when http_status:404 is wrong for them.
	// +optional
	Routing *TunnelRoutingSpec `json:"routing,omitempty"`
}

// ConnectorSpec configures the operator-managed cloudflared Deployment.
type ConnectorSpec struct {
	// Replicas is the desired Pod count. Default 2. Range 1-25. No HPA.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=25
	Replicas int32 `json:"replicas"`

	// Image specifies the cloudflared container image. When omitted, the
	// operator uses a compile-time default (cloudflare/cloudflared:<pinned>).
	// +optional
	Image *ConnectorImage `json:"image,omitempty"`

	// Protocol selects cloudflared's transport. auto|http2|quic.
	// +kubebuilder:default=auto
	// +kubebuilder:validation:Enum=auto;http2;quic
	Protocol string `json:"protocol"`

	// LogLevel passes to cloudflared --loglevel.
	// +kubebuilder:default=info
	// +kubebuilder:validation:Enum=debug;info;warn;error
	LogLevel string `json:"logLevel"`

	// GracePeriodSeconds is the cloudflared --grace-period (seconds).
	// terminationGracePeriodSeconds on the Pod is set to GracePeriodSeconds+15.
	// +kubebuilder:default=30
	// +kubebuilder:validation:Minimum=0
	GracePeriodSeconds int64 `json:"gracePeriodSeconds"`

	// Resources are the container resource requests/limits. Defaults observe-
	// not-prescribe: 50m/64Mi requests, 200m/256Mi limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// NodeSelector is a pass-through to the Pod spec.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations is a pass-through to the Pod spec.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// Affinity is a pass-through to the Pod spec.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// TopologySpreadConstraints is a pass-through to the Pod spec.
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`

	// OriginCASecretRef, when set, mounts the referenced Secret at
	// /etc/cloudflared/ca/ in the cloudflared Pod and threads
	// originRequest.caPool: /etc/cloudflared/ca/<key> into ingress entries
	// when noTLSVerify is false. Use for self-signed in-cluster origin TLS.
	// +optional
	OriginCASecretRef *SecretReference `json:"originCASecretRef,omitempty"`
}

// ConnectorImage specifies the cloudflared container image.
type ConnectorImage struct {
	// Repository is the container image repository.
	// +kubebuilder:default="docker.io/cloudflare/cloudflared"
	// +optional
	Repository string `json:"repository,omitempty"`

	// Tag is the image tag. When empty, the operator's compile-time default
	// applies. Partial overrides (repository-only OR tag-only) preserve the
	// user's value and combine with the default for the unset half.
	// +optional
	Tag string `json:"tag,omitempty"`
}

// TunnelRoutingSpec configures tunnel-wide routing defaults.
type TunnelRoutingSpec struct {
	// Fallback handles traffic that no synthesized ingress entry matches.
	// Omit to fall through to the auto-appended http_status:404.
	// +optional
	Fallback *TunnelFallback `json:"fallback,omitempty"`

	// OriginRequest defaults applied to all synthesized rules unless overridden
	// by per-source annotations (no-tls-verify, origin-server-name, …).
	// +optional
	OriginRequest *TunnelOriginRequest `json:"originRequest,omitempty"`
}

// TunnelFallback is the catch-all backend. Discriminated union: exactly one of
// URL or HTTPStatus must be set. Enforced via CEL on the parent CRD.
type TunnelFallback struct {
	// URL is a full URL backend (e.g. "http://default.svc.cluster.local").
	// +optional
	URL *string `json:"url,omitempty"`
	// HTTPStatus is a synthetic status backend (e.g. 404, 503).
	// +optional
	HTTPStatus *int32 `json:"httpStatus,omitempty"`
}

// TunnelOriginRequest mirrors cloudflared's originRequest block at the
// tunnel level (defaults inherited by every ingress entry).
type TunnelOriginRequest struct {
	// NoTLSVerify disables TLS verification to the origin.
	// +optional
	NoTLSVerify *bool `json:"noTLSVerify,omitempty"`
	// OriginServerName is the expected SAN on the origin certificate.
	// +optional
	OriginServerName *string `json:"originServerName,omitempty"`
	// ConnectTimeoutSeconds is the per-connection timeout to origin.
	// +optional
	ConnectTimeoutSeconds *int32 `json:"connectTimeoutSeconds,omitempty"`
}

// CloudflareTunnelStatus is the observed state.
type CloudflareTunnelStatus struct {
	// Conditions: Ready, ConnectorReady, RemoteConfigApplied, plus reason
	// strings drawn from internal/conventions/conditions.go.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Phase is a coarse summary derived from the Ready condition (Foundation §8).
	// +optional
	// +kubebuilder:default=Pending
	Phase Phase `json:"phase,omitempty"`

	// TunnelID is the Cloudflare-assigned UUID.
	// +optional
	TunnelID string `json:"tunnelID,omitempty"`

	// TunnelCNAME is <tunnelID>.cfargotunnel.com. Populated after create.
	// +optional
	TunnelCNAME string `json:"tunnelCNAME,omitempty"`

	// ConnectionsHealthy is the count of active connectors observed via
	// GET /cfd_tunnel/{id}/connections. Zero is a meaningful value (no
	// healthy connectors yet) and is always serialized.
	// +optional
	ConnectionsHealthy int32 `json:"connectionsHealthy"`

	// ObservedIngress is the materialized ingress list as last PUT to
	// /configurations. Used for drift detection — the reconciler skips a
	// PUT when the computed list matches this slice exactly.
	// +optional
	ObservedIngress []IngressEntrySnapshot `json:"observedIngress,omitempty"`

	// AttachedSources lists every source object currently contributing to
	// this tunnel's ingress. Informational; the lexicographically-first entry
	// is the owner-reference target (or the original owner if still present).
	// +listType=map
	// +listMapKey=kind
	// +listMapKey=namespace
	// +listMapKey=name
	// +optional
	AttachedSources []AttachedSource `json:"attachedSources,omitempty"`

	// ObservedGeneration is the .metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastSyncedAt is the wall-clock time of the most recent successful
	// reconcile (drift check + remote-config PUT, even if a no-op).
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`

	// LastOrphanedAt is the timestamp of the first reconcile that observed this
	// CR as orphaned (auto-created with no OwnerReferences and an empty
	// Status.AttachedSources). Self-delete fires only when a subsequent
	// reconcile observes the same state past the pending-deletion grace window
	// (60s). Cleared as soon as a source attaches or owner-transfer succeeds.
	// Operator-managed; user edits will be reverted on the next reconcile.
	// +optional
	LastOrphanedAt *metav1.Time `json:"lastOrphanedAt,omitempty"`
}

// IngressEntrySnapshot is a status-only snapshot of one materialized ingress
// entry. NOT the source-of-truth shape — the reconciler computes ingress fresh
// each loop. This is for drift detection only.
type IngressEntrySnapshot struct {
	// Hostname is the public hostname.
	// +optional
	Hostname string `json:"hostname,omitempty"`
	// Path is the optional path filter.
	// +optional
	Path string `json:"path,omitempty"`
	// Service is the cloudflared service URL (e.g. http://svc.ns:80).
	// +optional
	Service string `json:"service,omitempty"`
}

// AttachedSource identifies one source object contributing to this tunnel.
// Fields are immutable post-create from the source reconciler's perspective.
type AttachedSource struct {
	// Kind is one of Service / Gateway / HTTPRoute / TLSRoute.
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`
	// Name of the source object.
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// Namespace of the source object.
	// +kubebuilder:validation:Required
	Namespace string `json:"namespace"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Tunnel Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Tunnel ID",type=string,JSONPath=`.status.tunnelID`
// +kubebuilder:printcolumn:name="CNAME",type=string,JSONPath=`.status.tunnelCNAME`
// +kubebuilder:printcolumn:name="Connectors",type=integer,JSONPath=`.status.connectionsHealthy`
// +kubebuilder:printcolumn:name=Phase,type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(self.spec.routing) || !has(self.spec.routing.fallback) || (has(self.spec.routing.fallback.url) ? 1 : 0) + (has(self.spec.routing.fallback.httpStatus) ? 1 : 0) == 1",message="routing.fallback: exactly one of url or httpStatus must be set"
// CloudflareTunnel is the Schema for the cloudflaretunnels API.
type CloudflareTunnel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CloudflareTunnelSpec   `json:"spec,omitempty"`
	Status CloudflareTunnelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// CloudflareTunnelList contains a list of CloudflareTunnel.
type CloudflareTunnelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareTunnel `json:"items"`
}
