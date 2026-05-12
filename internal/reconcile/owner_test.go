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
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

func TestSetControllerOwner(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	owner := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "zone-a", Namespace: "ns-a", UID: "owner-uid"},
	}
	child := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "child", Namespace: "ns-a"}}

	require.NoError(t, SetControllerOwner(owner, child, scheme))
	require.Len(t, child.OwnerReferences, 1)
	require.Equal(t, "zone-a", child.OwnerReferences[0].Name)
	require.True(t, *child.OwnerReferences[0].Controller)
	require.True(t, *child.OwnerReferences[0].BlockOwnerDeletion)
}
