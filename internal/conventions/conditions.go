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

	// Source-object reasons (Service, Gateway, HTTPRoute, TLSRoute).
	ReasonTunnelAttached            = "TunnelAttached"
	ReasonUnsupportedValue          = "UnsupportedValue"          // HTTPRoute matchers or weighted backends that cloudflared cannot express
	ReasonIncompatibleFilters       = "IncompatibleFilters"       // HTTPRoute filter types cloudflared cannot enforce
	ReasonNoListenerHostname        = "NoListenerHostname"        // Gateway with no listener hostname set
	ReasonClientSideClientRequired  = "ClientSideClientRequired"  // TLSRoute hostname is browser-unreachable (mTLS / non-HTTPS client required)
	ReasonNameTooLong               = "NameTooLong"               // hostname exceeds Cloudflare tunnel-config limits
	ReasonInvalidName               = "InvalidName"               // hostname fails DNS-label / Cloudflare validity rules
	ReasonGatewayServiceUnspecified = "GatewayServiceUnspecified" // Gateway annotated for tunnel but missing cloudflare.io/gateway-service

	// Additional source-object reasons (T11+ surface these via Events because we
	// cannot write Status on user-owned Gateway/HTTPRoute objects).
	ReasonGatewayServiceUnresolved = "GatewayServiceUnresolved" // annotation present but Service Get/parse failed
	ReasonUnsupportedProtocol      = "UnsupportedProtocol"      // listener protocol cloudflared cannot serve
	ReasonOrphanedDNSRecordPruned  = "OrphanedDNSRecordPruned"  // emitted DNSRecord CR pruned: its hostname left the source's desired set
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
	}
}
