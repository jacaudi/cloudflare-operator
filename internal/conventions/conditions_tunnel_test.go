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

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTunnelReasons_AllDefined(t *testing.T) {
	want := []string{
		// CloudflareTunnel reasons.
		ReasonTunnelCreated, ReasonTunnelCreating, ReasonConnectorDeploying, ReasonConnectorReady,
		ReasonRemoteConfigApplied, ReasonRemoteConfigStale, ReasonConnectionsDraining, ReasonNoConnectors,
		ReasonOwnerTransferred,
		// Source-object reasons.
		ReasonTunnelAttached, ReasonUnsupportedValue, ReasonIncompatibleFilters,
		ReasonNoListenerHostname, ReasonClientSideClientRequired,
		ReasonNameTooLong, ReasonInvalidName,
		ReasonGatewayServiceUnspecified,
	}
	for _, r := range want {
		require.NotEmpty(t, r)
		require.False(t, strings.Contains(r, " "), "reason %q must be CamelCase, no spaces", r)
		require.False(t, strings.Contains(r, "_"), "reason %q must be CamelCase, no underscores", r)
	}
}

func TestTunnelReasons_NoDuplicatesWithBase(t *testing.T) {
	base := map[string]struct{}{}
	for _, r := range BaseReasons() {
		base[r] = struct{}{}
	}
	tunnel := []string{
		ReasonTunnelCreated, ReasonTunnelCreating, ReasonConnectorDeploying, ReasonConnectorReady,
		ReasonRemoteConfigApplied, ReasonRemoteConfigStale, ReasonConnectionsDraining, ReasonNoConnectors,
		ReasonOwnerTransferred, ReasonTunnelAttached, ReasonUnsupportedValue, ReasonIncompatibleFilters,
		ReasonNoListenerHostname, ReasonClientSideClientRequired, ReasonNameTooLong, ReasonInvalidName,
		ReasonGatewayServiceUnspecified,
	}
	for _, r := range tunnel {
		_, dup := base[r]
		require.False(t, dup, "tunnel reason %q collides with Foundation base reason", r)
	}
}

// TestTunnelReasons_NoDuplicatesAcrossSets mirrors the Phase-2 pattern in
// conventions_test.go::TestZoneReasons_Registered — aggregate every reason
// set the package exposes and assert no duplicates appear across them.
func TestTunnelReasons_NoDuplicatesAcrossSets(t *testing.T) {
	all := append(BaseReasons(), ZoneReasons()...)
	all = append(all, TunnelReasons()...)
	seen := map[string]struct{}{}
	for _, r := range all {
		require.NotContains(t, seen, r, "duplicate reason across base + zone + tunnel: %s", r)
		seen[r] = struct{}{}
	}
}

func TestConditionTypes_TunnelSet(t *testing.T) {
	require.Equal(t, "ConnectorReady", ConditionTypeConnectorReady)
	require.Equal(t, "RemoteConfigApplied", ConditionTypeRemoteConfigApplied)
	require.Equal(t, "Accepted", ConditionTypeAccepted)
	require.Equal(t, "PartiallyInvalid", ConditionTypePartiallyInvalid)
}
