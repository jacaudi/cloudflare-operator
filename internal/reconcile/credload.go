/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package reconcile

import (
	"context"
	"errors"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
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
	ref v2alpha1.CloudflareCredentialRef,
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
	return cloudflare.Credentials{Token: cloudflare.Secret(token), AccountID: accountID}, true
}

// LoadCredentialsHierarchical implements hierarchical resolution:
//  1. If crRef is non-nil and non-empty → resolve from the K8s Secret.
//  2. Else → use process env vars (default credentials wired by the bootstrap
//     reconciler into the controller Deployment).
//  3. Else → return a halt-result with CredentialsUnavailable.
//
// Per-CR override wins; env-var default is the fallback. Both controllers
// (zone, tunnel) use this helper at the top of every reconcile.
//
// API-shape note: this function's callees deliberately differ. LoadCredentials
// returns (creds, *ctrl.Result, error) — the reconcile-loop idiom where a
// missing Secret produces a halt result, not an error. EnvCredentials returns
// (creds, bool) — the process-startup idiom where a missing env var is just
// "no default available", not a halt condition. The shapes match their
// callers' control flow; do not harmonize without a use case.
func LoadCredentialsHierarchical(
	ctx context.Context,
	c client.Client,
	crRef *v2alpha1.CloudflareCredentialRef,
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
