package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CloudflareTunnelSpec is a scaffold; spec 3 populates fields.
type CloudflareTunnelSpec struct {
	// Cloudflare overrides the top-level credential + account.
	// +optional
	Cloudflare *CloudflareCredentialRef `json:"cloudflare,omitempty"`
}

// CloudflareTunnelStatus is a scaffold.
type CloudflareTunnelStatus struct {
	// Conditions reflects the current reconciliation state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// CloudflareTunnel represents a Cloudflare tunnel + its cloudflared dataplane.
type CloudflareTunnel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CloudflareTunnelSpec   `json:"spec,omitempty"`
	Status            CloudflareTunnelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// CloudflareTunnelList contains a list of CloudflareTunnel.
type CloudflareTunnelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareTunnel `json:"items"`
}
