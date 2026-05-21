/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnel

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// Sentinels for attach errors. Surfaced as conditions / events on the source
// object so the operator surfaces "why" not just "failed". See review-pattern
// #2 — every classifiable failure mode gets a sentinel.
var (
	// ErrNameTooLong indicates the derived tunnel CR name would exceed 52
	// chars, blowing the cloudflared-<name> 63-char DNS-label budget on the
	// downstream Deployment. Surfaced as Reason=NameTooLong.
	ErrNameTooLong = errors.New("tunnel CR name exceeds 52 chars (so cloudflared-<name> fits the DNS-1123 label budget)")
	// ErrInvalidName indicates one of the inputs (namespace or tunnel-name
	// annotation) fails DNS-1123 label rules. Surfaced as Reason=InvalidName.
	ErrInvalidName = errors.New("tunnel CR name must satisfy DNS-1123 label rules")
)

// dns1123 is the DNS-1123 label regex. Lowercase a-z, 0-9, '-', alphanumeric
// start/end. Identical to the regex k8s/apimachinery uses for label validation;
// we don't import that package because we want the regex form for surfacing
// the error reason cleanly.
var dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// DeriveTunnelName produces the operator-derived auto-created tunnel CR name.
//
// With tunnelNameAnnotation set: returns "<sourceNamespace>-<n>".
// With it empty (per-namespace pool): returns "<sourceNamespace>".
//
// Both inputs must be valid DNS-1123 labels. The total result is capped at 52
// chars so the dataplane Deployment name "cloudflared-<cr-name>" stays under
// Kubernetes' 63-char DNS-label limit. Backlog #5 (2026-05-20) dropped the
// legacy `cf-` prefix; existing auto-created tunnels with the old shape
// migrate via the P4 cascade-GC self-delete on next reconcile (sources
// reattach to the new-named tunnel; the old loses all attached sources and
// retires after the orphan-state grace window). Direct-create (user-
// authored) tunnels are never renamed or GC'd.
//
// Case-sensitivity contract: the inputs are treated case-sensitively; the
// caller is responsible for the DNS-1123-mandated lowercase form. We do NOT
// silently lowercase — an uppercase character returns ErrInvalidName.
func DeriveTunnelName(sourceNamespace, tunnelNameAnnotation string) (string, error) {
	if !dns1123.MatchString(sourceNamespace) {
		return "", fmt.Errorf("%w: namespace %q", ErrInvalidName, sourceNamespace)
	}
	var name string
	if tunnelNameAnnotation == "" {
		name = sourceNamespace
	} else {
		if !dns1123.MatchString(tunnelNameAnnotation) {
			return "", fmt.Errorf("%w: tunnel-name annotation %q", ErrInvalidName, tunnelNameAnnotation)
		}
		name = sourceNamespace + "-" + tunnelNameAnnotation
	}
	if len(name) > 52 {
		return "", fmt.Errorf("%w: would be %q (%d chars)", ErrNameTooLong, name, len(name))
	}
	return name, nil
}

// EnsureTunnelCR finds-or-creates a CloudflareTunnel CR with the derived
// name in the source object's namespace. First source to win the create race
// owns it via OwnerReferences. Subsequent attachers re-Get the existing CR
// without modifying ownership.
//
// Returns the resulting CR. defaults is the operator-level ConnectorSpec
// applied to auto-created CRs.
//
// ownerKind is the source kind ("Service", "HTTPRoute", etc.) used for the
// source-kind label on the auto-created CR. The caller must pass a literal
// because the typed controller-runtime client clears TypeMeta on Get —
// reading the kind off the live object via its ObjectKind / GVK accessor
// returns the empty string for objects fetched through the typed cache.
// Foundation §7 auditability requires the label be set correctly.
//
// Owner transfer on original-owner deletion: see docs/follow/tunnel-deferred.md
// "Follow-up A".
func EnsureTunnelCR(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	owner client.Object,
	ownerKind string,
	derivedName string,
	defaults v2alpha1.ConnectorSpec,
) (*v2alpha1.CloudflareTunnel, error) {
	key := types.NamespacedName{Namespace: owner.GetNamespace(), Name: derivedName}
	var existing v2alpha1.CloudflareTunnel
	if err := c.Get(ctx, key, &existing); err == nil {
		return &existing, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}
	// Not found — create.
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:        derivedName,
			Namespace:   owner.GetNamespace(),
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
		},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name:      derivedName,
			Connector: defaults,
		},
	}
	reconcilelib.StampSourceLabels(tn, ownerKind, owner.GetName(), owner.GetNamespace())
	if err := reconcilelib.SetControllerOwner(owner, tn, scheme); err != nil {
		return nil, err
	}
	if err := c.Create(ctx, tn); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Lost the create race — fetch and treat as attach.
			if err := c.Get(ctx, key, &existing); err != nil {
				return nil, err
			}
			return &existing, nil
		}
		return nil, err
	}
	return tn, nil
}

// isAutoCreated reports whether a CloudflareTunnel CR carries the
// AnnotationAutoCreated marker stamped by EnsureTunnelCR on creation.
// Strict equality with "true" — any other value (including absent,
// empty, "yes", "1") returns false. Documented as: only CRs the operator
// creates itself are eligible for cascade-GC.
func isAutoCreated(tn *v2alpha1.CloudflareTunnel) bool {
	return tn.Annotations[conventions.AnnotationAutoCreated] == "true"
}

// cascadeGCEligible reports whether a tunnel is subject to cascade-GC
// (owner-transfer + self-delete). True iff it carries the auto-created
// annotation OR the durable operator source labels (which survive the
// orphan state — orphaning clears ownerRefs/AttachedSources but not
// metadata.labels). Source labels are the same trust tier as the annotation
// (a user could hand-set either); broadening adds no new attack surface.
// isAutoCreated is intentionally NOT changed — it remains annotation-only
// for true-provenance use.
func cascadeGCEligible(tn *v2alpha1.CloudflareTunnel) bool {
	return isAutoCreated(tn) || reconcilelib.HasSourceLabels(tn)
}

// needsOwnerTransfer reports whether a cascade-GC-eligible tunnel CR has
// lost its OwnerReferences but still has attaching sources tracked in
// Status.AttachedSources. When true, the reconciler should attempt to
// promote one of the remaining attachers to owner. See design §4.2 for
// the algorithm.
//
// cascadeGCEligible-gated, symmetric with isOrphaned: this predicate applies
// ONLY to CRs the operator created itself (auto-created annotation OR operator
// source labels). Direct-create (user-authored) CRs are user-managed — the
// operator never takes controller-ownership of them, so a Service
// annotation-attaching to a user's CR never makes that Service the CR's
// k8s-controller-owner, and Kubernetes GC therefore never cascade-deletes the
// user's CR when the Service is removed (design §7). With both cascade-GC
// predicates cascadeGCEligible-gated, the entire cascade-GC machinery
// (owner-transfer rebalancing AND self-delete) is inert for direct-create CRs.
func needsOwnerTransfer(tn *v2alpha1.CloudflareTunnel) bool {
	return cascadeGCEligible(tn) &&
		len(tn.OwnerReferences) == 0 &&
		len(tn.Status.AttachedSources) > 0
}

// isOrphaned reports whether the tunnel CR is a cascade-GC-eligible CR with no
// remaining attaching sources and no owner. The cascade-GC self-delete
// path uses this predicate (subject to the two-tick grace window on
// Status.LastOrphanedAt). Mutually exclusive with needsOwnerTransfer.
//
// CRs that carry neither the AnnotationAutoCreated marker nor operator source
// labels are NEVER orphaned regardless of OwnerReferences / AttachedSources
// state — they survive indefinitely without operator-driven removal.
func isOrphaned(tn *v2alpha1.CloudflareTunnel) bool {
	return cascadeGCEligible(tn) &&
		len(tn.OwnerReferences) == 0 &&
		len(tn.Status.AttachedSources) == 0
}

// transferOwnershipMaxAttempts bounds how many AttachedSources candidates
// transferOwnershipIfNeeded will probe in a single call. The list is
// lex-sorted first, so the cap deterministically prefers the lex-smallest
// candidates. A bound exists so a pathological AttachedSources list (many
// stale entries that are all NotFound/terminating) cannot turn one reconcile
// into an unbounded apiserver-Get loop — the next reconcile retries the
// remainder once the state stabilizes.
const transferOwnershipMaxAttempts = 5

// getSourceObject resolves an empty typed object for the given AttachedSource
// kind so the caller can issue a typed Get against the live apiserver state
// (rather than trusting the possibly-stale AttachedSources snapshot).
//
// Returns an error for an unrecognized kind — that is a programming/config
// error, not a transient one, so callers propagate it rather than skip.
func getSourceObject(src v2alpha1.AttachedSource) (client.Object, error) {
	switch src.Kind {
	case "Service":
		return &corev1.Service{}, nil
	case "Gateway":
		return &gwv1.Gateway{}, nil
	case "HTTPRoute":
		return &gwv1.HTTPRoute{}, nil
	case "TLSRoute":
		return &gwv1a2.TLSRoute{}, nil
	default:
		return nil, fmt.Errorf("unknown source kind %q", src.Kind)
	}
}

// transferOwnershipIfNeeded promotes the lex-smallest LIVE remaining source in
// tn.Status.AttachedSources to be the new controller-owner of tn, via a
// MergeFromWithOptimisticLock Patch that carries tn.ResourceVersion so the
// apiserver rejects a stale Patch with 409 Conflict. See design §4.2.
//
// Candidates are sorted lexicographically by (Kind, Namespace, Name) and
// probed up to transferOwnershipMaxAttempts. For each candidate a fresh typed
// Get is issued: an IsNotFound (the source was deleted between the snapshot
// and now) or a non-nil DeletionTimestamp (the source is itself leaving)
// causes a skip to the next candidate. The first viable candidate becomes the
// new controller owner.
//
// Return contract:
//   - (true, nil)  — ownership transferred; the in-memory tn.OwnerReferences
//     is updated so the caller observes the post-patch state.
//   - (false, nil) — either every candidate was NotFound/terminating (the next
//     reconcile retries once state stabilizes), OR the Patch hit a
//     ResourceVersion Conflict (stale RV — the next reconcile retries with a
//     fresh ResourceVersion). Neither is an error.
//   - (false, err) — an unexpected error (unknown kind, non-Conflict Patch
//     failure, owner-ref construction failure). Cross-namespace attachers (a
//     source in a different namespace than the tunnel CR) also surface here via
//     SetControllerReference's namespace-mismatch guard and are out of scope.
//
// Owner-ref construction reuses reconcile.SetControllerOwner
// (controllerutil.SetControllerReference) rather than a hand-built
// apiutil.GVKForObject OwnerReference: project code-reuse mandate, identical
// Controller/BlockOwnerDeletion/GVK shape, and it derives the GVK from the
// scheme so the typed Get clearing TypeMeta is a non-issue. needsOwnerTransfer
// guarantees zero pre-existing owner refs before this is ever called, so the
// single-owner replace cannot collide with SetControllerReference's
// "different controller already set" guard.
func transferOwnershipIfNeeded(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	tn *v2alpha1.CloudflareTunnel,
	recorder record.EventRecorder,
) (bool, error) {
	candidates := make([]v2alpha1.AttachedSource, len(tn.Status.AttachedSources))
	copy(candidates, tn.Status.AttachedSources)
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})

	for i, cand := range candidates {
		if i >= transferOwnershipMaxAttempts {
			break
		}

		live, err := getSourceObject(cand)
		if err != nil {
			return false, err
		}
		key := types.NamespacedName{Namespace: cand.Namespace, Name: cand.Name}
		if err := c.Get(ctx, key, live); err != nil {
			if apierrors.IsNotFound(err) {
				// Deleted between the AttachedSources snapshot and now.
				continue
			}
			return false, fmt.Errorf("get source %s/%s/%s: %w", cand.Kind, cand.Namespace, cand.Name, err)
		}
		if live.GetDeletionTimestamp() != nil {
			// It's leaving too — don't promote a terminating source.
			continue
		}

		patched := tn.DeepCopy()
		patched.OwnerReferences = nil // single-owner replace; defensive even though needsOwnerTransfer guarantees empty
		if err := reconcilelib.SetControllerOwner(live, patched, scheme); err != nil {
			return false, fmt.Errorf("set controller owner %s/%s/%s: %w", cand.Kind, cand.Namespace, cand.Name, err)
		}
		if err := c.Patch(ctx, patched, client.MergeFromWithOptions(tn, client.MergeFromWithOptimisticLock{})); err != nil {
			if apierrors.IsConflict(err) {
				// Optimistic-lock contract: stale RV. The next reconcile
				// retries with a fresh ResourceVersion. NOT an error.
				return false, nil
			}
			return false, fmt.Errorf("patch ownerReferences: %w", err)
		}
		if recorder != nil {
			recorder.Eventf(tn, corev1.EventTypeNormal, conventions.ReasonOwnerTransferred,
				"ownership transferred to %s/%s/%s", cand.Kind, cand.Namespace, cand.Name)
		}
		// Reflect the post-patch refs so the caller sees the new owner.
		tn.OwnerReferences = patched.OwnerReferences
		return true, nil
	}

	// Every candidate was NotFound or terminating — the next reconcile retries
	// when state stabilizes. NOT an error.
	return false, nil
}

// cacheTracker is the shared per-controller "last attached tunnel-key" index
// used by every source reconciler. The map is mutex-guarded because
// controller-runtime may call Reconcile concurrently from its worker pool.
//
// Background: each source reconciler must clear its cache entry under the
// PRIOR tunnel-key whenever a source's tunnel-name annotation changes between
// reconciles, otherwise the cache leaks phantom contributions. Tracking the
// last attached key per source is the simplest reliable way to do that — we
// don't know what the "prior" key was from the source object alone after the
// annotation has changed.
//
// Extracted to attach.go when a third call site appeared — matches the
// Phase-2 reconcile.HaltDependency precedent (extract on the third use,
// not the second).
type cacheTracker struct {
	mu           sync.Mutex
	lastAttached map[tunnelsynth.SourceKey]tunnelsynth.TunnelKey
}

// newCacheTracker constructs an empty tracker. Returning a value (not pointer)
// makes embedding in reconcilers explicit without forgetting initialization;
// callers use `*cacheTracker` fields and initialize lazily.
func newCacheTracker() *cacheTracker {
	return &cacheTracker{}
}

// swap records the new tunnel-key for this source and returns the prior key
// (zero value if none). The caller is responsible for clearing the prior key
// from the tunnelsynth.Cache; we do not couple the tracker to the cache so it
// stays unit-testable in isolation.
func (t *cacheTracker) swap(src tunnelsynth.SourceKey, newKey tunnelsynth.TunnelKey) (prior tunnelsynth.TunnelKey, hadPrior bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastAttached == nil {
		t.lastAttached = map[tunnelsynth.SourceKey]tunnelsynth.TunnelKey{}
	}
	if prev, ok := t.lastAttached[src]; ok && prev != newKey {
		t.lastAttached[src] = newKey
		return prev, true
	}
	t.lastAttached[src] = newKey
	return tunnelsynth.TunnelKey{}, false
}

// sweep forgets the prior tunnel-key tracked for this source and returns it
// (zero value if none). Caller clears the cache entry under the returned key.
func (t *cacheTracker) sweep(src tunnelsynth.SourceKey) (prior tunnelsynth.TunnelKey, hadPrior bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastAttached == nil {
		return tunnelsynth.TunnelKey{}, false
	}
	if prev, ok := t.lastAttached[src]; ok {
		delete(t.lastAttached, src)
		return prev, true
	}
	return tunnelsynth.TunnelKey{}, false
}

// errGatewayServiceAnnotationMissing distinguishes "annotation absent"
// (GatewayServiceUnspecified) from "annotation present but Service can't be
// resolved" (GatewayServiceUnresolved). Shared by every reconciler that
// resolves the Gateway's underlying Service.
var errGatewayServiceAnnotationMissing = errors.New("cloudflare.io/gateway-service annotation required when cloudflare.io/tunnel is set on a Gateway")

// resolveGatewayService reads the REQUIRED cloudflare.io/gateway-service
// annotation: "<namespace>/<name>" or "<namespace>/<name>:<port>" (or
// "<name>" / "<name>:<port>" with the Gateway's namespace as the default).
//
// Required (not optional) because every Gateway implementation exposes its
// listener Service differently — no reliable label convention exists. If
// absent, callers surface GatewayServiceUnspecified on the Gateway and refuse
// synthesis.
//
// Returns the resolved Service plus the chosen port. When the annotation
// omits the port, falls back to Service.Spec.Ports[0].Port — this is the
// IN-CLUSTER port cloudflared connects to, NOT the listener's public-facing
// port. They are routinely different (e.g. listener 443 → Service 8443).
//
// Shared by every source reconciler that needs to resolve a Gateway's
// underlying Service for the cloudflared origin URL.
func resolveGatewayService(ctx context.Context, c client.Client, gw *gwv1.Gateway) (*corev1.Service, int32, error) {
	raw := gw.Annotations[conventions.AnnotationGatewayService]
	if raw == "" {
		return nil, 0, errGatewayServiceAnnotationMissing
	}
	ns, name, port, err := parseGatewayServiceRef(raw, gw.Namespace)
	if err != nil {
		return nil, 0, err
	}
	var svc corev1.Service
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &svc); err != nil {
		return nil, 0, fmt.Errorf("get Gateway service %s/%s: %w", ns, name, err)
	}
	if port == 0 {
		// Annotation didn't specify a port — fall back to the Service's
		// first port. A Service with no ports is a configuration error
		// (no tunnel URL can be synthesized without a port).
		if len(svc.Spec.Ports) == 0 {
			return nil, 0, fmt.Errorf("service %s/%s has no ports; annotation must specify a port", ns, name)
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
//
// Case-sensitivity: the function does no normalization. Namespace and name
// matching follows Kubernetes naming semantics (DNS-1123 labels are required
// to be lowercase; uppercase input would fail downstream Get by exact match).
func parseGatewayServiceRef(raw, defaultNS string) (namespace, name string, port int32, err error) {
	hostPart, portPart, hasPort := strings.Cut(raw, ":")
	if hasPort {
		p, perr := strconv.Atoi(portPart)
		if perr != nil || p <= 0 || p > 65535 {
			return "", "", 0, fmt.Errorf("invalid port %q in cloudflare.io/gateway-service", portPart)
		}
		port = int32(p) //nolint:gosec // G109: p is bounds-checked (1..65535) immediately above
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

// handleDeriveTunnelNameErr is the uniform error-handling shape for the
// 2 source controllers (Service, Gateway) when DeriveTunnelName returns an
// error. Classifies the error (InvalidName vs NameTooLong), emits a Warning
// Event via the recorder, sweeps the tracker so the source no longer
// contributes to any tunnel, and returns a nil-error reconcile result
// (the failure is not retryable without the user editing the annotation,
// so requeue-on-error would just spin).
//
// rec may wrap a nil EventRecorder — SafeRecorder's nil-guard handles it.
func handleDeriveTunnelNameErr(
	rec *conventions.SafeRecorder,
	obj client.Object,
	dedupe *eventDedupe,
	tracker *cacheTracker,
	cache *tunnelsynth.Cache,
	srcKey tunnelsynth.SourceKey,
	err error,
) (reconcile.Result, error) {
	reason := conventions.ReasonInvalidName
	if errors.Is(err, ErrNameTooLong) {
		reason = conventions.ReasonNameTooLong
	}
	dedupe.emit(rec, obj, corev1.EventTypeWarning, reason, err.Error())
	if prev, ok := tracker.sweep(srcKey); ok {
		cache.Clear(prev, srcKey)
	}
	return reconcile.Result{}, nil
}

// findTunnelTargetedParentRef scans parentRefs for the first parent Gateway
// that opts into tunnel attachment (cloudflare.io/tunnel truthy), has a
// derivable tunnel name, an existing CloudflareTunnel, and a resolvable
// Gateway Service. Returns all-nil when none qualifies. Get failures on a
// candidate are treated as "not this parent" (skip, don't fail). Shared by
// the HTTPRoute and TLSRoute source reconcilers.
func findTunnelTargetedParentRef(
	ctx context.Context,
	c client.Client,
	defaultNamespace string,
	parentRefs []gwv1.ParentReference,
) (*gwv1.ParentReference, *gwv1.Gateway, *v2alpha1.CloudflareTunnel, *corev1.Service, int32, error) {
	for i := range parentRefs {
		pr := parentRefs[i]
		gwNS := defaultNamespace
		if pr.Namespace != nil {
			gwNS = string(*pr.Namespace)
		}
		var gw gwv1.Gateway
		if err := c.Get(ctx, types.NamespacedName{Namespace: gwNS, Name: string(pr.Name)}, &gw); err != nil {
			continue
		}
		enabled, _ := conventions.ParseTruthy(gw.Annotations[conventions.AnnotationTunnel])
		if !enabled {
			continue
		}
		derived, err := DeriveTunnelName(gwNS, gw.Annotations[conventions.AnnotationTunnelName])
		if err != nil {
			continue
		}
		var tn v2alpha1.CloudflareTunnel
		if err := c.Get(ctx, types.NamespacedName{Namespace: gwNS, Name: derived}, &tn); err != nil {
			continue
		}
		gwSvc, port, err := resolveGatewayService(ctx, c, &gw)
		if err != nil {
			continue
		}
		return &pr, &gw, &tn, gwSvc, port, nil
	}
	return nil, nil, nil, nil, 0, nil
}
