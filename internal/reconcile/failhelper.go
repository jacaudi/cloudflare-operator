/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package reconcile

import (
	"context"
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

// DefaultRequeueAfter is the standard backoff for transient errors.
const DefaultRequeueAfter = 30 * time.Second

// FailReconcile logs the reason/msg and returns a Result configured to requeue
// after the default delay. Callers also typically write a typed Condition.
func FailReconcile(ctx context.Context, reason, msg string) *ctrl.Result {
	log.FromContext(ctx).Info("reconcile failed; will requeue", "reason", reason, "msg", msg)
	return &ctrl.Result{RequeueAfter: DefaultRequeueAfter}
}

// WrapDeleteErr collapses already-gone errors into nil so reconcilers don't get
// stuck holding a finalizer when the upstream object has been removed
// out-of-band. Handles four cases:
//   - Kubernetes apierrors.IsNotFound (object removed from etcd)
//   - cloudflare.ErrZoneNotFound (zone removed via dashboard/API)
//   - cloudflare.ErrRecordNotFound (DNS record removed via dashboard/API)
//   - cloudflare.ErrTunnelNotFound (tunnel removed via dashboard/API; also
//     covers the connectors sub-resource because DeleteConnections returns
//     the same sentinel when the parent tunnel is gone)
//
// Other errors pass through unchanged.
func WrapDeleteErr(err error) error {
	if err == nil {
		return nil
	}
	if apierrors.IsNotFound(err) {
		return nil
	}
	if errors.Is(err, cloudflare.ErrZoneNotFound) {
		return nil
	}
	if errors.Is(err, cloudflare.ErrRecordNotFound) {
		return nil
	}
	if errors.Is(err, cloudflare.ErrTunnelNotFound) {
		return nil
	}
	return err
}
