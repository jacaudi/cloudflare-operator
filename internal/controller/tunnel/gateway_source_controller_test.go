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
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

func gwScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, gwv1.Install(s))
	require.NoError(t, v2alpha1.AddToScheme(s))
	return s
}

// gwListener builds an HTTP listener with the given hostname and port. Helper
// to keep the test fixtures concise.
func gwListener(name, hostname string, port int32, proto gwv1.ProtocolType) gwv1.Listener {
	hp := gwv1.Hostname(hostname)
	return gwv1.Listener{
		Name:     gwv1.SectionName(name),
		Hostname: &hp,
		Port:     gwv1.PortNumber(port),
		Protocol: proto,
	}
}

// mkGw builds a Gateway opted in to the tunnel with the REQUIRED
// cloudflare.io/gateway-service annotation. Each entry of hostnames
// produces one HTTP listener at port 80.
//
// gwSvcRef accepts the annotation form "<ns>/<name>" or "<ns>/<name>:<port>".
func mkGw(name, ns, gwSvcRef string, hostnames []string) *gwv1.Gateway {
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: gwSvcRef,
			},
		},
	}
	for _, h := range hostnames {
		gw.Spec.Listeners = append(gw.Spec.Listeners, gwListener(h, h, 80, gwv1.HTTPProtocolType))
	}
	return gw
}

// gwPreCreatedTunnel returns a CloudflareTunnel with TunnelCNAME populated so
// the Gateway reconciler can emit DNSRecord CRs on the first pass.
func gwPreCreatedTunnel(name, namespace string) *v2alpha1.CloudflareTunnel {
	return &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name: name,
			Connector: v2alpha1.ConnectorSpec{
				Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
			},
		},
		Status: v2alpha1.CloudflareTunnelStatus{
			TunnelID:    "tun-abc",
			TunnelCNAME: "tun-abc.cfargotunnel.com",
		},
	}
}

func TestGatewaySource_HappyPath(t *testing.T) {
	gw := mkGw("gw", "gw-ns", "gw-ns/envoy-gw", []string{"external.example.com"})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-gw", Namespace: "gw-ns"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	preTun := gwPreCreatedTunnel("cf-gw-ns-edge", "gw-ns")
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, preTun).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}, &v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	cache := tunnelsynth.NewCache()
	r := &GatewaySourceReconciler{
		Client: c, Scheme: gwScheme(t), Cache: cache,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "gw-ns", Name: "gw"}})
	require.NoError(t, err)

	// Tunnel CR exists (pre-created).
	var tn v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "gw-ns", Name: "cf-gw-ns-edge"}, &tn))

	// Cache snapshot has exactly one HTTP contribution to envoy-gw.gw-ns.
	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Len(t, snap, 1)
	require.Equal(t, "external.example.com", snap[0].Hostname)
	require.Contains(t, snap[0].Service, "envoy-gw.gw-ns.svc.cluster.local")
	require.Contains(t, snap[0].Service, ":80")

	// Exactly one DNSRecord emitted, owner-reffed to the Gateway, CNAME → tunnel CNAME.
	var dnsList v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Len(t, dnsList.Items, 1)
	dr := dnsList.Items[0]
	require.Equal(t, "CNAME", dr.Spec.Type)
	require.Equal(t, "external.example.com", dr.Spec.Name)
	require.NotNil(t, dr.Spec.Content)
	require.Equal(t, "tun-abc.cfargotunnel.com", *dr.Spec.Content)
	require.Equal(t, "Gateway", dr.Labels[conventions.LabelSourceKind])
	require.Equal(t, "gw", dr.Labels[conventions.LabelSourceName])
	require.Equal(t, "gw-ns", dr.Labels[conventions.LabelSourceNamespace])
	require.Len(t, dr.OwnerReferences, 1)
	require.Equal(t, "gw", dr.OwnerReferences[0].Name)
}

func TestGatewaySource_AutoCreateStampsSourceLabels(t *testing.T) {
	// Auto-create path: no pre-existing tunnel CR. EnsureTunnelCR must stamp
	// source-kind="Gateway" (not "" — typed client clears TypeMeta on Get).
	gw := mkGw("gw", "gw-ns", "gw-ns/envoy-gw", []string{"a.example.com"})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-gw", Namespace: "gw-ns"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	r := &GatewaySourceReconciler{
		Client: c, Scheme: gwScheme(t), Cache: tunnelsynth.NewCache(),
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "gw-ns", Name: "gw"}})
	require.NoError(t, err)

	var tn v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "gw-ns", Name: "cf-gw-ns-edge"}, &tn))
	require.Equal(t, "Gateway", tn.Labels[conventions.LabelSourceKind],
		"source-kind label must be 'Gateway' literal (typed client clears TypeMeta on Get)")
	require.Equal(t, "gw", tn.Labels[conventions.LabelSourceName])
	require.Equal(t, "gw-ns", tn.Labels[conventions.LabelSourceNamespace])
}

func TestGatewaySource_OptOut_ClearsCache(t *testing.T) {
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: "ns",
			Annotations: map[string]string{}, // no opt-in
		},
		Spec: gwv1.GatewaySpec{},
	}
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	// Pre-populate a stale entry for this source.
	cache.Set(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns"},
		tunnelsynth.SourceKey{Kind: "Gateway", Namespace: "ns", Name: "gw"},
		[]tunnelsynth.IngressContribution{{Hostname: "stale.example.com", Service: "http://x:80"}})

	r := &GatewaySourceReconciler{Client: c, Scheme: gwScheme(t), Cache: cache}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)
	require.Empty(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns"}))
}

func TestGatewaySource_NoGatewayServiceAnnotation_RejectsWithEvent(t *testing.T) {
	// Opted in, but the cloudflare.io/gateway-service annotation is missing.
	// Expect a GatewayServiceUnspecified Warning event and no CR / no cache write.
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "edge",
				// AnnotationGatewayService deliberately missing.
			},
		},
		Spec: gwv1.GatewaySpec{Listeners: []gwv1.Listener{gwListener("h", "h.example.com", 80, gwv1.HTTPProtocolType)}},
	}
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	rec := record.NewFakeRecorder(8)
	r := &GatewaySourceReconciler{
		Client: c, Scheme: gwScheme(t), Cache: tunnelsynth.NewCache(), Recorder: rec,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)

	// No DNSRecord emitted.
	var dnsList v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Empty(t, dnsList.Items)

	select {
	case ev := <-rec.Events:
		require.Contains(t, ev, conventions.ReasonGatewayServiceUnspecified)
	default:
		t.Fatal("expected GatewayServiceUnspecified event")
	}
}

func TestGatewaySource_NoListenerHostname_RejectsWithEvent(t *testing.T) {
	// Listener exists but has no hostname — emit NoListenerHostname and clear cache.
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationGatewayService: "ns/svc",
			},
		},
		Spec: gwv1.GatewaySpec{Listeners: []gwv1.Listener{{Name: "n", Port: 80, Protocol: gwv1.HTTPProtocolType}}},
	}
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	rec := record.NewFakeRecorder(8)
	r := &GatewaySourceReconciler{Client: c, Scheme: gwScheme(t), Cache: tunnelsynth.NewCache(), Recorder: rec}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)

	select {
	case ev := <-rec.Events:
		require.Contains(t, ev, conventions.ReasonNoListenerHostname)
	default:
		t.Fatal("expected NoListenerHostname event")
	}
}

func TestGatewaySource_MultipleListenerHostnames(t *testing.T) {
	gw := mkGw("gw", "ns", "ns/gw-svc", []string{"a.example.com", "b.example.com"})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: "ns"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	preTun := gwPreCreatedTunnel("cf-ns-edge", "ns")
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, preTun).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &GatewaySourceReconciler{
		Client: c, Scheme: gwScheme(t), Cache: cache,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)
	require.Len(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-edge"}), 2)

	var dnsList v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Len(t, dnsList.Items, 2)
}

func TestGatewaySource_TunnelCNAMEEmpty_DefersEmission(t *testing.T) {
	// Tunnel CR exists but TunnelCNAME empty — must NOT emit a DNSRecord with
	// Content=&"". Cache still populated for the tunnel reconciler's config PUT.
	gw := mkGw("gw", "ns", "ns/gw-svc", []string{"a.example.com"})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: "ns"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-ns-edge", Namespace: "ns"},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name: "cf-ns-edge",
			Connector: v2alpha1.ConnectorSpec{
				Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
			},
		},
		// No Status.TunnelCNAME.
	}
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &GatewaySourceReconciler{
		Client: c, Scheme: gwScheme(t), Cache: cache,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)

	var dnsList v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Empty(t, dnsList.Items, "must not emit DNSRecord when TunnelCNAME is empty")
	require.Len(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-edge"}), 1,
		"cache still populated so the tunnel reconciler's config PUT proceeds in parallel")
}

func TestGatewaySource_AnnotationPortOverride(t *testing.T) {
	// Annotation overrides the Service port: "ns/svc:9090" should win over the
	// Service's first port (80) and the listener's port (80).
	gw := mkGw("gw", "ns", "ns/gw-svc:9090", []string{"a.example.com"})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: "ns"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	preTun := gwPreCreatedTunnel("cf-ns-edge", "ns")
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, preTun).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &GatewaySourceReconciler{
		Client: c, Scheme: gwScheme(t), Cache: cache,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)

	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-edge"})
	require.Len(t, snap, 1)
	require.Contains(t, snap[0].Service, ":9090", "annotation port must override listener/service ports: %s", snap[0].Service)
}

func TestGatewaySource_FallsBackToServiceFirstPort(t *testing.T) {
	// Annotation has no port — fall back to Service.Spec.Ports[0].Port = 8080.
	gw := mkGw("gw", "ns", "ns/gw-svc", []string{"a.example.com"})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: "ns"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	preTun := gwPreCreatedTunnel("cf-ns-edge", "ns")
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, preTun).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &GatewaySourceReconciler{
		Client: c, Scheme: gwScheme(t), Cache: cache,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)

	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-edge"})
	require.Len(t, snap, 1)
	require.Contains(t, snap[0].Service, ":8080", "must fall back to Service's first port when annotation has no port")
}

func TestGatewaySource_AllTLSListeners_RegistersEmptyContribs(t *testing.T) {
	// Gateway with only TLS listeners: HTTP/HTTPS contribs are empty (TLS is
	// owned by T13's TLSRoute reconciler). Source is still registered with the
	// empty slice — required for symmetric annotation-change sweeps later.
	hp := gwv1.Hostname("tls.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: "ns/gw-svc",
			},
		},
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{{
				Name:     "tls",
				Hostname: &hp,
				Port:     443,
				Protocol: gwv1.TLSProtocolType,
			}},
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: "ns"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 443}}},
	}
	preTun := gwPreCreatedTunnel("cf-ns-edge", "ns")
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, preTun).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &GatewaySourceReconciler{
		Client: c, Scheme: gwScheme(t), Cache: cache,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)

	tk := tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-edge"}
	require.Empty(t, cache.Snapshot(tk), "TLS-only listeners contribute zero HTTP/HTTPS entries")
	// Source still registered (cache.Set was called with the empty slice).
	require.Contains(t, cache.AttachedSources(tk),
		tunnelsynth.SourceKey{Kind: "Gateway", Namespace: "ns", Name: "gw"},
		"source must remain registered with zero contributions for symmetric sweeps")
}

func TestGatewaySource_UnsupportedProtocol_EmitsEvent(t *testing.T) {
	// TCP listener — must emit an UnsupportedProtocol event. Mixed with one
	// HTTP listener so we also verify HTTP contributions are still captured.
	hpHTTP := gwv1.Hostname("ok.example.com")
	hpTCP := gwv1.Hostname("raw.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: "ns/gw-svc",
			},
		},
		Spec: gwv1.GatewaySpec{Listeners: []gwv1.Listener{
			{Name: "http", Hostname: &hpHTTP, Port: 80, Protocol: gwv1.HTTPProtocolType},
			{Name: "tcp", Hostname: &hpTCP, Port: 9000, Protocol: gwv1.TCPProtocolType},
		}},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: "ns"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	preTun := gwPreCreatedTunnel("cf-ns-edge", "ns")
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, preTun).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	rec := record.NewFakeRecorder(16)
	cache := tunnelsynth.NewCache()
	r := &GatewaySourceReconciler{
		Client: c, Scheme: gwScheme(t), Cache: cache, Recorder: rec,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)

	// One HTTP contribution captured.
	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-edge"})
	require.Len(t, snap, 1)
	require.Equal(t, "ok.example.com", snap[0].Hostname)

	// Drain events; at least one must contain UnsupportedProtocol.
	found := false
	for drained := false; !drained; {
		select {
		case ev := <-rec.Events:
			if strings.Contains(ev, "UnsupportedProtocol") {
				found = true
			}
		default:
			drained = true
		}
	}
	require.True(t, found, "expected an UnsupportedProtocol event for the TCP listener")
}

func TestGatewaySource_HTTPSScheme(t *testing.T) {
	// HTTPS listener → Service URL scheme=https.
	hp := gwv1.Hostname("secure.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: "ns/gw-svc",
			},
		},
		Spec: gwv1.GatewaySpec{Listeners: []gwv1.Listener{{
			Name: "https", Hostname: &hp, Port: 443, Protocol: gwv1.HTTPSProtocolType,
		}}},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: "ns"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 443}}},
	}
	preTun := gwPreCreatedTunnel("cf-ns-edge", "ns")
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, preTun).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &GatewaySourceReconciler{
		Client: c, Scheme: gwScheme(t), Cache: cache,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)

	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-edge"})
	require.Len(t, snap, 1)
	require.True(t, strings.HasPrefix(snap[0].Service, "https://"), "https listener must produce https scheme: %s", snap[0].Service)
}

func TestGatewaySource_AnnotationChangeSweepsPriorKey(t *testing.T) {
	// First reconcile attaches to "cf-ns-edge". Mutate the tunnel-name annotation
	// to "billing" and reconcile again; the prior tunnel-key must be cleared.
	gw := mkGw("gw", "ns", "ns/gw-svc", []string{"a.example.com"})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: "ns"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	tnA := gwPreCreatedTunnel("cf-ns-edge", "ns")
	tnB := gwPreCreatedTunnel("cf-ns-billing", "ns")
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, tnA, tnB).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &GatewaySourceReconciler{
		Client: c, Scheme: gwScheme(t), Cache: cache,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)
	require.Len(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-edge"}), 1)

	// Mutate annotation.
	var live gwv1.Gateway
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "gw"}, &live))
	live.Annotations[conventions.AnnotationTunnelName] = "billing"
	require.NoError(t, c.Update(context.Background(), &live))

	_, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)

	require.Empty(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-edge"}),
		"prior tunnel-key must be swept on annotation change")
	require.Len(t, cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-billing"}), 1)
}

func TestGatewaySource_GatewayDeletedSweepsCache(t *testing.T) {
	gw := mkGw("gw", "ns", "ns/gw-svc", []string{"a.example.com"})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: "ns"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	preTun := gwPreCreatedTunnel("cf-ns-edge", "ns")
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, preTun).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &GatewaySourceReconciler{
		Client: c, Scheme: gwScheme(t), Cache: cache,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	tunKey := tunnelsynth.TunnelKey{Namespace: "ns", Name: "cf-ns-edge"}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)
	require.Len(t, cache.Snapshot(tunKey), 1)

	require.NoError(t, c.Delete(context.Background(), gw))
	_, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)
	require.Empty(t, cache.Snapshot(tunKey), "NotFound branch must sweep the cache entry for the deleted Gateway")
}

func TestGatewaySource_ZoneRefAndAdoptThreaded(t *testing.T) {
	gw := mkGw("gw", "ns", "ns/gw-svc", []string{"a.example.com"})
	gw.Annotations[conventions.AnnotationZoneRef] = "my-zone"
	gw.Annotations[conventions.AnnotationAdopt] = "true"
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: "ns"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	preTun := gwPreCreatedTunnel("cf-ns-edge", "ns")
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, preTun).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := &GatewaySourceReconciler{
		Client: c, Scheme: gwScheme(t), Cache: tunnelsynth.NewCache(),
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)

	var dnsList v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Len(t, dnsList.Items, 1)
	require.NotNil(t, dnsList.Items[0].Spec.ZoneRef)
	require.Equal(t, "my-zone", dnsList.Items[0].Spec.ZoneRef.Name)
	require.Equal(t, "ns", dnsList.Items[0].Spec.ZoneRef.Namespace)
	require.True(t, dnsList.Items[0].Spec.Adopt, "Adopt annotation must thread to DNSRecord spec")
}

func TestGatewaySource_InvalidName_StatusEventOnly(t *testing.T) {
	gw := mkGw("gw", "ns", "ns/gw-svc", []string{"a.example.com"})
	gw.Annotations[conventions.AnnotationTunnelName] = "Invalid_Name" // uppercase + underscore
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	rec := record.NewFakeRecorder(8)
	r := &GatewaySourceReconciler{Client: c, Scheme: gwScheme(t), Cache: tunnelsynth.NewCache(), Recorder: rec}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)

	var list v2alpha1.CloudflareTunnelList
	require.NoError(t, c.List(context.Background(), &list))
	require.Empty(t, list.Items)

	select {
	case ev := <-rec.Events:
		require.Contains(t, ev, conventions.ReasonInvalidName)
	default:
		t.Fatal("expected an InvalidName event")
	}
}

// TestGatewaySourceOptOut_ClearsDerivedKey is a regression-lock characterization
// test that pins the opt-out sweep to the DeriveTunnelName-derived key. A future
// naming-template change that is not mirrored in the opt-out path would cause this
// test to fail, making the drift visible before it ships.
//
// Strategy: pre-seed the Cache under the DeriveTunnelName-derived key for the
// Gateway's namespace + tunnel-name annotation, then reconcile with the opt-in
// annotation absent (opt-out). Assert the cache entry is gone, proving the sweep
// targeted exactly the derived key and not a hand-built variant.
func TestGatewaySourceOptOut_ClearsDerivedKey(t *testing.T) {
	const ns = "ns"
	const tn = "edge"

	// DeriveTunnelName("ns", "edge") == "cf-ns-edge" — verified by the
	// function's own contract. The test is deliberately written in terms of
	// DeriveTunnelName so that if the naming template ever changes legitimately,
	// this test tracks that change correctly.
	derivedKey, err := DeriveTunnelName(ns, tn)
	if err != nil {
		t.Fatalf("DeriveTunnelName(%q, %q) unexpected error: %v", ns, tn, err)
	}

	// Gateway WITHOUT the opt-in annotation (opt-out), but with the
	// tunnel-name annotation still present so the opt-out sweep must derive
	// the named form (cf-<ns>-<tn>) rather than the pool form (cf-<ns>).
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gw",
			Namespace: ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnelName: tn,
				// AnnotationTunnel deliberately absent → opt-out.
			},
		},
		Spec: gwv1.GatewaySpec{},
	}
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()

	// Pre-seed a stale contribution under the DeriveTunnelName-derived tunnel-key
	// for this source, simulating a post-restart state where the in-memory tracker
	// is empty but the cache holds an entry from a previous opt-in reconcile.
	srcKey := tunnelsynth.SourceKey{Kind: "Gateway", Namespace: ns, Name: "gw"}
	tunKey := tunnelsynth.TunnelKey{Namespace: ns, Name: derivedKey}
	cache.Set(tunKey, srcKey, []tunnelsynth.IngressContribution{
		{Hostname: "stale.example.com", Service: "http://svc:80"},
	})

	// Confirm the seed is in place.
	require.NotEmpty(t, cache.Snapshot(tunKey), "pre-condition: cache must be seeded before opt-out reconcile")

	r := &GatewaySourceReconciler{Client: c, Scheme: gwScheme(t), Cache: cache}
	_, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "gw"}})
	require.NoError(t, err)

	// The opt-out sweep must have cleared the DeriveTunnelName-derived key.
	require.Empty(t, cache.Snapshot(tunKey),
		"opt-out reconcile must clear the DeriveTunnelName-derived key %q for this source", derivedKey)
}

// TestGatewaySource_NoDNSForTCPListener asserts that a TCP listener's hostname
// does NOT receive a CloudflareDNSRecord even when it is returned by
// listenerHostnames. Only the HTTPS listener (HTTP/HTTPS contribution path)
// must get a record; the TCP listener produces no ingress contribution and
// therefore must not appear in the desired set that drives DNS emission.
//
// This test is RED against current code: the emit loop iterates the unfiltered
// listenerHostnames result, so db.example.com currently gets a record too.
func TestGatewaySource_NoDNSForTCPListener(t *testing.T) {
	// Build a Gateway with TWO listeners, both carrying hostnames:
	//   - HTTPS: web.example.com  → SHOULD get a CloudflareDNSRecord
	//   - TCP:   db.example.com   → must NOT get a CloudflareDNSRecord
	hpHTTPS := gwv1.Hostname("web.example.com")
	hpTCP := gwv1.Hostname("db.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: "ns/gw-svc",
			},
		},
		Spec: gwv1.GatewaySpec{Listeners: []gwv1.Listener{
			{Name: "https", Hostname: &hpHTTPS, Port: 443, Protocol: gwv1.HTTPSProtocolType},
			{Name: "tcp", Hostname: &hpTCP, Port: 5432, Protocol: gwv1.TCPProtocolType},
		}},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: "ns"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 443}}},
	}
	// gwPreCreatedTunnel pre-populates Status.TunnelCNAME so the reconciler
	// emits DNSRecord CRs on the very first pass (no second reconcile needed).
	preTun := gwPreCreatedTunnel("cf-ns-edge", "ns")
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, preTun).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}, &v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	rec := record.NewFakeRecorder(16)
	cache := tunnelsynth.NewCache()
	r := &GatewaySourceReconciler{
		Client: c, Scheme: gwScheme(t), Cache: cache, Recorder: rec,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)

	// Exactly ONE CloudflareDNSRecord must exist: the one for web.example.com.
	// db.example.com must not receive a record (TCP → no ingress contribution).
	var dnsList v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Len(t, dnsList.Items, 1, "expected exactly one DNSRecord (HTTPS listener only); TCP listener must not emit")
	require.Equal(t, "web.example.com", dnsList.Items[0].Spec.Name,
		"the single emitted record must be for the HTTPS listener hostname")

	// Explicitly verify the TCP hostname was NOT emitted.
	for _, dr := range dnsList.Items {
		require.NotEqual(t, "db.example.com", dr.Spec.Name,
			"TCP listener hostname must never receive a CloudflareDNSRecord")
	}
}

// TestGatewaySource_TLSListenerEmitsApexCNAME is a regression lock for the
// IMP-2 fix. The TCP/UDP black-hole fix must NOT also drop the apex CNAME for
// a TLS-mode listener: gateway.emitDNSRecord is the ONLY emitter of
// "<apex> CNAME -> tunnelCNAME" for a Gateway. TLSRoute.emitChainDNSRecord
// emits "route-host -> gwApex" (Content=gwApex), never "gwApex -> tunnelCNAME".
// So a TLS-apex Gateway still legitimately needs its apex->tunnel record, or
// the entire TLSRoute chain resolves to nothing.
//
// This test FAILS against the regressed code (desired built from contribs,
// which is HTTP/HTTPS-only — the TLS arm appends nothing, so zero records are
// emitted for a TLS-only Gateway).
func TestGatewaySource_TLSListenerEmitsApexCNAME(t *testing.T) {
	// Single TLS listener — the canonical tunnel-apex-for-TLSRoute pattern.
	hpTLS := gwv1.Hostname("tls-apex.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: "ns/gw-svc",
			},
		},
		Spec: gwv1.GatewaySpec{Listeners: []gwv1.Listener{
			{Name: "tls", Hostname: &hpTLS, Port: 443, Protocol: gwv1.TLSProtocolType},
		}},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: "ns"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 443}}},
	}
	// gwPreCreatedTunnel pre-populates Status.TunnelCNAME ("tun-abc.cfargotunnel.com")
	// so the reconciler emits DNSRecord CRs on the first pass.
	preTun := gwPreCreatedTunnel("cf-ns-edge", "ns")
	base := fake.NewClientBuilder().WithScheme(gwScheme(t)).WithObjects(gw, svc, preTun).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}, &v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	rec := record.NewFakeRecorder(16)
	cache := tunnelsynth.NewCache()
	r := &GatewaySourceReconciler{
		Client: c, Scheme: gwScheme(t), Cache: cache, Recorder: rec,
		DefaultConnector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"}})
	require.NoError(t, err)

	// Exactly ONE CloudflareDNSRecord: the apex CNAME tls-apex.example.com ->
	// tunnel CNAME. This is the record the TLSRoute chain depends on.
	var dnsList v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &dnsList))
	require.Len(t, dnsList.Items, 1, "TLS-apex Gateway must still emit its apex->tunnel CNAME")
	dr := dnsList.Items[0]
	require.Equal(t, "tls-apex.example.com", dr.Spec.Name,
		"the emitted record must be for the TLS listener's apex hostname")
	require.NotNil(t, dr.Spec.Content)
	require.Equal(t, "tun-abc.cfargotunnel.com", *dr.Spec.Content,
		"apex record Content must be the tunnel CNAME (proving it's the apex->tunnel record, not a route->apex one)")
}

// Verify the reconciler satisfies the controller-runtime Reconciler interface.
var _ reconcile.Reconciler = (*GatewaySourceReconciler)(nil)
