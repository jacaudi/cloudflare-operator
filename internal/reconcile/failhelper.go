package reconcile

import (
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
)

// DefaultRequeueAfter is the standard backoff for transient errors.
const DefaultRequeueAfter = 30 * time.Second

// FailReconcile returns a Result configured to requeue after the default delay.
// reason and msg are informational — callers also typically write a Condition.
func FailReconcile(reason, msg string) *ctrl.Result {
	_ = reason
	_ = msg
	return &ctrl.Result{RequeueAfter: DefaultRequeueAfter}
}

// WrapDeleteErr collapses NotFound (already gone) into nil. Other errors pass through.
func WrapDeleteErr(err error) error {
	if err == nil || apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
