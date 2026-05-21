/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

// Package cloudflare wraps the cloudflare-go SDK with our credential
// resolution, error classification, and retry semantics.
package cloudflare

import (
	"errors"
)

// Sentinel errors returned by ResolveCredentials.
var (
	ErrSecretNotFound   = errors.New("credential Secret not found")
	ErrSecretKeyMissing = errors.New("credential Secret missing required key")
	ErrAccountIDUnset   = errors.New("credential accountID unset")
)
