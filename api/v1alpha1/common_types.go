// api/v1alpha1/common_types.go
package v1alpha1

// SecretReference refers to a Kubernetes Secret containing Cloudflare credentials.
type SecretReference struct {
	// Name of the Secret.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ZoneReference refers to a CloudflareZone CR in the same namespace.
type ZoneReference struct {
	// Name of the CloudflareZone resource.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

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

// Condition type constants used across all CRDs.
const (
	ConditionTypeReady             = "Ready"
	ConditionTypeValid             = "Valid"
	ConditionTypeTunnelAccepted    = "TunnelAccepted"
	ConditionTypeConflict          = "Conflict"
	ConditionTypeConnectorReady    = "ConnectorReady"
	ConditionTypeIngressConfigured = "IngressConfigured"
)

// Condition reason constants.
const (
	ReasonReconciling       = "Reconciling"
	ReasonReconcileSuccess  = "ReconcileSuccess"
	ReasonReconcileError    = "ReconcileError"
	ReasonCloudflareError   = "CloudflareAPIError"
	ReasonSecretNotFound    = "SecretNotFound"
	ReasonInvalidSpec       = "InvalidSpec"
	ReasonDeletingResource  = "DeletingResource"
	ReasonIPResolutionError = "IPResolutionError"
	ReasonZonePending       = "ZonePending"
	ReasonZoneNotActive     = "ZoneNotActive"
	ReasonZoneRefNotReady   = "ZoneRefNotReady"

	// Added for Gateway API source + tunnel runtime (v1).
	ReasonInvalidAnnotation       = "InvalidAnnotation"
	ReasonNoMatchingZone          = "NoMatchingZone"
	ReasonAmbiguousZone           = "AmbiguousZone"
	ReasonTunnelNotFound          = "TunnelNotFound"
	ReasonTunnelNotReady          = "TunnelNotReady"
	ReasonGatewayAddressNotReady  = "GatewayAddressNotReady"
	ReasonRecordOwnershipConflict = "RecordOwnershipConflict"
	ReasonTxtRegistryGap          = "TxtRegistryGap"
	// ReasonTxtDecryptFailed is retained as a placeholder for the encryption
	// code path that is in-tree but not yet active. Do not remove.
	ReasonTxtDecryptFailed  = "TxtDecryptFailed"
	ReasonRecordAdopted     = "RecordAdopted"
	ReasonDNSReconciled     = "DNSReconciled"
	ReasonDuplicateHostname = "DuplicateHostname"
)

// FinalizerName is the finalizer used by all cloudflare-operator controllers.
const FinalizerName = "cloudflare.io/finalizer"
