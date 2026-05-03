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
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

// CredentialFactory is the subset of cfclient.ClientFactory that
// LoadCredentials needs. Defined as an interface so reconciler tests can
// substitute a stub without standing up a real ClientFactory.
type CredentialFactory interface {
	GetCredentials(ctx context.Context, secretName, namespace string) (cfclient.Credentials, error)
}

// LoadCredentials fetches Cloudflare credentials for a CR and centralizes
// the failure-mode plumbing every reconciler would otherwise duplicate.
//
// Returns:
//
//   - creds: populated on the success path; zero value on any error.
//
//   - halt: non-nil when the caller MUST return immediately. Reconcilers
//     use the pattern:
//
//     creds, halt, err := LoadCredentials(...)
//     if halt != nil {
//     return *halt, err
//     }
//
//   - err: surfaced from failReconcile. Always nil on the success path.
//     Always nil on the SecretNotFound and SecretNotLabeled paths
//     (failReconcile swallows status-write errors and returns a timed
//     requeue, matching existing controller behavior).
//
// Failure-mode mapping:
//   - cfclient.ErrSecretNotLabeled → ReasonSecretNotLabeled, recorder
//     event with the actionable label-the-Secret guidance, requeue.
//   - apierrors.IsNotFound (wrapped) → ReasonSecretNotFound, requeue.
//   - any other error → ReasonReconcileError, requeue.
func LoadCredentials(
	ctx context.Context,
	c client.Client,
	factory CredentialFactory,
	secretName, namespace string,
	recorder record.EventRecorder,
	obj client.Object,
	conditions *[]metav1.Condition,
	requeue time.Duration,
) (cfclient.Credentials, *ctrl.Result, error) {
	creds, err := factory.GetCredentials(ctx, secretName, namespace)
	if err == nil {
		return creds, nil, nil
	}

	switch {
	case errors.Is(err, cfclient.ErrSecretNotLabeled):
		if recorder != nil {
			recorder.Eventf(obj, corev1.EventTypeWarning,
				cloudflarev1alpha1.ReasonSecretNotLabeled,
				"Secret %s/%s exists but is missing label cloudflare.io/managed=true; "+
					"add the label to allow the operator to read it",
				namespace, secretName)
		}
		halt, herr := failReconcile(ctx, c, obj, conditions,
			cloudflarev1alpha1.ReasonSecretNotLabeled, err, requeue)
		return cfclient.Credentials{}, &halt, herr

	case apierrors.IsNotFound(err):
		halt, herr := failReconcile(ctx, c, obj, conditions,
			cloudflarev1alpha1.ReasonSecretNotFound, err, requeue)
		return cfclient.Credentials{}, &halt, herr

	default:
		halt, herr := failReconcile(ctx, c, obj, conditions,
			cloudflarev1alpha1.ReasonReconcileError,
			fmt.Errorf("load credentials: %w", err), requeue)
		return cfclient.Credentials{}, &halt, herr
	}
}
