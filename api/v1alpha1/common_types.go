// api/v1alpha1/common_types.go
package v1alpha1

// SecretReference refers to a Kubernetes Secret containing Cloudflare credentials.
type SecretReference struct {
	// Name of the Secret.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// Condition type constants used across all CRDs.
const (
	ConditionTypeReady  = "Ready"
	ConditionTypeSynced = "Synced"
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
)

// FinalizerName is the finalizer used by all cloudflare-operator controllers.
const FinalizerName = "cloudflare.io/finalizer"
