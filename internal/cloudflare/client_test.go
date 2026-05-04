package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	factory := NewClientFactory(k8sClient, k8sClient)

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
	factory := NewClientFactory(k8sClient, k8sClient)

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
	factory := NewClientFactory(k8sClient, k8sClient)

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

func TestNewClientFactory_AcceptsAPIReader(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	cached := fake.NewClientBuilder().WithScheme(scheme).Build()
	// API reader is conventionally a separate non-caching reader; the fake
	// client implements client.Reader so we reuse it here for type-fit only.
	var apiReader client.Reader = cached

	factory := NewClientFactory(cached, apiReader)
	if factory == nil {
		t.Fatal("expected non-nil factory")
	}
}

func TestGetCredentials_UnlabeledSecret_ReturnsErrSecretNotLabeled(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Cached client: empty (simulates the label-filtered cache excluding the Secret).
	cached := fake.NewClientBuilder().WithScheme(scheme).Build()

	// API reader: has the Secret, with the API token but no managed label.
	unlabeled := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-token",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"apiToken": []byte("test-token"),
		},
	}
	apiReader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(unlabeled).Build()

	factory := NewClientFactory(cached, apiReader)
	_, err := factory.GetCredentials(context.Background(), "cf-token", "default")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrSecretNotLabeled) {
		t.Fatalf("expected ErrSecretNotLabeled, got %v", err)
	}
}

func TestGetCredentials_BothMiss_ReturnsNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	cached := fake.NewClientBuilder().WithScheme(scheme).Build()
	apiReader := fake.NewClientBuilder().WithScheme(scheme).Build()

	factory := NewClientFactory(cached, apiReader)
	_, err := factory.GetCredentials(context.Background(), "missing", "default")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrSecretNotLabeled) {
		t.Fatalf("expected NotFound-flavored error, got ErrSecretNotLabeled")
	}
	if !apierrors.IsNotFound(err) {
		// The wrapped error must satisfy IsNotFound for downstream callers
		// (existing ReasonSecretNotFound path).
		t.Fatalf("expected IsNotFound to match, got %v", err)
	}
}

func TestGetCredentials_CacheHit_DoesNotCallAPIReader(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	labeled := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-token",
			Namespace: "default",
			Labels:    map[string]string{"cloudflare.io/managed": "true"},
		},
		Data: map[string][]byte{
			"apiToken": []byte("steady-state-token"),
		},
	}
	cached := fake.NewClientBuilder().WithScheme(scheme).WithObjects(labeled).Build()
	// API reader empty: if the helper accidentally calls it on the steady-state
	// path, it would return a misleading NotFound and the test would fail.
	apiReader := fake.NewClientBuilder().WithScheme(scheme).Build()

	factory := NewClientFactory(cached, apiReader)
	creds, err := factory.GetCredentials(context.Background(), "cf-token", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.APIToken != "steady-state-token" {
		t.Errorf("expected steady-state-token, got %s", creds.APIToken)
	}
}
