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
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
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
	ErrNameTooLong = errors.New("tunnel CR name exceeds 52 chars (so cloudflared-<name> fits 63-char DNS label)")
	// ErrInvalidName indicates one of the inputs (namespace or tunnel-name
	// annotation) fails DNS-1123 label rules. Surfaced as Reason=InvalidName.
	ErrInvalidName = errors.New("tunnel CR name must satisfy DNS-1123 label rules")
)

// dns1123 is the DNS-1123 label regex. Lowercase a-z, 0-9, '-', alphanumeric
// start/end. Identical to the regex k8s/apimachinery uses for label validation;
// we don't import that package because we want the regex form for surfacing
// the error reason cleanly.
var dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// DeriveTunnelName applies the locked spec 3 §6.1 name template.
//
// With tunnelNameAnnotation set: returns "cf-<sourceNamespace>-<n>".
// With it empty (per-namespace pool): returns "cf-<sourceNamespace>".
//
// Both inputs must be valid DNS-1123 labels. The total result is capped at 52
// chars so the dataplane Deployment name "cloudflared-<cr-name>" stays under
// Kubernetes' 63-char DNS-label limit.
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
		name = "cf-" + sourceNamespace
	} else {
		if !dns1123.MatchString(tunnelNameAnnotation) {
			return "", fmt.Errorf("%w: tunnel-name annotation %q", ErrInvalidName, tunnelNameAnnotation)
		}
		name = "cf-" + sourceNamespace + "-" + tunnelNameAnnotation
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
	defaults v1alpha1.ConnectorSpec,
) (*v1alpha1.CloudflareTunnel, error) {
	key := types.NamespacedName{Namespace: owner.GetNamespace(), Name: derivedName}
	var existing v1alpha1.CloudflareTunnel
	if err := c.Get(ctx, key, &existing); err == nil {
		return &existing, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}
	// Not found — create.
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:        derivedName,
			Namespace:   owner.GetNamespace(),
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
		},
		Spec: v1alpha1.CloudflareTunnelSpec{
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
func isAutoCreated(tn *v1alpha1.CloudflareTunnel) bool {
	return tn.Annotations[conventions.AnnotationAutoCreated] == "true"
}

// needsOwnerTransfer reports whether an auto-created tunnel CR has lost its
// OwnerReferences but still has attaching sources tracked in
// Status.AttachedSources. When true, the reconciler should attempt to
// promote one of the remaining attachers to owner. See design §4.2 for
// the algorithm.
//
// isAutoCreated-gated, symmetric with isOrphaned: this predicate applies
// ONLY to CRs the operator created itself. Direct-create (user-authored)
// CRs are user-managed — the operator never takes controller-ownership of
// them, so a Service annotation-attaching to a user's CR never makes that
// Service the CR's k8s-controller-owner, and Kubernetes GC therefore never
// cascade-deletes the user's CR when the Service is removed (design §7).
// With both cascade-GC predicates isAutoCreated-gated, the entire
// cascade-GC machinery (owner-transfer rebalancing AND self-delete) is
// inert for direct-create CRs.
func needsOwnerTransfer(tn *v1alpha1.CloudflareTunnel) bool {
	return isAutoCreated(tn) &&
		len(tn.OwnerReferences) == 0 &&
		len(tn.Status.AttachedSources) > 0
}

// isOrphaned reports whether the tunnel CR is an auto-created CR with no
// remaining attaching sources and no owner. The cascade-GC self-delete
// path uses this predicate (subject to the two-tick grace window on
// Status.LastOrphanedAt). Mutually exclusive with needsOwnerTransfer.
//
// Direct-create CRs (no AnnotationAutoCreated marker) are NEVER orphaned
// regardless of OwnerReferences / AttachedSources state — they survive
// indefinitely without operator-driven removal.
func isOrphaned(tn *v1alpha1.CloudflareTunnel) bool {
	return isAutoCreated(tn) &&
		len(tn.OwnerReferences) == 0 &&
		len(tn.Status.AttachedSources) == 0
}

// transferOwnershipMaxAttempts bounds how many AttachedSources candidates
// TransferOwnershipIfNeeded will probe in a single call. The list is
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
func getSourceObject(src v1alpha1.AttachedSource) (client.Object, error) {
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

// TransferOwnershipIfNeeded promotes the lex-smallest LIVE remaining source in
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
func TransferOwnershipIfNeeded(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	tn *v1alpha1.CloudflareTunnel,
	recorder record.EventRecorder,
) (bool, error) {
	candidates := make([]v1alpha1.AttachedSource, len(tn.Status.AttachedSources))
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
