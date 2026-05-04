package cloudflare

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/option"
)

// ErrSecretNotLabeled is returned by GetCredentials when the referenced
// Secret exists in the API server but is missing the
// cloudflare.io/managed=true label, so the operator's cache filter has
// excluded it. Distinct from a genuine NotFound — see GetCredentials.
var ErrSecretNotLabeled = errors.New(
	"secret exists but is not labeled cloudflare.io/managed=true")

// Secret data keys where Cloudflare credentials are expected.
const (
	SecretKeyAPIToken  = "apiToken"
	SecretKeyAccountID = "accountID"
)

// ClientFactory creates Cloudflare API clients from Kubernetes Secrets.
//
// k8sClient is the cached client used for steady-state Secret reads.
// apiReader is the manager's uncached API reader, used only to
// disambiguate the cache-miss path (label-filtered vs. truly missing).
// In tests where the cache and API server are not distinguished,
// the same reader may be passed for both fields.
type ClientFactory struct {
	k8sClient client.Client
	apiReader client.Reader
}

// NewClientFactory creates a new ClientFactory. apiReader must be the
// manager's non-caching API reader (mgr.GetAPIReader()) so the
// disambiguation path bypasses the (label-filtered) cache.
func NewClientFactory(k8sClient client.Client, apiReader client.Reader) *ClientFactory {
	return &ClientFactory{k8sClient: k8sClient, apiReader: apiReader}
}

// Credentials holds the Cloudflare API token and, optionally, the Account ID
// read from a single Kubernetes Secret.
type Credentials struct {
	APIToken  string
	AccountID string
}

// GetAPIToken reads a Cloudflare API token from a Kubernetes Secret.
func (f *ClientFactory) GetAPIToken(ctx context.Context, secretName, namespace string) (string, error) {
	creds, err := f.GetCredentials(ctx, secretName, namespace)
	if err != nil {
		return "", err
	}
	return creds.APIToken, nil
}

// GetCredentials reads the Cloudflare API token (required) and Account ID
// (optional, empty string if not set) from a single Kubernetes Secret.
//
// On cache miss (k8sClient returns IsNotFound), GetCredentials does a
// single uncached read via apiReader to disambiguate:
//   - apiReader also returns IsNotFound → the original cache error is
//     returned (downstream surfaces ReasonSecretNotFound as today).
//   - apiReader returns the Secret → the Secret exists in the API server
//     but the cache filter has excluded it; ErrSecretNotLabeled is
//     returned so reconcilers can surface the actionable
//     ReasonSecretNotLabeled message.
//   - apiReader returns any other error → that error is returned (rare,
//     e.g. transient API server unavailability on the slow path).
//
// The fallback runs only on the cache-miss path. Steady-state credential
// reads remain fully cached.
func (f *ClientFactory) GetCredentials(ctx context.Context, secretName, namespace string) (Credentials, error) {
	key := types.NamespacedName{Name: secretName, Namespace: namespace}

	secret := &corev1.Secret{}
	err := f.k8sClient.Get(ctx, key, secret)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return Credentials{}, fmt.Errorf("failed to get secret %s/%s: %w", namespace, secretName, err)
		}
		// Cache miss — disambiguate via the uncached API reader.
		probe := &corev1.Secret{}
		probeErr := f.apiReader.Get(ctx, key, probe)
		switch {
		case probeErr == nil:
			// Secret exists in the API server but the cache filter excluded it.
			return Credentials{}, fmt.Errorf("secret %s/%s: %w", namespace, secretName, ErrSecretNotLabeled)
		case apierrors.IsNotFound(probeErr):
			// Genuinely missing — surface the original cache error so
			// callers' apierrors.IsNotFound checks continue to match.
			return Credentials{}, fmt.Errorf("failed to get secret %s/%s: %w", namespace, secretName, err)
		default:
			return Credentials{}, fmt.Errorf("failed to get secret %s/%s via api reader: %w", namespace, secretName, probeErr)
		}
	}

	token, ok := secret.Data[SecretKeyAPIToken]
	if !ok {
		return Credentials{}, fmt.Errorf("secret %s/%s does not contain %q key", namespace, secretName, SecretKeyAPIToken)
	}

	return Credentials{
		APIToken:  string(token),
		AccountID: string(secret.Data[SecretKeyAccountID]),
	}, nil
}

// NewCloudflareClient creates a new Cloudflare API client from an API token.
func NewCloudflareClient(apiToken string) *cfgo.Client {
	return cfgo.NewClient(option.WithAPIToken(apiToken))
}
