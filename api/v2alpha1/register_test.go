/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package v2alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestAllTypesRegistered(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, AddToScheme(s))

	kinds := []string{
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
