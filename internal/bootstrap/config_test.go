/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfig_TunnelConnectorResourcesJSON_OptionalDefault(t *testing.T) {
	require.NoError(t, Config{}.Validate())                     // empty is valid
	require.Equal(t, "", Config{}.TunnelConnectorResourcesJSON) // zero value
	c := Config{TunnelConnectorResourcesJSON: `{"limits":{"memory":"256Mi"}}`}
	require.NoError(t, c.Validate()) // opaque, not validated here
	require.Equal(t, `{"limits":{"memory":"256Mi"}}`, c.TunnelConnectorResourcesJSON)
}

func TestConfigValidate_TunnelRequiresZone(t *testing.T) {
	require.Error(t, Config{TunnelEnabled: true, ZoneEnabled: false}.Validate())
	require.NoError(t, Config{TunnelEnabled: true, ZoneEnabled: true}.Validate())
	require.NoError(t, Config{ZoneEnabled: true}.Validate())
	require.NoError(t, Config{}.Validate())
}
