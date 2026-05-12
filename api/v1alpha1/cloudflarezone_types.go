package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CloudflareZoneSpec is a scaffold; spec 2 populates fields.
type CloudflareZoneSpec struct {
	// Cloudflare overrides the top-level credential + account.
	// +optional
	Cloudflare *CloudflareCredentialRef `json:"cloudflare,omitempty"`
}

// CloudflareZoneStatus is a scaffold.
type CloudflareZoneStatus struct {
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
// CloudflareZone represents a Cloudflare DNS zone managed by the operator.
type CloudflareZone struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CloudflareZoneSpec   `json:"spec,omitempty"`
	Status            CloudflareZoneStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// CloudflareZoneList contains a list of CloudflareZone.
type CloudflareZoneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareZone `json:"items"`
}
