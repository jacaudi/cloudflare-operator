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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CloudflareTunnelSpec defines the desired state of a Cloudflare Tunnel.
type CloudflareTunnelSpec struct {
	// Name is the tunnel name in Cloudflare.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// SecretRef references a Secret containing Cloudflare API credentials.
	// +kubebuilder:validation:Required
	SecretRef SecretReference `json:"secretRef"`

	// GeneratedSecretName is the name of the Secret to create with tunnel
	// credentials.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	GeneratedSecretName string `json:"generatedSecretName"`

	// Interval is the reconciliation interval.
	// +kubebuilder:default="30m"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`

	// Connector configures an operator-managed cloudflared workload for this
	// tunnel. When disabled (default), users run cloudflared themselves.
	// +optional
	Connector *ConnectorSpec `json:"connector,omitempty"`

	// Routing configures tunnel-wide defaults for cloudflared ingress:
	// the default backend (for traffic no CloudflareTunnelRule matches) and
	// originRequest defaults applied to all rules.
	// +optional
	Routing *TunnelRoutingSpec `json:"routing,omitempty"`

	// ApexHostname is an opt-in operator-managed apex CNAME for this
	// tunnel. When set, the operator reconciles a single
	// CloudflareDNSRecord named "<metadata.name>-apex" in this CR's
	// namespace, owner-reffed to the tunnel. Per-route records emitted by
	// the source controllers (HTTPRoute, Service) targeting this tunnel
	// CNAME to the apex name once it is Ready. See issue #101.
	// +optional
	ApexHostname *ApexHostnameSpec `json:"apexHostname,omitempty"`
}

// ConnectorSpec configures the operator-managed cloudflared Deployment.
type ConnectorSpec struct {
	// Enabled toggles whether the operator creates a cloudflared Deployment.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled"`

	// Replicas is the desired pod count.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1
	// +optional
	Replicas int32 `json:"replicas"`

	// Image specifies the cloudflared container image. When omitted, the
	// operator uses a compile-time default bumped per operator release.
	// +optional
	Image *ConnectorImage `json:"image,omitempty"`

	// Resources are the container resource requests/limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// NodeSelector is a pass-through to the pod spec.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations is a pass-through to the pod spec.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// Affinity is a pass-through to the pod spec.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// TopologySpreadConstraints is a pass-through to the pod spec.
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`

	// NameOverride sets the base name for the operator-managed connector
	// resources. When set, the Deployment and ServiceAccount are named
	// exactly NameOverride and the ConfigMap is named "<NameOverride>-config".
	// When unset, names default to the "cloudflared-<tunnel.metadata.name>"
	// family (Deployment and ServiceAccount) and
	// "cloudflared-<tunnel.metadata.name>-config" (ConfigMap).
	//
	// On upgrade from operator versions that defaulted the base to
	// "<tunnel.metadata.name>-connector", the connector reconciler
	// automatically deletes the legacy-named resources owned by this
	// CloudflareTunnel after the new-named resources are running. Setting
	// NameOverride suppresses this auto-cleanup; the user is in charge.
	//
	// Changing NameOverride on a live tunnel reconciles new resources at the
	// new name; the old resources are not cleaned up automatically (see #52).
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=253
	// +optional
	NameOverride string `json:"nameOverride,omitempty"`
}

// ConnectorImage specifies the cloudflared container image.
type ConnectorImage struct {
	// Repository is the container image repository, for example
	// "docker.io/cloudflare/cloudflared". Defaults to the upstream
	// Cloudflare image.
	// +kubebuilder:default="docker.io/cloudflare/cloudflared"
	// +optional
	Repository string `json:"repository"`

	// Tag is the image tag. When omitted, the operator uses a
	// compile-time default bumped per operator release.
	// +optional
	Tag string `json:"tag,omitempty"`
}

// TunnelRoutingSpec configures tunnel-wide routing defaults.
type TunnelRoutingSpec struct {
	// DefaultBackend handles traffic that no CloudflareTunnelRule matches.
	// Omit to fall through to the auto-appended http_status:404.
	// +optional
	DefaultBackend *TunnelRuleBackend `json:"defaultBackend,omitempty"`

	// OriginRequest defaults applied to all rules unless overridden.
	// +optional
	OriginRequest *TunnelRuleOriginRequest `json:"originRequest,omitempty"`
}

// ApexHostnameSpec configures an opt-in operator-managed apex CNAME for
// this tunnel. When set, the operator reconciles a single
// CloudflareDNSRecord at the named FQDN that CNAMEs to the tunnel's
// .cfargotunnel.com address; per-route records emitted by the
// HTTPRoute/Service sources targeting this tunnel CNAME to the apex
// instead of the tunnel UUID, so tunnel rotation is one record update
// rather than N. See issue #101.
type ApexHostnameSpec struct {
	// Name is the apex FQDN. Must fall under the zone referenced by
	// ZoneRef (i.e. equal the zone name or have it as a suffix).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// ZoneRef references the CloudflareZone that owns Name.
	// +kubebuilder:validation:Required
	ZoneRef ZoneReference `json:"zoneRef"`

	// Proxied controls whether the apex record is orange-clouded.
	// Defaults to true; the apex's purpose is hiding the tunnel UUID
	// behind a stable user-facing FQDN, so proxied is the natural default.
	// +kubebuilder:default=true
	// +optional
	Proxied *bool `json:"proxied,omitempty"`
}

// ApexHostnameStatus reflects the operator-managed apex CloudflareDNSRecord.
type ApexHostnameStatus struct {
	// Name is the resolved apex FQDN currently being reconciled.
	// +optional
	Name string `json:"name,omitempty"`

	// RecordID is the Cloudflare DNS record ID for the apex CNAME, copied
	// from the underlying CloudflareDNSRecord's status. Empty until the
	// apex record has reconciled at least once.
	// +optional
	RecordID string `json:"recordID,omitempty"`
}

// CloudflareTunnelStatus defines the observed state of a CloudflareTunnel.
type CloudflareTunnelStatus struct {
	// Conditions: Ready, ConnectorReady, IngressConfigured.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Phase is a coarse summary of the reconciliation state. See
	// cloudflarev1alpha1.Phase for the enum values.
	// +optional
	// +kubebuilder:default=Pending
	Phase Phase `json:"phase,omitempty"`

	// TunnelID is the Cloudflare Tunnel ID.
	// +optional
	TunnelID string `json:"tunnelID,omitempty"`

	// TunnelCNAME is the CNAME for the tunnel (tunnelID.cfargotunnel.com).
	// +optional
	TunnelCNAME string `json:"tunnelCNAME,omitempty"`

	// CredentialsSecretName is the name of the generated credentials Secret.
	// +optional
	CredentialsSecretName string `json:"credentialsSecretName,omitempty"`

	// Connector reflects the state of the operator-managed cloudflared
	// Deployment (when spec.connector.enabled=true).
	// +optional
	Connector *ConnectorStatus `json:"connector,omitempty"`

	// ApexHostname reflects the operator-managed apex CloudflareDNSRecord
	// when spec.apexHostname is set. Cleared when spec.apexHostname is
	// removed.
	// +optional
	ApexHostname *ApexHostnameStatus `json:"apexHostname,omitempty"`

	// LastSyncedAt is the last time the tunnel was successfully synced.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`

	// ObservedGeneration is the most recently observed generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// ConnectorStatus reports on the operator-managed cloudflared workload.
type ConnectorStatus struct {
	// Replicas is the desired pod count from the spec at last render.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// ReadyReplicas mirrors Deployment.status.readyReplicas.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// ConfigHash is the sha256 hash of the rendered cloudflared config.yaml.
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// Image is the image reference actually running.
	// +optional
	Image string `json:"image,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Tunnel Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Tunnel ID",type=string,JSONPath=`.status.tunnelID`
// +kubebuilder:printcolumn:name="CNAME",type=string,JSONPath=`.status.tunnelCNAME`
// +kubebuilder:printcolumn:name="Apex",type=string,JSONPath=`.status.apexHostname.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(self.spec.routing) || !has(self.spec.routing.defaultBackend) || (has(self.spec.routing.defaultBackend.serviceRef) ? 1 : 0) + (has(self.spec.routing.defaultBackend.url) ? 1 : 0) + (has(self.spec.routing.defaultBackend.httpStatus) ? 1 : 0) == 1",message="routing.defaultBackend: exactly one of serviceRef, url, httpStatus must be set"

// CloudflareTunnel is the Schema for the cloudflaretunnels API
type CloudflareTunnel struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of CloudflareTunnel
	// +required
	Spec CloudflareTunnelSpec `json:"spec"`

	// status defines the observed state of CloudflareTunnel
	// +optional
	Status CloudflareTunnelStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// CloudflareTunnelList contains a list of CloudflareTunnel
type CloudflareTunnelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []CloudflareTunnel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CloudflareTunnel{}, &CloudflareTunnelList{})
}
