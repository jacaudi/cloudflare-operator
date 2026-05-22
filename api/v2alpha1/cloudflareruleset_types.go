/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package v2alpha1

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
// by default). Setting enabled=false explicitly will diff against the API on
// every reconcile because Cloudflare's response shape can't distinguish that
// case from "no logging configured".
type RuleLogging struct {
	// Enabled enables per-rule logging.
	//
	// Note: due to Cloudflare API semantics, setting Enabled=false is
	// indistinguishable from omitting the Logging block entirely. The
	// operator normalizes both forms to "logging unset" on write to avoid
	// spurious drift loops. To enable logging, set true.
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
	// ZoneID is the Cloudflare Zone ID. Mutually exclusive with ZoneRef.
	// +optional
	// +kubebuilder:validation:MinLength=1
	ZoneID string `json:"zoneID,omitempty"`

	// ZoneRef references a CloudflareZone CR. Mutually exclusive with ZoneID.
	// +optional
	ZoneRef *ZoneReference `json:"zoneRef,omitempty"`

	// Cloudflare overrides the top-level credential + account.
	// +optional
	Cloudflare *CloudflareCredentialRef `json:"cloudflare,omitempty"`

	// Name is the human-readable name for the ruleset.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Description is an informative description of the ruleset.
	// +optional
	Description string `json:"description,omitempty"`

	// Phase is the Cloudflare ruleset entrypoint phase. This is the
	// Cloudflare API surface (not the operator's lifecycle Phase).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=http_request_firewall_custom;http_request_firewall_managed;http_request_late_transform;http_request_redirect;http_request_transform;http_response_headers_transform;http_response_firewall_managed;http_config_settings;http_custom_errors;http_ratelimit;http_request_cache_settings;http_request_origin;http_request_dynamic_redirect;http_response_compression
	Phase string `json:"phase"`

	// Rules is the list of rules in the ruleset.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Rules []RulesetRuleSpec `json:"rules"`

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

	// Phase is a coarse summary of the reconciliation state. See
	// Phase for the enum values.
	// +optional
	// +kubebuilder:default=Pending
	Phase Phase `json:"phase,omitempty"`

	// LastReconcileToken is the controller-owned ack of the most recent
	// cloudflare.io/reconcile-at annotation value the controller has
	// observed. The prelude in internal/reconcile.ForceReconcileRequested
	// compares this against the live annotation; mismatch forces a full
	// re-check this reconcile (bypassing the change-detection short-
	// circuit). The operator NEVER modifies the annotation itself — only
	// this status field — so admin force-triggers are not auto-cleared.
	// +optional
	LastReconcileToken string `json:"lastReconcileToken,omitempty"`
}

// GetLastSyncedAt returns the LastSyncedAt bookkeeping field.
func (s *CloudflareRulesetStatus) GetLastSyncedAt() *metav1.Time { return s.LastSyncedAt }

// SetLastSyncedAt sets the LastSyncedAt bookkeeping field.
func (s *CloudflareRulesetStatus) SetLastSyncedAt(t *metav1.Time) { s.LastSyncedAt = t }

// GetObservedGeneration returns the ObservedGeneration bookkeeping field.
func (s *CloudflareRulesetStatus) GetObservedGeneration() int64 { return s.ObservedGeneration }

// SetObservedGeneration sets the ObservedGeneration bookkeeping field.
func (s *CloudflareRulesetStatus) SetObservedGeneration(g int64) { s.ObservedGeneration = g }

// GetLastReconcileToken returns the LastReconcileToken bookkeeping field.
func (s *CloudflareRulesetStatus) GetLastReconcileToken() string { return s.LastReconcileToken }

// SetLastReconcileToken sets the LastReconcileToken bookkeeping field.
func (s *CloudflareRulesetStatus) SetLastReconcileToken(t string) { s.LastReconcileToken = t }

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ruleset Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Ruleset Phase",type=string,JSONPath=`.spec.phase`
// +kubebuilder:printcolumn:name="Rules",type=integer,JSONPath=`.status.ruleCount`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="has(self.spec.zoneID) || has(self.spec.zoneRef)",message="one of zoneID or zoneRef is required"
// +kubebuilder:validation:XValidation:rule="!(has(self.spec.zoneID) && has(self.spec.zoneRef))",message="zoneID and zoneRef are mutually exclusive"
// CloudflareRuleset is the Schema for the cloudflarerulesets API.
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
