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
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
)

// EmitOpts is the parameter bag for EmitDNSRecord. Owner+OwnerKind are split
// because the controller-runtime typed cache erases TypeMeta on Get; the kind
// cannot be read back from the object, so the caller passes it literally.
type EmitOpts struct {
	// Owner is the source object (Service / Gateway / HTTPRoute / TLSRoute) to
	// which the emitted CR is owner-reffed. Must already exist (have a UID).
	Owner client.Object
	// OwnerKind is the literal Kind name. Used for the source-kind label and
	// the owner-ref kind.
	OwnerKind string
	// Hostname is the public FQDN this record routes for (becomes Spec.Name).
	Hostname string
	// Content is the CNAME target. For Service callers, this is the tunnel
	// CNAME (Status.TunnelCNAME). For Route callers, this is the Gateway apex
	// hostname (the intermediate stabilizer in the per-design §4.2 CNAME chain).
	Content string
	// Annotations carries optional source-object annotations that thread into
	// the emitted CR. Recognized keys: AnnotationZoneRef, AnnotationZoneRefNamespace,
	// AnnotationAdopt, AnnotationProxied, AnnotationTTL. Unrecognized keys are ignored.
	//
	// AnnotationProxied defaults to true for tunnel-emitted records (CNAME →
	// <uuid>.cfargotunnel.com generally needs to be proxied to route);
	// override with cloudflare.io/proxied: "false". Malformed values
	// (unrecognized by conventions.ParseTruthy) fall back to the default.
	// AnnotationTTL accepts an integer; absent/malformed → Spec.TTL=0 (auto).
	Annotations map[string]string
}

// EmitDNSRecord upserts a CloudflareDNSRecord CR for opts.Hostname, owner-
// reffed to opts.Owner and labeled with the source-kind. Routes through
// reconcile.Apply (server-side apply) so per-reconcile annotation drift on
// the source (e.g. cloudflare.io/adopt flipping false → true) propagates
// into the emitted CR's Spec. The legacy Create+IsAlreadyExists path that
// this replaces silently dropped such drift — see design Item D1 /
// docs/follow/tunnel-deferred.md Follow-up B.
//
// TypeMeta is set unconditionally; SSA hard-rejects objects without it.
//
// Field ownership: this helper operates as the "cloudflare-operator" field
// manager (per reconcile.Apply). Operator-edits-win is intentional — matches
// Foundation's SSA convention for owned children. A user `kubectl edit` of
// an emitted CR will be reverted on the next reconcile — including Spec.Adopt,
// Spec.ZoneRef, Spec.Proxied, and Spec.TTL, which are re-asserted from the
// SOURCE object's annotations (cloudflare.io/adopt, cloudflare.io/zone-ref,
// cloudflare.io/zone-ref-namespace, cloudflare.io/proxied, cloudflare.io/ttl).
// Toggle these on the source, not on the emitted CR.
func EmitDNSRecord(ctx context.Context, c client.Client, scheme *runtime.Scheme, opts EmitOpts) error {
	content := opts.Content // local copy so we can take its address; Spec.Content is *string
	dr := &v2alpha1.CloudflareDNSRecord{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v2alpha1.GroupVersion.String(),
			Kind:       "CloudflareDNSRecord",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      emittedDNSRecordName(opts.Hostname),
			Namespace: opts.Owner.GetNamespace(),
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Type:    "CNAME",
			Name:    opts.Hostname,
			Content: &content,
		},
	}
	reconcilelib.StampSourceLabels(dr, opts.OwnerKind, opts.Owner.GetName(), opts.Owner.GetNamespace())
	if err := reconcilelib.SetControllerOwner(opts.Owner, dr, scheme); err != nil {
		return err
	}
	if zr := opts.Annotations[conventions.AnnotationZoneRef]; zr != "" {
		zoneNS := opts.Annotations[conventions.AnnotationZoneRefNamespace]
		if zoneNS == "" {
			zoneNS = opts.Owner.GetNamespace()
		}
		dr.Spec.ZoneRef = &v2alpha1.ZoneReference{Name: zr, Namespace: zoneNS}
	}
	if adopt, _ := conventions.ParseTruthy(opts.Annotations[conventions.AnnotationAdopt]); adopt {
		dr.Spec.Adopt = true
	}

	// Proxied: default true for tunnel-emitted records (CNAME →
	// <uuid>.cfargotunnel.com requires proxied to route). Annotation
	// override: cloudflare.io/proxied: "false" yields grey-cloud.
	// Malformed values silently fall back to the default (true).
	proxiedDefault := true
	dr.Spec.Proxied = &proxiedDefault
	if v, ok := opts.Annotations[conventions.AnnotationProxied]; ok && v != "" {
		if p, err := conventions.ParseTruthy(v); err == nil {
			b := p
			dr.Spec.Proxied = &b
		}
	}

	// TTL: optional integer annotation; absent or malformed leaves Spec.TTL=0
	// (Cloudflare interprets 0 as automatic).
	if v := opts.Annotations[conventions.AnnotationTTL]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			dr.Spec.TTL = n
		}
	}

	return reconcilelib.Apply(ctx, c, dr)
}
