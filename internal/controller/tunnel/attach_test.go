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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeriveTunnelName_WithName(t *testing.T) {
	got, err := DeriveTunnelName("app-foo", "payments")
	require.NoError(t, err)
	require.Equal(t, "cf-app-foo-payments", got)
}

func TestDeriveTunnelName_WithoutName_PerNamespacePool(t *testing.T) {
	got, err := DeriveTunnelName("app-foo", "")
	require.NoError(t, err)
	require.Equal(t, "cf-app-foo", got)
}

func TestDeriveTunnelName_NameTooLong(t *testing.T) {
	// 52-char cap on the resulting CR name.
	_, err := DeriveTunnelName("very-very-long-namespace-name", "very-very-long-tunnel-name-here")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNameTooLong))
}

func TestDeriveTunnelName_InvalidNamespace(t *testing.T) {
	_, err := DeriveTunnelName("Bad_Namespace", "ok")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidName))
}

func TestDeriveTunnelName_InvalidAnnotation(t *testing.T) {
	_, err := DeriveTunnelName("ok", "Has Space")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidName))
}

func TestDeriveTunnelName_BoundaryAt52(t *testing.T) {
	// 52 exactly is OK; 53 is not. cf- (3) + ns (21) + - (1) + nm (27) = 52.
	ns := "namespace-name-twelve"       // 21
	nm := "tunnel-name-twenty-seven-ok" // 27
	got, err := DeriveTunnelName(ns, nm)
	require.NoError(t, err)
	require.Len(t, got, 52)

	// One char more pushes us to 53 → ErrNameTooLong.
	_, err = DeriveTunnelName(ns, nm+"k")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNameTooLong))
}
