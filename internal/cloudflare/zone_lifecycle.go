/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
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
	if apiErr, ok := errors.AsType[*cfgo.Error](err); ok && apiErr.StatusCode == http.StatusNotFound {
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
	if apiErr, ok := errors.AsType[*cfgo.Error](err); ok && apiErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("delete zone hold %s: %w", zoneID, err)
}
