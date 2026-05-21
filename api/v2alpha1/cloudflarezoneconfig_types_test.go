/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package v2alpha1

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
