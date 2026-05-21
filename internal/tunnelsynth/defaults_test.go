/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnelsynth

import (
	"testing"

	"github.com/stretchr/testify/require"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
)

func TestDefaultsFor(t *testing.T) {
	osn := "origin.example.com"
	ntv := true

	cases := []struct {
		name string
		tn   *v2alpha1.CloudflareTunnel
		want Defaults
	}{
		{
			name: "nil tunnel → zero defaults",
			tn:   nil,
			want: Defaults{},
		},
		{
			name: "tunnel without routing → zero defaults",
			tn:   &v2alpha1.CloudflareTunnel{},
			want: Defaults{},
		},
		{
			name: "tunnel with routing but no originRequest → zero defaults",
			tn: &v2alpha1.CloudflareTunnel{Spec: v2alpha1.CloudflareTunnelSpec{
				Routing: &v2alpha1.TunnelRoutingSpec{},
			}},
			want: Defaults{},
		},
		{
			name: "originServerName only",
			tn: &v2alpha1.CloudflareTunnel{Spec: v2alpha1.CloudflareTunnelSpec{
				Routing: &v2alpha1.TunnelRoutingSpec{
					OriginRequest: &v2alpha1.TunnelOriginRequest{OriginServerName: &osn},
				},
			}},
			want: Defaults{OriginServerNameDefault: &osn},
		},
		{
			name: "both fields",
			tn: &v2alpha1.CloudflareTunnel{Spec: v2alpha1.CloudflareTunnelSpec{
				Routing: &v2alpha1.TunnelRoutingSpec{
					OriginRequest: &v2alpha1.TunnelOriginRequest{
						NoTLSVerify:      &ntv,
						OriginServerName: &osn,
					},
				},
			}},
			want: Defaults{NoTLSVerifyDefault: &ntv, OriginServerNameDefault: &osn},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DefaultsFor(c.tn)
			if c.want.NoTLSVerifyDefault == nil {
				require.Nil(t, got.NoTLSVerifyDefault)
			} else {
				require.NotNil(t, got.NoTLSVerifyDefault)
				require.Equal(t, *c.want.NoTLSVerifyDefault, *got.NoTLSVerifyDefault)
			}
			if c.want.OriginServerNameDefault == nil {
				require.Nil(t, got.OriginServerNameDefault)
			} else {
				require.NotNil(t, got.OriginServerNameDefault)
				require.Equal(t, *c.want.OriginServerNameDefault, *got.OriginServerNameDefault)
			}
		})
	}
}

// Regression: confirms DefaultsFor returns a COPY by value
// (mutating tn after the call must not affect got).
func TestDefaultsFor_DeepCopiesValues(t *testing.T) {
	osn := "origin.example.com"
	tn := &v2alpha1.CloudflareTunnel{Spec: v2alpha1.CloudflareTunnelSpec{
		Routing: &v2alpha1.TunnelRoutingSpec{
			OriginRequest: &v2alpha1.TunnelOriginRequest{OriginServerName: &osn},
		},
	}}
	got := DefaultsFor(tn)
	newVal := "new.example.com"
	tn.Spec.Routing.OriginRequest.OriginServerName = &newVal
	require.Equal(t, "origin.example.com", *got.OriginServerNameDefault, "DefaultsFor must deep-copy values")
}
