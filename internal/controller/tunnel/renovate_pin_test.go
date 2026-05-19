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
