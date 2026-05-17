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
