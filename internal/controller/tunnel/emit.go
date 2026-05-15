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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
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
	// the emitted CR. Recognized keys: AnnotationZoneRef, AnnotationAdopt.
	// Unrecognized keys are ignored.
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
// an emitted CR will be reverted on the next reconcile.
func EmitDNSRecord(ctx context.Context, c client.Client, scheme *runtime.Scheme, opts EmitOpts) error {
	content := opts.Content // local copy so we can take its address; Spec.Content is *string
	dr := &v1alpha1.CloudflareDNSRecord{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha1.GroupVersion.String(),
			Kind:       "CloudflareDNSRecord",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      emittedDNSRecordName(opts.Owner.GetName(), opts.Hostname),
			Namespace: opts.Owner.GetNamespace(),
		},
		Spec: v1alpha1.CloudflareDNSRecordSpec{
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
		dr.Spec.ZoneRef = &v1alpha1.ZoneReference{Name: zr, Namespace: opts.Owner.GetNamespace()}
	}
	if adopt, _ := conventions.ParseTruthy(opts.Annotations[conventions.AnnotationAdopt]); adopt {
		dr.Spec.Adopt = true
	}
	return reconcilelib.Apply(ctx, c, dr)
}
