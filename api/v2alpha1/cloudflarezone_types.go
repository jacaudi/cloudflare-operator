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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CloudflareZoneSpec defines the desired state of a Cloudflare Zone.
type CloudflareZoneSpec struct {
	// Name is the domain name to onboard (e.g., "example.com").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Type is the zone type. "full" means Cloudflare is authoritative DNS;
	// "partial" is CNAME setup; "secondary" mirrors an upstream master.
	// Immutable after creation.
	// +kubebuilder:validation:Enum=full;partial;secondary
	// +kubebuilder:default=full
	Type string `json:"type"`

	// Paused indicates whether the zone is paused (not serving traffic through Cloudflare).
	// +optional
	Paused *bool `json:"paused,omitempty"`

	// DeletionPolicy controls what happens when the CR is deleted.
	// "Retain" (default) leaves the zone in Cloudflare; "Delete" removes it.
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default=Retain
	DeletionPolicy string `json:"deletionPolicy"`

	// Cloudflare overrides the top-level credential + account from the
	// CloudflareOperator CR. Per Foundation §5 the token and accountID
	// are inherited or overridden as a unit; CEL on this CRD must reject
	// setting only one. Omitted entirely → top-level default applies.
	// +optional
	Cloudflare *CloudflareCredentialRef `json:"cloudflare,omitempty"`

	// Interval is the reconciliation interval.
	// +kubebuilder:default="30m"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`
}

// CloudflareZoneStatus defines the observed state of a CloudflareZone.
type CloudflareZoneStatus struct {
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	ZoneID string `json:"zoneID,omitempty"`

	// Status is the zone status in Cloudflare (initializing, pending, active, moved).
	// +optional
	Status string `json:"status,omitempty"`

	// +optional
	NameServers []string `json:"nameServers,omitempty"`

	// +optional
	OriginalNameServers []string `json:"originalNameServers,omitempty"`

	// +optional
	OriginalRegistrar string `json:"originalRegistrar,omitempty"`

	// +optional
	ActivatedOn *metav1.Time `json:"activatedOn,omitempty"`

	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse summary derived from the Ready condition (Foundation §8).
	// +optional
	// +kubebuilder:default=Pending
	Phase Phase `json:"phase,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Domain,type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Zone ID",type=string,JSONPath=`.status.zoneID`
// +kubebuilder:printcolumn:name=Status,type=string,JSONPath=`.status.status`
// +kubebuilder:printcolumn:name=Phase,type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name=Ready,type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name=Age,type=date,JSONPath=`.metadata.creationTimestamp`
// CloudflareZone is the Schema for the cloudflarezones API.
type CloudflareZone struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CloudflareZoneSpec   `json:"spec,omitempty"`
	Status CloudflareZoneStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// CloudflareZoneList contains a list of CloudflareZone.
type CloudflareZoneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareZone `json:"items"`
}
