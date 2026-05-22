/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package v2alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// RecordMode controls operator write behavior on a CloudflareDNSRecord.
// +kubebuilder:validation:Enum=Managed;Observe
type RecordMode string

const (
	// RecordModeManaged is the default. The operator creates / updates /
	// deletes the underlying Cloudflare record and TXT companion as needed.
	RecordModeManaged RecordMode = "Managed"

	// RecordModeObserve means the operator reads Cloudflare state and
	// populates Status, but never writes. Spec.Adopt has no effect. Useful
	// for verifying state before promoting to Managed (which would
	// otherwise refuse adoption without a matching TXT companion under
	// design §2 Q2's no-silent-backfill rule).
	RecordModeObserve RecordMode = "Observe"
)

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

	// Priority is the MX record priority (lower = preferred). SRV records use
	// srvData.priority instead.
	// +optional
	Priority *int `json:"priority,omitempty"`

	// Adopt, when true, lets the operator take over a pre-existing Cloudflare
	// record instead of creating a new one. Adoption is TXT-ownership-verified:
	// the operator only adopts a record whose companion TXT registry entry
	// identifies THIS CloudflareDNSRecord. A record with no companion, a
	// foreign companion, or an unparseable one is refused
	// (AdoptRefusedNoTXT / AdoptRefusedForeign) — there is no silent backfill.
	// Pre-feature adopted records must be migrated via the documented
	// TXT-registry migration procedure (design §5.4) before Adopt succeeds.
	// +optional
	Adopt bool `json:"adopt,omitempty"`

	// Mode controls operator write behavior on this record.
	// Default Managed: operator creates / updates / deletes the underlying
	// Cloudflare record and TXT companion as needed.
	// Observe: operator reads but never writes. Useful for verifying state
	// before claiming a record under Adopt:true (which would otherwise
	// refuse without a matching TXT companion).
	// +kubebuilder:default=Managed
	// +optional
	Mode RecordMode `json:"mode,omitempty"`

	// Cloudflare overrides the operator-level default credential (sourced
	// from the operator's CLOUDFLARE_API_TOKEN/CLOUDFLARE_ACCOUNT_ID env,
	// chart-set from a Secret). Per Foundation §5 the token and accountID
	// are inherited or overridden as a unit; CEL rejects mixing.
	// Omitted entirely → the operator-level env default applies.
	// +optional
	Cloudflare *CloudflareCredentialRef `json:"cloudflare,omitempty"`

	// Interval is the reconciliation interval for drift detection.
	// +kubebuilder:default="5m"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`
}

// SRVData contains SRV-specific record fields.
type SRVData struct {
	// Service is the symbolic service name (e.g., "_satisfactory", "_minecraft").
	// +kubebuilder:validation:Required
	Service string `json:"service"`

	// Proto is the transport protocol.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=_tcp;_udp;_tls
	Proto string `json:"proto"`

	// Priority is the SRV priority (lower = preferred).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	Priority int `json:"priority"`

	// Weight is the SRV weight for records with the same priority
	// (higher = more traffic).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	Weight int `json:"weight"`

	// Port is the TCP/UDP port the service listens on.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	Port int `json:"port"`

	// Target is the canonical hostname of the machine providing the service.
	// +kubebuilder:validation:Required
	Target string `json:"target"`
}

// ObservedTXTPayload mirrors the decoded RegistryPayload fields in the CR's
// Status for user-visible diagnostics. The internal payload type lives in
// internal/cloudflare/; this is the API-stable surface.
type ObservedTXTPayload struct {
	// Version is the payload schema version (currently always 1).
	// +optional
	Version int `json:"version,omitempty"`
	// Kind is the encoded owner kind ("CloudflareDNSRecord" in v2alpha1).
	// +optional
	Kind string `json:"kind,omitempty"`
	// Namespace is the encoded owner namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Name is the encoded owner name.
	// +optional
	Name string `json:"name,omitempty"`
	// ContentHash is the SHA256 of the canonicalized spec.content at TXT
	// write time. Used by drift detection.
	// +optional
	ContentHash string `json:"contentHash,omitempty"`
	// RawContent is the raw TXT content as received from Cloudflare when
	// decoding failed. Set instead of Version/Kind/Namespace/Name so users
	// can see what's there even when the operator can't parse it.
	// +optional
	RawContent string `json:"rawContent,omitempty"`
	// Codec reports which decoder ("plaintext", "aes-gcm", or
	// "unrecognized") produced this payload.
	// +optional
	Codec string `json:"codec,omitempty"`
}

// CloudflareDNSRecordStatus defines the observed state.
type CloudflareDNSRecordStatus struct {
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Phase is a coarse summary derived from the Ready condition (Foundation §8).
	// +optional
	// +kubebuilder:default=Pending
	Phase Phase `json:"phase,omitempty"`
	// RecordID is the Cloudflare ID of the managed DNS record.
	// +optional
	RecordID string `json:"recordID,omitempty"`
	// CurrentContent is the most-recently-observed record content (post-resolve
	// for DynamicIP).
	// +optional
	CurrentContent string `json:"currentContent,omitempty"`
	// LastSyncedAt is the timestamp of the most recent successful reconcile.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`
	// TxtRecordID is the Cloudflare-side ID of the companion TXT record.
	// Empty when no TXT companion has been written yet. Set on successful
	// TXT write; cleared on delete.
	// +optional
	TxtRecordID string `json:"txtRecordID,omitempty"`
	// TxtAffix is the prefix used for the companion TXT record name (today
	// always "cf-txt"). Recorded for forensic clarity if the convention
	// changes (e.g., v2 affixing scheme). Operator-managed; users should
	// not edit.
	// +optional
	TxtAffix string `json:"txtAffix,omitempty"`
	// ObservedTXT carries the decoded TXT companion payload as last
	// observed from Cloudflare. Populated by both Managed and Observe modes
	// when a TXT companion exists. RawContent is set instead when decoding
	// fails.
	// +optional
	ObservedTXT *ObservedTXTPayload `json:"observedTXT,omitempty"`
	// ObservedGeneration is the .metadata.generation observed by the controller
	// during its last reconcile. When this lags .metadata.generation the
	// controller has not yet processed the latest spec.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// LastReconcileToken is the controller-owned ack of the most recent
	// cloudflare.io/reconcile-at annotation value the controller has
	// observed. The prelude in internal/reconcile.ForceReconcileRequested
	// compares this against the live annotation; mismatch forces a full
	// re-check this reconcile (bypassing the change-detection short-
	// circuit). The operator NEVER modifies the annotation itself — only
	// this status field — so admin force-triggers are not auto-cleared.
	// +optional
	LastReconcileToken string `json:"lastReconcileToken,omitempty"`
	// LegacyCompanionGCDone marks a record as having completed the one-time
	// legacy-name companion GC sweep. When true, gcLegacyCompanion is
	// skipped on subsequent reconciles. Stamped after a successful pass
	// that either (a) found no legacy candidates, or (b) successfully
	// deleted a legacy companion. Pre-S1 CRs reconcile once, set the
	// field, and never pay the GC cost again. Purely additive: existing
	// CRs without the field behave like field=false on first reconcile.
	// +optional
	LegacyCompanionGCDone bool `json:"legacyCompanionGCDone,omitempty"`
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
// +kubebuilder:validation:XValidation:rule="!(has(self.spec.content) && has(self.spec.dynamicIP) && self.spec.dynamicIP)",message="content and dynamicIP are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!(self.spec.type == 'SRV' && has(self.spec.priority))",message="for SRV records use srvData.priority, not spec.priority"
// +kubebuilder:validation:XValidation:rule="self.spec.type == 'MX' ? has(self.spec.priority) : true",message="MX records require spec.priority"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.dynamicIP) || !self.spec.dynamicIP || (self.spec.type == 'A' || self.spec.type == 'AAAA')",message="dynamicIP is only valid for A or AAAA records"
// CloudflareDNSRecord is the Schema for the cloudflarednsrecords API.
type CloudflareDNSRecord struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CloudflareDNSRecordSpec   `json:"spec,omitempty"`
	Status            CloudflareDNSRecordStatus `json:"status,omitempty"`
}

// GetLastSyncedAt returns the LastSyncedAt bookkeeping field.
func (s *CloudflareDNSRecordStatus) GetLastSyncedAt() *metav1.Time { return s.LastSyncedAt }

// SetLastSyncedAt sets the LastSyncedAt bookkeeping field.
func (s *CloudflareDNSRecordStatus) SetLastSyncedAt(t *metav1.Time) { s.LastSyncedAt = t }

// GetObservedGeneration returns the ObservedGeneration bookkeeping field.
func (s *CloudflareDNSRecordStatus) GetObservedGeneration() int64 { return s.ObservedGeneration }

// SetObservedGeneration sets the ObservedGeneration bookkeeping field.
func (s *CloudflareDNSRecordStatus) SetObservedGeneration(g int64) { s.ObservedGeneration = g }

// GetLastReconcileToken returns the LastReconcileToken bookkeeping field.
func (s *CloudflareDNSRecordStatus) GetLastReconcileToken() string { return s.LastReconcileToken }

// SetLastReconcileToken sets the LastReconcileToken bookkeeping field.
func (s *CloudflareDNSRecordStatus) SetLastReconcileToken(t string) { s.LastReconcileToken = t }

// +kubebuilder:object:root=true
// CloudflareDNSRecordList contains a list of CloudflareDNSRecord.
type CloudflareDNSRecordList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareDNSRecord `json:"items"`
}
