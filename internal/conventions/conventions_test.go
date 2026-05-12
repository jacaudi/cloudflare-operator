package conventions

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFinalizerName(t *testing.T) {
	require.Equal(t, "cloudflare-operator.cloudflare.io/finalizer", FinalizerName)
}

func TestSourceLabelKeys(t *testing.T) {
	require.Equal(t, "cloudflare.io/source-kind", LabelSourceKind)
	require.Equal(t, "cloudflare.io/source-name", LabelSourceName)
	require.Equal(t, "cloudflare.io/source-namespace", LabelSourceNamespace)
}

func TestAnnotationPrefix(t *testing.T) {
	require.Equal(t, "cloudflare.io/", AnnotationPrefix)
}

func TestReservedAnnotationPrefix(t *testing.T) {
	require.True(t, IsReservedAnnotation("cloudflare.io/tunnel"))
	require.False(t, IsReservedAnnotation("example.com/anything"))
}

func TestBaseReasonsAreUnique(t *testing.T) {
	seen := map[string]struct{}{}
	for _, r := range BaseReasons() {
		require.NotContains(t, seen, r, "duplicate reason: %s", r)
		seen[r] = struct{}{}
		require.NotEmpty(t, r)
		require.False(t, strings.Contains(r, " "), "reason must be CamelCase, no spaces: %q", r)
	}
}
