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
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// LoadCredentials wraps cloudflare.ResolveCredentials with reconcile-loop
// ergonomics: a missing Secret returns (zero, halt-result, nil) so the caller
// can `return *halt, nil` and the controller-runtime engine will requeue with
// backoff. Unexpected errors return (zero, nil, err).
//
// The caller is responsible for writing a typed Condition before returning
// the halt result.
func LoadCredentials(
	ctx context.Context,
	c client.Client,
	ref v1alpha1.CloudflareCredentialRef,
	defaultNamespace string,
) (cloudflare.Credentials, *ctrl.Result, error) {
	creds, err := cloudflare.ResolveCredentials(ctx, c, ref, defaultNamespace)
	if err == nil {
		return creds, nil, nil
	}
	if errors.Is(err, cloudflare.ErrSecretNotFound) ||
		errors.Is(err, cloudflare.ErrSecretKeyMissing) ||
		errors.Is(err, cloudflare.ErrAccountIDUnset) {
		return cloudflare.Credentials{}, FailReconcile(ctx, conventions.ReasonCredentialsUnavailable, err.Error()), nil
	}
	return cloudflare.Credentials{}, nil, err
}

// EnvCredentials reads CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID from the
// process environment. The bootstrap reconciler wires these into the zone and
// tunnel controller Deployments (Foundation §5 env passthrough); this is the
// default-credential path. Returns (zero, false) when either is unset.
func EnvCredentials() (cloudflare.Credentials, bool) {
	token := os.Getenv("CLOUDFLARE_API_TOKEN")
	accountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	if token == "" || accountID == "" {
		return cloudflare.Credentials{}, false
	}
	return cloudflare.Credentials{Token: token, AccountID: accountID}, true
}

// LoadCredentialsHierarchical implements hierarchical resolution:
//  1. If crRef is non-nil and non-empty → resolve from the K8s Secret.
//  2. Else → use process env vars (default credentials wired by the bootstrap
//     reconciler into the controller Deployment).
//  3. Else → return a halt-result with CredentialsUnavailable.
//
// Per-CR override wins; env-var default is the fallback. Both controllers
// (zone, tunnel) use this helper at the top of every reconcile.
func LoadCredentialsHierarchical(
	ctx context.Context,
	c client.Client,
	crRef *v1alpha1.CloudflareCredentialRef,
	defaultNamespace string,
) (cloudflare.Credentials, *ctrl.Result, error) {
	if crRef != nil && !crRef.TokenSecretRef.IsEmpty() {
		return LoadCredentials(ctx, c, *crRef, defaultNamespace)
	}
	creds, ok := EnvCredentials()
	if !ok {
		return cloudflare.Credentials{}, FailReconcile(ctx, conventions.ReasonCredentialsUnavailable,
			"no per-CR credential override and CLOUDFLARE_API_TOKEN/CLOUDFLARE_ACCOUNT_ID env vars unset"), nil
	}
	return creds, nil, nil
}
