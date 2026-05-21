/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package cloudflare

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRulesetClient_ConstructorSmoke(t *testing.T) {
	// NewRulesetClientFromCF stores cf without dereferencing it, so nil is
	// legal at construction time and must produce a non-nil client.
	require.NotNil(t, NewRulesetClientFromCF(nil))
}

func TestErrPhaseEntrypointNotFound_Defined(t *testing.T) {
	require.NotNil(t, ErrPhaseEntrypointNotFound)
	require.True(t, errors.Is(ErrPhaseEntrypointNotFound, ErrPhaseEntrypointNotFound))
}
