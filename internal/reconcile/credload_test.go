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
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

func credLoadScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func TestLoadCredentials_HappyPath(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cf", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("t")},
	}
	c := fake.NewClientBuilder().WithScheme(credLoadScheme(t)).WithObjects(secret).Build()
	ref := v1alpha1.CloudflareCredentialRef{
		TokenSecretRef: v1alpha1.SecretReference{Name: "cf", Namespace: "default", Key: "token"},
		AccountID:      "acct",
	}
	creds, result, err := LoadCredentials(context.Background(), c, ref, "default")
	require.NoError(t, err)
	require.Nil(t, result, "no requeue expected on success")
	require.Equal(t, cloudflare.Secret("t"), creds.Token)
}

func TestLoadCredentials_MissingSecretReturnsRequeue(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(credLoadScheme(t)).Build()
	ref := v1alpha1.CloudflareCredentialRef{
		TokenSecretRef: v1alpha1.SecretReference{Name: "missing", Namespace: "default", Key: "token"},
		AccountID:      "acct",
	}
	_, result, err := LoadCredentials(context.Background(), c, ref, "default")
	require.NoError(t, err, "caller-friendly behavior: return halt-result, no error")
	require.NotNil(t, result)
	require.True(t, result.RequeueAfter > 0)
}
