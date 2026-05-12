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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// DefaultRequeueAfter is the standard backoff for transient errors.
const DefaultRequeueAfter = 30 * time.Second

// FailReconcile logs the reason/msg and returns a Result configured to requeue
// after the default delay. Callers also typically write a typed Condition.
func FailReconcile(ctx context.Context, reason, msg string) *ctrl.Result {
	log.FromContext(ctx).Info("reconcile failed; will requeue", "reason", reason, "msg", msg)
	return &ctrl.Result{RequeueAfter: DefaultRequeueAfter}
}

// WrapDeleteErr collapses NotFound (already gone) into nil. Other errors pass through.
func WrapDeleteErr(err error) error {
	if err == nil || apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
