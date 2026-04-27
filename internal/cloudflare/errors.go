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
