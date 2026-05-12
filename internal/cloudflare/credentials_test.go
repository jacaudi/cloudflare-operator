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

package cloudflare

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

func newFakeClient(t *testing.T, objs ...runtime.Object) *fake.ClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...)
}

func TestResolveCredentials_HappyPath(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-token", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("test-token-abc")},
	}
	c := newFakeClient(t, secret).Build()

	ref := v1alpha1.CloudflareCredentialRef{
		TokenSecretRef: v1alpha1.SecretReference{Name: "cf-token", Namespace: "default", Key: "token"},
		AccountID:      "acct-123",
	}
	creds, err := ResolveCredentials(context.Background(), c, ref, "default")
	require.NoError(t, err)
	require.Equal(t, "test-token-abc", creds.Token)
	require.Equal(t, "acct-123", creds.AccountID)
}

func TestResolveCredentials_MissingSecret(t *testing.T) {
	c := newFakeClient(t).Build()
	ref := v1alpha1.CloudflareCredentialRef{
		TokenSecretRef: v1alpha1.SecretReference{Name: "missing", Namespace: "default", Key: "token"},
		AccountID:      "acct-123",
	}
	_, err := ResolveCredentials(context.Background(), c, ref, "default")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSecretNotFound)
}

func TestResolveCredentials_MissingKey(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-token", Namespace: "default"},
		Data:       map[string][]byte{"other": []byte("x")},
	}
	c := newFakeClient(t, secret).Build()
	ref := v1alpha1.CloudflareCredentialRef{
		TokenSecretRef: v1alpha1.SecretReference{Name: "cf-token", Namespace: "default", Key: "token"},
		AccountID:      "acct-123",
	}
	_, err := ResolveCredentials(context.Background(), c, ref, "default")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSecretKeyMissing)
}

func TestResolveCredentials_DefaultsNamespace(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-token", Namespace: "media"},
		Data:       map[string][]byte{"token": []byte("token-xyz")},
	}
	c := newFakeClient(t, secret).Build()
	ref := v1alpha1.CloudflareCredentialRef{
		TokenSecretRef: v1alpha1.SecretReference{Name: "cf-token", Key: "token"}, // Namespace empty
		AccountID:      "acct-123",
	}
	creds, err := ResolveCredentials(context.Background(), c, ref, "media")
	require.NoError(t, err)
	require.Equal(t, "token-xyz", creds.Token)
}

func TestResolveCredentials_MissingAccountID(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-token", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("abc")},
	}
	c := newFakeClient(t, secret).Build()
	ref := v1alpha1.CloudflareCredentialRef{
		TokenSecretRef: v1alpha1.SecretReference{Name: "cf-token", Namespace: "default", Key: "token"},
		AccountID:      "",
	}
	_, err := ResolveCredentials(context.Background(), c, ref, "default")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrAccountIDUnset)
}
