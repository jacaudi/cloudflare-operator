/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
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
// whenever the new key differs. Initialized once via sourceBase.ensure, called
// at the top of every Reconcile and guarded by sync.Once so concurrent
// reconciles on the same instance (MaxConcurrentReconciles > 1) cannot
// race to allocate competing trackers — the first allocator wins and the
// rest see the same instance, preserving prior-attachment state.
type ServiceSourceReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Cache            *tunnelsynth.Cache
	Recorder         record.EventRecorder
	recorder         *conventions.SafeRecorder
	DefaultConnector v2alpha1.ConnectorSpec

	sourceBase
	recorderOnce sync.Once
}

// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflaretunnels,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarednsrecords,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives one iteration of the Service-source state machine.
// ensureRecorder lazily initializes r.recorder on first Reconcile.
func (r *ServiceSourceReconciler) ensureRecorder() {
	r.recorderOnce.Do(func() {
		if r.recorder == nil {
			r.recorder = conventions.NewSafeRecorder(r.Recorder)
		}
	})
}

func (r *ServiceSourceReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("service", req.NamespacedName)
	r.ensureRecorder()
	r.ensure()

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
		// Post-restart the in-memory tracker is empty, so clear the derived
		// key directly. Use DeriveTunnelName — the single source of truth the
		// opt-in path uses — so the swept key can never silently diverge from
		// what opt-in wrote if the naming template changes. One call covers
		// both the no-annotation (<ns>) and named (<ns>-<tn>) forms; for
		// a name DeriveTunnelName rejects, the opt-in path also wrote nothing,
		// so skipping the Clear is correct.
		if k, derr := DeriveTunnelName(svc.Namespace, svc.Annotations[conventions.AnnotationTunnelName]); derr == nil {
			r.Cache.Clear(tunnelsynth.TunnelKey{Namespace: svc.Namespace, Name: k}, srcKey)
		}
		// Deactivation prune (issue #145): source opted out of the tunnel.
		// Delete previously-emitted CRs so they don't squat on Cloudflare-
		// side records. Best-effort: log and continue.
		pruned, perr := pruneOrphanedDNSRecords(ctx, r.Client, "Service", svc.Name, svc.Namespace, nil)
		if perr != nil {
			logger.Error(perr, "orphan-prune failed during deactivation sweep")
		} else if len(pruned) > 0 {
			r.dedupe.emit(r.Recorder, &svc, corev1.EventTypeNormal, conventions.ReasonOrphanedDNSRecordPruned,
				fmt.Sprintf("deleted %d orphaned DNSRecord CR(s) on source deactivation", len(pruned)))
		}
		return reconcile.Result{}, nil
	}

	// Derive target tunnel name. Stable failures (NameTooLong, InvalidName)
	// are surfaced via an Event on the Service and a nil error return — the
	// failure is not retryable without the user editing the annotation, so
	// requeue-on-error would just spin.
	derived, err := DeriveTunnelName(svc.Namespace, svc.Annotations[conventions.AnnotationTunnelName])
	if err != nil {
		return handleDeriveTunnelNameErr(r.recorder, &svc, r.dedupe, r.tracker, r.Cache, srcKey, err)
	}

	tn, err := EnsureTunnelCR(ctx, r.Client, r.Scheme, &svc, "Service", derived, r.DefaultConnector)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("ensure tunnel: %w", err)
	}

	// Translate annotations + Service spec → contributions.
	contribs, warns := tunnelsynth.TranslateService(&svc, defaultsFromAnnotations(svc.GetAnnotations(), tunnelsynth.DefaultsFor(tn)))
	for _, w := range warns {
		r.dedupe.emit(r.recorder, &svc, corev1.EventTypeWarning, w.Reason, w.Message)
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
	desired := make(map[string]struct{}, len(contribs))
	for _, ic := range contribs {
		desired[ic.Hostname] = struct{}{}
		if err := r.emitDNSRecord(ctx, &svc, ic.Hostname, tn); err != nil {
			return reconcile.Result{}, fmt.Errorf("emit dns record for %q: %w", ic.Hostname, err)
		}
	}

	// Prune previously-emitted DNSRecord CRs whose hostname is no longer in
	// the desired set. Best-effort: a prune error logs and continues — the
	// desired records are already emitted, and any surviving orphan is retried
	// on the next reconcile. Placed strictly AFTER the emit loop on the
	// post-emit path; never reached on the deferred-emission early-return
	// above (where desired would be empty and would wrongly delete live CRs).
	pruned, perr := pruneOrphanedDNSRecords(ctx, r.Client, "Service", svc.Name, svc.Namespace, desired)
	if perr != nil {
		logger.Error(perr, "orphan-prune failed (continuing)")
	} else if len(pruned) > 0 {
		r.dedupe.emit(r.Recorder, &svc, corev1.EventTypeNormal, conventions.ReasonOrphanedDNSRecordPruned,
			fmt.Sprintf("deleted %d orphaned DNSRecord CR(s) for hostnames no longer in spec", len(pruned)))
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
func (r *ServiceSourceReconciler) emitDNSRecord(ctx context.Context, svc *corev1.Service, hostname string, tn *v2alpha1.CloudflareTunnel) error {
	return EmitDNSRecord(ctx, r.Client, r.Scheme, EmitOpts{
		Owner:       svc,
		OwnerKind:   "Service",
		Hostname:    hostname,
		Content:     tn.Status.TunnelCNAME,
		Annotations: svc.GetAnnotations(),
	})
}

// emittedDNSRecordName produces a stable, DNS-1123-compliant CR name from the
// hostname alone — the source object's name is INTENTIONALLY not part of the
// derivation. Two sources emitting the same hostname converge to one CR
// (correct: DNS is per-hostname; the CF-side record is shared either way).
// The previous `<sourceName>-<sanitizedHost>-<hash>` shape produced visible
// doubling for sources whose name already encoded the hostname (e.g. a
// HTTPRoute named `jellyfin` emitting for `jellyfin.example.com` →
// `jellyfin-jellyfin-example-com-<hash>`); see backlog item #6 (2026-05-19).
//
// Output shape: "<sanitized-hostname-truncated>-<8-hex-hash>", ≤63 chars,
// DNS-1123 valid (alphanumeric start/end, internal hyphens). The hash is
// sha256(hostname) truncated to 4 bytes / 8 hex chars: 32-bit collision
// resistance is plenty (collision domain is per-namespace; any collision is
// surfaced loudly by the apiserver, not silently dropped).
//
// Budget: 63 chars total - 1 (sep) - 8 (hash) = 54 chars for sanitized.
// Pathological all-separator hostnames produce the hash alone (still a valid
// DNS-1123 label — hex digits are alphanumeric).
func emittedDNSRecordName(hostname string) string {
	sum := sha256.Sum256([]byte(hostname))
	short := hex.EncodeToString(sum[:4]) // 8 hex chars
	sanitized := sanitizeHostname(hostname)
	const maxSan = 63 - 1 - 8 // 54
	if len(sanitized) > maxSan {
		sanitized = strings.TrimRight(sanitized[:maxSan], "-")
	}
	if sanitized == "" {
		// Pathological hostname (all non-alphanumeric) — the hash alone is
		// DNS-1123 valid (hex digits are alphanumeric).
		return short
	}
	return sanitized + "-" + short
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
