/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package reconcile

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
)

func TestSetControllerOwner(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, v2alpha1.AddToScheme(scheme))

	owner := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "zone-a", Namespace: "ns-a", UID: "owner-uid"},
	}
	child := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "child", Namespace: "ns-a"}}

	require.NoError(t, SetControllerOwner(owner, child, scheme))
	require.Len(t, child.OwnerReferences, 1)
	require.Equal(t, "zone-a", child.OwnerReferences[0].Name)
	require.True(t, *child.OwnerReferences[0].Controller)
	require.True(t, *child.OwnerReferences[0].BlockOwnerDeletion)
}
