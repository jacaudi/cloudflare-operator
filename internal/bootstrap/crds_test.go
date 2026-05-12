package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBundleCRDs_Zone(t *testing.T) {
	crds, err := BundleCRDs(BundleZone)
	require.NoError(t, err)
	names := make([]string, len(crds))
	for i, c := range crds {
		names[i] = c.Name
	}
	require.ElementsMatch(t, []string{
		"cloudflarezones.cloudflare.io",
		"cloudflarezoneconfigs.cloudflare.io",
		"cloudflarednsrecords.cloudflare.io",
		"cloudflarerulesets.cloudflare.io",
	}, names)
}

func TestBundleCRDs_Tunnel(t *testing.T) {
	crds, err := BundleCRDs(BundleTunnel)
	require.NoError(t, err)
	require.Len(t, crds, 1)
	require.Equal(t, "cloudflaretunnels.cloudflare.io", crds[0].Name)
}

func TestBundleCRDs_UnknownReturnsError(t *testing.T) {
	_, err := BundleCRDs("nope")
	require.Error(t, err)
}
