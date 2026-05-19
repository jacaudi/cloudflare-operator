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
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
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
//   - TLS — ingress contribution is owned by the TLSRoute reconciler
//     (skipped here so it can build its own under the same tunnel-key
//     without conflict); the Gateway still emits the apex CNAME → tunnel
//     CNAME so TLSRoute chains have a resolvable anchor.
//   - TCP / UDP — rejected with an UnsupportedProtocol Warning event.
//
// No label-based fallback for Service discovery. Every Gateway controller
// (Envoy Gateway, Contour, Cilium, Istio) exposes its listener Service under
// a different convention — explicit annotation is the only reliable contract.
//
// Stale-key sweep: when a Gateway's tunnel-name annotation changes between
// reconciles, the cache entry under the prior tunnel-key would otherwise be
// orphaned. The shared cacheTracker (attach.go) tracks the last attached
// tunnel-key per source so we can clear the prior key whenever the new key
// differs. Initialized once via ensureTracker, called at the top of every
// Reconcile and guarded by sync.Once so concurrent reconciles on the same
// instance (MaxConcurrentReconciles > 1) cannot race to allocate competing
// trackers — the first allocator wins and the rest see the same instance,
// preserving prior-attachment state.
type GatewaySourceReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Cache            *tunnelsynth.Cache
	Recorder         record.EventRecorder
	DefaultConnector v2alpha1.ConnectorSpec

	tracker     *cacheTracker
	trackerOnce sync.Once
	dedupe      *eventDedupe // D2 event dedupe; lazy-inited inside trackerOnce.
}

// ensureTracker initializes r.tracker exactly once. Safe against concurrent
// callers (controller-runtime worker pool with MaxConcurrentReconciles > 1).
// Idempotent: tests that pre-seed r.tracker keep their fixture untouched.
func (r *GatewaySourceReconciler) ensureTracker() {
	r.trackerOnce.Do(func() {
		if r.tracker == nil {
			r.tracker = newCacheTracker()
		}
		if r.dedupe == nil {
			r.dedupe = newEventDedupe(0, 0)
		}
	})
}

// +kubebuilder:rbac:groups="gateway.networking.k8s.io",resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflaretunnels,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarednsrecords,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives one iteration of the Gateway-source state machine.
func (r *GatewaySourceReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("gateway", req.NamespacedName)
	r.ensureTracker()

	var gw gwv1.Gateway
	if err := r.Get(ctx, req.NamespacedName, &gw); err != nil {
		if apierrors.IsNotFound(err) {
			// Gateway deleted — sweep the prior tunnel-key for this source.
			srcKey := tunnelsynth.SourceKey{Kind: "Gateway", Namespace: req.Namespace, Name: req.Name}
			if prev, ok := r.tracker.sweep(srcKey); ok {
				r.Cache.Clear(prev, srcKey)
			}
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
		if prev, ok := r.tracker.sweep(srcKey); ok {
			r.Cache.Clear(prev, srcKey)
		}
		// Post-restart the in-memory tracker is empty, so clear the derived
		// key directly. Use DeriveTunnelName — the single source of truth the
		// opt-in path uses — so the swept key can never silently diverge from
		// what opt-in wrote if the naming template changes. One call covers
		// both the no-annotation (cf-<ns>) and named (cf-<ns>-<tn>) forms; for
		// a name DeriveTunnelName rejects, the opt-in path also wrote nothing,
		// so skipping the Clear is correct.
		if k, derr := DeriveTunnelName(gw.Namespace, gw.Annotations[conventions.AnnotationTunnelName]); derr == nil {
			r.Cache.Clear(tunnelsynth.TunnelKey{Namespace: gw.Namespace, Name: k}, srcKey)
		}
		return reconcile.Result{}, nil
	}

	// Hostname gate: at least one listener must have a hostname. Otherwise
	// the Gateway-as-tunnel-apex pattern has nothing to publish.
	hostnames := listenerHostnames(&gw)
	if len(hostnames) == 0 {
		if r.Recorder != nil {
			r.dedupe.emit(r.Recorder, &gw, corev1.EventTypeWarning, conventions.ReasonNoListenerHostname,
				"Gateway has no listener with a hostname; tunnel-apex synthesis requires at least one")
		}
		if prev, ok := r.tracker.sweep(srcKey); ok {
			r.Cache.Clear(prev, srcKey)
		}
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
			r.dedupe.emit(r.Recorder, &gw, corev1.EventTypeWarning, reason, err.Error())
		}
		if prev, ok := r.tracker.sweep(srcKey); ok {
			r.Cache.Clear(prev, srcKey)
		}
		return reconcile.Result{}, nil
	}

	// Resolve the Gateway's underlying Service BEFORE EnsureTunnelCR — if the
	// annotation is missing or the Service can't be found, we want to surface
	// the failure without creating a CloudflareTunnel that ends up orphaned.
	gwSvc, port, err := resolveGatewayService(ctx, r.Client, &gw)
	if err != nil {
		reason := conventions.ReasonGatewayServiceUnspecified
		if !errors.Is(err, errGatewayServiceAnnotationMissing) {
			// Annotation present but Service Get / parse failed. Use a
			// distinct reason for observability.
			reason = conventions.ReasonGatewayServiceUnresolved
		}
		if r.Recorder != nil {
			r.dedupe.emit(r.Recorder, &gw, corev1.EventTypeWarning, reason, err.Error())
		}
		if prev, ok := r.tracker.sweep(srcKey); ok {
			r.Cache.Clear(prev, srcKey)
		}
		return reconcile.Result{}, nil
	}

	tn, err := EnsureTunnelCR(ctx, r.Client, r.Scheme, &gw, "Gateway", derived, r.DefaultConnector)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("ensure tunnel: %w", err)
	}

	// Build per-listener contributions. HTTP/HTTPS only; TLS is owned by the
	// TLSRoute reconciler; TCP/UDP are rejected with an Event.
	//
	// LOCKSTEP: the protocol filter applied in this loop (HTTP/HTTPS → contribs,
	// TLS → tlsApexHostnames, default → Event) MUST stay in sync with
	// publishableListenerHostnames in apex.go. Diverging the two causes
	// chainContentFor and this emission loop to disagree about which hostnames
	// are "published", silently breaking apex-CNAME emission or chain-record gating.
	contribs := make([]tunnelsynth.IngressContribution, 0, len(gw.Spec.Listeners))
	tlsApexHostnames := make([]string, 0, len(gw.Spec.Listeners))
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
			// Ingress contribution is owned by the TLSRoute reconciler (it
			// builds the route entry under the same tunnel-key). But the TLS
			// listener hostname is the tunnel apex: TLSRoutes CNAME their
			// route hostnames to it, and nothing else emits apex->tunnel, so
			// the Gateway must still publish its CNAME -> tunnel record.
			tlsApexHostnames = append(tlsApexHostnames, string(*l.Hostname))
		default:
			if r.Recorder != nil {
				r.dedupe.emit(r.Recorder, &gw, corev1.EventTypeWarning, conventions.ReasonUnsupportedProtocol,
					fmt.Sprintf("listener %q protocol %s not supported on tunnel-apex Gateway", l.Name, l.Protocol))
			}
		}
	}

	tunnelKey := tunnelsynth.TunnelKey{Namespace: tn.Namespace, Name: tn.Name}

	// Annotation-change sweep: clear the prior tunnel-key if it differs.
	if prev, ok := r.tracker.swap(srcKey, tunnelKey); ok {
		r.Cache.Clear(prev, srcKey)
	}
	// Register this source under the new key, even if contribs is empty
	// (e.g. all listeners are TLS). The empty registration keeps the
	// per-source bookkeeping symmetric — subsequent sweeps remain a no-op
	// rather than leaving phantom contributions on reconcile thrash.
	r.Cache.Set(tunnelKey, srcKey, contribs)

	// Guard: defer DNSRecord emission until Status.TunnelCNAME populates.
	// The manager wires a Watch on the tunnel CR so its status update
	// retriggers this reconciler — a second pass without busy-waiting.
	if tn.Status.TunnelCNAME == "" {
		logger.V(1).Info("tunnel CNAME not yet populated; deferring DNSRecord emission",
			"tunnel", tunnelKey)
		return reconcile.Result{}, nil
	}

	// Emit one CloudflareDNSRecord (CNAME → tunnel CNAME) for every hostname
	// that routes through the tunnel: HTTP/HTTPS ingress contributions plus
	// TLS-listener apex hostnames. TCP/UDP (and otherwise unsupported)
	// listener hostnames are deliberately excluded — nothing routes them, so
	// a CNAME would resolve to a black hole (IMP-2).
	//
	// If a valid cloudflare.io/gateway-apex override is present, collapse DNS
	// emission to a single record for that apex hostname (CNAME → tunnel CNAME).
	// The ingress contribs (r.Cache.Set above) are unchanged — cloudflared still
	// routes the real listener wildcard hostnames. An invalid-but-present override
	// emits a Warning event and falls through to per-listener emission.
	apexHost, apexValid, apexPresent := gatewayApexOverride(&gw)
	if apexPresent && !apexValid && r.Recorder != nil {
		r.dedupe.emit(r.Recorder, &gw, corev1.EventTypeWarning,
			conventions.ReasonGatewayApexInvalid,
			fmt.Sprintf("cloudflare.io/gateway-apex %q is not a valid non-wildcard hostname; ignoring",
				gw.GetAnnotations()[conventions.AnnotationGatewayApex]))
	}

	desired := make(map[string]struct{}, len(contribs)+len(tlsApexHostnames))
	emitOne := func(h string) error {
		if _, seen := desired[h]; seen {
			return nil
		}
		desired[h] = struct{}{}
		if err := r.emitDNSRecord(ctx, &gw, h, tn); err != nil {
			return fmt.Errorf("emit dns record for %q: %w", h, err)
		}
		return nil
	}
	if apexValid {
		// Valid override: emit exactly one apex record → tunnel CNAME.
		// Stale per-listener records are pruned below by pruneOrphanedDNSRecords.
		if err := emitOne(apexHost); err != nil {
			return reconcile.Result{}, err
		}
	} else {
		for _, cont := range contribs {
			if err := emitOne(cont.Hostname); err != nil {
				return reconcile.Result{}, err
			}
		}
		for _, h := range tlsApexHostnames {
			if err := emitOne(h); err != nil {
				return reconcile.Result{}, err
			}
		}
	}

	// Prune previously-emitted DNSRecord CRs whose hostname is no longer in
	// the desired set. Best-effort: a prune error logs and continues — the
	// desired records are already emitted, and any surviving orphan is retried
	// on the next reconcile. Placed strictly AFTER the emit loop on the
	// post-emit path; never reached on the deferred-emission early-return
	// above (where desired would be empty and would wrongly delete live CRs).
	pruned, perr := pruneOrphanedDNSRecords(ctx, r.Client, "Gateway", gw.Name, gw.Namespace, desired)
	if perr != nil {
		logger.Error(perr, "orphan-prune failed (continuing)")
	} else if len(pruned) > 0 {
		r.dedupe.emit(r.Recorder, &gw, corev1.EventTypeNormal, conventions.ReasonOrphanedDNSRecordPruned,
			fmt.Sprintf("deleted %d orphaned DNSRecord CR(s) for hostnames no longer in spec", len(pruned)))
	}

	// No cross-controller Status write: the tunnel reconciler reads
	// Cache.AttachedSources on its own loop and writes
	// tn.Status.AttachedSources from there. A status write from this
	// controller would race with that loop.

	return reconcile.Result{}, nil
}

// listenerHostnames returns the non-empty hostnames of all Gateway listeners.
// Used by the no-hostname gate: a Gateway with no listener hostname has
// nothing to publish. It does NOT drive DNSRecord emission — emission is
// derived from the protocol-filtered contribs + TLS apex hostnames, so a
// hostname'd TCP/UDP listener never receives a black-hole CNAME.
func listenerHostnames(gw *gwv1.Gateway) []string {
	out := make([]string, 0, len(gw.Spec.Listeners))
	for _, l := range gw.Spec.Listeners {
		if l.Hostname != nil && *l.Hostname != "" {
			out = append(out, string(*l.Hostname))
		}
	}
	return out
}

// emitDNSRecord upserts the CloudflareDNSRecord CR for this Gateway +
// hostname pair via the shared SSA-based helper. Annotation drift
// (cloudflare.io/adopt, cloudflare.io/zone-ref) propagates to the emitted
// CR because EmitDNSRecord uses SSA.
//
// Operator-edits-win: a user `kubectl edit` on the emitted CR will be
// reverted on the next reconcile.
func (r *GatewaySourceReconciler) emitDNSRecord(ctx context.Context, gw *gwv1.Gateway, hostname string, tn *v2alpha1.CloudflareTunnel) error {
	return EmitDNSRecord(ctx, r.Client, r.Scheme, EmitOpts{
		Owner:       gw,
		OwnerKind:   "Gateway",
		Hostname:    hostname,
		Content:     tn.Status.TunnelCNAME,
		Annotations: gw.GetAnnotations(),
	})
}

var _ reconcile.Reconciler = (*GatewaySourceReconciler)(nil)
