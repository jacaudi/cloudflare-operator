package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseFlags_Defaults(t *testing.T) {
	opts, err := parseFlags([]string{})
	require.NoError(t, err)
	require.Equal(t, ModeMeta, opts.Mode)
	require.Equal(t, "info", opts.LogLevel)
}

func TestParseFlags_ModeOverride(t *testing.T) {
	opts, err := parseFlags([]string{"--mode=zone", "--log-level=debug"})
	require.NoError(t, err)
	require.Equal(t, ModeZone, opts.Mode)
	require.Equal(t, "debug", opts.LogLevel)
}

func TestParseFlags_InvalidMode(t *testing.T) {
	_, err := parseFlags([]string{"--mode=bogus"})
	require.Error(t, err)
}
