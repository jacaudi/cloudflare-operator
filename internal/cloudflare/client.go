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

// ClientFactory creates Cloudflare API clients from Kubernetes Secrets.
type ClientFactory struct {
	k8sClient client.Client
}

// NewClientFactory creates a new ClientFactory.
func NewClientFactory(k8sClient client.Client) *ClientFactory {
	return &ClientFactory{k8sClient: k8sClient}
}

// GetAPIToken reads a Cloudflare API token from a Kubernetes Secret.
func (f *ClientFactory) GetAPIToken(ctx context.Context, secretName, namespace string) (string, error) {
	secret := &corev1.Secret{}
	err := f.k8sClient.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: namespace,
	}, secret)
	if err != nil {
		return "", fmt.Errorf("failed to get secret %s/%s: %w", namespace, secretName, err)
	}

	token, ok := secret.Data["apiToken"]
	if !ok {
		return "", fmt.Errorf("secret %s/%s does not contain 'apiToken' key", namespace, secretName)
	}

	return string(token), nil
}

// NewCloudflareClient creates a new Cloudflare API client from an API token.
func NewCloudflareClient(apiToken string) *cfgo.Client {
	return cfgo.NewClient(option.WithAPIToken(apiToken))
}
