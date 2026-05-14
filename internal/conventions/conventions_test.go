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

func TestFinalizerName(t *testing.T) {
	require.Equal(t, "cloudflare-operator.cloudflare.io/finalizer", FinalizerName)
}

func TestSourceLabelKeys(t *testing.T) {
	require.Equal(t, "cloudflare.io/source-kind", LabelSourceKind)
	require.Equal(t, "cloudflare.io/source-name", LabelSourceName)
	require.Equal(t, "cloudflare.io/source-namespace", LabelSourceNamespace)
}

func TestAnnotationPrefix(t *testing.T) {
	require.Equal(t, "cloudflare.io/", AnnotationPrefix)
}

func TestReservedAnnotationPrefix(t *testing.T) {
	require.True(t, IsReservedAnnotation("cloudflare.io/tunnel"))
	require.False(t, IsReservedAnnotation("example.com/anything"))
}

func TestBaseReasonsAreUnique(t *testing.T) {
	seen := map[string]struct{}{}
	for _, r := range BaseReasons() {
		require.NotContains(t, seen, r, "duplicate reason: %s", r)
		seen[r] = struct{}{}
		require.NotEmpty(t, r)
		require.False(t, strings.Contains(r, " "), "reason must be CamelCase, no spaces: %q", r)
	}
}

func TestZoneReasons_Registered(t *testing.T) {
	want := []string{
		ReasonZoneActivated, ReasonZoneActivating, ReasonAdoptedExistingRecord,
		ReasonDriftDetected, ReasonSSLApplied, ReasonSecurityApplied,
		ReasonPerformanceApplied, ReasonNetworkApplied, ReasonDNSApplied,
		ReasonBotManagementApplied,
	}
	for _, r := range want {
		require.NotEmpty(t, r)
		require.False(t, strings.Contains(r, " "))
	}
	all := append(BaseReasons(), ZoneReasons()...)
	seen := map[string]struct{}{}
	for _, r := range all {
		require.NotContains(t, seen, r, "duplicate reason across base + zone: %s", r)
		seen[r] = struct{}{}
	}
}

func TestZoneConditionTypes(t *testing.T) {
	require.Equal(t, "SSLApplied", ConditionTypeSSLApplied)
	require.Equal(t, "SecurityApplied", ConditionTypeSecurityApplied)
}
