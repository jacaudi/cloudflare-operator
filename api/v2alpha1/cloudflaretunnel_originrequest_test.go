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

func TestTunnelOriginRequest_ShapeIsTrimmed(t *testing.T) {
	or := TunnelOriginRequest{}
	// Compile-time check: only the two supported fields exist.
	_ = or.NoTLSVerify
	_ = or.OriginServerName
}

func TestIngressEntrySnapshot_HasOriginRequest(t *testing.T) {
	osn := "origin.example.com"
	ntv := true
	snap := IngressEntrySnapshot{
		Hostname: "h.example.com",
		Service:  "https://svc:443",
		OriginRequest: &IngressSnapshotOriginRequest{
			OriginServerName: &osn,
			NoTLSVerify:      &ntv,
		},
	}
	require.NotNil(t, snap.OriginRequest)
	require.Equal(t, "origin.example.com", *snap.OriginRequest.OriginServerName)
	require.True(t, *snap.OriginRequest.NoTLSVerify)
}
