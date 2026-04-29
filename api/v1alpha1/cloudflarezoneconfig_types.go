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

// SSLSettings defines SSL/TLS settings for a Cloudflare zone.
type SSLSettings struct {
	// Mode is the SSL mode.
	// +kubebuilder:validation:Enum=off;flexible;full;strict
	// +optional
	Mode *string `json:"mode,omitempty"`

	// MinTLSVersion is the minimum TLS version.
	// +kubebuilder:validation:Enum="1.0";"1.1";"1.2";"1.3"
	// +optional
	MinTLSVersion *string `json:"minTLSVersion,omitempty"`

	// TLS13 controls TLS 1.3 setting.
	// +kubebuilder:validation:Enum=on;off;zrt
	// +optional
	TLS13 *string `json:"tls13,omitempty"`

	// AlwaysUseHTTPS redirects all HTTP requests to HTTPS.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	AlwaysUseHTTPS *string `json:"alwaysUseHTTPS,omitempty"`

	// AutomaticHTTPSRewrites rewrites HTTP URLs to HTTPS in page content.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	AutomaticHTTPSRewrites *string `json:"automaticHTTPSRewrites,omitempty"`

	// OpportunisticEncryption enables opportunistic encryption.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	OpportunisticEncryption *string `json:"opportunisticEncryption,omitempty"`
}

// SecurityHeaderSettings models the zone-level HSTS / Strict-Transport-Security
// setting (the strict_transport_security payload of the Cloudflare
// security_header API). All fields are optional; nil fields are omitted from
// the API call so individual flags can be toggled without re-asserting the rest.
type SecurityHeaderSettings struct {
	// Enabled toggles HSTS for the zone.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// MaxAge is the HSTS max-age in seconds.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=31536000
	// +optional
	MaxAge *int `json:"maxAge,omitempty"`

	// IncludeSubdomains extends HSTS to subdomains.
	// +optional
	IncludeSubdomains *bool `json:"includeSubdomains,omitempty"`

	// Preload requests inclusion in browser HSTS preload lists.
	// +optional
	Preload *bool `json:"preload,omitempty"`

	// Nosniff enables the X-Content-Type-Options: nosniff response header.
	// +optional
	Nosniff *bool `json:"nosniff,omitempty"`
}

// SecuritySettings defines security settings for a Cloudflare zone.
type SecuritySettings struct {
	// SecurityLevel controls the security level.
	// +kubebuilder:validation:Enum=essentially_off;low;medium;high;under_attack
	// +optional
	SecurityLevel *string `json:"securityLevel,omitempty"`

	// ChallengeTTL is the challenge TTL in seconds.
	// +kubebuilder:validation:Enum=300;900;1800;2700;3600;7200;10800;14400;28800;57600;86400
	// +optional
	ChallengeTTL *int `json:"challengeTTL,omitempty"`

	// BrowserCheck enables browser integrity check.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	BrowserCheck *string `json:"browserCheck,omitempty"`

	// EmailObfuscation enables email obfuscation.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	EmailObfuscation *string `json:"emailObfuscation,omitempty"`

	// SecurityHeader configures the zone's HSTS / Strict-Transport-Security header.
	// +optional
	SecurityHeader *SecurityHeaderSettings `json:"securityHeader,omitempty"`

	// ServerSideExclude hides sensitive content from suspicious visitors.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	ServerSideExclude *string `json:"serverSideExclude,omitempty"`

	// HotlinkProtection blocks hotlinking of images.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	HotlinkProtection *string `json:"hotlinkProtection,omitempty"`
}

// MinifySettings defines minification settings for CSS, HTML, and JavaScript.
type MinifySettings struct {
	// CSS enables CSS minification.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	CSS *string `json:"css,omitempty"`

	// HTML enables HTML minification.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	HTML *string `json:"html,omitempty"`

	// JS enables JavaScript minification.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	JS *string `json:"js,omitempty"`
}

// PerformanceSettings defines performance settings for a Cloudflare zone.
type PerformanceSettings struct {
	// CacheLevel controls the cache level.
	// +kubebuilder:validation:Enum=aggressive;basic;simplified
	// +optional
	CacheLevel *string `json:"cacheLevel,omitempty"`

	// BrowserCacheTTL is the browser cache TTL in seconds. 0 means respect existing headers.
	// +kubebuilder:validation:Minimum=0
	// +optional
	BrowserCacheTTL *int `json:"browserCacheTTL,omitempty"`

	// Minify controls minification settings.
	// +optional
	Minify *MinifySettings `json:"minify,omitempty"`

	// Polish controls image optimization.
	// +kubebuilder:validation:Enum=off;lossless;lossy
	// +optional
	Polish *string `json:"polish,omitempty"`

	// Brotli enables brotli compression.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	Brotli *string `json:"brotli,omitempty"`

	// EarlyHints enables early hints.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	EarlyHints *string `json:"earlyHints,omitempty"`

	// HTTP2 enables HTTP/2.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	HTTP2 *string `json:"http2,omitempty"`

	// HTTP3 enables HTTP/3.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	HTTP3 *string `json:"http3,omitempty"`

	// AlwaysOnline serves cached pages when the origin is unreachable.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	AlwaysOnline *string `json:"alwaysOnline,omitempty"`

	// RocketLoader defers JavaScript loading to improve perceived performance.
	// Cloudflare is sunsetting Rocket Loader; the field will be removed when
	// the API is retired.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	RocketLoader *string `json:"rocketLoader,omitempty"`
}

// NetworkSettings defines network settings for a Cloudflare zone.
type NetworkSettings struct {
	// IPv6 enables IPv6 support.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	IPv6 *string `json:"ipv6,omitempty"`

	// WebSockets enables WebSocket support.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	WebSockets *string `json:"websockets,omitempty"`

	// PseudoIPv4 controls Pseudo IPv4 behavior.
	// +kubebuilder:validation:Enum=off;add_header;overwrite_header
	// +optional
	PseudoIPv4 *string `json:"pseudoIPv4,omitempty"`

	// IPGeolocation enables IP geolocation.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	IPGeolocation *string `json:"ipGeolocation,omitempty"`

	// OpportunisticOnion enables onion routing.
	// +kubebuilder:validation:Enum=on;off
	// +optional
	OpportunisticOnion *string `json:"opportunisticOnion,omitempty"`
}

// DNSSettings defines DNS-related zone settings.
type DNSSettings struct {
	// CNAMEFlattening controls how the zone resolves CNAME records.
	// flatten_at_root: only flatten the apex (default Cloudflare behavior).
	// flatten_all: flatten every CNAME.
	// flatten_none: never flatten.
	// +kubebuilder:validation:Enum=flatten_at_root;flatten_all;flatten_none
	// +optional
	CNAMEFlattening *string `json:"cnameFlattening,omitempty"`
}

// BotManagementSettings defines bot management settings for a Cloudflare zone.
//
// Configuring this section requires the Zone:Bot Management:Edit scope on the
// API token and a Cloudflare plan that supports bot management. On Free plans
// this section's API call returns 403; the controller will surface that on
// the BotManagementApplied condition with reason=PermissionDenied without
// preventing other groups (ssl / security / performance / network) from
// being applied.
type BotManagementSettings struct {
	// EnableJS enables JavaScript detections.
	// +optional
	EnableJS *bool `json:"enableJS,omitempty"`

	// FightMode enables bot fight mode.
	// +optional
	FightMode *bool `json:"fightMode,omitempty"`
}

// CloudflareZoneConfigSpec defines the desired state of CloudflareZoneConfig.
type CloudflareZoneConfigSpec struct {
	// ZoneID is the Cloudflare Zone ID.
	// Mutually exclusive with ZoneRef.
	// +optional
	// +kubebuilder:validation:MinLength=1
	ZoneID string `json:"zoneID,omitempty"`

	// ZoneRef references a CloudflareZone resource in the same namespace.
	// The controller resolves the zone ID from the referenced resource's status.
	// Mutually exclusive with ZoneID.
	// +optional
	ZoneRef *ZoneReference `json:"zoneRef,omitempty"`

	// SecretRef references a Secret containing Cloudflare API credentials.
	// +kubebuilder:validation:Required
	SecretRef SecretReference `json:"secretRef"`

	// Interval is the reconciliation interval.
	// +kubebuilder:default="30m"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`

	// SSL defines SSL/TLS settings for the zone.
	// +optional
	SSL *SSLSettings `json:"ssl,omitempty"`

	// Security defines security settings for the zone.
	// +optional
	Security *SecuritySettings `json:"security,omitempty"`

	// Performance defines performance settings for the zone.
	// +optional
	Performance *PerformanceSettings `json:"performance,omitempty"`

	// Network defines network settings for the zone.
	// +optional
	Network *NetworkSettings `json:"network,omitempty"`

	// DNS defines DNS-related settings for the zone.
	// +optional
	DNS *DNSSettings `json:"dns,omitempty"`

	// BotManagement defines bot management settings for the zone.
	// +optional
	BotManagement *BotManagementSettings `json:"botManagement,omitempty"`
}

// CloudflareZoneConfigStatus defines the observed state of CloudflareZoneConfig.
type CloudflareZoneConfigStatus struct {
	// Conditions represent the latest available observations of the resource's state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ZoneID is the resolved Cloudflare Zone ID, populated regardless of
	// whether the spec used zoneID or zoneRef.
	// +optional
	ZoneID string `json:"zoneID,omitempty"`

	// AppliedSpecHash is a hash of the settings-relevant spec fields the last
	// time reconciliation successfully applied them. When the current hash
	// matches, the controller skips the per-setting API calls.
	// +optional
	AppliedSpecHash string `json:"appliedSpecHash,omitempty"`

	// LastSyncedAt is the last time the zone config was successfully synced.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`

	// ObservedGeneration is the most recently observed generation of the CR.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Zone ID",type=string,JSONPath=`.status.zoneID`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Last Synced",type=date,JSONPath=`.status.lastSyncedAt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="has(self.spec.zoneID) || has(self.spec.zoneRef)",message="one of zoneID or zoneRef is required"
// +kubebuilder:validation:XValidation:rule="!(has(self.spec.zoneID) && has(self.spec.zoneRef))",message="zoneID and zoneRef are mutually exclusive"

// CloudflareZoneConfig is the Schema for the cloudflarezoneconfigs API
type CloudflareZoneConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of CloudflareZoneConfig
	// +required
	Spec CloudflareZoneConfigSpec `json:"spec"`

	// status defines the observed state of CloudflareZoneConfig
	// +optional
	Status CloudflareZoneConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// CloudflareZoneConfigList contains a list of CloudflareZoneConfig
type CloudflareZoneConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []CloudflareZoneConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CloudflareZoneConfig{}, &CloudflareZoneConfigList{})
}
