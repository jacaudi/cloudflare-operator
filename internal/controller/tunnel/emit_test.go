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

func TestEmitDNSRecord_ZoneRefNamespace(t *testing.T) {
	s := emitTestScheme(t)
	svc := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns", UID: "uid-svc"},
	}

	// (a) zone-ref-namespace set → emitted zoneRef.Namespace = annotation value
	// (zone-ns is intentionally distinct from the owner namespace "ns" so this
	// case fails if emit.go's namespace-override logic is reverted).
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(svc).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	require.NoError(t, EmitDNSRecord(context.Background(), c, s, EmitOpts{
		Owner: svc, OwnerKind: "Service", Hostname: "foo.example.com",
		Content: "tunnel.cfargotunnel.com", Annotations: map[string]string{
			conventions.AnnotationZoneRef:          "example-com",
			conventions.AnnotationZoneRefNamespace: "zone-ns",
		}}))
	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{
		Namespace: "ns", Name: emittedDNSRecordName("svc", "foo.example.com")}, &got))
	require.NotNil(t, got.Spec.ZoneRef)
	require.Equal(t, "example-com", got.Spec.ZoneRef.Name)
	require.Equal(t, "zone-ns", got.Spec.ZoneRef.Namespace)

	// (b) zone-ref-namespace ABSENT → falls back to owner namespace
	base2 := fake.NewClientBuilder().WithScheme(s).WithObjects(svc).Build()
	c2 := reconcilelib.SSATranslatingClient(t, base2)
	require.NoError(t, EmitDNSRecord(context.Background(), c2, s, EmitOpts{
		Owner: svc, OwnerKind: "Service", Hostname: "foo.example.com",
		Content: "tunnel.cfargotunnel.com", Annotations: map[string]string{
			conventions.AnnotationZoneRef: "example-com",
		}}))
	var got2 v2alpha1.CloudflareDNSRecord
	require.NoError(t, c2.Get(context.Background(), client.ObjectKey{
		Namespace: "ns", Name: emittedDNSRecordName("svc", "foo.example.com")}, &got2))
	require.NotNil(t, got2.Spec.ZoneRef)
	require.Equal(t, "ns", got2.Spec.ZoneRef.Namespace)
}

// TestEmitDNSRecord_ProxiedDefaultsTrue_NoAnnotation closes #4 in part 1:
// when the source object has no cloudflare.io/proxied annotation, the
// emitted CR's Spec.Proxied must default to true (tunnel-emitted records
// generally need to be proxied to route via <uuid>.cfargotunnel.com).
func TestEmitDNSRecord_ProxiedDefaultsTrue_NoAnnotation(t *testing.T) {
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
	require.NotNil(t, got.Spec.Proxied, "tunnel-emitted Spec.Proxied must be non-nil (default-true)")
	require.True(t, *got.Spec.Proxied, "default proxied must be true; got false")
}

// TestEmitDNSRecord_ProxiedFalseOverride closes #4 in part 2: an explicit
// cloudflare.io/proxied: "false" annotation must produce Spec.Proxied=&false
// (grey-cloud override; preserves the user's workaround for chains that
// require non-proxied origin behavior).
func TestEmitDNSRecord_ProxiedFalseOverride(t *testing.T) {
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
		Annotations: map[string]string{conventions.AnnotationProxied: "false"},
	}))

	var got v2alpha1.CloudflareDNSRecord
	name := emittedDNSRecordName("svc", "foo.example.com")
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "ns"}, &got))
	require.NotNil(t, got.Spec.Proxied, "Spec.Proxied must be non-nil when annotation set")
	require.False(t, *got.Spec.Proxied, "cloudflare.io/proxied=false must yield Spec.Proxied=&false")
}

// TestEmitDNSRecord_ProxiedTrueExplicit covers the redundant-but-valid
// explicit "true" case (must equal the default).
func TestEmitDNSRecord_ProxiedTrueExplicit(t *testing.T) {
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
		Annotations: map[string]string{conventions.AnnotationProxied: "true"},
	}))

	var got v2alpha1.CloudflareDNSRecord
	name := emittedDNSRecordName("svc", "foo.example.com")
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "ns"}, &got))
	require.NotNil(t, got.Spec.Proxied)
	require.True(t, *got.Spec.Proxied, "explicit cloudflare.io/proxied=true must yield Spec.Proxied=&true")
}

// TestEmitDNSRecord_ProxiedMalformedFallsBackToDefault: when the annotation
// value is unparseable by conventions.ParseTruthy, the wiring must fall back
// to the default (true). Test guards against silent grey-cloud regressions
// from typos.
func TestEmitDNSRecord_ProxiedMalformedFallsBackToDefault(t *testing.T) {
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
		Annotations: map[string]string{conventions.AnnotationProxied: "definitely-not-a-bool"},
	}))

	var got v2alpha1.CloudflareDNSRecord
	name := emittedDNSRecordName("svc", "foo.example.com")
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "ns"}, &got))
	require.NotNil(t, got.Spec.Proxied, "malformed annotation must NOT produce nil Spec.Proxied")
	require.True(t, *got.Spec.Proxied, "malformed proxied annotation must fall back to default-true; got false")
}

// TestEmitDNSRecord_TTLAnnotationWired: a valid integer TTL annotation must
// be parsed and assigned to Spec.TTL.
func TestEmitDNSRecord_TTLAnnotationWired(t *testing.T) {
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
		Annotations: map[string]string{conventions.AnnotationTTL: "300"},
	}))

	var got v2alpha1.CloudflareDNSRecord
	name := emittedDNSRecordName("svc", "foo.example.com")
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "ns"}, &got))
	require.Equal(t, 300, got.Spec.TTL, "cloudflare.io/ttl=300 must yield Spec.TTL=300")
}

// TestEmitDNSRecord_TTLAbsentStaysZero: no annotation → Spec.TTL stays 0
// (Cloudflare interprets 0 as "automatic"). Preserves the existing default.
func TestEmitDNSRecord_TTLAbsentStaysZero(t *testing.T) {
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
	require.Equal(t, 0, got.Spec.TTL, "no TTL annotation must leave Spec.TTL=0 (auto)")
}

// TestEmitDNSRecord_TTLMalformedSilentlyZero: a non-integer TTL annotation
// must be silently ignored (Spec.TTL stays 0). Surfacing parse errors would
// require a status condition on the source object, which is out of S3 scope;
// silent fallback matches the existing tolerant style used for proxied.
func TestEmitDNSRecord_TTLMalformedSilentlyZero(t *testing.T) {
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
		Annotations: map[string]string{conventions.AnnotationTTL: "not-an-int"},
	}))

	var got v2alpha1.CloudflareDNSRecord
	name := emittedDNSRecordName("svc", "foo.example.com")
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "ns"}, &got))
	require.Equal(t, 0, got.Spec.TTL, "malformed TTL annotation must fall back to 0")
}
