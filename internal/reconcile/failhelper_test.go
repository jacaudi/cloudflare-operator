/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package reconcile

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

func TestWrapDeleteErr_NotFoundReturnsNil(t *testing.T) {
	gvr := schema.GroupResource{Group: "", Resource: "configmaps"}
	err := apierrors.NewNotFound(gvr, "x")
	require.NoError(t, WrapDeleteErr(err))
}

func TestWrapDeleteErr_OtherErrorPasses(t *testing.T) {
	err := errors.New("boom")
	require.Error(t, WrapDeleteErr(err))
}

func TestWrapDeleteErr_CloudflareZoneNotFoundReturnsNil(t *testing.T) {
	// Bare sentinel.
	require.NoError(t, WrapDeleteErr(cloudflare.ErrZoneNotFound))
	// Wrapped sentinel — matches how internal/cloudflare classifies API errors.
	wrapped := fmt.Errorf("%w: upstream said 404", cloudflare.ErrZoneNotFound)
	require.NoError(t, WrapDeleteErr(wrapped))
}

func TestWrapDeleteErr_CloudflareRecordNotFoundReturnsNil(t *testing.T) {
	require.NoError(t, WrapDeleteErr(cloudflare.ErrRecordNotFound))
	wrapped := fmt.Errorf("%w: upstream said 404", cloudflare.ErrRecordNotFound)
	require.NoError(t, WrapDeleteErr(wrapped))
}

func TestWrapDeleteErr_CloudflareTunnelNotFoundReturnsNil(t *testing.T) {
	// Bare sentinel.
	require.NoError(t, WrapDeleteErr(cloudflare.ErrTunnelNotFound))
	// Wrapped sentinel — matches classifyTunnelAPIErr's wrapping shape so
	// the tunnel reconciler's finalizer drain stays tolerant of 404s from
	// either DeleteConnections or DeleteTunnel.
	wrapped := fmt.Errorf("%w: upstream said 404", cloudflare.ErrTunnelNotFound)
	require.NoError(t, WrapDeleteErr(wrapped))
}

func TestFailReconcile_DefaultDelay(t *testing.T) {
	r := FailReconcile(context.Background(), "X", "boom")
	require.NotNil(t, r)
	require.True(t, r.RequeueAfter > 0)
}
