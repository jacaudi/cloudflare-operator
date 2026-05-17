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

package tunnel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
)

func emitTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, v2alpha1.AddToScheme(s))
	return s
}

func TestEmitDNSRecord_CreatesNew(t *testing.T) {
	s := emitTestScheme(t)
	svc := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns", UID: "uid-svc"},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(svc).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	err := EmitDNSRecord(context.Background(), c, s, EmitOpts{
		Owner:       svc,
		OwnerKind:   "Service",
		Hostname:    "foo.example.com",
		Content:     "tunnel.cfargotunnel.com",
		Annotations: map[string]string{conventions.AnnotationZoneRef: "example-com", conventions.AnnotationAdopt: "true"},
	})
	require.NoError(t, err)

	var got v2alpha1.CloudflareDNSRecord
	name := emittedDNSRecordName("svc", "foo.example.com")
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "ns"}, &got))
	require.Equal(t, "CNAME", got.Spec.Type)
	require.Equal(t, "foo.example.com", got.Spec.Name)
	require.NotNil(t, got.Spec.Content)
	require.Equal(t, "tunnel.cfargotunnel.com", *got.Spec.Content)
	require.NotNil(t, got.Spec.ZoneRef)
	require.Equal(t, "example-com", got.Spec.ZoneRef.Name)
	require.True(t, got.Spec.Adopt)
	require.Equal(t, "Service", got.Labels[conventions.LabelSourceKind])
}

func TestEmitDNSRecord_UpdatesSpecOnAnnotationChange(t *testing.T) {
	// This is the silent-bug regression check. Emit with adopt=false, then
	// emit again with adopt=true: the second emit must update the CR (SSA
	// path) rather than swallow it (Create+IsAlreadyExists path).
	s := emitTestScheme(t)
	svc := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns", UID: "uid-svc"},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(svc).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	require.NoError(t, EmitDNSRecord(context.Background(), c, s, EmitOpts{
		Owner: svc, OwnerKind: "Service",
		Hostname: "foo.example.com", Content: "tunnel.cfargotunnel.com",
		Annotations: map[string]string{conventions.AnnotationAdopt: "false"},
	}))
	require.NoError(t, EmitDNSRecord(context.Background(), c, s, EmitOpts{
		Owner: svc, OwnerKind: "Service",
		Hostname: "foo.example.com", Content: "tunnel.cfargotunnel.com",
		Annotations: map[string]string{conventions.AnnotationAdopt: "true"},
	}))

	var got v2alpha1.CloudflareDNSRecord
	name := emittedDNSRecordName("svc", "foo.example.com")
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "ns"}, &got))
	require.True(t, got.Spec.Adopt,
		"adopt annotation flip must propagate through SSA; got Spec.Adopt=false (silent-bug regression)")
}

func TestEmitDNSRecord_NoAnnotationsAreNoOpFields(t *testing.T) {
	s := emitTestScheme(t)
	svc := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns", UID: "uid-svc"},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(svc).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	require.NoError(t, EmitDNSRecord(context.Background(), c, s, EmitOpts{
		Owner: svc, OwnerKind: "Service",
		Hostname: "foo.example.com", Content: "tunnel.cfargotunnel.com",
	}))

	var got v2alpha1.CloudflareDNSRecord
	name := emittedDNSRecordName("svc", "foo.example.com")
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "ns"}, &got))
	require.Nil(t, got.Spec.ZoneRef, "no zoneRef annotation → no ZoneRef")
	require.False(t, got.Spec.Adopt, "no adopt annotation → Adopt=false")
}
