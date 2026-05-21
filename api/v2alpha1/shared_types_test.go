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

func TestSecretReference_IsEmpty(t *testing.T) {
	cases := []struct {
		name string
		ref  SecretReference
		want bool
	}{
		{"empty struct", SecretReference{}, true},
		{"name only", SecretReference{Name: "foo"}, false},
		{"full ref", SecretReference{Name: "foo", Namespace: "bar", Key: "token"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, c.ref.IsEmpty())
		})
	}
}

func TestZoneReference_Validate(t *testing.T) {
	cases := []struct {
		name    string
		ref     ZoneReference
		wantErr bool
	}{
		{"both empty", ZoneReference{}, true},
		{"name only", ZoneReference{Name: "test"}, false},
		{"name and namespace", ZoneReference{Name: "test", Namespace: "media"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.ref.Validate()
			if c.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestPhase_Constants(t *testing.T) {
	require.Equal(t, Phase("Ready"), PhaseReady)
	require.Equal(t, Phase("Reconciling"), PhaseReconciling)
	require.Equal(t, Phase("Error"), PhaseError)
	require.Equal(t, Phase("Pending"), PhasePending)
}
