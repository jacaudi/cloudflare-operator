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

// CloudflareZoneSpec defines the desired state of a Cloudflare Zone.
type CloudflareZoneSpec struct {
	// Name is the domain name to onboard (e.g., "example.com").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Type is the zone type. "full" means Cloudflare is the authoritative DNS.
	// "partial" is a CNAME setup. Immutable after creation.
	// +kubebuilder:validation:Enum=full;partial;secondary
	// +kubebuilder:default="full"
	// +optional
	Type string `json:"type,omitempty"`

	// Paused indicates whether the zone is paused (not serving traffic through Cloudflare).
	// +optional
	Paused *bool `json:"paused,omitempty"`

	// DeletionPolicy controls what happens when the CR is deleted.
	// "Retain" (default) leaves the zone in Cloudflare.
	// "Delete" removes the zone from Cloudflare.
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default="Retain"
	// +optional
	DeletionPolicy string `json:"deletionPolicy,omitempty"`

	// SecretRef references a Secret containing Cloudflare API credentials.
	// +kubebuilder:validation:Required
	SecretRef SecretReference `json:"secretRef"`

	// Interval is the reconciliation interval.
	// +kubebuilder:default="30m"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`
}

// CloudflareZoneStatus defines the observed state of a CloudflareZone.
type CloudflareZoneStatus struct {
	// Conditions represent the latest available observations.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ZoneID is the Cloudflare Zone ID.
	// +optional
	ZoneID string `json:"zoneID,omitempty"`

	// Status is the zone status in Cloudflare (initializing, pending, active, moved).
	// +optional
	Status string `json:"status,omitempty"`

	// NameServers are the Cloudflare-assigned nameservers for this zone.
	// Update your registrar's NS records to these values to activate the zone.
	// +optional
	NameServers []string `json:"nameServers,omitempty"`

	// OriginalNameServers are the nameservers before migration to Cloudflare.
	// +optional
	OriginalNameServers []string `json:"originalNameServers,omitempty"`

	// OriginalRegistrar is the registrar at the time of onboarding.
	// +optional
	OriginalRegistrar string `json:"originalRegistrar,omitempty"`

	// ActivatedOn is the time the zone became active.
	// +optional
	ActivatedOn *metav1.Time `json:"activatedOn,omitempty"`

	// LastSyncedAt is the last time the zone was successfully synced.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`

	// ObservedGeneration is the most recently observed generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Domain",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Zone ID",type=string,JSONPath=`.status.zoneID`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.status`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CloudflareZone is the Schema for the cloudflarezones API
type CloudflareZone struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of CloudflareZone
	// +required
	Spec CloudflareZoneSpec `json:"spec"`

	// status defines the observed state of CloudflareZone
	// +optional
	Status CloudflareZoneStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// CloudflareZoneList contains a list of CloudflareZone
type CloudflareZoneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []CloudflareZone `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CloudflareZone{}, &CloudflareZoneList{})
}
