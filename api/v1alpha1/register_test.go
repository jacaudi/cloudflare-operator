package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestAllTypesRegistered(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, AddToScheme(s))

	kinds := []string{
		"CloudflareOperator",
		"CloudflareZone",
		"CloudflareZoneConfig",
		"CloudflareDNSRecord",
		"CloudflareRuleset",
		"CloudflareTunnel",
	}
	for _, k := range kinds {
		gvk := GroupVersion.WithKind(k)
		_, err := s.New(gvk)
		require.NoErrorf(t, err, "%s should be registered", k)
	}
}
