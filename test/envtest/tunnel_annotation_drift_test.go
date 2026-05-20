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
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// TestServiceSourceEnvtest_AnnotationChangePropagatesToDNSRecord pins the
// SSA-pivot regression check against a real apiserver. The pre-pivot
// Create+IsAlreadyExists path failed this silently: flipping
// cloudflare.io/adopt on the source Service did not propagate to the
// emitted CloudflareDNSRecord because Create returned IsAlreadyExists and
// no Update path existed.
//
// After the SSA pivot (P2 T2-T6), the change must propagate.
// See design D1 / docs/follow/tunnel-deferred.md Follow-up B for the
// reproducer narrative.
func TestServiceSourceEnvtest_AnnotationChangePropagatesToDNSRecord(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupServiceEnv(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Zone CR for example.com — the emitted DNSRecord references this via
	// spec.zoneRef per the same convention used in the sibling envtests.
	zone := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	// Annotated Service with adopt=false initially.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "foo.example.com",
				conventions.AnnotationZoneRef:    "example-com",
				conventions.AnnotationAdopt:      "false",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, svc))

	// Wait for the tunnel CR + Status.TunnelCNAME (same as the sibling
	// envtest's deferred-emission flow).
	expectedTunnel := f.ns + "-payments"
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: expectedTunnel}, &tn); err != nil {
			return false
		}
		return tn.Status.TunnelCNAME != ""
	}, 30*time.Second, 250*time.Millisecond, "tunnel Status.TunnelCNAME populated")

	// Wait for the emitted DNSRecord with Spec.Adopt=false.
	// List by namespace and filter — the namespace is unique to this test.
	var initialDR *v2alpha1.CloudflareDNSRecord
	require.Eventually(t, func() bool {
		var list v2alpha1.CloudflareDNSRecordList
		if err := f.c.List(ctx, &list, client.InNamespace(f.ns)); err != nil {
			return false
		}
		for i := range list.Items {
			dr := &list.Items[i]
			if dr.Spec.Name == "foo.example.com" && !dr.Spec.Adopt {
				initialDR = dr
				return true
			}
		}
		return false
	}, 30*time.Second, 250*time.Millisecond, "DNSRecord with Spec.Adopt=false should be emitted")
	require.NotNil(t, initialDR)
	drName := initialDR.Name

	// Flip the annotation: adopt=false → adopt=true.
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: "svc"}, svc))
	svc.Annotations[conventions.AnnotationAdopt] = "true"
	require.NoError(t, f.c.Update(ctx, svc))

	// The next source-reconciler pass must propagate the annotation change
	// to Spec.Adopt on the existing emitted CR. The silent-bug regression
	// would leave Spec.Adopt=false here.
	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareDNSRecord
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: drName}, &got); err != nil {
			return false
		}
		return got.Spec.Adopt
	}, 30*time.Second, 250*time.Millisecond,
		"after annotation flip, Spec.Adopt must become true (silent-bug regression catch)")

	// The DNSRecord must be updated in place, not recreated. A future
	// regression that accidentally introduced delete+recreate would pass
	// the Spec.Adopt poll but break this UID assertion.
	var finalDR v2alpha1.CloudflareDNSRecord
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: drName}, &finalDR))
	require.Equal(t, initialDR.UID, finalDR.UID,
		"DNSRecord must be updated in place (UID preserved), not recreated")
}
