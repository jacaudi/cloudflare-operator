package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CloudflareOperatorSingletonName is the only accepted .metadata.name.
const CloudflareOperatorSingletonName = "cluster"

// CloudflareOperatorSpec defines the desired state of the meta-operator.
type CloudflareOperatorSpec struct {
	// Cloudflare is the default credential + account used by all reconciled
	// CRs that do not override at the CR level.
	// +kubebuilder:validation:Required
	Cloudflare CloudflareCredentialRef `json:"cloudflare"`

	// Controllers toggles the zone and tunnel controller bundles.
	// +kubebuilder:validation:Required
	Controllers ControllersSpec `json:"controllers"`

	// Observability holds metrics / health / leader-election knobs.
	// +optional
	Observability ObservabilitySpec `json:"observability,omitempty"`
}

// ControllersSpec is the two-bundle toggle.
type ControllersSpec struct {
	// +optional
	Zone ControllerSpec `json:"zone,omitempty"`
	// +optional
	Tunnel ControllerSpec `json:"tunnel,omitempty"`
}

// ControllerSpec is the per-bundle knob set.
type ControllerSpec struct {
	// Enabled toggles the bundle.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled"`

	// Replicas is the desired Deployment replica count.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas int32 `json:"replicas"`

	// Image override; empty = use the meta-operator's own image.
	// +optional
	Image string `json:"image,omitempty"`

	// LogLevel is the structured-log level (debug|info|warn|error).
	// +kubebuilder:default=info
	// +kubebuilder:validation:Enum=debug;info;warn;error
	// +optional
	LogLevel string `json:"logLevel,omitempty"`
}

// ObservabilitySpec holds runtime knobs that don't fit elsewhere.
type ObservabilitySpec struct {
	// MetricsAddress is the bind address for /metrics.
	// +kubebuilder:default=":8080"
	// +kubebuilder:validation:MinLength=1
	// +optional
	MetricsAddress string `json:"metricsAddress,omitempty"`

	// HealthAddress is the bind address for /healthz and /readyz.
	// +kubebuilder:default=":8081"
	// +kubebuilder:validation:MinLength=1
	// +optional
	HealthAddress string `json:"healthAddress,omitempty"`

	// LeaderElection configures controller-runtime leader election.
	// +optional
	LeaderElection LeaderElectionSpec `json:"leaderElection,omitempty"`
}

// LeaderElectionSpec mirrors controller-runtime's leader-election knobs.
type LeaderElectionSpec struct {
	// Enabled toggles leader election. Ignored when replicas == 1.
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled"`
}

// CloudflareOperatorStatus is the observed state.
type CloudflareOperatorStatus struct {
	// Conditions reflects the current reconciliation state of the meta-operator.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// InstalledBundles lists the bundles currently installed.
	// +optional
	InstalledBundles []string `json:"installedBundles,omitempty"`

	// InstalledCRDs lists the CRDs the bootstrap reconciler has SSA'd.
	// +optional
	InstalledCRDs []string `json:"installedCRDs,omitempty"`

	// ObservedGeneration is the .metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Ready,type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name=Bundles,type=string,JSONPath=`.status.installedBundles`
// +kubebuilder:printcolumn:name=Age,type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(self.spec) || !has(self.spec.controllers) || !has(self.spec.controllers.tunnel) || !has(self.spec.controllers.tunnel.enabled) || self.spec.controllers.tunnel.enabled != true || (has(self.spec.controllers.zone) && has(self.spec.controllers.zone.enabled) && self.spec.controllers.zone.enabled == true)",message="controllers.tunnel.enabled requires controllers.zone.enabled"
// CloudflareOperator is the top-level CR that drives bundle installation.
type CloudflareOperator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CloudflareOperatorSpec   `json:"spec,omitempty"`
	Status CloudflareOperatorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// CloudflareOperatorList contains a list of CloudflareOperator.
type CloudflareOperatorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareOperator `json:"items"`
}
