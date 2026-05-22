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

func TestDNSRecord_TypesEnumerated(t *testing.T) {
	cases := []string{"A", "AAAA", "CNAME", "SRV", "MX", "TXT", "NS"}
	for _, ty := range cases {
		t.Run(ty, func(t *testing.T) {
			r := CloudflareDNSRecord{Spec: CloudflareDNSRecordSpec{Name: "x.example.com", Type: ty, ZoneID: "z"}}
			require.Equal(t, ty, r.Spec.Type)
		})
	}
}

func TestDNSRecord_DynamicIPDefaultsFalse(t *testing.T) {
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
