package cloudflare

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

// Credentials are the resolved token + account pair used to talk to Cloudflare.
type Credentials struct {
	Token     string
	AccountID string
}

// ResolveCredentials reads the referenced Secret and returns the resolved
// credentials. defaultNamespace is used when the SecretReference omits a
// namespace (typical for top-level vs per-CR references).
//
// Per Foundation §5, the credential + accountID are inherited or overridden
// as a unit; this function does not implement the inheritance — callers do
// that by passing either the top-level ref or the CR-level ref. It only
// resolves a single ref to (token, accountID).
func ResolveCredentials(
	ctx context.Context,
	c client.Client,
	ref v1alpha1.CloudflareCredentialRef,
	defaultNamespace string,
) (Credentials, error) {
	if ref.TokenSecretRef.IsEmpty() {
		return Credentials{}, fmt.Errorf("%w: tokenSecretRef.name unset", ErrSecretNotFound)
	}
	if ref.AccountID == "" {
		return Credentials{}, fmt.Errorf("%w", ErrAccountIDUnset)
	}

	ns := ref.TokenSecretRef.Namespace
	if ns == "" {
		ns = defaultNamespace
	}
	key := ref.TokenSecretRef.Key
	if key == "" {
		key = "token"
	}

	var secret corev1.Secret
	err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.TokenSecretRef.Name}, &secret)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			return Credentials{}, fmt.Errorf("%w: %s/%s", ErrSecretNotFound, ns, ref.TokenSecretRef.Name)
		}
		return Credentials{}, fmt.Errorf("get Secret %s/%s: %w", ns, ref.TokenSecretRef.Name, err)
	}

	tokenBytes, ok := secret.Data[key]
	if !ok || len(tokenBytes) == 0 {
		return Credentials{}, fmt.Errorf("%w: %s/%s missing key %q", ErrSecretKeyMissing, ns, ref.TokenSecretRef.Name, key)
	}

	return Credentials{Token: string(tokenBytes), AccountID: ref.AccountID}, nil
}
