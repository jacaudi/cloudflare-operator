/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnel

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenovateCustomManagerMatchesConst(t *testing.T) {
	src, err := os.ReadFile("dataplane.go")
	require.NoError(t, err)
	re := regexp.MustCompile(`const DefaultCloudflaredImage = "docker\.io/cloudflare/cloudflared:(?P<currentValue>[^"]+)"`)
	m := re.FindStringSubmatch(string(src))
	require.NotNil(t, m, "Renovate customManager regex no longer matches dataplane.go const")
	require.Equal(t, "2026.5.0", m[1])
}
