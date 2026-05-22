/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package reconcile_test

import (
	"testing"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
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
	require.False(t, reconcilelib.ShouldMutate(string(v2alpha1.RecordModeObserve)))
	require.True(t, reconcilelib.ShouldMutate(string(v2alpha1.RecordModeManaged)))
	require.True(t, reconcilelib.ShouldMutate(""))
}
