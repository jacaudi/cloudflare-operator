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

// CloudflareDNSRecordSpec defines the desired state of a Cloudflare DNS record.
type CloudflareDNSRecordSpec struct {
	// ZoneID is the Cloudflare Zone ID.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ZoneID string `json:"zoneID"`

	// Name is the DNS record name (e.g., "example.com", "sub.example.com").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Type is the DNS record type.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=A;AAAA;CNAME;SRV;MX;TXT;NS
	Type string `json:"type"`

	// Content is the record content (IP address, hostname, etc.).
	// Mutually exclusive with DynamicIP.
	// +optional
	Content *string `json:"content,omitempty"`

	// DynamicIP enables automatic external IP resolution for this record.
	// Only valid for type A. Mutually exclusive with Content.
	// +optional
	DynamicIP bool `json:"dynamicIP,omitempty"`

	// TTL is the time-to-live in seconds. Use 1 for automatic.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +optional
	TTL int `json:"ttl,omitempty"`

	// Proxied indicates whether the record is proxied through Cloudflare.
	// +optional
	Proxied *bool `json:"proxied,omitempty"`

	// SRVData contains SRV-specific record data.
	// Required when Type is SRV.
	// +optional
	SRVData *SRVData `json:"srvData,omitempty"`

	// Priority is the record priority (used for MX and SRV records).
	// +optional
	Priority *int `json:"priority,omitempty"`

	// SecretRef references a Secret containing Cloudflare API credentials.
	// +kubebuilder:validation:Required
	SecretRef SecretReference `json:"secretRef"`

	// Interval is the reconciliation interval for drift detection.
	// +kubebuilder:default="5m"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`
}

// SRVData contains SRV-specific record fields.
type SRVData struct {
	// Service is the SRV service name (e.g., "_satisfactory").
	// +kubebuilder:validation:Required
	Service string `json:"service"`

	// Proto is the SRV protocol.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=_tcp;_udp;_tls
	Proto string `json:"proto"`

	// Priority of the SRV record.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	Priority int `json:"priority"`

	// Weight of the SRV record.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	Weight int `json:"weight"`

	// Port is the target port.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	Port int `json:"port"`

	// Target is the target hostname for the SRV record.
	// +kubebuilder:validation:Required
	Target string `json:"target"`
}

// CloudflareDNSRecordStatus defines the observed state of a CloudflareDNSRecord.
type CloudflareDNSRecordStatus struct {
	// Conditions represent the latest available observations of the resource's state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// RecordID is the Cloudflare DNS record ID.
	// +optional
	RecordID string `json:"recordID,omitempty"`

	// CurrentContent is the current content/value of the DNS record in Cloudflare.
	// +optional
	CurrentContent string `json:"currentContent,omitempty"`

	// LastSyncedAt is the last time the record was successfully synced.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`

	// ObservedGeneration is the most recently observed generation of the CR.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Record Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Content",type=string,JSONPath=`.status.currentContent`
// +kubebuilder:printcolumn:name="Proxied",type=boolean,JSONPath=`.spec.proxied`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CloudflareDNSRecord is the Schema for the cloudflarednsrecords API
type CloudflareDNSRecord struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of CloudflareDNSRecord
	// +required
	Spec CloudflareDNSRecordSpec `json:"spec"`

	// status defines the observed state of CloudflareDNSRecord
	// +optional
	Status CloudflareDNSRecordStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// CloudflareDNSRecordList contains a list of CloudflareDNSRecord
type CloudflareDNSRecordList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []CloudflareDNSRecord `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CloudflareDNSRecord{}, &CloudflareDNSRecordList{})
}
