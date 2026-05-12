// Package cloudflare wraps the cloudflare-go SDK with our credential
// resolution, error classification, and retry semantics.
package cloudflare

import (
	"errors"
	"fmt"
	"net/http"
)

// Sentinel errors returned by ResolveCredentials.
var (
	ErrSecretNotFound   = errors.New("credential Secret not found")
	ErrSecretKeyMissing = errors.New("credential Secret missing required key")
	ErrAccountIDUnset   = errors.New("credential accountID unset")
)

// APIError is the structured error we expect from cloudflare-go responses.
// We carry it ourselves so callers can classify failures without depending
// on the SDK's internal types.
type APIError struct {
	StatusCode int
	Code       int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("cloudflare API error: http=%d code=%d msg=%s", e.StatusCode, e.Code, e.Message)
}

// ErrorKind classifies an error for reconcile-loop decision making.
type ErrorKind int

const (
	KindUnknown ErrorKind = iota
	KindNotFound
	KindRateLimited
	KindInsufficientScope
	KindBadRequest
	KindServerError
)

// Classify maps an error to a coarse-grained category. Non-APIError values
// return KindUnknown.
func Classify(err error) ErrorKind {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return KindUnknown
	}
	switch {
	case apiErr.StatusCode == http.StatusNotFound:
		return KindNotFound
	case apiErr.StatusCode == http.StatusTooManyRequests:
		return KindRateLimited
	case apiErr.StatusCode == http.StatusForbidden && apiErr.Code == 10000:
		return KindInsufficientScope
	case apiErr.StatusCode >= 400 && apiErr.StatusCode < 500:
		return KindBadRequest
	case apiErr.StatusCode >= 500:
		return KindServerError
	}
	return KindUnknown
}
