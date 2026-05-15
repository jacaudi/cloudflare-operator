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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// ServiceSourceReconciler watches Services. On opt-in (cloudflare.io/tunnel="true"):
//   - resolves / auto-creates the target CloudflareTunnel,
//   - translates annotations + Service spec into IngressContributions (T8),
//   - emits one CloudflareDNSRecord CR per hostname (CNAME → tunnel CNAME),
//   - writes into the tunnelsynth cache (the cache is the source of truth for
//     attachedSources; the tunnel reconciler reads it on its loop).
//
// On opt-out / delete: clears cache entries for this source's prior
// tunnel-keys; emitted DNSRecord CRs are garbage-collected via OwnerReferences
// stamped at emit time.
//
// Reconcile is triggered by Service events AND CloudflareTunnel events. The
// latter retriggers source reconciliation when the tunnel CR's TunnelCNAME
// populates after Create — without this, the first reconcile may early-return
// with no DNSRecord emitted because Status.TunnelCNAME is empty.
//
// Stale-key sweep (Correction C): when a Service's tunnel-name annotation
// changes between reconciles, the cache entry under the prior tunnel-key
// would otherwise be orphaned. The shared cacheTracker (attach.go) tracks
// the last attached tunnel-key per source so we can clear the prior key
// whenever the new key differs. Initialized once via ensureTracker, called
// at the top of every Reconcile and guarded by sync.Once so concurrent
// reconciles on the same instance (MaxConcurrentReconciles > 1) cannot
// race to allocate competing trackers — the first allocator wins and the
// rest see the same instance, preserving prior-attachment state.
type ServiceSourceReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Cache            *tunnelsynth.Cache
	Recorder         record.EventRecorder
	DefaultConnector v1alpha1.ConnectorSpec

	tracker     *cacheTracker
	trackerOnce sync.Once
}

// ensureTracker initializes r.tracker exactly once. Safe against concurrent
// callers (controller-runtime worker pool with MaxConcurrentReconciles > 1).
// Idempotent: tests that pre-seed r.tracker keep their fixture untouched.
func (r *ServiceSourceReconciler) ensureTracker() {
	r.trackerOnce.Do(func() {
		if r.tracker == nil {
			r.tracker = newCacheTracker()
		}
	})
}

// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflaretunnels,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarednsrecords,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives one iteration of the Service-source state machine.
func (r *ServiceSourceReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("service", req.NamespacedName)
	r.ensureTracker()

	var svc corev1.Service
	if err := r.Get(ctx, req.NamespacedName, &svc); err != nil {
		if apierrors.IsNotFound(err) {
			// Service deleted — sweep the prior tunnel-key for this source.
			srcKey := tunnelsynth.SourceKey{Kind: "Service", Namespace: req.Namespace, Name: req.Name}
			if prev, ok := r.tracker.sweep(srcKey); ok {
				r.Cache.Clear(prev, srcKey)
			}
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	srcKey := tunnelsynth.SourceKey{Kind: "Service", Namespace: svc.Namespace, Name: svc.Name}

	// Opt-in gate.
	enabled, _ := conventions.ParseTruthy(svc.Annotations[conventions.AnnotationTunnel])
	if !enabled {
		// Sweep every tunnel-key that might have a stale entry for this
		// source: the previously-tracked key, plus the two derivable-from-
		// current-annotations candidates (pool + named).
		if prev, ok := r.tracker.sweep(srcKey); ok {
			r.Cache.Clear(prev, srcKey)
		}
		r.Cache.Clear(tunnelsynth.TunnelKey{Namespace: svc.Namespace, Name: "cf-" + svc.Namespace}, srcKey)
		if tn := svc.Annotations[conventions.AnnotationTunnelName]; tn != "" {
			r.Cache.Clear(tunnelsynth.TunnelKey{Namespace: svc.Namespace, Name: "cf-" + svc.Namespace + "-" + tn}, srcKey)
		}
		return reconcile.Result{}, nil
	}

	// Derive target tunnel name. Stable failures (NameTooLong, InvalidName)
	// are surfaced via an Event on the Service and a nil error return — the
	// failure is not retryable without the user editing the annotation, so
	// requeue-on-error would just spin.
	derived, err := DeriveTunnelName(svc.Namespace, svc.Annotations[conventions.AnnotationTunnelName])
	if err != nil {
		reason := conventions.ReasonInvalidName
		if errors.Is(err, ErrNameTooLong) {
			reason = conventions.ReasonNameTooLong
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(&svc, corev1.EventTypeWarning, reason, "%v", err)
		}
		// Sweep any stale prior key — the source is now in a broken state and
		// must not contribute to any tunnel.
		if prev, ok := r.tracker.sweep(srcKey); ok {
			r.Cache.Clear(prev, srcKey)
		}
		return reconcile.Result{}, nil
	}

	tn, err := EnsureTunnelCR(ctx, r.Client, r.Scheme, &svc, "Service", derived, r.DefaultConnector)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("ensure tunnel: %w", err)
	}

	// Translate annotations + Service spec → contributions.
	contribs, warns := tunnelsynth.TranslateService(&svc, tunnelsynth.Defaults{})
	for _, w := range warns {
		if r.Recorder != nil {
			r.Recorder.Eventf(&svc, corev1.EventTypeWarning, w.Reason, "%s", w.Message)
		}
	}

	tunnelKey := tunnelsynth.TunnelKey{Namespace: tn.Namespace, Name: tn.Name}

	// Correction C: clear the prior tunnel-key for this source if the
	// new key differs. This handles annotation changes (tunnel-name added,
	// removed, or renamed) between reconciles.
	if prev, ok := r.tracker.swap(srcKey, tunnelKey); ok {
		r.Cache.Clear(prev, srcKey)
	}
	r.Cache.Set(tunnelKey, srcKey, contribs)

	// Guard (Correction A): the DNSRecord CR's spec.content is a *string,
	// and emitting Content=&"" would produce an invalid record. Defer
	// emission until the tunnel reconciler populates Status.TunnelCNAME;
	// the manager wires a Watch on the tunnel CR so its status update
	// retriggers this reconciler. Cache write above still happens so the
	// tunnel reconciler can compute its ingress list in parallel.
	if tn.Status.TunnelCNAME == "" {
		logger.V(1).Info("tunnel CNAME not yet populated; deferring DNSRecord emission",
			"tunnel", tunnelKey)
		return reconcile.Result{}, nil
	}

	// Emit one CloudflareDNSRecord CR per hostname.
	for _, ic := range contribs {
		if err := r.emitDNSRecord(ctx, &svc, ic.Hostname, tn); err != nil {
			return reconcile.Result{}, fmt.Errorf("emit dns record for %q: %w", ic.Hostname, err)
		}
	}

	// Per Correction B: the cache IS the source of truth for
	// attachedSources. The tunnel reconciler reads Cache.AttachedSources()
	// on its loop and writes tn.Status.AttachedSources from there. A
	// cross-controller Status().Update from here would race with the tunnel
	// reconciler's own status writes — so we deliberately do NOT write to
	// tn.Status.AttachedSources from this controller.

	return reconcile.Result{}, nil
}

// emitDNSRecord upserts the CloudflareDNSRecord CR for this Service +
// hostname pair via the shared SSA-based helper. Annotation drift
// (cloudflare.io/adopt, cloudflare.io/zone-ref) propagates to the emitted
// CR because EmitDNSRecord uses SSA.
//
// Operator-edits-win: a user `kubectl edit` on the emitted CR will be
// reverted on the next reconcile.
func (r *ServiceSourceReconciler) emitDNSRecord(ctx context.Context, svc *corev1.Service, hostname string, tn *v1alpha1.CloudflareTunnel) error {
	return EmitDNSRecord(ctx, r.Client, r.Scheme, EmitOpts{
		Owner:       svc,
		OwnerKind:   "Service",
		Hostname:    hostname,
		Content:     tn.Status.TunnelCNAME,
		Annotations: svc.GetAnnotations(),
	})
}

// emittedDNSRecordName produces a stable, DNS-1123-compliant CR name combining
// the source object's name, a sanitized hostname prefix, and a short content
// hash of the original hostname. The hash guarantees uniqueness across
// hostnames that would otherwise alias under sanitization
// (e.g. "foo.example.com" vs "foo-example-com") or truncation (two long
// hostnames sharing the first N chars).
//
// Output shape: "<svcName>-<sanitized-prefix>-<8-hex-hash>", ≤63 chars,
// DNS-1123 valid (alphanumeric start/end, internal hyphens).
//
// The hash is sha256(hostname) truncated to 4 bytes / 8 hex chars: 32-bit
// collision resistance is plenty here (collision domain is per-Service, and
// any conflict is surfaced loudly by the apiserver, not silently dropped).
//
// Trade-off vs. pure sanitize-and-truncate: kubectl names are slightly less
// readable ("svc-foo-example-com-9a3f2b1c") but correctness is unconditional.
// The previous sanitize-only scheme silently dropped the second of any
// aliasing pair via the IsAlreadyExists swallow in Create.
func emittedDNSRecordName(sourceName, hostname string) string {
	sum := sha256.Sum256([]byte(hostname))
	short := hex.EncodeToString(sum[:4]) // 8 hex chars
	sanitized := sanitizeHostname(hostname)
	// Budget for the sanitized middle segment:
	// 63 - len(sourceName) - 1 (sep) - 1 (sep) - 8 (hash) = 53 - len(sourceName).
	maxSan := 63 - len(sourceName) - 1 - 1 - 8
	if maxSan < 1 {
		// Source name is itself approaching the 63-char ceiling — drop the
		// sanitized middle and emit "<sourceName>-<hash>". Caller's
		// responsibility to keep source names DNS-1123 (≤63); defensive.
		return sourceName + "-" + short
	}
	if len(sanitized) > maxSan {
		sanitized = strings.TrimRight(sanitized[:maxSan], "-")
	}
	if sanitized == "" {
		// Pathological hostname (all non-alphanumeric) — fall back to
		// "<sourceName>-<hash>". Hash still disambiguates per hostname.
		return sourceName + "-" + short
	}
	return sourceName + "-" + sanitized + "-" + short
}

// sanitizeHostname lowercases and replaces non-[a-z0-9] with '-', collapses
// hyphen runs, and trims leading/trailing hyphens. Result is a DNS-1123 label
// (modulo length) or empty if no alphanumerics survive.
func sanitizeHostname(h string) string {
	if h == "" {
		return ""
	}
	out := make([]byte, 0, len(h))
	prevHyphen := false
	for i := 0; i < len(h); i++ {
		c := h[i]
		switch {
		case c >= 'a' && c <= 'z':
			out = append(out, c)
			prevHyphen = false
		case c >= '0' && c <= '9':
			out = append(out, c)
			prevHyphen = false
		case c >= 'A' && c <= 'Z':
			out = append(out, c+32)
			prevHyphen = false
		default:
			if !prevHyphen {
				out = append(out, '-')
				prevHyphen = true
			}
		}
	}
	// Trim leading/trailing hyphens.
	s := string(out)
	s = strings.Trim(s, "-")
	return s
}
