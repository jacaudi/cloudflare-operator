package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CloudflareZoneConfigSpec is a scaffold; spec 2 populates fields.
type CloudflareZoneConfigSpec struct {
	// Cloudflare overrides the top-level credential + account.
	// +optional
	Cloudflare *CloudflareCredentialRef `json:"cloudflare,omitempty"`
}

// CloudflareZoneConfigStatus is a scaffold.
type CloudflareZoneConfigStatus struct {
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
// CloudflareZoneConfig holds zone-level settings (DNSSEC, SSL, security headers, etc.).
type CloudflareZoneConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CloudflareZoneConfigSpec   `json:"spec,omitempty"`
	Status            CloudflareZoneConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// CloudflareZoneConfigList contains a list of CloudflareZoneConfig.
type CloudflareZoneConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareZoneConfig `json:"items"`
}
