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

package controller

import (
	"errors"
	"fmt"

	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

// ErrZoneRefNotReady is returned by ResolveZoneID when the referenced
// CloudflareZone exists but has not yet populated its status.ZoneID. Callers
// can check with errors.Is to log this as an Info-level "waiting on
// dependency" rather than an Error, since it resolves on its own once the
// zone reconciles.
var ErrZoneRefNotReady = errors.New("zone reference is not ready")

// zoneReferencer is implemented by CRDs whose spec either hardcodes a Cloudflare
// zone ID or references a CloudflareZone CR. It lets ResolveZoneID accept any of
// them without repeating positional field extraction at call sites.
type zoneReferencer interface {
	client.Object
	GetZoneID() string
	GetZoneRef() *cloudflarev1alpha1.ZoneReference
}

// ResolveZoneID returns the Cloudflare zone ID for obj: either its inline
// Spec.ZoneID, or the ZoneID from the CloudflareZone it references via
// Spec.ZoneRef. The lookup namespace is ref.Namespace when set, otherwise
// it falls back to obj's own namespace. Returns ErrZoneRefNotReady
// (wrapped) when the referenced zone exists but hasn't populated ZoneID yet.
func ResolveZoneID(ctx context.Context, k8sClient client.Client, obj zoneReferencer) (string, error) {
	if id := obj.GetZoneID(); id != "" {
		return id, nil
	}
	ref := obj.GetZoneRef()
	if ref == nil {
		return "", fmt.Errorf("one of zoneID or zoneRef is required")
	}

	ns := ref.Namespace
	if ns == "" {
		ns = obj.GetNamespace()
	}

	var zone cloudflarev1alpha1.CloudflareZone
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      ref.Name,
		Namespace: ns,
	}, &zone); err != nil {
		return "", fmt.Errorf("failed to get CloudflareZone %q: %w", ref.Name, err)
	}

	if zone.Status.ZoneID == "" {
		return "", fmt.Errorf("%w: CloudflareZone %q (status: %q)", ErrZoneRefNotReady, ref.Name, zone.Status.Status)
	}

	return zone.Status.ZoneID, nil
}
