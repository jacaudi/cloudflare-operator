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

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
)

// Sentinel errors.
var (
	ErrZoneRefXOR      = errors.New("exactly one of zoneRef or zoneID must be set")
	ErrZoneRefNotFound = errors.New("zoneRef target CloudflareZone CR not found")
)

// ZoneRefInputs is the union of (zoneRef, zoneID) the caller might have.
// Caller-side CEL validation should already enforce XOR; this helper also
// enforces it defensively.
type ZoneRefInputs struct {
	ZoneID  string
	ZoneRef *v2alpha1.ZoneReference
}

// ZoneRefResult is the resolved zone identity. ZoneObject is set only when
// the input used a ZoneRef (so callers can read status / propagate).
type ZoneRefResult struct {
	ZoneID     string
	ZoneName   string
	ZoneObject *v2alpha1.CloudflareZone
}

// ResolveZoneID converts ZoneRefInputs to (zoneID, zoneName).
//
// defaultNamespace is used when ZoneRef.Namespace is empty (typical when a CR
// references a zone in its own namespace).
//
// Every CR that binds to a zone uses this helper. No caller hand-rolls zone
// resolution.
//
// Result contract:
//   - On `ZoneID` input path: result.ZoneID is the literal input; ZoneObject is nil.
//   - On `ZoneRef` input path: result.ZoneObject is the fetched CloudflareZone.
//     As of spec 2, result.ZoneID is populated from z.Status.ZoneID. An empty
//     value still means "zone CR exists but reconciler hasn't populated status
//     yet — requeue." Callers should treat "ZoneID == "" && ZoneObject != nil"
//     as that requeue case.
//   - On `ZoneRef` input path with the target CR missing: returns ErrZoneRefNotFound.
func ResolveZoneID(ctx context.Context, c client.Client, in ZoneRefInputs, defaultNamespace string) (ZoneRefResult, error) {
	switch {
	case in.ZoneID != "" && in.ZoneRef != nil:
		return ZoneRefResult{}, ErrZoneRefXOR
	case in.ZoneID == "" && in.ZoneRef == nil:
		return ZoneRefResult{}, ErrZoneRefXOR
	case in.ZoneID != "":
		return ZoneRefResult{ZoneID: in.ZoneID}, nil
	}

	if err := in.ZoneRef.Validate(); err != nil {
		return ZoneRefResult{}, err
	}
	ns := in.ZoneRef.Namespace
	if ns == "" {
		ns = defaultNamespace
	}
	var zone v2alpha1.CloudflareZone
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: in.ZoneRef.Name}, &zone); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return ZoneRefResult{}, fmt.Errorf("%w: %s/%s", ErrZoneRefNotFound, ns, in.ZoneRef.Name)
		}
		return ZoneRefResult{}, fmt.Errorf("get CloudflareZone %s/%s: %w", ns, in.ZoneRef.Name, err)
	}
	// status.zoneID is populated by spec 2's CloudflareZone reconciler.
	// An unset value means "not ready yet" — caller should requeue. We surface
	// that via the empty string.
	return ZoneRefResult{
		ZoneID:     zoneStatusID(&zone),
		ZoneName:   in.ZoneRef.Name,
		ZoneObject: &zone,
	}, nil
}

// zoneStatusID returns the Cloudflare zone ID recorded by the CloudflareZone
// reconciler in status. An empty string means the reconciler has not yet
// populated status; callers should treat that as a requeue signal.
func zoneStatusID(z *v2alpha1.CloudflareZone) string {
	return z.Status.ZoneID
}
