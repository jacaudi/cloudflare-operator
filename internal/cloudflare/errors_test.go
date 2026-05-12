package cloudflare

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassify_NotFound(t *testing.T) {
	err := &APIError{StatusCode: http.StatusNotFound, Code: 1001, Message: "Tunnel not found"}
	require.Equal(t, KindNotFound, Classify(err))
}

func TestClassify_RateLimited(t *testing.T) {
	err := &APIError{StatusCode: http.StatusTooManyRequests}
	require.Equal(t, KindRateLimited, Classify(err))
}

func TestClassify_InsufficientScope(t *testing.T) {
	err := &APIError{StatusCode: http.StatusForbidden, Code: 10000}
	require.Equal(t, KindInsufficientScope, Classify(err))
}

func TestClassify_GenericError(t *testing.T) {
	require.Equal(t, KindUnknown, Classify(errors.New("network down")))
}
