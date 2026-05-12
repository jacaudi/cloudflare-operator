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

package reconcile

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
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
	ZoneRef *v1alpha1.ZoneReference
}

// ZoneRefResult is the resolved zone identity. ZoneObject is set only when
// the input used a ZoneRef (so callers can read status / propagate).
type ZoneRefResult struct {
	ZoneID     string
	ZoneName   string
	ZoneObject *v1alpha1.CloudflareZone
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
//     result.ZoneID is read from status.zoneID — which is populated by spec 2's
//     CloudflareZone reconciler. **In Foundation, status.zoneID does not exist
//     yet**, so result.ZoneID is always "" on this path. Callers should treat
//     "ZoneID == "" && ZoneObject != nil" as "zone exists, status not yet
//     populated — requeue."
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
	var zone v1alpha1.CloudflareZone
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: in.ZoneRef.Name}, &zone); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return ZoneRefResult{}, fmt.Errorf("%w: %s/%s", ErrZoneRefNotFound, ns, in.ZoneRef.Name)
		}
		return ZoneRefResult{}, fmt.Errorf("get CloudflareZone %s/%s: %w", ns, in.ZoneRef.Name, err)
	}
	// status.zoneID is populated by spec 2's CloudflareZone reconciler.
	// Foundation returns whatever is there; an unset value means "not ready yet"
	// — caller should requeue. We surface that via the empty string.
	return ZoneRefResult{
		ZoneID:     zoneStatusID(&zone),
		ZoneName:   in.ZoneRef.Name,
		ZoneObject: &zone,
	}, nil
}

// zoneStatusID is a forward-compatible accessor. Spec 2 will populate
// CloudflareZoneStatus.ZoneID; until then this returns "".
func zoneStatusID(z *v1alpha1.CloudflareZone) string {
	// The scaffold status (T3) has no ZoneID field yet; spec 2's plan adds it.
	// Foundation returns "" so callers know to requeue.
	_ = z
	return ""
}
