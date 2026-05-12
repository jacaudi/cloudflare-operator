package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CloudflareRulesetSpec is a scaffold; spec 2 populates fields.
type CloudflareRulesetSpec struct {
	// Cloudflare overrides the top-level credential + account.
	// +optional
	Cloudflare *CloudflareCredentialRef `json:"cloudflare,omitempty"`
}

// CloudflareRulesetStatus is a scaffold.
type CloudflareRulesetStatus struct {
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
// CloudflareRuleset represents a Cloudflare ruleset for a phase entrypoint.
type CloudflareRuleset struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CloudflareRulesetSpec   `json:"spec,omitempty"`
	Status            CloudflareRulesetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// CloudflareRulesetList contains a list of CloudflareRuleset.
type CloudflareRulesetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareRuleset `json:"items"`
}
