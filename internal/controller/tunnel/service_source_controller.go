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

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
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
// Reconcile is triggered by Service events AND CloudflareTunnel events (T14
// wires the Watches hook). The latter retriggers source reconciliation when
// the tunnel CR's TunnelCNAME populates after Create — without this, the
// first reconcile may early-return with no DNSRecord emitted because
// Status.TunnelCNAME is empty.
//
// Stale-key sweep (Correction C): when a Service's tunnel-name annotation
// changes between reconciles, the cache entry under the prior tunnel-key
// would otherwise be orphaned. We track the last attached tunnel-key per
// source in an in-memory map and clear the prior key whenever the new key
// differs. The map is mutex-guarded because controller-runtime may call
// Reconcile concurrently from its worker pool.
type ServiceSourceReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Cache            *tunnelsynth.Cache
	Recorder         record.EventRecorder
	DefaultConnector v1alpha1.ConnectorSpec

	mu           sync.Mutex
	lastAttached map[tunnelsynth.SourceKey]tunnelsynth.TunnelKey
}

// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflaretunnels,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=cloudflare-operator.cloudflare.io,resources=cloudflarednsrecords,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives one iteration of the Service-source state machine.
func (r *ServiceSourceReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("service", req.NamespacedName)

	var svc corev1.Service
	if err := r.Get(ctx, req.NamespacedName, &svc); err != nil {
		if apierrors.IsNotFound(err) {
			// Service deleted — sweep the prior tunnel-key for this source.
			srcKey := tunnelsynth.SourceKey{Kind: "Service", Namespace: req.Namespace, Name: req.Name}
			r.sweepPriorKey(srcKey)
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
		r.sweepPriorKey(srcKey)
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
		r.sweepPriorKey(srcKey)
		return reconcile.Result{}, nil
	}

	tn, err := EnsureTunnelCR(ctx, r.Client, r.Scheme, &svc, derived, r.DefaultConnector)
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
	r.swapAttachedKey(srcKey, tunnelKey)
	r.Cache.Set(tunnelKey, srcKey, contribs)

	// Guard (Correction A): the DNSRecord CR's spec.content is a *string,
	// and emitting Content=&"" would produce an invalid record. Defer
	// emission until the tunnel reconciler populates Status.TunnelCNAME;
	// the Watches hook (T14) retriggers this reconciler on the tunnel's
	// status update. Cache write above still happens so the tunnel
	// reconciler can compute its ingress list in parallel.
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

// swapAttachedKey records the new tunnel-key for this source and clears any
// prior key that differs. Thread-safe.
func (r *ServiceSourceReconciler) swapAttachedKey(src tunnelsynth.SourceKey, newKey tunnelsynth.TunnelKey) {
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
func (r *ServiceSourceReconciler) sweepPriorKey(src tunnelsynth.SourceKey) {
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

// emitDNSRecord creates (idempotently) a CloudflareDNSRecord CR for the given
// hostname, owner-reffed to the Service and stamped with source labels.
//
// Per spec 2 contract:
//   - spec.zoneRef.name (resolved by the zone reconciler from
//     cloudflare.io/zone-ref OR longest-suffix match) — never spec.zoneID
//   - spec.type = CNAME
//   - spec.name = hostname
//   - spec.content = tunnel CNAME (populated; guarded by caller)
func (r *ServiceSourceReconciler) emitDNSRecord(ctx context.Context, svc *corev1.Service, hostname string, tn *v1alpha1.CloudflareTunnel) error {
	content := tn.Status.TunnelCNAME // copy to take address; caller guards non-empty
	dr := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      emittedDNSRecordName(svc.Name, hostname),
			Namespace: svc.Namespace,
		},
		Spec: v1alpha1.CloudflareDNSRecordSpec{
			Type:    "CNAME",
			Name:    hostname,
			Content: &content,
		},
	}
	reconcilelib.StampSourceLabels(dr, "Service", svc.Name, svc.Namespace)
	if err := reconcilelib.SetControllerOwner(svc, dr, r.Scheme); err != nil {
		return err
	}
	if zr := svc.Annotations[conventions.AnnotationZoneRef]; zr != "" {
		dr.Spec.ZoneRef = &v1alpha1.ZoneReference{Name: zr, Namespace: svc.Namespace}
	}
	if adopt, _ := conventions.ParseTruthy(svc.Annotations[conventions.AnnotationAdopt]); adopt {
		dr.Spec.Adopt = true
	}
	if err := r.Create(ctx, dr); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	// Suppress unused-tn warning in the IsAlreadyExists path — we deliberately
	// do not update existing records here; the DNSRecord reconciler picks up
	// edits on its next loop driven by the CR's own resourceVersion.
	_ = tn
	return nil
}

// emittedDNSRecordName produces a stable, DNS-1123-compliant CR name from
// (svcName, hostname). The name is used as the CloudflareDNSRecord CR's
// metadata.name — it must satisfy DNS-1123 subdomain rules and stay ≤63 chars.
//
// Strategy (Correction D — sanitize + collapse + truncate):
//  1. Sanitize the hostname: lowercase a-z/0-9 kept; everything else → '-'.
//  2. Collapse runs of '-' into a single '-'.
//  3. Trim leading/trailing '-'.
//  4. Combine "<svc>-<sanitized>", then truncate the SUFFIX if needed so the
//     total stays under DNS-1123's 63-char label limit, re-trimming any
//     trailing '-' the truncation introduced.
//
// We deliberately use sanitize-and-truncate (debuggable) over a hash-based
// suffix (compact but opaque): kubectl gets readable names like
// "svc-foo-example-com" instead of "svc-9a3f2b1c4d".
func emittedDNSRecordName(svcName, hostname string) string {
	suffix := sanitizeHostname(hostname)
	// Reserve room for the "<svc>-" prefix.
	const maxLabel = 63
	maxSuffix := maxLabel - len(svcName) - 1 // -1 for the separator
	if maxSuffix < 1 {
		// svcName itself is already at the limit — return a trimmed prefix.
		// Caller's responsibility to keep Service names DNS-1123 (≤63);
		// defensive fall-through.
		if len(svcName) > maxLabel {
			return strings.TrimRight(svcName[:maxLabel], "-")
		}
		return svcName
	}
	if len(suffix) > maxSuffix {
		suffix = strings.TrimRight(suffix[:maxSuffix], "-")
	}
	if suffix == "" {
		// Pathological hostname (all non-alphanumeric) — fall back to the bare
		// service name. Caller should not reach this in practice because the
		// translator rejects empty hostnames via the MissingHostnames warning.
		return svcName
	}
	return svcName + "-" + suffix
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
