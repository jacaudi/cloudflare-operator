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
	"strconv"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

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
// TODO: Owner-transfer on owner deletion is design §6.4 territory. The
// lexicographically-first remaining attacher should be promoted via an
// ownerReferences Patch when the original owner is deleted. Deferred until
// multiple source kinds exist (T11+) so the shared helper can be factored
// against a real common shape — a generic ownerRef Patch without GVK+UID is
// guesswork. Until then, the owner CR remains owner-less once the original
// owner Service is deleted; the controller still reconciles via the source
// labels on cache entries.
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
		ObjectMeta: metav1.ObjectMeta{Name: derivedName, Namespace: owner.GetNamespace()},
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
// Extracted to attach.go (from inline copies in T10 + T11) when T12 (HTTPRoute)
// became the third call site — matches the Phase-2 reconcile.HaltDependency
// precedent (extract on the third use, not the second).
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
// Shared by GatewaySourceReconciler (T11), HTTPRouteSourceReconciler (T12),
// and TLSRouteSourceReconciler (T13).
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
