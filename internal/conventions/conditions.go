package conventions

// Status condition reason vocabulary.
//
// Foundation seeds the base set below. Spec 2 and spec 3 plans APPEND their
// own reasons to this file (per Foundation §6.1.1 append-only contract).
const (
	// Ready is the summary condition type used on every operator-managed CR.
	ConditionTypeReady = "Ready"

	// Reasons (Foundation base set).
	ReasonReady                   = "Ready" // intentionally matches ConditionTypeReady; serves the .Reason field, not .Type
	ReasonReconciling             = "Reconciling"
	ReasonDegraded                = "Degraded"
	ReasonPlanTierInsufficient    = "PlanTierInsufficient"
	ReasonCredentialsUnavailable  = "CredentialsUnavailable"
	ReasonCredentialsInsufficient = "CredentialsInsufficient"
	ReasonDependencyMissing       = "DependencyMissing"
	ReasonIgnored                 = "Ignored"
	ReasonDuplicateHostname       = "DuplicateHostname"
	ReasonControllerOffline       = "ControllerOffline"
	ReasonBundlesInstalled        = "BundlesInstalled"
	ReasonDeploymentsReady        = "DeploymentsReady"

	// --- Append-only: spec 2 zone reasons, spec 3 tunnel reasons land below this line ---
)

// BaseReasons returns the Foundation-owned reason vocabulary.
// Tests use this to verify uniqueness; reconcilers do not import it.
func BaseReasons() []string {
	return []string{
		ReasonReady,
		ReasonReconciling,
		ReasonDegraded,
		ReasonPlanTierInsufficient,
		ReasonCredentialsUnavailable,
		ReasonCredentialsInsufficient,
		ReasonDependencyMissing,
		ReasonIgnored,
		ReasonDuplicateHostname,
		ReasonControllerOffline,
		ReasonBundlesInstalled,
		ReasonDeploymentsReady,
	}
}
