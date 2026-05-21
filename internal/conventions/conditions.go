/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

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

// --- Spec 2 zone bundle (append-only per Foundation §6.1.1) ---

// Condition types specific to the zone bundle.
const (
	ConditionTypeSSLApplied           = "SSLApplied"
	ConditionTypeSecurityApplied      = "SecurityApplied"
	ConditionTypePerformanceApplied   = "PerformanceApplied"
	ConditionTypeNetworkApplied       = "NetworkApplied"
	ConditionTypeDNSApplied           = "DNSApplied"
	ConditionTypeBotManagementApplied = "BotManagementApplied"
)

// Reasons specific to the zone bundle (spec 2 §8).
const (
	ReasonZoneActivated         = "ZoneActivated"
	ReasonZoneActivating        = "ZoneActivating"
	ReasonAdoptedExistingRecord = "AdoptedExistingRecord"
	ReasonDriftDetected         = "DriftDetected"
	ReasonSSLApplied            = "SSLApplied"
	ReasonSecurityApplied       = "SecurityApplied"
	ReasonPerformanceApplied    = "PerformanceApplied"
	ReasonNetworkApplied        = "NetworkApplied"
	ReasonDNSApplied            = "DNSApplied"
	ReasonBotManagementApplied  = "BotManagementApplied"
)

// --- Spec 5 (TXT registry + observe mode) appends ---

// Reasons appended by spec 5 for the TXT registry and observe mode.
// Per Foundation §6.1.1 append-only contract — no existing reason renamed.
const (
	// ReasonAdoptRefusedNoTXT marks a CR with Adopt:true where the matching
	// Cloudflare record has no TXT companion (or one that fails both
	// plaintext and AES decode). Adoption is refused; the user must migrate
	// via docs/plans/2026-05-14-txt-registry-design.md §5.4.
	ReasonAdoptRefusedNoTXT = "AdoptRefusedNoTXT"

	// ReasonAdoptRefusedForeign marks a CR with Adopt:true where the
	// matching record's TXT companion decodes successfully but to a
	// different (k, ns, n) tuple. Another CR (or external system using the
	// same registry format) already claims this record.
	ReasonAdoptRefusedForeign = "AdoptRefusedForeign"

	// ReasonTxtRegistryKeyUnavailable marks a halt when the operator's
	// configured TXT-registry encryption key Secret is missing or the key
	// is the wrong length. Encryption is required (key configured) but
	// cannot operate.
	ReasonTxtRegistryKeyUnavailable = "TxtRegistryKeyUnavailable"

	// ReasonObserving marks a CR running with Spec.Mode=Observe. The
	// operator reads but does not mutate; Status reflects current
	// Cloudflare state.
	ReasonObserving = "Observing"

	// ReasonTxtRegistryWriteFailed marks a partial failure: the Cloudflare
	// DNS record was written but its TXT companion write failed. The
	// reconcile does NOT fail (DNS is correct); the TXT write retries on
	// the next reconcile. Surfaced as a Warning Event.
	ReasonTxtRegistryWriteFailed = "TxtRegistryWriteFailed"

	// ReasonOwnershipCompanionFailed marks a CR whose primary Cloudflare
	// record is healthy but whose TXT ownership companion could not be
	// brought to the desired state this reconcile (name-miss, foreign
	// owner, undecodable content, or a Cloudflare write error). S1 gates
	// Ready=False on this so a broken anti-hijack companion is never
	// masked behind "DNS record synced".
	ReasonOwnershipCompanionFailed = "OwnershipCompanionFailed"

	// ReasonSettingsApplied / ReasonSettingsApplyFailed mark per-group
	// transitions emitted by the zoneconfig reconciler when a settings
	// group (SSL, Security, Performance, Network, DNS, BotManagement)
	// finishes a reconcile pass. Normal event on success transition,
	// Warning event on failure transition.
	ReasonSettingsApplied     = "SettingsApplied"
	ReasonSettingsApplyFailed = "SettingsApplyFailed"

	// ReasonLegacyCompanionGCFailed is emitted as a Warning Event when the
	// one-shot legacy-companion GC sweep encounters a transient error
	// (gcLegacyCompanion returns legacyFound=true with a non-nil error).
	// The Status.LegacyCompanionGCDone ack is NOT set; the next reconcile
	// retries. Introduced by simplify slice 1 finding B.
	ReasonLegacyCompanionGCFailed = "LegacyCompanionGCFailed"
)

// ZoneReasons returns the reason vocabulary appended by spec 2.
// Mirrors BaseReasons() for uniqueness testing.
func ZoneReasons() []string {
	return []string{
		ReasonZoneActivated,
		ReasonZoneActivating,
		ReasonAdoptedExistingRecord,
		ReasonDriftDetected,
		ReasonSSLApplied,
		ReasonSecurityApplied,
		ReasonPerformanceApplied,
		ReasonNetworkApplied,
		ReasonDNSApplied,
		ReasonBotManagementApplied,
		ReasonAdoptRefusedNoTXT,
		ReasonAdoptRefusedForeign,
		ReasonTxtRegistryKeyUnavailable,
		ReasonObserving,
		ReasonTxtRegistryWriteFailed,
		ReasonOwnershipCompanionFailed,
		ReasonSettingsApplied,
		ReasonSettingsApplyFailed,
		ReasonLegacyCompanionGCFailed,
	}
}

// --- Spec 3 (tunnel) appends ---

// Condition types specific to tunnel + source objects. Foundation owns the
// generic Ready type via ConditionTypeReady; the types below are scoped to
// the tunnel reconciler and source reconcilers and do NOT participate in
// BaseReasons (they live alongside).
const (
	ConditionTypeConnectorReady      = "ConnectorReady"
	ConditionTypeRemoteConfigApplied = "RemoteConfigApplied"
	ConditionTypeAccepted            = "Accepted"         // on source objects (Gateway API contract)
	ConditionTypePartiallyInvalid    = "PartiallyInvalid" // on source objects (Gateway API contract)
)

// Reasons appended by spec 3 for CloudflareTunnel and for source objects.
// Per Foundation §6.1.1 append-only contract — no existing reason renamed.
const (
	// CloudflareTunnel-side reasons.
	ReasonTunnelCreated       = "TunnelCreated"
	ReasonTunnelCreating      = "TunnelCreating"
	ReasonConnectorDeploying  = "ConnectorDeploying"
	ReasonConnectorReady      = "ConnectorReady"
	ReasonRemoteConfigApplied = "RemoteConfigApplied"
	ReasonRemoteConfigStale   = "RemoteConfigStale"
	ReasonConnectionsDraining = "ConnectionsDraining"
	ReasonNoConnectors        = "NoConnectors"
	ReasonOwnerTransferred    = "OwnerTransferred"
	// ReasonTerminalNoSources marks an auto-created CloudflareTunnel CR that
	// has no remaining attaching sources and no owner. After the pending-
	// deletion grace window (60s) elapses, the reconciler self-deletes the CR.
	// Emitted as a Warning Event before the Delete; also used as the Ready=
	// False reason in the brief pre-delete window.
	ReasonTerminalNoSources = "TerminalNoSources"

	// Source-object reasons (Service, Gateway, HTTPRoute, TLSRoute).
	ReasonTunnelAttached            = "TunnelAttached"
	ReasonUnsupportedValue          = "UnsupportedValue"          // HTTPRoute matchers or weighted backends that cloudflared cannot express
	ReasonIncompatibleFilters       = "IncompatibleFilters"       // HTTPRoute filter types cloudflared cannot enforce
	ReasonNoListenerHostname        = "NoListenerHostname"        // no hostname could be resolved — Gateway listener or HTTPRoute/TLSRoute Spec.Hostnames empty
	ReasonClientSideClientRequired  = "ClientSideClientRequired"  // TLSRoute hostname is browser-unreachable (mTLS / non-HTTPS client required)
	ReasonNameTooLong               = "NameTooLong"               // hostname exceeds Cloudflare tunnel-config limits
	ReasonInvalidName               = "InvalidName"               // hostname fails DNS-label / Cloudflare validity rules
	ReasonGatewayServiceUnspecified = "GatewayServiceUnspecified" // Gateway annotated for tunnel but missing cloudflare.io/gateway-service

	// Additional source-object reasons (T11+ surface these via Events because we
	// cannot write Status on user-owned Gateway/HTTPRoute objects).
	ReasonGatewayServiceUnresolved = "GatewayServiceUnresolved" // annotation present but Service Get/parse failed
	ReasonUnsupportedProtocol      = "UnsupportedProtocol"      // listener protocol cloudflared cannot serve
	ReasonOrphanedDNSRecordPruned  = "OrphanedDNSRecordPruned"  // emitted DNSRecord CR pruned: its hostname left the source's desired set

	// ReasonGatewayApexRequired: a wildcard-only Gateway has no
	// cloudflare.io/gateway-apex; the route chain cannot be published (a
	// wildcard is an invalid CNAME target — Cloudflare 9007).
	ReasonGatewayApexRequired = "GatewayApexRequired"
	// ReasonGatewayApexInvalid: cloudflare.io/gateway-apex is set but not a
	// valid non-wildcard DNS1123 hostname; it is ignored.
	ReasonGatewayApexInvalid = "GatewayApexInvalid"

	// ReasonOrphanedUnmanaged: a tunnel has no sources and no owner but is
	// NOT cascade-GC-eligible (no auto-created annotation, no operator
	// source labels). The operator will NOT auto-delete it (design §7);
	// surfaced so the state is never silent. Admin must adopt/label or
	// delete it manually.
	ReasonOrphanedUnmanaged = "OrphanedUnmanaged"

	// ReasonOriginRequestWiped: a one-shot Warning event emitted when the
	// tunnel controller detects a Cloudflare-side originRequest block that
	// has no matching annotation or Spec.Routing.OriginRequest and wipes it.
	ReasonOriginRequestWiped = "OriginRequestWiped"
)

// TunnelReasons returns the reason vocabulary appended by spec 3 for the
// CloudflareTunnel reconciler and the four source reconcilers.
// Mirrors BaseReasons() / ZoneReasons() for uniqueness testing.
func TunnelReasons() []string {
	return []string{
		ReasonTunnelCreated,
		ReasonTunnelCreating,
		ReasonConnectorDeploying,
		ReasonConnectorReady,
		ReasonRemoteConfigApplied,
		ReasonRemoteConfigStale,
		ReasonConnectionsDraining,
		ReasonNoConnectors,
		ReasonOwnerTransferred,
		ReasonTerminalNoSources,
		ReasonTunnelAttached,
		ReasonUnsupportedValue,
		ReasonIncompatibleFilters,
		ReasonNoListenerHostname,
		ReasonClientSideClientRequired,
		ReasonNameTooLong,
		ReasonInvalidName,
		ReasonGatewayServiceUnspecified,
		ReasonGatewayServiceUnresolved,
		ReasonUnsupportedProtocol,
		ReasonOrphanedDNSRecordPruned,
		ReasonGatewayApexRequired,
		ReasonGatewayApexInvalid,
		ReasonOrphanedUnmanaged,
		ReasonOriginRequestWiped,
	}
}
