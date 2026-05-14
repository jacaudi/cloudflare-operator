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
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// GatewaySourceReconciler watches Gateways with cloudflare.io/tunnel="true"
// and implements the Gateway-as-tunnel-apex pattern (design §4.2).
//
// Each listener with a hostname becomes a tunnel-apex hostname:
//   - one IngressContribution routing the hostname to the Gateway's underlying
//     Service (resolved via the REQUIRED cloudflare.io/gateway-service
//     annotation — "<ns>/<name>" or "<ns>/<name>:<port>"),
//   - one CloudflareDNSRecord (CNAME → tunnel CNAME) per hostname.
//
// Listener-protocol filter:
//   - HTTP / HTTPS — synthesized here.
//   - TLS — owned by the TLSRoute reconciler; skipped silently here so the
//     TLSRoute controller can build its own contribution under the same
//     tunnel-key without conflict.
//   - TCP / UDP — rejected with an UnsupportedProtocol Warning event.
//
// No label-based fallback for Service discovery. Every Gateway controller
// (Envoy Gateway, Contour, Cilium, Istio) exposes its listener Service under
// a different convention — explicit annotation is the only reliable contract.
//
// Stale-key sweep: when a Gateway's tunnel-name annotation changes between
// reconciles, the cache entry under the prior tunnel-key would otherwise be
// orphaned. We track the last attached tunnel-key per source in an in-memory
// map and clear the prior key whenever the new key differs. The map is
// mutex-guarded because controller-runtime may call Reconcile concurrently.
//
// TODO: when the third source reconciler lands (T12 HTTPRoute or T13 TLSRoute),
// extract this mu+lastAttached pattern into a shared cacheTracker type in
// attach.go. With three call sites the refactor pays off; with two it's
// premature (matches the Phase-2 reconcile.HaltDependency extraction
// precedent — extract on the third use).
type GatewaySourceReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Cache            *tunnelsynth.Cache
	Recorder         record.EventRecorder
	DefaultConnector v1alpha1.ConnectorSpec

	mu           sync.Mutex
	lastAttached map[tunnelsynth.SourceKey]tunnelsynth.TunnelKey
}

// +kubebuilder:rbac:groups="gateway.networking.k8s.io",resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflaretunnels,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarednsrecords,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives one iteration of the Gateway-source state machine.
func (r *GatewaySourceReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("gateway", req.NamespacedName)

	var gw gwv1.Gateway
	if err := r.Get(ctx, req.NamespacedName, &gw); err != nil {
		if apierrors.IsNotFound(err) {
			// Gateway deleted — sweep the prior tunnel-key for this source.
			srcKey := tunnelsynth.SourceKey{Kind: "Gateway", Namespace: req.Namespace, Name: req.Name}
			r.sweepPriorKey(srcKey)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	srcKey := tunnelsynth.SourceKey{Kind: "Gateway", Namespace: gw.Namespace, Name: gw.Name}

	// Opt-in gate.
	enabled, _ := conventions.ParseTruthy(gw.Annotations[conventions.AnnotationTunnel])
	if !enabled {
		// Sweep every tunnel-key that might have a stale entry for this
		// source: the previously-tracked key, plus the two derivable-from-
		// current-annotations candidates (pool + named). Mirrors the
		// ServiceSourceReconciler opt-out path.
		r.sweepPriorKey(srcKey)
		r.Cache.Clear(tunnelsynth.TunnelKey{Namespace: gw.Namespace, Name: "cf-" + gw.Namespace}, srcKey)
		if tn := gw.Annotations[conventions.AnnotationTunnelName]; tn != "" {
			r.Cache.Clear(tunnelsynth.TunnelKey{Namespace: gw.Namespace, Name: "cf-" + gw.Namespace + "-" + tn}, srcKey)
		}
		return reconcile.Result{}, nil
	}

	// Hostname gate: at least one listener must have a hostname. Otherwise
	// the Gateway-as-tunnel-apex pattern has nothing to publish.
	hostnames := listenerHostnames(&gw)
	if len(hostnames) == 0 {
		if r.Recorder != nil {
			r.Recorder.Eventf(&gw, corev1.EventTypeWarning, conventions.ReasonNoListenerHostname,
				"Gateway has no listener with a hostname; tunnel-apex synthesis requires at least one")
		}
		r.sweepPriorKey(srcKey)
		return reconcile.Result{}, nil
	}

	// Derive target tunnel name. Stable failures (NameTooLong, InvalidName)
	// surfaced via Event with nil error return — not retryable without the
	// user editing the annotation.
	derived, err := DeriveTunnelName(gw.Namespace, gw.Annotations[conventions.AnnotationTunnelName])
	if err != nil {
		reason := conventions.ReasonInvalidName
		if errors.Is(err, ErrNameTooLong) {
			reason = conventions.ReasonNameTooLong
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(&gw, corev1.EventTypeWarning, reason, "%v", err)
		}
		r.sweepPriorKey(srcKey)
		return reconcile.Result{}, nil
	}

	// Resolve the Gateway's underlying Service BEFORE EnsureTunnelCR — if the
	// annotation is missing or the Service can't be found, we want to surface
	// the failure without creating a CloudflareTunnel that ends up orphaned.
	gwSvc, port, err := r.resolveGatewayService(ctx, &gw)
	if err != nil {
		reason := conventions.ReasonGatewayServiceUnspecified
		if !errors.Is(err, errGatewayServiceAnnotationMissing) {
			// Annotation present but Service Get / parse failed. Use a
			// distinct reason for observability.
			reason = "GatewayServiceUnresolved"
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(&gw, corev1.EventTypeWarning, reason, "%v", err)
		}
		r.sweepPriorKey(srcKey)
		return reconcile.Result{}, nil
	}

	tn, err := EnsureTunnelCR(ctx, r.Client, r.Scheme, &gw, "Gateway", derived, r.DefaultConnector)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("ensure tunnel: %w", err)
	}

	// Build per-listener contributions. HTTP/HTTPS only; TLS is owned by the
	// TLSRoute reconciler; TCP/UDP are rejected with an Event.
	contribs := make([]tunnelsynth.IngressContribution, 0, len(gw.Spec.Listeners))
	for _, l := range gw.Spec.Listeners {
		if l.Hostname == nil || *l.Hostname == "" {
			continue
		}
		switch l.Protocol {
		case gwv1.HTTPProtocolType, gwv1.HTTPSProtocolType:
			scheme := "http"
			if l.Protocol == gwv1.HTTPSProtocolType {
				scheme = "https"
			}
			// Service URL uses the IN-CLUSTER Service port (from annotation
			// or first Service port), NOT the listener's public-facing port.
			contribs = append(contribs, tunnelsynth.IngressContribution{
				Hostname: string(*l.Hostname),
				Service:  fmt.Sprintf("%s://%s.%s.svc.cluster.local:%d", scheme, gwSvc.Name, gwSvc.Namespace, port),
			})
		case gwv1.TLSProtocolType:
			// Owned by the TLSRoute reconciler; skip here so it can build a
			// separate contribution under the same tunnel-key without clash.
		default:
			if r.Recorder != nil {
				r.Recorder.Eventf(&gw, corev1.EventTypeWarning, "UnsupportedProtocol",
					"listener %q protocol %s not supported on tunnel-apex Gateway", l.Name, l.Protocol)
			}
		}
	}

	tunnelKey := tunnelsynth.TunnelKey{Namespace: tn.Namespace, Name: tn.Name}

	// Annotation-change sweep: clear the prior tunnel-key if it differs.
	r.swapAttachedKey(srcKey, tunnelKey)
	// Register this source under the new key, even if contribs is empty
	// (e.g. all listeners are TLS). The empty registration keeps the
	// per-source bookkeeping symmetric — subsequent sweeps remain a no-op
	// rather than leaving phantom contributions on reconcile thrash.
	r.Cache.Set(tunnelKey, srcKey, contribs)

	// Guard: defer DNSRecord emission until Status.TunnelCNAME populates.
	// The Watches hook (T14) retriggers this reconciler on the tunnel CR's
	// status update so we get a second pass without busy-waiting.
	if tn.Status.TunnelCNAME == "" {
		logger.V(1).Info("tunnel CNAME not yet populated; deferring DNSRecord emission",
			"tunnel", tunnelKey)
		return reconcile.Result{}, nil
	}

	// Emit one CloudflareDNSRecord (CNAME → tunnel CNAME) per listener hostname.
	for _, h := range hostnames {
		if err := r.emitDNSRecord(ctx, &gw, h, tn); err != nil {
			return reconcile.Result{}, fmt.Errorf("emit dns record for %q: %w", h, err)
		}
	}

	// No cross-controller Status write: the tunnel reconciler reads
	// Cache.AttachedSources on its own loop and writes
	// tn.Status.AttachedSources from there. A status write from this
	// controller would race with that loop.

	return reconcile.Result{}, nil
}

// swapAttachedKey records the new tunnel-key for this source and clears any
// prior key that differs. Thread-safe.
func (r *GatewaySourceReconciler) swapAttachedKey(src tunnelsynth.SourceKey, newKey tunnelsynth.TunnelKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastAttached == nil {
		r.lastAttached = map[tunnelsynth.SourceKey]tunnelsynth.TunnelKey{}
	}
	if prev, ok := r.lastAttached[src]; ok && prev != newKey {
		r.Cache.Clear(prev, src)
	}
	r.lastAttached[src] = newKey
}

// sweepPriorKey clears the prior tunnel-key tracked for this source (if any)
// and forgets it. Thread-safe.
func (r *GatewaySourceReconciler) sweepPriorKey(src tunnelsynth.SourceKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastAttached == nil {
		return
	}
	if prev, ok := r.lastAttached[src]; ok {
		r.Cache.Clear(prev, src)
		delete(r.lastAttached, src)
	}
}

// listenerHostnames returns the non-empty hostnames of all Gateway listeners.
// Used both for the "no-hostname" gate and to drive DNSRecord emission. A
// listener whose protocol is HTTP/HTTPS but lacks a hostname still does not
// contribute — the contribution loop has its own per-listener nil check.
func listenerHostnames(gw *gwv1.Gateway) []string {
	out := make([]string, 0, len(gw.Spec.Listeners))
	for _, l := range gw.Spec.Listeners {
		if l.Hostname != nil && *l.Hostname != "" {
			out = append(out, string(*l.Hostname))
		}
	}
	return out
}

// errGatewayServiceAnnotationMissing distinguishes "annotation absent"
// (GatewayServiceUnspecified) from "annotation present but Service can't be
// resolved" (GatewayServiceUnresolved). Internal to this file.
var errGatewayServiceAnnotationMissing = errors.New("cloudflare.io/gateway-service annotation required when cloudflare.io/tunnel is set on a Gateway")

// resolveGatewayService reads the REQUIRED cloudflare.io/gateway-service
// annotation: "<namespace>/<name>" or "<namespace>/<name>:<port>" (or
// "<name>" / "<name>:<port>" with the Gateway's namespace as the default).
//
// Required (not optional) because every Gateway implementation exposes its
// listener Service differently — no reliable label convention exists. If
// absent, surface GatewayServiceUnspecified on the Gateway and refuse
// synthesis.
//
// Returns the resolved Service plus the chosen port. When the annotation
// omits the port, falls back to Service.Spec.Ports[0].Port — this is the
// IN-CLUSTER port cloudflared connects to, NOT the listener's public-facing
// port. They are routinely different (e.g. listener 443 → Service 8443).
func (r *GatewaySourceReconciler) resolveGatewayService(ctx context.Context, gw *gwv1.Gateway) (*corev1.Service, int32, error) {
	raw := gw.Annotations[conventions.AnnotationGatewayService]
	if raw == "" {
		return nil, 0, errGatewayServiceAnnotationMissing
	}
	ns, name, port, err := parseGatewayServiceRef(raw, gw.Namespace)
	if err != nil {
		return nil, 0, err
	}
	var svc corev1.Service
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &svc); err != nil {
		return nil, 0, fmt.Errorf("get Gateway service %s/%s: %w", ns, name, err)
	}
	if port == 0 {
		// Annotation didn't specify a port — fall back to the Service's
		// first port. A Service with no ports is treated as a configuration
		// error (we can't synthesize a tunnel URL without a port).
		if len(svc.Spec.Ports) == 0 {
			return nil, 0, fmt.Errorf("Service %s/%s has no ports; annotation must specify a port", ns, name)
		}
		port = svc.Spec.Ports[0].Port
	}
	return &svc, port, nil
}

// parseGatewayServiceRef parses the cloudflare.io/gateway-service annotation
// values:
//   - "<ns>/<name>"
//   - "<ns>/<name>:<port>"
//   - "<name>"            (uses defaultNS)
//   - "<name>:<port>"     (uses defaultNS)
//
// Returns port = 0 when omitted; the caller falls back to the Service's first
// port.
func parseGatewayServiceRef(raw, defaultNS string) (namespace, name string, port int32, err error) {
	hostPart, portPart, hasPort := strings.Cut(raw, ":")
	if hasPort {
		p, perr := strconv.Atoi(portPart)
		if perr != nil || p <= 0 || p > 65535 {
			return "", "", 0, fmt.Errorf("invalid port %q in cloudflare.io/gateway-service", portPart)
		}
		port = int32(p)
	}
	if ns, nm, ok := strings.Cut(hostPart, "/"); ok {
		if ns == "" || nm == "" {
			return "", "", 0, fmt.Errorf("malformed cloudflare.io/gateway-service %q (want '<ns>/<name>[:<port>]')", raw)
		}
		return ns, nm, port, nil
	}
	if hostPart == "" {
		return "", "", 0, fmt.Errorf("malformed cloudflare.io/gateway-service %q (empty)", raw)
	}
	return defaultNS, hostPart, port, nil
}

// emitDNSRecord creates (idempotently) a CloudflareDNSRecord CR for one
// Gateway listener hostname, owner-reffed to the Gateway and stamped with
// source labels. The CR name uses the same hash-suffixed scheme as the
// Service source reconciler (emittedDNSRecordName helper) so we never collide
// across alias hostnames or get truncated into a clash.
//
// Per spec 2 contract:
//   - spec.zoneRef.name (resolved by the zone reconciler) when
//     cloudflare.io/zone-ref is set on the Gateway — never spec.zoneID
//   - spec.type = CNAME
//   - spec.name = hostname
//   - spec.content = tunnel CNAME (caller guards non-empty)
//   - spec.adopt threaded from cloudflare.io/adopt
func (r *GatewaySourceReconciler) emitDNSRecord(ctx context.Context, gw *gwv1.Gateway, hostname string, tn *v1alpha1.CloudflareTunnel) error {
	content := tn.Status.TunnelCNAME // copy so we can take its address
	dr := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      emittedDNSRecordName(gw.Name, hostname),
			Namespace: gw.Namespace,
		},
		Spec: v1alpha1.CloudflareDNSRecordSpec{
			Type:    "CNAME",
			Name:    hostname,
			Content: &content,
		},
	}
	reconcilelib.StampSourceLabels(dr, "Gateway", gw.Name, gw.Namespace)
	if err := reconcilelib.SetControllerOwner(gw, dr, r.Scheme); err != nil {
		return err
	}
	if zr := gw.Annotations[conventions.AnnotationZoneRef]; zr != "" {
		dr.Spec.ZoneRef = &v1alpha1.ZoneReference{Name: zr, Namespace: gw.Namespace}
	}
	if adopt, _ := conventions.ParseTruthy(gw.Annotations[conventions.AnnotationAdopt]); adopt {
		dr.Spec.Adopt = true
	}
	if err := r.Create(ctx, dr); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

var _ reconcile.Reconciler = (*GatewaySourceReconciler)(nil)
