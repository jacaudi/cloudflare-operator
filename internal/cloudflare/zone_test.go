/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package cloudflare

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZoneClient_ConstructorRequiresCF(t *testing.T) {
	// NewZoneClientFromCF stores cf without dereferencing it, so nil is
	// legal at construction time and must produce a non-nil client.
	// Functional tests against the SDK live in envtest under test/envtest/.
	require.NotNil(t, NewZoneClientFromCF(nil))
}
