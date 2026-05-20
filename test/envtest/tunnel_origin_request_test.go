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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// TestEnvtest_OriginRequest is the parent func whose sub-tests each cover one
// segment of the OriginRequest precedence chain and the post-upgrade wipe path.
// All four sub-tests reuse setupHTTPRouteEnv — it wires Tunnel + Gateway +
// HTTPRoute reconcilers sharing one tunnelsynth.Cache, which is the minimal
// setup needed to drive the full annotation→contribution→PUT chain.
//
// The four cases are kept as separate sub-tests (not merged) so the precedence
// chain is auditable failure-by-failure.
func TestEnvtest_OriginRequest(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}

	t.Run("GatewayAnnotation", testEnvtest_OriginRequest_GatewayAnnotation)
	t.Run("RouteOverridesGateway", testEnvtest_OriginRequest_RouteOverridesGateway)
	t.Run("SpecFallback", testEnvtest_OriginRequest_SpecFallback)
	t.Run("WipePath", testEnvtest_OriginRequest_WipePath)
}

// testEnvtest_OriginRequest_GatewayAnnotation covers OR-T15 case 1:
// cloudflare.io/origin-server-name on the Gateway propagates through
// TranslateHTTPRoute → Resolve → PutConfiguration and lands in both the
// mock's last PUT payload and Status.ObservedIngress.
func testEnvtest_OriginRequest_GatewayAnnotation(t *testing.T) {
	t.Helper()
	f := setupHTTPRouteEnv(t)
	ctx := context.Background()

	zone := minimalZone("example-com", f.ns)
	require.NoError(t, f.c.Create(ctx, zone))

	gwSvc := minimalService("gw-svc", f.ns, 80)
	require.NoError(t, f.c.Create(ctx, gwSvc))

	gwHostname := gwv1.Hostname("ext.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:           "true",
				conventions.AnnotationTunnelName:       "edge",
				conventions.AnnotationGatewayService:   f.ns + "/gw-svc",
				conventions.AnnotationOriginServerName: "origin.example.com",
			},
		},
		Spec: gwv1.GatewaySpec{
			GatewayClassName: "any-class",
			Listeners: []gwv1.Listener{{
				Name: "h", Hostname: &gwHostname, Port: 80, Protocol: gwv1.HTTPProtocolType,
			}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gw))

	tunnelName := "cf-" + f.ns + "-edge"
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &tn); err != nil {
			return false
		}
		return tn.Status.TunnelCNAME != ""
	}, 30*time.Second, 250*time.Millisecond, "tunnel %q must reach Status.TunnelCNAME", tunnelName)

	nsRef := gwv1.Namespace(f.ns)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationZoneRef: "example-com",
				// No origin-server-name on the route — must inherit from Gateway.
			},
		},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"app.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: &nsRef}},
			},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	require.NoError(t, f.c.Create(ctx, rt))

	var tn v2alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &tn))
	nudgeTunnel(t, f.c, ctx, &tn)

	// (a) PutConfiguration payload carries OriginRequest.OriginServerName == "origin.example.com".
	require.Eventually(t, func() bool {
		cfg, err := f.mock.Tunnel.GetConfiguration(ctx, "acct-1", mustGetTunnelID(t, f.c, ctx, f.ns, tunnelName))
		if err != nil {
			return false
		}
		for _, e := range cfg.Config.Ingress {
			if e.Hostname == "app.example.com" &&
				e.OriginRequest != nil &&
				e.OriginRequest.OriginServerName != nil &&
				*e.OriginRequest.OriginServerName == "origin.example.com" {
				return true
			}
		}
		return false
	}, 20*time.Second, 250*time.Millisecond,
		"mock PUT payload must carry OriginRequest.OriginServerName=origin.example.com for app.example.com")

	// (b) Status.ObservedIngress reflects the OriginRequest.
	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &got); err != nil {
			return false
		}
		for _, snap := range got.Status.ObservedIngress {
			if snap.Hostname == "app.example.com" &&
				snap.OriginRequest != nil &&
				snap.OriginRequest.OriginServerName != nil &&
				*snap.OriginRequest.OriginServerName == "origin.example.com" {
				return true
			}
		}
		return false
	}, 20*time.Second, 250*time.Millisecond,
		"Status.ObservedIngress must carry OriginServerName=origin.example.com for app.example.com")
}

// testEnvtest_OriginRequest_RouteOverridesGateway covers OR-T15 case 2:
// when both Gateway and HTTPRoute carry origin-server-name, the route's value
// wins (route annotation > Gateway annotation per defaultsFromAnnotations
// precedence chain).
func testEnvtest_OriginRequest_RouteOverridesGateway(t *testing.T) {
	t.Helper()
	f := setupHTTPRouteEnv(t)
	ctx := context.Background()

	zone := minimalZone("example-com", f.ns)
	require.NoError(t, f.c.Create(ctx, zone))

	gwSvc := minimalService("gw-svc", f.ns, 80)
	require.NoError(t, f.c.Create(ctx, gwSvc))

	gwHostname := gwv1.Hostname("ext.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:           "true",
				conventions.AnnotationTunnelName:       "edge",
				conventions.AnnotationGatewayService:   f.ns + "/gw-svc",
				conventions.AnnotationOriginServerName: "gw.example.com", // Gateway value
			},
		},
		Spec: gwv1.GatewaySpec{
			GatewayClassName: "any-class",
			Listeners: []gwv1.Listener{{
				Name: "h", Hostname: &gwHostname, Port: 80, Protocol: gwv1.HTTPProtocolType,
			}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gw))

	tunnelName := "cf-" + f.ns + "-edge"
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &tn); err != nil {
			return false
		}
		return tn.Status.TunnelCNAME != ""
	}, 30*time.Second, 250*time.Millisecond, "tunnel %q must reach Status.TunnelCNAME", tunnelName)

	nsRef := gwv1.Namespace(f.ns)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationZoneRef:          "example-com",
				conventions.AnnotationOriginServerName: "route.example.com", // Route value — must win
			},
		},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"app.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: &nsRef}},
			},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	require.NoError(t, f.c.Create(ctx, rt))

	var tn v2alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &tn))
	nudgeTunnel(t, f.c, ctx, &tn)

	// PUT body and Status must both carry route.example.com (route wins).
	require.Eventually(t, func() bool {
		cfg, err := f.mock.Tunnel.GetConfiguration(ctx, "acct-1", mustGetTunnelID(t, f.c, ctx, f.ns, tunnelName))
		if err != nil {
			return false
		}
		for _, e := range cfg.Config.Ingress {
			if e.Hostname == "app.example.com" &&
				e.OriginRequest != nil &&
				e.OriginRequest.OriginServerName != nil &&
				*e.OriginRequest.OriginServerName == "route.example.com" {
				return true
			}
		}
		return false
	}, 20*time.Second, 250*time.Millisecond,
		"mock PUT payload must carry OriginServerName=route.example.com (route overrides Gateway)")

	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &got); err != nil {
			return false
		}
		for _, snap := range got.Status.ObservedIngress {
			if snap.Hostname == "app.example.com" &&
				snap.OriginRequest != nil &&
				snap.OriginRequest.OriginServerName != nil &&
				*snap.OriginRequest.OriginServerName == "route.example.com" {
				return true
			}
		}
		return false
	}, 20*time.Second, 250*time.Millisecond,
		"Status.ObservedIngress must carry OriginServerName=route.example.com (route overrides Gateway)")
}

// testEnvtest_OriginRequest_SpecFallback covers OR-T15 case 3:
// when no annotations set origin-server-name, Spec.Routing.OriginRequest on
// the CloudflareTunnel CR is used as the fallback (DefaultsFor path).
func testEnvtest_OriginRequest_SpecFallback(t *testing.T) {
	t.Helper()
	f := setupHTTPRouteEnv(t)
	ctx := context.Background()

	zone := minimalZone("example-com", f.ns)
	require.NoError(t, f.c.Create(ctx, zone))

	gwSvc := minimalService("gw-svc", f.ns, 80)
	require.NoError(t, f.c.Create(ctx, gwSvc))

	gwHostname := gwv1.Hostname("ext.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:       "true",
				conventions.AnnotationTunnelName:   "edge",
				conventions.AnnotationGatewayService: f.ns + "/gw-svc",
				// No origin-server-name on Gateway.
			},
		},
		Spec: gwv1.GatewaySpec{
			GatewayClassName: "any-class",
			Listeners: []gwv1.Listener{{
				Name: "h", Hostname: &gwHostname, Port: 80, Protocol: gwv1.HTTPProtocolType,
			}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gw))

	tunnelName := "cf-" + f.ns + "-edge"
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &tn); err != nil {
			return false
		}
		return tn.Status.TunnelCNAME != ""
	}, 30*time.Second, 250*time.Millisecond, "tunnel %q must reach Status.TunnelCNAME", tunnelName)

	// Patch the auto-created tunnel CR to add Spec.Routing.OriginRequest.
	specOSN := "spec.example.com"
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &tn); err != nil {
			return false
		}
		tn.Spec.Routing = &v2alpha1.TunnelRoutingSpec{
			OriginRequest: &v2alpha1.TunnelOriginRequest{
				OriginServerName: &specOSN,
			},
		}
		return f.c.Update(ctx, &tn) == nil
	}, 10*time.Second, 250*time.Millisecond, "patch CloudflareTunnel with Spec.Routing.OriginRequest")

	nsRef := gwv1.Namespace(f.ns)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationZoneRef: "example-com",
				// No origin-server-name on route — Spec fallback must apply.
			},
		},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"app.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: &nsRef}},
			},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	require.NoError(t, f.c.Create(ctx, rt))

	// Nudge the tunnel so it reconciles with the updated spec.
	var tn v2alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &tn))
	nudgeTunnel(t, f.c, ctx, &tn)

	// PUT body must carry spec.example.com (spec fallback).
	require.Eventually(t, func() bool {
		cfg, err := f.mock.Tunnel.GetConfiguration(ctx, "acct-1", mustGetTunnelID(t, f.c, ctx, f.ns, tunnelName))
		if err != nil {
			return false
		}
		for _, e := range cfg.Config.Ingress {
			if e.Hostname == "app.example.com" &&
				e.OriginRequest != nil &&
				e.OriginRequest.OriginServerName != nil &&
				*e.OriginRequest.OriginServerName == "spec.example.com" {
				return true
			}
		}
		return false
	}, 20*time.Second, 250*time.Millisecond,
		"mock PUT payload must carry OriginServerName=spec.example.com (Spec.Routing.OriginRequest fallback)")

	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &got); err != nil {
			return false
		}
		for _, snap := range got.Status.ObservedIngress {
			if snap.Hostname == "app.example.com" &&
				snap.OriginRequest != nil &&
				snap.OriginRequest.OriginServerName != nil &&
				*snap.OriginRequest.OriginServerName == "spec.example.com" {
				return true
			}
		}
		return false
	}, 20*time.Second, 250*time.Millisecond,
		"Status.ObservedIngress must carry OriginServerName=spec.example.com (Spec.Routing.OriginRequest fallback)")
}

// testEnvtest_OriginRequest_WipePath covers OR-T15 case 4:
// when the live Cloudflare config carries an OriginRequest on an ingress entry
// but no current annotation or spec sets it, the operator wipes it and emits
// an OriginRequestWiped Warning event.
//
// Scenario:
//  1. Create Gateway + HTTPRoute (no origin annotations) → tunnel settles with
//     ObservedIngress populated (baseline PUT, no OriginRequest).
//  2. Out-of-band: SeedConfig on the mock to inject an OriginRequest onto the
//     live entry for the route's hostname (simulating a dashboard edit).
//  3. Trigger re-reconcile via annotation touch.
//  4. Assert: (a) OriginRequestWiped Warning event emitted on the tunnel CR.
//             (b) Mock's live config no longer has OriginRequest on that entry.
//             (c) Status.ObservedIngress carries nil OriginRequest on that entry.
func testEnvtest_OriginRequest_WipePath(t *testing.T) {
	t.Helper()
	f := setupHTTPRouteEnv(t)
	ctx := context.Background()

	zone := minimalZone("example-com", f.ns)
	require.NoError(t, f.c.Create(ctx, zone))

	gwSvc := minimalService("gw-svc", f.ns, 80)
	require.NoError(t, f.c.Create(ctx, gwSvc))

	gwHostname := gwv1.Hostname("ext.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: f.ns + "/gw-svc",
				// No origin-server-name.
			},
		},
		Spec: gwv1.GatewaySpec{
			GatewayClassName: "any-class",
			Listeners: []gwv1.Listener{{
				Name: "h", Hostname: &gwHostname, Port: 80, Protocol: gwv1.HTTPProtocolType,
			}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gw))

	tunnelName := "cf-" + f.ns + "-edge"
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &tn); err != nil {
			return false
		}
		return tn.Status.TunnelCNAME != ""
	}, 30*time.Second, 250*time.Millisecond, "tunnel %q must reach Status.TunnelCNAME", tunnelName)

	nsRef := gwv1.Namespace(f.ns)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationZoneRef: "example-com",
			},
		},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"app.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: &nsRef}},
			},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	require.NoError(t, f.c.Create(ctx, rt))

	// Wait for the first reconcile to settle: ObservedIngress must be
	// populated (non-nil, non-empty) so the drift-check guard passes on the
	// next reconcile. Without this the emitOriginRequestWipedEvents call is
	// a no-op (live is only fetched when ObservedIngress > 0).
	var tunnelID string
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &tn); err != nil {
			return false
		}
		if tn.Status.TunnelID == "" || len(tn.Status.ObservedIngress) == 0 {
			return false
		}
		tunnelID = tn.Status.TunnelID
		return true
	}, 30*time.Second, 250*time.Millisecond, "tunnel must settle with ObservedIngress populated")

	// Nudge the tunnel once to pick up the rt (first route) contribution from
	// the cache. Wait for app.example.com to appear in ObservedIngress — this
	// is the "settled with route" baseline we need before injecting out-of-band
	// OriginRequest. Without this, the SeedConfig below would not find
	// app.example.com in the live config, so the injection would be a no-op.
	var firstTn v2alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &firstTn))
	nudgeTunnel(t, f.c, ctx, &firstTn)

	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &tn); err != nil {
			return false
		}
		for _, snap := range tn.Status.ObservedIngress {
			if snap.Hostname == "app.example.com" {
				return true
			}
		}
		return false
	}, 20*time.Second, 250*time.Millisecond, "ObservedIngress must include app.example.com after first route settled")

	// Out-of-band: inject an OriginRequest onto the live Cloudflare config for
	// the route's hostname (simulating a dashboard or external-tool edit).
	// At this point the operator's live config (from the nudge PUT) includes
	// app.example.com — we mutate that entry to add OriginRequest.
	liveCfg, err := f.mock.Tunnel.GetConfiguration(ctx, "acct-1", tunnelID)
	require.NoError(t, err)

	osn := "injected.example.com"
	seededIngress := make([]cloudflare.IngressEntry, len(liveCfg.Config.Ingress))
	copy(seededIngress, liveCfg.Config.Ingress)
	injected := false
	for i, e := range seededIngress {
		if e.Hostname == "app.example.com" {
			seededIngress[i].OriginRequest = &cloudflare.IngressOriginRequest{
				OriginServerName: &osn,
			}
			injected = true
		}
	}
	require.True(t, injected, "app.example.com must be in live config before seeding OriginRequest")
	f.mock.Tunnel.SeedConfig(tunnelID, cloudflare.TunnelConfig{Ingress: seededIngress})

	// Add a second HTTPRoute to force a snapshot diff that makes the
	// reconciler advance past the reflect.DeepEqual early-return and
	// actually call emitOriginRequestWipedEvents + PutConfiguration.
	// (The wipe-event function runs only when wantSnap != ObservedIngress;
	// adding a new hostname guarantees the counts differ.)
	rt2 := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r2", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationZoneRef: "example-com",
			},
		},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"app2.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: &nsRef}},
			},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	require.NoError(t, f.c.Create(ctx, rt2))

	// Wait for the HTTPRoute source reconciler to emit the chain CNAME for
	// app2.example.com. That DNS record is emitted at the end of the source
	// reconcile pass — once it exists in the apiserver, the cache write for
	// app2's contribution has already happened.
	require.Eventually(t, func() bool {
		var list v2alpha1.CloudflareDNSRecordList
		if err := f.c.List(ctx, &list, client.InNamespace(f.ns)); err != nil {
			return false
		}
		for _, dr := range list.Items {
			if dr.Spec.Type == "CNAME" && dr.Spec.Name == "app2.example.com" {
				return true
			}
		}
		return false
	}, 30*time.Second, 250*time.Millisecond,
		"chain DNSRecord for app2.example.com must exist (confirms HTTPRoute source cache write completed)")

	// (a) OriginRequestWiped Warning event must appear on the tunnel CR.
	//
	// Once app2's contribution is in the cache, wantSnap (n+1 entries) !=
	// ObservedIngress (n entries) → the next tunnel reconcile enters the PUT
	// path, fetches live (which has the injected OriginRequest on
	// app.example.com), and emits OriginRequestWiped before PUTting clean config.
	//
	// The tunnel reconciler may not see the cache update until nudged; we nudge
	// inside the Eventually loop so that if a nudge fires before the cache write
	// is visible to the reconciler, the next nudge iteration catches it.
	nudgeCtr := 0
	require.Eventually(t, func() bool {
		// Periodically re-nudge the tunnel so the reconciler picks up the
		// updated cache (app2 contribution) and enters the PUT path. The first
		// nudge may race the cache write; subsequent nudges guarantee the
		// reconciler sees the diff.
		if nudgeCtr%4 == 0 { // nudge every ~1s (4 * 250ms polls)
			var latest v2alpha1.CloudflareTunnel
			if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &latest); err == nil {
				if latest.Annotations == nil {
					latest.Annotations = map[string]string{}
				}
				latest.Annotations["test.cloudflare.io/nudge"] = fmt.Sprintf("%d", nudgeCtr)
				_ = f.c.Update(ctx, &latest) // best-effort; conflict → retry next poll
			}
		}
		nudgeCtr++

		var evList corev1.EventList
		if err := f.c.List(ctx, &evList, client.InNamespace(f.ns)); err != nil {
			return false
		}
		for _, ev := range evList.Items {
			if ev.Reason == conventions.ReasonOriginRequestWiped &&
				ev.Type == corev1.EventTypeWarning &&
				ev.InvolvedObject.Name == tunnelName {
				return true
			}
		}
		return false
	}, 30*time.Second, 250*time.Millisecond,
		"OriginRequestWiped Warning event must be emitted on tunnel %q", tunnelName)

	// (b) Mock's live config no longer carries OriginRequest on app.example.com.
	require.Eventually(t, func() bool {
		cfg, err := f.mock.Tunnel.GetConfiguration(ctx, "acct-1", tunnelID)
		if err != nil {
			return false
		}
		for _, e := range cfg.Config.Ingress {
			if e.Hostname == "app.example.com" {
				// OriginRequest must be absent (nil) after the wipe PUT.
				return e.OriginRequest == nil
			}
		}
		return false
	}, 20*time.Second, 250*time.Millisecond,
		"mock live config must have nil OriginRequest for app.example.com after wipe PUT")

	// (c) Status.ObservedIngress no longer carries OriginRequest on that entry.
	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &got); err != nil {
			return false
		}
		for _, snap := range got.Status.ObservedIngress {
			if snap.Hostname == "app.example.com" {
				return snap.OriginRequest == nil
			}
		}
		return false
	}, 20*time.Second, 250*time.Millisecond,
		"Status.ObservedIngress must have nil OriginRequest for app.example.com after wipe")
}

// --- shared fixture helpers --------------------------------------------------

// minimalZone returns a bare CloudflareZone CR with a valid spec. Required by
// DNSRecord CEL admission (has(zoneRef) || has(zoneID)) for emitted records.
func minimalZone(name, ns string) *v2alpha1.CloudflareZone {
	return &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
}

// minimalService returns a ClusterIP Service with a single port. Used as the
// backing Service for tunnel-targeted Gateways.
func minimalService(name, ns string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: port}},
		},
	}
}

// nudgeTunnel annotates the tunnel CR to trigger an extra reconcile without
// changing any spec. The annotation key is test-scoped.
func nudgeTunnel(t *testing.T, c client.Client, ctx context.Context, tn *v2alpha1.CloudflareTunnel) {
	t.Helper()
	// Re-fetch to avoid stale-resource-version errors.
	var latest v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: tn.Namespace, Name: tn.Name}, &latest))
	if latest.Annotations == nil {
		latest.Annotations = map[string]string{}
	}
	latest.Annotations["test.cloudflare.io/nudge"] = "1"
	require.NoError(t, c.Update(ctx, &latest))
}

// mustGetTunnelID returns the Cloudflare-assigned TunnelID from the tunnel CR's
// status. Fails the test if the ID is not yet populated.
func mustGetTunnelID(t *testing.T, c client.Client, ctx context.Context, ns, name string) string {
	t.Helper()
	var tn v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &tn))
	require.NotEmpty(t, tn.Status.TunnelID, "TunnelID must be populated")
	return tn.Status.TunnelID
}
