/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cloudflare

import (
	"errors"
	"net/http"
	"slices"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
)

// IsPermissionDenied reports whether err originated from a Cloudflare API
// 403 response. The most common cause is a missing token scope (e.g.,
// Zone:Bot Management Write) or a plan that does not permit the requested
// setting (e.g., bot_management on a Free zone). Wrapped errors are
// unwrapped via errors.As.
func IsPermissionDenied(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *cfgo.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden
}

// IsBadRequest reports whether err originated from a Cloudflare API 400
// response. The user must edit the spec; the operator cannot fix this.
func IsBadRequest(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *cfgo.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest
}

// IsNotFound reports whether err originated from a Cloudflare API 404
// response. Wrapped errors are unwrapped via errors.As.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *cfgo.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// planTierErrorCodes is the set of Cloudflare API error codes that signal
// "this feature requires a higher plan." Initially seeded with 1015.
//
// The Workers error reference documents 1015 as a rate-limit code in the
// Workers namespace; the core API reuses 4xx-class numeric codes
// inconsistently across product surfaces, so the operator treats 1015 on a
// 403 as plan-tier rather than rate-limit. Add codes here as they are
// observed in the wild on plan-restricted endpoints. Treat this slice as
// append-only — do not reassign it, so accumulated codes are preserved.
var planTierErrorCodes = []int64{1015}

// IsPlanTierRequired reports whether err is a Cloudflare 403 carrying an
// error code in planTierErrorCodes. Distinguishes plan-restricted features
// (e.g. BotManagement on Free) from token-permission denials.
func IsPlanTierRequired(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *cfgo.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden {
		return false
	}
	for _, e := range apiErr.Errors {
		if slices.Contains(planTierErrorCodes, e.Code) {
			return true
		}
	}
	return false
}

// tunnelActiveConnectionsErrorCode is the Cloudflare API error code returned
// from DELETE /accounts/{id}/cfd_tunnel/{uuid} when the tunnel still has
// connected cloudflared replicas. The HTTP status accompanying this code is
// 400. Cloudflare's message is "This tunnel has active connections.
// Please stop all cloudflared replicas, or wait a few minutes for connections
// to close, then try again."
const tunnelActiveConnectionsErrorCode = 1022

// IsTunnelHasActiveConnections reports whether err is a Cloudflare 400
// carrying error code 1022. Cloudflare returns this when DeleteTunnel
// is called while cloudflared replicas are still connected. The
// operator handles this by scaling its managed connector Deployment to
// zero before calling DeleteTunnel, but a transient drain-tail can
// still surface this error briefly after pods exit; in that case the
// caller should requeue with backoff.
func IsTunnelHasActiveConnections(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *cfgo.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		return false
	}
	for _, e := range apiErr.Errors {
		if e.Code == tunnelActiveConnectionsErrorCode {
			return true
		}
	}
	return false
}
