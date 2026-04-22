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
	"k8s.io/apimachinery/pkg/util/intstr"
)

// TunnelReference identifies a CloudflareTunnel this rule attaches to.
type TunnelReference struct {
	// Name of the CloudflareTunnel resource.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the CloudflareTunnel. Defaults to the rule's own namespace
	// when empty.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// TunnelRuleServiceRef identifies a Kubernetes Service to route traffic to.
type TunnelRuleServiceRef struct {
	// Name of the Service.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the Service. Defaults to the rule's namespace when empty.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Port may be an integer or a named port.
	// +kubebuilder:validation:Required
	Port intstr.IntOrString `json:"port"`

	// Scheme is http, https, h2c, or tcp. When empty, inferred at reconcile
	// time from the Service's port name.
	// +kubebuilder:validation:Enum=http;https;h2c;tcp
	// +optional
	Scheme string `json:"scheme,omitempty"`
}

// TunnelRuleBackend is a discriminated union: exactly one of ServiceRef, URL,
// or HTTPStatus must be set. Enforced via x-kubernetes-validations on
// CloudflareTunnelRule.
type TunnelRuleBackend struct {
	// ServiceRef routes to a Kubernetes Service by reference. The operator
	// resolves the URL from cluster DNS at render time.
	// +optional
	ServiceRef *TunnelRuleServiceRef `json:"serviceRef,omitempty"`

	// URL is a raw backend URL. Use for sources (Gateway-upstream overrides)
	// where the backend is not expressible as a Service reference.
	// +optional
	URL *string `json:"url,omitempty"`

	// HTTPStatus produces a cloudflared http_status:<code> entry. Use for
	// explicit "reject at this hostname" rules.
	// +optional
	HTTPStatus *int `json:"httpStatus,omitempty"`
}

// IsExactlyOne returns true when exactly one of ServiceRef / URL / HTTPStatus
// is set.
func (b TunnelRuleBackend) IsExactlyOne() bool {
	n := 0
	if b.ServiceRef != nil {
		n++
	}
	if b.URL != nil {
		n++
	}
	if b.HTTPStatus != nil {
		n++
	}
	return n == 1
}

// TunnelRuleOriginRequest is a pass-through to cloudflared's originRequest.
type TunnelRuleOriginRequest struct {
	// +optional
	NoTLSVerify bool `json:"noTLSVerify,omitempty"`
	// +optional
	OriginServerName string `json:"originServerName,omitempty"`
	// +optional
	ConnectTimeout *metav1.Duration `json:"connectTimeout,omitempty"`
	// +optional
	HTTPHostHeader string `json:"httpHostHeader,omitempty"`
}

// TunnelRuleSourceRef is populated by emitting controllers to record which
// Kubernetes object caused this rule to exist. Omitted for hand-authored rules.
type TunnelRuleSourceRef struct {
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
	// +optional
	Kind string `json:"kind,omitempty"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	UID string `json:"uid,omitempty"`
}

// CloudflareTunnelRuleSpec defines one cloudflared ingress rule (or group of
// rules sharing a backend) that attaches to a CloudflareTunnel.
type CloudflareTunnelRuleSpec struct {
	// TunnelRef points at the CloudflareTunnel this rule attaches to.
	// +kubebuilder:validation:Required
	TunnelRef TunnelReference `json:"tunnelRef"`

	// Hostnames that cloudflared should route to the Backend. At least one
	// is required; order is preserved within the aggregated ingress list.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Hostnames []string `json:"hostnames"`

	// Backend describes where traffic for these hostnames flows.
	// +kubebuilder:validation:Required
	Backend TunnelRuleBackend `json:"backend"`

	// OriginRequest pass-through options.
	// +optional
	OriginRequest *TunnelRuleOriginRequest `json:"originRequest,omitempty"`

	// SourceRef identifies the source that produced this rule. Present on
	// operator-emitted rules; absent on hand-authored rules.
	// +optional
	SourceRef *TunnelRuleSourceRef `json:"sourceRef,omitempty"`

	// Priority determines evaluation order within the aggregated ingress list.
	// Higher values are evaluated first. Default 100; ties broken by
	// metadata.name ascending.
	// +kubebuilder:default=100
	// +optional
	Priority int `json:"priority,omitempty"`
}

// CloudflareTunnelRuleStatus is the observed state.
type CloudflareTunnelRuleStatus struct {
	// Conditions: Valid, TunnelAccepted, Conflict. Written by the
	// CloudflareTunnel controller during aggregation.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ResolvedBackend is the URL cloudflared was configured with for this
	// rule. Populated after the tunnel controller renders a config.
	// +optional
	ResolvedBackend string `json:"resolvedBackend,omitempty"`

	// AppliedToConfigHash records the tunnel's config-hash at the last time
	// this rule was included. Useful for debugging drift.
	// +optional
	AppliedToConfigHash string `json:"appliedToConfigHash,omitempty"`

	// ObservedGeneration is the most recently observed generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Tunnel",type=string,JSONPath=`.spec.tunnelRef.name`
// +kubebuilder:printcolumn:name="Hostnames",type=string,JSONPath=`.spec.hostnames`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="TunnelAccepted")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="(has(self.spec.backend.serviceRef) ? 1 : 0) + (has(self.spec.backend.url) ? 1 : 0) + (has(self.spec.backend.httpStatus) ? 1 : 0) == 1",message="exactly one of backend.serviceRef, backend.url, backend.httpStatus must be set"

// CloudflareTunnelRule is the Schema for the cloudflaretunnelrules API.
type CloudflareTunnelRule struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec CloudflareTunnelRuleSpec `json:"spec"`

	// +optional
	Status CloudflareTunnelRuleStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// CloudflareTunnelRuleList contains a list of CloudflareTunnelRule.
type CloudflareTunnelRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []CloudflareTunnelRule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CloudflareTunnelRule{}, &CloudflareTunnelRuleList{})
}
