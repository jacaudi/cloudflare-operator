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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RuleLogging configures per-rule logging. Sibling of ActionParameters in
// the Cloudflare API. Today exposes only the API's `enabled` flag; future
// fields (sampling, destinations) extend this struct without rename.
//
// Reconciliation note: omitting the logging block leaves Cloudflare's per-action
// default in place. Set logging.enabled only when you want to override the
// default for that action (e.g. enabled=true on `skip`, where logging is off
// by default).
type RuleLogging struct {
	// Enabled opts the rule into per-action logging. Useful for actions
	// (e.g. skip) where logging is off by default.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
}

// RulesetRuleSpec defines a single rule within a Cloudflare Ruleset.
type RulesetRuleSpec struct {
	// Action is the action to perform when the rule matches.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=block;challenge;js_challenge;managed_challenge;log;skip;execute;redirect;rewrite;route;score;serve_error;set_cache_settings;set_config;compress_response;force_connection_close
	Action string `json:"action"`

	// Expression is the filter expression for the rule.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Expression string `json:"expression"`

	// Description is an informative description of the rule.
	// +optional
	Description string `json:"description,omitempty"`

	// Enabled indicates whether the rule is active.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// ActionParameters contains action-specific parameters as free-form JSON.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	// +optional
	ActionParameters *apiextensionsv1.JSON `json:"actionParameters,omitempty"`

	// Logging configures per-rule logging behavior. Sibling of ActionParameters
	// in the Cloudflare API; do not encode logging via ActionParameters.
	// +optional
	Logging *RuleLogging `json:"logging,omitempty"`
}

// CloudflareRulesetSpec defines the desired state of CloudflareRuleset.
type CloudflareRulesetSpec struct {
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

	// Name is the human-readable name for the ruleset.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Description is an informative description of the ruleset.
	// +optional
	Description string `json:"description,omitempty"`

	// Phase is the phase of the ruleset.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=http_request_firewall_custom;http_request_firewall_managed;http_request_late_transform;http_request_redirect;http_request_transform;http_response_headers_transform;http_response_firewall_managed;http_config_settings;http_custom_errors;http_ratelimit;http_request_cache_settings;http_request_origin;http_request_dynamic_redirect;http_response_compression
	Phase string `json:"phase"`

	// Rules is the list of rules in the ruleset.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Rules []RulesetRuleSpec `json:"rules"`

	// SecretRef references a Secret containing Cloudflare API credentials.
	// +kubebuilder:validation:Required
	SecretRef SecretReference `json:"secretRef"`

	// Interval is the reconciliation interval.
	// +kubebuilder:default="30m"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`
}

// CloudflareRulesetStatus defines the observed state of CloudflareRuleset.
type CloudflareRulesetStatus struct {
	// Conditions represent the latest available observations of the resource's state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// RulesetID is the Cloudflare Ruleset ID.
	// +optional
	RulesetID string `json:"rulesetID,omitempty"`

	// RuleCount is the number of rules in the ruleset.
	// +optional
	RuleCount int `json:"ruleCount,omitempty"`

	// LastSyncedAt is the last time the ruleset was successfully synced.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`

	// ObservedGeneration is the most recently observed generation of the CR.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ruleset Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.spec.phase`
// +kubebuilder:printcolumn:name="Rules",type=integer,JSONPath=`.status.ruleCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="has(self.spec.zoneID) || has(self.spec.zoneRef)",message="one of zoneID or zoneRef is required"
// +kubebuilder:validation:XValidation:rule="!(has(self.spec.zoneID) && has(self.spec.zoneRef))",message="zoneID and zoneRef are mutually exclusive"

// CloudflareRuleset is the Schema for the cloudflarerulesets API
type CloudflareRuleset struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of CloudflareRuleset
	// +required
	Spec CloudflareRulesetSpec `json:"spec"`

	// status defines the observed state of CloudflareRuleset
	// +optional
	Status CloudflareRulesetStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// CloudflareRulesetList contains a list of CloudflareRuleset
type CloudflareRulesetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []CloudflareRuleset `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CloudflareRuleset{}, &CloudflareRulesetList{})
}
