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

// Package tunnel wires the five tunnel-bundle reconcilers (CloudflareTunnel
// plus Service / Gateway / HTTPRoute / TLSRoute source controllers) into a
// controller-runtime manager. AddToManager mirrors the shape of the zone
// bundle's setup: factories for per-reconcile credential resolution, a single
// shared tunnelsynth.Cache, a single shared EventRecorder, and the manager
// itself drives leader-election + signal-handling.
package tunnel

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// Options carries per-process configuration for the tunnel bundle.
//
// Per-reconcile credentials are resolved via reconcile.LoadCredentialsHierarchical
// inside each reconciler — no static creds are held here. TunnelClientFn is the
// factory the tunnel reconciler uses to construct a Cloudflare TunnelClient for
// the credentials it just resolved; production wires NewClient → NewTunnelClientFromCF,
// tests inject an in-memory mock.
type Options struct {
	// DefaultImage overrides the compile-time pinned cloudflared image. Empty
	// means use tunnel.DefaultCloudflaredImage.
	DefaultImage string

	// TunnelClientFn builds a TunnelClient from resolved credentials. Empty
	// means use the production factory (cloudflare.NewClient → NewTunnelClientFromCF).
	TunnelClientFn func(cloudflare.Credentials) (cloudflare.TunnelClient, error)

	// DefaultConnector is the ConnectorSpec seeded into newly-created tunnel
	// CRs by the source reconcilers. Empty fields fall back to internal
	// defaults (Replicas=2, Protocol="auto", LogLevel="info", GracePeriod=30s).
	DefaultConnector v2alpha1.ConnectorSpec
}

// tlsRouteSupported reports whether the gateway-api TLSRoute kind is served
// by the cluster (it lives only in the gateway-api EXPERIMENTAL channel).
// (false, nil) => CRD genuinely absent (degrade gracefully). (false, err)
// => discovery failed (caller should fail fast rather than silently drop
// TLSRoute support on a transient blip).
func tlsRouteSupported(rm meta.RESTMapper) (bool, error) {
	_, err := rm.RESTMapping(
		schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "TLSRoute"},
		"v1alpha2",
	)
	if err == nil {
		return true, nil
	}
	if meta.IsNoMatchError(err) {
		return false, nil
	}
	return false, err
}

// applyOptionDefaults fills in zero-value fields of opts with production
// defaults. It is called at the top of AddToManager and is a separate function
// to make the defaulting logic unit-testable without a controller-runtime manager.
func applyOptionDefaults(opts *Options) {
	if opts.DefaultImage == "" {
		opts.DefaultImage = DefaultCloudflaredImage
	}
	if opts.TunnelClientFn == nil {
		opts.TunnelClientFn = func(creds cloudflare.Credentials) (cloudflare.TunnelClient, error) {
			cli, err := cloudflare.NewClient(creds)
			if err != nil {
				return nil, err
			}
			return cloudflare.NewTunnelClientFromCF(cli.CF()), nil
		}
	}
	if opts.DefaultConnector.Replicas == 0 {
		opts.DefaultConnector.Replicas = 2
	}
	if opts.DefaultConnector.Protocol == "" {
		opts.DefaultConnector.Protocol = "auto"
	}
	if opts.DefaultConnector.LogLevel == "" {
		opts.DefaultConnector.LogLevel = "info"
	}
	if opts.DefaultConnector.GracePeriodSeconds == 0 {
		opts.DefaultConnector.GracePeriodSeconds = 30
	}
}

// AddToManager registers all five tunnel-bundle reconcilers with mgr.
// Caller is responsible for leader-election and signal-handling — the
// controller-runtime manager wires those.
func AddToManager(mgr ctrl.Manager, opts Options) error {
	scheme := mgr.GetScheme()
	c := mgr.GetClient()
	rec := mgr.GetEventRecorderFor("cloudflare-operator-tunnel")

	applyOptionDefaults(&opts)

	tlsOK, err := tlsRouteSupported(mgr.GetRESTMapper())
	if err != nil {
		return fmt.Errorf("probe TLSRoute CRD support: %w", err)
	}
	if !tlsOK {
		ctrl.Log.WithName("tunnel").Info(
			"gateway-api TLSRoute CRD absent (experimental channel not installed) — " +
				"skipping TLSRoute source; HTTPRoute/Service sources unaffected")
	}

	// Field indexer so gatewayToHTTPRoutes can List HTTPRoutes by parent
	// gateway instead of scanning the cluster-wide route cache. See A1. The
	// mirror TLSRoute indexer is registered below, gated on tlsOK.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &gwv1.HTTPRoute{}, IndexKeyRouteByGatewayParent,
		indexHTTPRouteByGatewayParent,
	); err != nil {
		return fmt.Errorf("register HTTPRoute parent-gateway index: %w", err)
	}
	if tlsOK {
		// Mirror of the HTTPRoute indexer above so gatewayToTLSRoutes can
		// List TLSRoutes by parent gateway. See A1.
		if err := mgr.GetFieldIndexer().IndexField(
			context.Background(), &gwv1a2.TLSRoute{}, IndexKeyRouteByGatewayParent,
			indexTLSRouteByGatewayParent,
		); err != nil {
			return fmt.Errorf("register TLSRoute parent-gateway index: %w", err)
		}
	}

	// Shared cache across the source reconcilers (writers) and the tunnel
	// reconciler (reader).
	cache := tunnelsynth.NewCache()

	// --- CloudflareTunnel reconciler ----------------------------------------
	tunnelR := &CloudflareTunnelReconciler{
		Client:         c,
		Scheme:         scheme,
		Recorder:       rec,
		TunnelClientFn: opts.TunnelClientFn,
		Cache:          cache,
		DefaultImage:   opts.DefaultImage,
	}
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&v2alpha1.CloudflareTunnel{}).
		Complete(tunnelR); err != nil {
		return fmt.Errorf("setup CloudflareTunnel: %w", err)
	}

	// --- ServiceSource reconciler -------------------------------------------
	// Watches:
	//   - Services (primary)
	//   - CloudflareTunnel (so a TunnelCNAME populating re-triggers attached
	//     sources whose DNSRecord emission was deferred).
	svcR := &ServiceSourceReconciler{
		Client:           c,
		Scheme:           scheme,
		Cache:            cache,
		Recorder:         rec,
		DefaultConnector: opts.DefaultConnector,
	}
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("service-source").
		For(&corev1.Service{}).
		Owns(&v2alpha1.CloudflareDNSRecord{}).
		Watches(&v2alpha1.CloudflareTunnel{}, handler.EnqueueRequestsFromMapFunc(tunnelToServices(mgr))).
		Complete(svcR); err != nil {
		return fmt.Errorf("setup ServiceSource: %w", err)
	}

	// --- GatewaySource reconciler -------------------------------------------
	// Watches:
	//   - Gateway (primary)
	//   - CloudflareTunnel (deferred-emission retrigger)
	//
	// Per design §4.2: Gateway emits its own contributions from its own
	// listeners; it does NOT watch attached HTTPRoute / TLSRoute (those would
	// be re-enqueue noise, not state changes).
	gwR := &GatewaySourceReconciler{
		Client:           c,
		Scheme:           scheme,
		Cache:            cache,
		Recorder:         rec,
		DefaultConnector: opts.DefaultConnector,
	}
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("gateway-source").
		For(&gwv1.Gateway{}).
		Owns(&v2alpha1.CloudflareDNSRecord{}).
		Watches(&v2alpha1.CloudflareTunnel{}, handler.EnqueueRequestsFromMapFunc(tunnelToGateways(mgr))).
		Complete(gwR); err != nil {
		return fmt.Errorf("setup GatewaySource: %w", err)
	}

	// --- HTTPRouteSource reconciler -----------------------------------------
	// Watches:
	//   - HTTPRoute (primary)
	//   - Gateway (annotation/listener change → re-reconcile attached routes)
	//   - CloudflareTunnel (deferred-emission retrigger)
	httpR := &HTTPRouteSourceReconciler{
		Client:   c,
		Scheme:   scheme,
		Cache:    cache,
		Recorder: rec,
	}
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("httproute-source").
		For(&gwv1.HTTPRoute{}).
		Owns(&v2alpha1.CloudflareDNSRecord{}).
		Watches(&gwv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(gatewayToHTTPRoutes(mgr))).
		Watches(&v2alpha1.CloudflareTunnel{}, handler.EnqueueRequestsFromMapFunc(tunnelToHTTPRoutes(mgr))).
		Complete(httpR); err != nil {
		return fmt.Errorf("setup HTTPRouteSource: %w", err)
	}

	// --- TLSRouteSource reconciler ------------------------------------------
	// Watches:
	//   - TLSRoute (primary)
	//   - Gateway (annotation/listener change → re-reconcile attached routes)
	//   - CloudflareTunnel (deferred-emission retrigger)
	if tlsOK {
		tlsR := &TLSRouteSourceReconciler{
			Client:   c,
			Scheme:   scheme,
			Cache:    cache,
			Recorder: rec,
		}
		if err := ctrl.NewControllerManagedBy(mgr).
			Named("tlsroute-source").
			For(&gwv1a2.TLSRoute{}).
			Owns(&v2alpha1.CloudflareDNSRecord{}).
			Watches(&gwv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(gatewayToTLSRoutes(mgr))).
			Watches(&v2alpha1.CloudflareTunnel{}, handler.EnqueueRequestsFromMapFunc(tunnelToTLSRoutes(mgr))).
			Complete(tlsR); err != nil {
			return fmt.Errorf("setup TLSRouteSource: %w", err)
		}
	}

	return nil
}

// gatewayToHTTPRoutes enqueues every HTTPRoute whose parentRefs include the
// changed Gateway. Uses the IndexKeyRouteByGatewayParent field indexer so the
// cache returns only matching routes instead of scanning cluster-wide. See A1.
func gatewayToHTTPRoutes(mgr manager.Manager) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		gw, ok := obj.(*gwv1.Gateway)
		if !ok {
			return nil
		}
		var routes gwv1.HTTPRouteList
		if err := mgr.GetClient().List(ctx, &routes, client.MatchingFields{
			IndexKeyRouteByGatewayParent: (types.NamespacedName{Namespace: gw.Namespace, Name: gw.Name}).String(),
		}); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0, len(routes.Items))
		for _, rt := range routes.Items {
			out = append(out, reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: rt.Namespace, Name: rt.Name},
			})
		}
		return out
	}
}

// gatewayToTLSRoutes enqueues every TLSRoute whose parentRefs include the
// changed Gateway. Mirrors gatewayToHTTPRoutes but for v1alpha2.TLSRoute.
// Uses the IndexKeyRouteByGatewayParent field indexer. See A1.
func gatewayToTLSRoutes(mgr manager.Manager) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		gw, ok := obj.(*gwv1.Gateway)
		if !ok {
			return nil
		}
		var routes gwv1a2.TLSRouteList
		if err := mgr.GetClient().List(ctx, &routes, client.MatchingFields{
			IndexKeyRouteByGatewayParent: (types.NamespacedName{Namespace: gw.Namespace, Name: gw.Name}).String(),
		}); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0, len(routes.Items))
		for _, rt := range routes.Items {
			out = append(out, reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: rt.Namespace, Name: rt.Name},
			})
		}
		return out
	}
}

// tunnelToServices enqueues every Service in the tunnel's namespace annotated
// with cloudflare.io/tunnel=true. The reconciler filters non-attached or
// re-attached sources internally, so a slightly-broad enqueue is safe and
// cheap (Services in one namespace).
func tunnelToServices(mgr manager.Manager) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		tn, ok := obj.(*v2alpha1.CloudflareTunnel)
		if !ok {
			return nil
		}
		var svcs corev1.ServiceList
		if err := mgr.GetClient().List(ctx, &svcs, client.InNamespace(tn.Namespace)); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0)
		for _, s := range svcs.Items {
			if s.Annotations[conventions.AnnotationTunnel] == "true" {
				out = append(out, reconcile.Request{
					NamespacedName: types.NamespacedName{Namespace: s.Namespace, Name: s.Name},
				})
			}
		}
		return out
	}
}

// tunnelToGateways enqueues every Gateway in the tunnel's namespace annotated
// with cloudflare.io/tunnel=true.
func tunnelToGateways(mgr manager.Manager) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		tn, ok := obj.(*v2alpha1.CloudflareTunnel)
		if !ok {
			return nil
		}
		var gws gwv1.GatewayList
		if err := mgr.GetClient().List(ctx, &gws, client.InNamespace(tn.Namespace)); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0)
		for _, gw := range gws.Items {
			if gw.Annotations[conventions.AnnotationTunnel] == "true" {
				out = append(out, reconcile.Request{
					NamespacedName: types.NamespacedName{Namespace: gw.Namespace, Name: gw.Name},
				})
			}
		}
		return out
	}
}

// tunnelToHTTPRoutes enqueues every HTTPRoute in the tunnel's namespace
// annotated with cloudflare.io/tunnel=true.
func tunnelToHTTPRoutes(mgr manager.Manager) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		tn, ok := obj.(*v2alpha1.CloudflareTunnel)
		if !ok {
			return nil
		}
		var routes gwv1.HTTPRouteList
		if err := mgr.GetClient().List(ctx, &routes, client.InNamespace(tn.Namespace)); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0)
		for _, rt := range routes.Items {
			if rt.Annotations[conventions.AnnotationTunnel] == "true" {
				out = append(out, reconcile.Request{
					NamespacedName: types.NamespacedName{Namespace: rt.Namespace, Name: rt.Name},
				})
			}
		}
		return out
	}
}

// tunnelToTLSRoutes enqueues every TLSRoute in the tunnel's namespace
// annotated with cloudflare.io/tunnel=true.
func tunnelToTLSRoutes(mgr manager.Manager) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		tn, ok := obj.(*v2alpha1.CloudflareTunnel)
		if !ok {
			return nil
		}
		var routes gwv1a2.TLSRouteList
		if err := mgr.GetClient().List(ctx, &routes, client.InNamespace(tn.Namespace)); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0)
		for _, rt := range routes.Items {
			if rt.Annotations[conventions.AnnotationTunnel] == "true" {
				out = append(out, reconcile.Request{
					NamespacedName: types.NamespacedName{Namespace: rt.Namespace, Name: rt.Name},
				})
			}
		}
		return out
	}
}
