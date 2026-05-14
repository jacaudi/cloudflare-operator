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
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

func srcScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

// preCreatedTunnel returns a CloudflareTunnel with TunnelCNAME populated so
// the source reconciler can emit a DNSRecord on the first pass without
// waiting for a Watches-driven retrigger.
func preCreatedTunnel(name, namespace string) *v1alpha1.CloudflareTunnel {
	return &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name: name,
			Connector: v1alpha1.ConnectorSpec{
				Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
			},
		},
		Status: v1alpha1.CloudflareTunnelStatus{
			TunnelID:    "tun-abc",
			TunnelCNAME: "tun-abc.cfargotunnel.com",
		},
	}
}

func TestServiceSource_OptInCreatesTunnelAndDNSRecord(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "app-foo",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "foo.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	// Pre-create the tunnel with TunnelCNAME populated so the source
	// reconciler proceeds to emit the DNSRecord on first reconcile.
	tn := preCreatedTunnel("cf-app-foo-payments", "app-foo")
	c := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc, tn).
		WithStatusSubresource(&v1alpha1.CloudflareDNSRecord{}, &v1alpha1.CloudflareTunnel{}).Build()

	cache := tunnelsynth.NewCache()
	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: cache,
		DefaultConnector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app-foo", Name: "svc"}})
	require.NoError(t, err)

	// CloudflareTunnel still present in source's namespace.
	var got v1alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app-foo", Name: "cf-app-foo-payments"}, &got))

	// CloudflareDNSRecord emitted, owner-reffed to the Service, source labels stamped.
	var dnsList v1alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Len(t, dnsList.Items, 1)
	dr := dnsList.Items[0]
	require.Equal(t, "Service", dr.Labels[conventions.LabelSourceKind])
	require.Equal(t, "svc", dr.Labels[conventions.LabelSourceName])
	require.Equal(t, "app-foo", dr.Labels[conventions.LabelSourceNamespace])
	require.Equal(t, "CNAME", dr.Spec.Type)
	require.Equal(t, "foo.example.com", dr.Spec.Name)
	require.NotNil(t, dr.Spec.Content)
	require.Equal(t, "tun-abc.cfargotunnel.com", *dr.Spec.Content)
	require.Len(t, dr.OwnerReferences, 1)
	require.Equal(t, "svc", dr.OwnerReferences[0].Name)

	// Cache populated under the named-tunnel key.
	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "app-foo", Name: "cf-app-foo-payments"})
	require.Len(t, snap, 1)
	require.Equal(t, "foo.example.com", snap[0].Hostname)
}

func TestServiceSource_NoTunnelNameAttachesToNamespacePool(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "app-foo",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:    "true",
				conventions.AnnotationHostnames: "bar.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	c := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc).
		WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).Build()

	r := &ServiceSourceReconciler{Client: c, Scheme: srcScheme(t), Cache: tunnelsynth.NewCache()}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app-foo", Name: "svc"}})
	require.NoError(t, err)

	var tn v1alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app-foo", Name: "cf-app-foo"}, &tn))
}

func TestServiceSource_OptOut_ClearsCache(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{}, // no opt-in
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	c := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc).Build()

	cache := tunnelsynth.NewCache()
	cache.Set(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns"},
		tunnelsynth.SourceKey{Kind: "Service", Namespace: "ns", Name: "svc"},
		[]tunnelsynth.IngressContribution{{Hostname: "stale.example.com", Service: "http://x:80"}})

	r := &ServiceSourceReconciler{Client: c, Scheme: srcScheme(t), Cache: cache}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "svc"}})
	require.NoError(t, err)

	require.Empty(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns"}))
}

// TestServiceSource_OptOutSweepsBothPoolAndNamed verifies Correction C — when
// a Service was previously attached to a named tunnel via annotation, then the
// annotation is dropped (or opt-in disabled), the cache entry under the named
// tunnel-key is swept clean.
func TestServiceSource_OptOutSweepsBothPoolAndNamed(t *testing.T) {
	// Service no longer opted in but still carries the tunnel-name annotation
	// (mid-edit state). The reconciler must clear the named-tunnel entry too.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnelName: "billing", // tunnel-name still set
				// no AnnotationTunnel=true
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	c := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc).Build()
	cache := tunnelsynth.NewCache()
	cache.Set(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-billing"},
		tunnelsynth.SourceKey{Kind: "Service", Namespace: "ns", Name: "svc"},
		[]tunnelsynth.IngressContribution{{Hostname: "stale.example.com", Service: "http://x:80"}})

	r := &ServiceSourceReconciler{Client: c, Scheme: srcScheme(t), Cache: cache}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "svc"}})
	require.NoError(t, err)

	require.Empty(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-billing"}))
}

// TestServiceSource_AnnotationChangeSweepsPriorKey verifies Correction C's
// in-memory tracking: a Service opted-in to "cf-ns-payments" gets retargeted
// to "cf-ns-billing"; the prior key must be cleared on the second reconcile.
func TestServiceSource_AnnotationChangeSweepsPriorKey(t *testing.T) {
	// First reconcile — attach to "payments".
	svcA := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "foo.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	tnA := preCreatedTunnel("cf-ns-payments", "ns")
	tnB := preCreatedTunnel("cf-ns-billing", "ns")
	c := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svcA, tnA, tnB).
		WithStatusSubresource(&v1alpha1.CloudflareDNSRecord{}, &v1alpha1.CloudflareTunnel{}).Build()

	cache := tunnelsynth.NewCache()
	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: cache,
		DefaultConnector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "svc"}})
	require.NoError(t, err)

	// First snapshot under "payments".
	require.Len(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-payments"}), 1)

	// Mutate the Service in place to switch to "billing".
	var live corev1.Service
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "svc"}, &live))
	live.Annotations[conventions.AnnotationTunnelName] = "billing"
	require.NoError(t, c.Update(context.Background(), &live))

	_, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "svc"}})
	require.NoError(t, err)

	// Prior key cleared.
	require.Empty(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-payments"}),
		"prior tunnel-key must be swept on annotation change")
	// New key populated.
	require.Len(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-billing"}), 1)
}

func TestServiceSource_NameTooLong_StatusEventOnly(t *testing.T) {
	// Name-too-long is a stable failure surfaced via event, not an error
	// return — no panic, no CR created, no cache write.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "very-very-long-namespace-name",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "very-very-long-tunnel-name-here",
				conventions.AnnotationHostnames:  "x.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	c := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc).Build()
	rec := record.NewFakeRecorder(8)
	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: tunnelsynth.NewCache(), Recorder: rec,
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}})
	require.NoError(t, err, "name-too-long is a stable failure surfaced via event, not an error return")

	var list v1alpha1.CloudflareTunnelList
	require.NoError(t, c.List(context.Background(), &list))
	require.Empty(t, list.Items)

	// Event with Reason=NameTooLong emitted.
	select {
	case ev := <-rec.Events:
		require.Contains(t, ev, conventions.ReasonNameTooLong)
	default:
		t.Fatal("expected NameTooLong event")
	}
}

func TestServiceSource_MultipleHostnames_EmitsOneRecordEach(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "app-foo",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "foo.example.com,bar.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	tn := preCreatedTunnel("cf-app-foo-payments", "app-foo")
	c := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc, tn).
		WithStatusSubresource(&v1alpha1.CloudflareDNSRecord{}, &v1alpha1.CloudflareTunnel{}).Build()

	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: tunnelsynth.NewCache(),
		DefaultConnector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app-foo", Name: "svc"}})
	require.NoError(t, err)

	var dnsList v1alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Len(t, dnsList.Items, 2)

	names := map[string]bool{}
	for _, dr := range dnsList.Items {
		names[dr.Spec.Name] = true
	}
	require.True(t, names["foo.example.com"])
	require.True(t, names["bar.example.com"])
}

// TestServiceSource_TunnelCNAMEEmpty_DefersEmission verifies Correction A —
// when the tunnel CR exists but TunnelCNAME is empty (e.g. tunnel reconciler
// hasn't completed Create yet), the source reconciler must NOT emit a
// DNSRecord with Content=&"". Defer until the Watches-driven retrigger.
func TestServiceSource_TunnelCNAMEEmpty_DefersEmission(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "app-foo",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "foo.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	// Tunnel CR exists but has no TunnelCNAME.
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-app-foo-payments", Namespace: "app-foo"},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name: "cf-app-foo-payments",
			Connector: v1alpha1.ConnectorSpec{
				Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc, tn).
		WithStatusSubresource(&v1alpha1.CloudflareDNSRecord{}, &v1alpha1.CloudflareTunnel{}).Build()
	cache := tunnelsynth.NewCache()
	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: cache,
		DefaultConnector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app-foo", Name: "svc"}})
	require.NoError(t, err)

	// No DNSRecord emitted yet — defer for Watches retrigger.
	var dnsList v1alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Empty(t, dnsList.Items, "must not emit DNSRecord when TunnelCNAME is empty")

	// Cache is still populated — that's the contract for the tunnel
	// reconciler to drive a config PUT in parallel.
	require.Len(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "app-foo", Name: "cf-app-foo-payments"}), 1)
}

func TestServiceSource_ZoneRefAnnotation_ThreadedToDNSRecord(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "app-foo",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "payments",
				conventions.AnnotationHostnames:  "foo.example.com",
				conventions.AnnotationZoneRef:    "my-zone",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	tn := preCreatedTunnel("cf-app-foo-payments", "app-foo")
	c := fake.NewClientBuilder().WithScheme(srcScheme(t)).WithObjects(svc, tn).
		WithStatusSubresource(&v1alpha1.CloudflareDNSRecord{}, &v1alpha1.CloudflareTunnel{}).Build()

	r := &ServiceSourceReconciler{
		Client: c, Scheme: srcScheme(t), Cache: tunnelsynth.NewCache(),
		DefaultConnector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app-foo", Name: "svc"}})
	require.NoError(t, err)

	var dnsList v1alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Len(t, dnsList.Items, 1)
	require.NotNil(t, dnsList.Items[0].Spec.ZoneRef)
	require.Equal(t, "my-zone", dnsList.Items[0].Spec.ZoneRef.Name)
	require.Equal(t, "app-foo", dnsList.Items[0].Spec.ZoneRef.Namespace)
}

// TestEmittedDNSRecordNameDNS1123Compliant exercises edge-case hostnames
// against the sanitize() suffix logic. Every emitted DNSRecord CR name must be
// a valid DNS-1123 subdomain — labels of [a-z0-9] with optional internal
// hyphens, ≤63 chars per label.
func TestEmittedDNSRecordNameDNS1123Compliant(t *testing.T) {
	// Top-level CR-name regex: lowercase a-z0-9 + internal hyphens, no
	// leading/trailing hyphens, ≤63 chars.
	dns1123Subdomain := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

	cases := []struct {
		name     string
		svcName  string
		hostname string
	}{
		{"trailing-dot", "svc", "foo.example.com."},
		{"consecutive-dots", "svc", "foo..bar.example.com"},
		{"mixed-case", "svc", "Foo.Example.COM"},
		{"long-hostname", "svc", strings.Repeat("a", 80) + ".example.com"},
		{"already-clean", "svc", "foo.example.com"},
		{"hyphen-prefix", "svc", "-leading.example.com"},
		{"underscore", "svc", "foo_bar.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := emittedDNSRecordName(tc.svcName, tc.hostname)
			require.LessOrEqual(t, len(got), 63, "name must fit DNS-1123 label limit: %q", got)
			require.True(t, dns1123Subdomain.MatchString(got), "name must be DNS-1123 valid: %q", got)
		})
	}
}

// Verify the reconciler satisfies the controller-runtime Reconciler interface.
var _ reconcile.Reconciler = (*ServiceSourceReconciler)(nil)
