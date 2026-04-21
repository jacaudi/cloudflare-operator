package cloudflare

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/option"
)

// Secret data keys where Cloudflare credentials are expected.
const (
	SecretKeyAPIToken  = "apiToken"
	SecretKeyAccountID = "accountID"
)

// ClientFactory creates Cloudflare API clients from Kubernetes Secrets.
type ClientFactory struct {
	k8sClient client.Client
}

// NewClientFactory creates a new ClientFactory.
func NewClientFactory(k8sClient client.Client) *ClientFactory {
	return &ClientFactory{k8sClient: k8sClient}
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
// Controllers that need both call this to avoid two Secret reads.
func (f *ClientFactory) GetCredentials(ctx context.Context, secretName, namespace string) (Credentials, error) {
	secret := &corev1.Secret{}
	err := f.k8sClient.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: namespace,
	}, secret)
	if err != nil {
		return Credentials{}, fmt.Errorf("failed to get secret %s/%s: %w", namespace, secretName, err)
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
