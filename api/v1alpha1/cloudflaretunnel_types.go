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

	// GeneratedSecretName is the name of the Secret to create with tunnel credentials.
	// The Secret will contain a "credentials.json" key with the tunnel credentials.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	GeneratedSecretName string `json:"generatedSecretName"`

	// Interval is the reconciliation interval.
	// +kubebuilder:default="30m"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`
}

// CloudflareTunnelStatus defines the observed state of a CloudflareTunnel.
type CloudflareTunnelStatus struct {
	// Conditions represent the latest available observations.
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

	// LastSyncedAt is the last time the tunnel was successfully synced.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`

	// ObservedGeneration is the most recently observed generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Tunnel Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Tunnel ID",type=string,JSONPath=`.status.tunnelID`
// +kubebuilder:printcolumn:name="CNAME",type=string,JSONPath=`.status.tunnelCNAME`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

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
