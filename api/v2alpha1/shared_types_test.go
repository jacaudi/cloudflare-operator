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
