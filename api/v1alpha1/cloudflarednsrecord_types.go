package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CloudflareDNSRecordSpec is a scaffold; spec 2 populates fields.
type CloudflareDNSRecordSpec struct {
	// Cloudflare overrides the top-level credential + account.
	// +optional
	Cloudflare *CloudflareCredentialRef `json:"cloudflare,omitempty"`
}

// CloudflareDNSRecordStatus is a scaffold.
type CloudflareDNSRecordStatus struct {
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
// CloudflareDNSRecord represents a single DNS record in a zone.
type CloudflareDNSRecord struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CloudflareDNSRecordSpec   `json:"spec,omitempty"`
	Status            CloudflareDNSRecordStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// CloudflareDNSRecordList contains a list of CloudflareDNSRecord.
type CloudflareDNSRecordList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareDNSRecord `json:"items"`
}
