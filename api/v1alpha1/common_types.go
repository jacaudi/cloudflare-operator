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

// Condition type constants used across all CRDs.
const (
	ConditionTypeReady = "Ready"
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
)

// FinalizerName is the finalizer used by all cloudflare-operator controllers.
const FinalizerName = "cloudflare.io/finalizer"
