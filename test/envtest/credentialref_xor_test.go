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

package envtest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
)

// zoneWithCred returns a minimal CloudflareZone whose spec.cloudflare is the
// given credentialRef. Type/DeletionPolicy are set explicitly because the
// non-omitempty zero strings serialize as "" and the apiserver rejects ""
// against the enum rather than defaulting it, so the credential XOR rule is
// the sole validation under test here.
func zoneWithCred(name string, cred *v2alpha1.CloudflareCredentialRef) *v2alpha1.CloudflareZone {
	return &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: v2alpha1.DeletionPolicyRetain,
			Cloudflare:     cred,
		},
	}
}

func TestCredentialRefXOR_NeitherRejected(t *testing.T) {
	err := sharedClient.Create(context.Background(), zoneWithCred("xor-neither", &v2alpha1.CloudflareCredentialRef{
		TokenSecretRef: v2alpha1.SecretReference{Name: "cf"},
	}))
	require.Error(t, err, "neither accountID nor accountIDSecretRef must be rejected")
}

func TestCredentialRefXOR_BothRejected(t *testing.T) {
	err := sharedClient.Create(context.Background(), zoneWithCred("xor-both", &v2alpha1.CloudflareCredentialRef{
		TokenSecretRef:     v2alpha1.SecretReference{Name: "cf"},
		AccountID:          "acct",
		AccountIDSecretRef: &v2alpha1.SecretReference{Name: "cf", Key: "accountID"},
	}))
	require.Error(t, err, "both accountID and accountIDSecretRef must be rejected")
}

func TestCredentialRefXOR_AccountIDOnlyAccepted(t *testing.T) {
	z := zoneWithCred("xor-id", &v2alpha1.CloudflareCredentialRef{
		TokenSecretRef: v2alpha1.SecretReference{Name: "cf"},
		AccountID:      "acct",
	})
	require.NoError(t, sharedClient.Create(context.Background(), z))
	t.Cleanup(func() { _ = sharedClient.Delete(context.Background(), z) })
}

func TestCredentialRefXOR_SecretRefOnlyAccepted(t *testing.T) {
	z := zoneWithCred("xor-ref", &v2alpha1.CloudflareCredentialRef{
		TokenSecretRef:     v2alpha1.SecretReference{Name: "cf"},
		AccountIDSecretRef: &v2alpha1.SecretReference{Name: "cf", Key: "accountID"},
	})
	require.NoError(t, sharedClient.Create(context.Background(), z))
	t.Cleanup(func() { _ = sharedClient.Delete(context.Background(), z) })
}
