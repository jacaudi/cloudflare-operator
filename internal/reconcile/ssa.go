package reconcile

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FieldManager is the field-manager string used for all server-side applies.
const FieldManager = "cloudflare-operator"

// Apply performs a server-side apply with our field manager and the Force flag
// (so we win ownership of fields we set, matching kubectl apply semantics).
func Apply(ctx context.Context, c client.Client, obj client.Object) error {
	return c.Patch(ctx, obj, client.Apply, client.FieldOwner(FieldManager), client.ForceOwnership)
}
