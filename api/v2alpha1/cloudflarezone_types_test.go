/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package v2alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestCloudflareZone_SpecFields(t *testing.T) {
	z := CloudflareZone{
		Spec: CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: DeletionPolicyRetain,
		},
	}
	require.Equal(t, "example.com", z.Spec.Name)
	require.Equal(t, "full", z.Spec.Type)
	require.Equal(t, "Retain", z.Spec.DeletionPolicy)
}

func TestCloudflareZone_StatusFields(t *testing.T) {
	z := CloudflareZone{
		Status: CloudflareZoneStatus{
			ZoneID:              "abc123",
			Status:              ZoneStatusActive,
			NameServers:         []string{"ns1.cloudflare.com", "ns2.cloudflare.com"},
			OriginalNameServers: []string{"ns1.registrar.example", "ns2.registrar.example"},
		},
	}
	require.Equal(t, "active", z.Status.Status)
	require.Len(t, z.Status.NameServers, 2)
}

func TestEnumConstants(t *testing.T) {
	require.Equal(t, "A", DNSRecordTypeA)
	require.Equal(t, "active", ZoneStatusActive)
	require.Equal(t, "Retain", DeletionPolicyRetain)
}

func TestCloudflareZone_RegistersInScheme(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, AddToScheme(s))
	_, err := s.New(GroupVersion.WithKind("CloudflareZone"))
	require.NoError(t, err)
}
