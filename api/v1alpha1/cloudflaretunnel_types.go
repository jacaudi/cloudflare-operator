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

// CloudflareTunnelStatus defines the observed state of a CloudflareTunnel.
type CloudflareTunnelStatus struct {
	// Conditions: Ready, ConnectorReady, IngressConfigured.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

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
