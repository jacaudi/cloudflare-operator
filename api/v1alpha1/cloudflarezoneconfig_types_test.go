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

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZoneConfig_AllGroupsOptional(t *testing.T) {
	c := CloudflareZoneConfig{Spec: CloudflareZoneConfigSpec{ZoneID: "abc"}}
	require.Nil(t, c.Spec.SSL)
	require.Nil(t, c.Spec.Security)
	require.Nil(t, c.Spec.Performance)
	require.Nil(t, c.Spec.Network)
	require.Nil(t, c.Spec.DNS)
	require.Nil(t, c.Spec.BotManagement)
}

func TestZoneConfig_GroupsPopulate(t *testing.T) {
	mode := "strict"
	level := "high"
	cache := "aggressive"
	ipv6 := "on"
	cname := "flatten_at_root"
	enableJS := true
	c := CloudflareZoneConfig{
		Spec: CloudflareZoneConfigSpec{
			ZoneID:        "abc",
			SSL:           &SSLSettings{Mode: &mode},
			Security:      &SecuritySettings{SecurityLevel: &level},
			Performance:   &PerformanceSettings{CacheLevel: &cache},
			Network:       &NetworkSettings{IPv6: &ipv6},
			DNS:           &DNSSettings{CNAMEFlattening: &cname},
			BotManagement: &BotManagementSettings{EnableJS: &enableJS},
		},
	}
	require.Equal(t, "strict", *c.Spec.SSL.Mode)
	require.Equal(t, "high", *c.Spec.Security.SecurityLevel)
	require.Equal(t, "aggressive", *c.Spec.Performance.CacheLevel)
	require.Equal(t, "on", *c.Spec.Network.IPv6)
	require.Equal(t, "flatten_at_root", *c.Spec.DNS.CNAMEFlattening)
	require.True(t, *c.Spec.BotManagement.EnableJS)
}

func TestZoneConfig_StatusAppliedSpecHash(t *testing.T) {
	c := CloudflareZoneConfig{Status: CloudflareZoneConfigStatus{AppliedSpecHash: "deadbeef"}}
	require.Equal(t, "deadbeef", c.Status.AppliedSpecHash)
}
