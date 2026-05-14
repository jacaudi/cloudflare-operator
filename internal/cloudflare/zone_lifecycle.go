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

package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/zones"
)

// ErrZoneNotFound is returned when the Cloudflare API responds with 404
// to a zone lookup. Use errors.Is to match.
var ErrZoneNotFound = errors.New("zone not found")

// classifyZoneAPIErr maps cloudflare-go errors to ErrZoneNotFound when
// the API responds with 404 on a zone path. Other errors pass through.
func classifyZoneAPIErr(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *cfgo.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %w", ErrZoneNotFound, err)
	}
	return err
}

// DrainZoneHold removes any hold on the zone so DeleteZone can proceed.
// Idempotent: a missing hold returns nil.
//
// This is a package function, not a ZoneClient method, because the hold
// lifecycle is orthogonal to "lifecycle of the zone object" — only the
// reconciler's finalizer path needs it.
func DrainZoneHold(ctx context.Context, cf *cfgo.Client, zoneID string) error {
	_, err := cf.Zones.Holds.Delete(ctx, zones.HoldDeleteParams{ZoneID: cfgo.F(zoneID)})
	if err == nil {
		return nil
	}
	var apiErr *cfgo.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("delete zone hold %s: %w", zoneID, err)
}
