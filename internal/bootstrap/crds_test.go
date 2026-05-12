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
