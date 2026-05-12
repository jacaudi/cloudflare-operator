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

func TestDNSRecord_TypesEnumerated(t *testing.T) {
	cases := []string{"A", "AAAA", "CNAME", "SRV", "MX", "TXT", "NS"}
	for _, ty := range cases {
		t.Run(ty, func(t *testing.T) {
			r := CloudflareDNSRecord{Spec: CloudflareDNSRecordSpec{Name: "x.example.com", Type: ty, ZoneID: "z"}}
			require.Equal(t, ty, r.Spec.Type)
		})
	}
}

func TestDNSRecord_ContentXorDynamicIP(t *testing.T) {
	content := "192.0.2.1"
	r := CloudflareDNSRecord{Spec: CloudflareDNSRecordSpec{
		Name:    "apex.example.com",
		Type:    "A",
		Content: &content,
		ZoneID:  "z",
	}}
	require.Equal(t, "192.0.2.1", *r.Spec.Content)
	require.False(t, r.Spec.DynamicIP)
}

func TestSRVData_Fields(t *testing.T) {
	d := SRVData{Service: "_satisfactory", Proto: "_tcp", Priority: 0, Weight: 10, Port: 7777, Target: "game.example.com"}
	require.Equal(t, "_satisfactory", d.Service)
	require.Equal(t, "_tcp", d.Proto)
}

func TestDNSRecord_AdoptField(t *testing.T) {
	r := CloudflareDNSRecord{Spec: CloudflareDNSRecordSpec{Adopt: true}}
	require.True(t, r.Spec.Adopt)
}
