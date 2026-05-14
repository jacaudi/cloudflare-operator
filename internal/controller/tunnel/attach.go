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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
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
	reconcilelib.StampSourceLabels(tn, owner.GetObjectKind().GroupVersionKind().Kind, owner.GetName(), owner.GetNamespace())
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
