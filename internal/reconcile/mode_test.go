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

package reconcile_test

import (
	"testing"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
	"github.com/stretchr/testify/require"
)

func TestShouldMutate_DefaultModeMutates(t *testing.T) {
	require.True(t, reconcilelib.ShouldMutate(""), "empty mode == default Managed; must mutate")
}

func TestShouldMutate_ManagedMutates(t *testing.T) {
	require.True(t, reconcilelib.ShouldMutate("Managed"))
}

func TestShouldMutate_ObserveDoesNotMutate(t *testing.T) {
	require.False(t, reconcilelib.ShouldMutate("Observe"))
}

func TestShouldMutate_UnknownModeMutates(t *testing.T) {
	// Conservative default: any value other than "Observe" is mutating.
	// Future CRD enums adding more values are mutating-by-default.
	require.True(t, reconcilelib.ShouldMutate("FutureMode"))
}

func TestShouldMutate_BoundToObserveConstant(t *testing.T) {
	require.False(t, reconcilelib.ShouldMutate(string(v1alpha1.RecordModeObserve)))
	require.True(t, reconcilelib.ShouldMutate(string(v1alpha1.RecordModeManaged)))
	require.True(t, reconcilelib.ShouldMutate(""))
}
