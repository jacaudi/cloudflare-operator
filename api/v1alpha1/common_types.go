// api/v1alpha1/common_types.go
package v1alpha1

// SecretReference refers to a Kubernetes Secret containing Cloudflare credentials.
type SecretReference struct {
	// Name of the Secret.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the Secret. Defaults to the dependent CR's own namespace
	// when empty.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ZoneReference refers to a CloudflareZone CR.
type ZoneReference struct {
	// Name of the CloudflareZone resource.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the CloudflareZone. Defaults to the referencing CR's own
	// namespace when empty.
	// +optional
	Namespace string `json:"namespace,omitempty"`
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
	ConditionTypeReady                = "Ready"
	ConditionTypeValid                = "Valid"
	ConditionTypeTunnelAccepted       = "TunnelAccepted"
	ConditionTypeConflict             = "Conflict"
	ConditionTypeConnectorReady       = "ConnectorReady"
	ConditionTypeIngressConfigured    = "IngressConfigured"
	ConditionTypeSSLApplied           = "SSLApplied"
	ConditionTypeSecurityApplied      = "SecurityApplied"
	ConditionTypePerformanceApplied   = "PerformanceApplied"
	ConditionTypeNetworkApplied       = "NetworkApplied"
	ConditionTypeDNSApplied           = "DNSApplied"
	ConditionTypeBotManagementApplied = "BotManagementApplied"
	ConditionTypeApexHostnameReady    = "ApexHostnameReady"
)

// Condition reason constants.
const (
	ReasonReconciling       = "Reconciling"
	ReasonReconcileSuccess  = "ReconcileSuccess"
	ReasonReconcileError    = "ReconcileError"
	ReasonCloudflareError   = "CloudflareAPIError"
	ReasonSecretNotFound    = "SecretNotFound"
	ReasonSecretNotLabeled  = "SecretNotLabeled"
	ReasonInvalidSpec       = "InvalidSpec"
	ReasonRemoteGone        = "RemoteGone"
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
	ReasonTxtDecryptFailed     = "TxtDecryptFailed"
	ReasonRecordAdopted        = "RecordAdopted"
	ReasonDNSReconciled        = "DNSReconciled"
	ReasonDuplicateHostname    = "DuplicateHostname"
	ReasonApplied              = "Applied"
	ReasonNotConfigured        = "NotConfigured"
	ReasonPermissionDenied     = "PermissionDenied"
	ReasonPlanTierRequired     = "PlanTierRequired"
	ReasonPartialApply         = "PartialApply"
	ReasonTunnelHasConnections = "TunnelHasConnections"
	ReasonDrainingConnector    = "DrainingConnector"

	// Tunnel apex hostname (issue #101).
	ReasonApexRecordPending = "ApexRecordPending"
)

// FinalizerName is the finalizer used by all cloudflare-operator controllers.
const FinalizerName = "cloudflare.io/finalizer"

// Phase is a coarse, human-friendly summary of a CR's reconciliation
// state. It is set atomically with the Ready condition by the
// internal/status package; reconcilers do not set Phase directly.
//
// +kubebuilder:validation:Enum=Pending;Reconciling;Ready;Deleting;Error
type Phase string

const (
	PhasePending     Phase = "Pending"
	PhaseReconciling Phase = "Reconciling"
	PhaseReady       Phase = "Ready"
	PhaseDeleting    Phase = "Deleting"
	PhaseError       Phase = "Error"
)

// InProgressReasons enumerates the Reason* constants that represent
// "work is happening or waiting on a precondition" rather than "this CR
// has failed and the user must intervene." derivePhase (in
// internal/status) checks membership via slices.Contains.
//
// If you add a Ready=False reason that represents waiting on a
// precondition, add it here. Anything not listed is mapped to
// PhaseError by derivePhase, including v0.12.0 (Part 1) classification
// reasons (InvalidSpec, RemoteGone, PermissionDenied, PlanTierRequired)
// and Part 2's SecretNotLabeled.
var InProgressReasons = []string{
	ReasonReconciling,
	ReasonZoneRefNotReady,
	ReasonZonePending,
	ReasonGatewayAddressNotReady,
	ReasonTunnelNotReady,
	ReasonDrainingConnector,
	ReasonTunnelHasConnections,
	ReasonApexRecordPending,
}
