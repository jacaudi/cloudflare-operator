package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetAPIToken_Success(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-token",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"apiToken": []byte("test-token-123"),
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	factory := NewClientFactory(k8sClient)

	token, err := factory.GetAPIToken(context.Background(), "cf-token", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "test-token-123" {
		t.Errorf("expected test-token-123, got %s", token)
	}
}

func TestGetAPIToken_SecretNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	factory := NewClientFactory(k8sClient)

	_, err := factory.GetAPIToken(context.Background(), "missing", "default")
	if err == nil {
		t.Error("expected error for missing secret")
	}
}

func TestGetAPIToken_MissingKey(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-token",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"wrongKey": []byte("test-token-123"),
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	factory := NewClientFactory(k8sClient)

	_, err := factory.GetAPIToken(context.Background(), "cf-token", "default")
	if err == nil {
		t.Error("expected error for missing apiToken key")
	}
}

func TestErrSecretNotLabeled_IsSentinel(t *testing.T) {
	wrapped := fmt.Errorf("loading credentials: %w", ErrSecretNotLabeled)
	if !errors.Is(wrapped, ErrSecretNotLabeled) {
		t.Fatalf("errors.Is should match wrapped sentinel; got false")
	}
}
