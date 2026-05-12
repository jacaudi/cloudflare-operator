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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CloudflareDNSRecordSpec defines the desired state of a Cloudflare DNS record.
type CloudflareDNSRecordSpec struct {
	// ZoneID is the Cloudflare Zone ID. Mutually exclusive with ZoneRef.
	// +optional
	// +kubebuilder:validation:MinLength=1
	ZoneID string `json:"zoneID,omitempty"`

	// ZoneRef references a CloudflareZone CR. Mutually exclusive with ZoneID.
	// +optional
	ZoneRef *ZoneReference `json:"zoneRef,omitempty"`

	// Name is the DNS record name (e.g., "example.com", "sub.example.com").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Type is the DNS record type.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=A;AAAA;CNAME;SRV;MX;TXT;NS
	Type string `json:"type"`

	// Content is the record content (IP, hostname, etc.). XOR with DynamicIP.
	// +optional
	Content *string `json:"content,omitempty"`

	// DynamicIP enables automatic external IP resolution. Only valid for A/AAAA.
	// XOR with Content.
	// +optional
	DynamicIP bool `json:"dynamicIP,omitempty"`

	// TTL in seconds. Use 1 for automatic.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +optional
	TTL int `json:"ttl,omitempty"`

	// Proxied indicates whether the record is proxied through Cloudflare.
	// +optional
	Proxied *bool `json:"proxied,omitempty"`

	// SRVData contains SRV-specific record fields. Required when Type=SRV.
	// +optional
	SRVData *SRVData `json:"srvData,omitempty"`

	// Priority for MX / SRV records.
	// +optional
	Priority *int `json:"priority,omitempty"`

	// Adopt takes over an existing record matching (name, type) if found.
	// No ownership verification is performed in this phase (the TXT companion
	// registry is deferred) — only enable for records you are sure are not
	// managed by another source.
	// +optional
	Adopt bool `json:"adopt,omitempty"`

	// Cloudflare overrides the top-level credential + account from the
	// CloudflareOperator CR. Per Foundation §5 the token and accountID are
	// inherited or overridden as a unit; CEL rejects mixing.
	// +optional
	Cloudflare *CloudflareCredentialRef `json:"cloudflare,omitempty"`

	// Interval is the reconciliation interval for drift detection.
	// +kubebuilder:default="5m"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`
}

// SRVData contains SRV-specific record fields.
type SRVData struct {
	// +kubebuilder:validation:Required
	Service string `json:"service"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=_tcp;_udp;_tls
	Proto string `json:"proto"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	Priority int `json:"priority"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	Weight int `json:"weight"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	Port int `json:"port"`
	// +kubebuilder:validation:Required
	Target string `json:"target"`
}

// CloudflareDNSRecordStatus defines the observed state.
type CloudflareDNSRecordStatus struct {
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Phase is a coarse summary derived from the Ready condition (Foundation §8).
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// RecordID is the Cloudflare ID of the managed DNS record.
	// +optional
	RecordID string `json:"recordID,omitempty"`
	// CurrentContent is the most-recently-observed record content (post-resolve
	// for DynamicIP).
	// +optional
	CurrentContent string `json:"currentContent,omitempty"`
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Record Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name=Type,type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name=Content,type=string,JSONPath=`.status.currentContent`
// +kubebuilder:printcolumn:name=Proxied,type=boolean,JSONPath=`.spec.proxied`
// +kubebuilder:printcolumn:name=Phase,type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name=Ready,type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name=Age,type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="has(self.spec.zoneID) || has(self.spec.zoneRef)",message="one of zoneID or zoneRef is required"
// +kubebuilder:validation:XValidation:rule="!(has(self.spec.zoneID) && has(self.spec.zoneRef))",message="zoneID and zoneRef are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!(has(self.spec.content) && self.spec.dynamicIP)",message="content and dynamicIP are mutually exclusive"
// CloudflareDNSRecord is the Schema for the cloudflarednsrecords API.
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
