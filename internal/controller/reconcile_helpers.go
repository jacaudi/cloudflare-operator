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

package controller

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/jacaudi/cloudflare-operator/internal/status"
)

// wrapDeleteErr wraps a delete-path error with the standard user-facing guidance
// about manually removing the finalizer. The returned error wraps the original
// via %w, so errors.Is / errors.As still work.
func wrapDeleteErr(err error) error {
	return fmt.Errorf("cannot delete Cloudflare resource: %w. Remove the finalizer manually to force deletion", err)
}

// failReconcile marks obj's Ready condition False with the given reason and error,
// persists status (logging but not surfacing status-write errors), and returns a
// timed requeue. Centralizes the repeated "set-status, log, requeue" tail found
// across every controller's error paths.
func failReconcile(
	ctx context.Context,
	c client.Client,
	obj client.Object,
	conditions *[]metav1.Condition,
	reason string,
	err error,
	requeue time.Duration,
) (ctrl.Result, error) {
	status.SetReady(conditions, metav1.ConditionFalse, reason, err.Error(), obj.GetGeneration())
	if statusErr := c.Status().Update(ctx, obj); statusErr != nil {
		log.FromContext(ctx).Error(statusErr, "failed to update status")
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}
