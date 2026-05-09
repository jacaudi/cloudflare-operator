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
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/status"
)

// Sentinel errors returned by validateApexSpec. Tests assert via errors.Is
// and the reconciler maps them to ApexHostnameReady condition reasons.
var (
	ErrApexInvalidName  = errors.New("apex hostname is not a valid DNS name")
	ErrApexZoneMismatch = errors.New("apex hostname does not fall under referenced zone")
	ErrApexZoneNotReady = errors.New("apex zone has no resolved name yet")
)

// apexRecordName is the deterministic CloudflareDNSRecord CR name for the
// apex hostname owned by tunnel. There is exactly one apex per tunnel,
// so no hash suffix is required.
func apexRecordName(tunnel *cloudflarev1alpha1.CloudflareTunnel) string {
	return tunnel.Name + "-apex"
}

// validateApexSpec checks that fqdn is a valid DNS name and falls under
// zoneFQDN (i.e. equals zoneFQDN exactly or has it as a dotted suffix).
// zoneFQDN comes from the referenced CloudflareZone's Spec.Name; the
// empty-zone case is reported as ErrApexZoneNotReady so the caller can
// requeue rather than mark the spec invalid.
//
// Reuses isValidDNSName (source_helpers.go) for the shape check so apex
// validation matches what the source emitters accept; the suffix-match
// rule does the apex-vs-zone correctness.
func validateApexSpec(fqdn, zoneFQDN string) error {
	if zoneFQDN == "" {
		return ErrApexZoneNotReady
	}
	if !isValidDNSName(fqdn) {
		return fmt.Errorf("%w: %q", ErrApexInvalidName, fqdn)
	}
	if fqdn == zoneFQDN {
		return nil
	}
	if strings.HasSuffix(fqdn, "."+zoneFQDN) {
		return nil
	}
	return fmt.Errorf("%w: name=%q zone=%q", ErrApexZoneMismatch, fqdn, zoneFQDN)
}

// findCollidingApexCR scans the namespace for a CloudflareDNSRecord whose
// spec.name == fqdn and whose metadata.name != ourCRName. The "ourCRName"
// guard skips the apex CR we are about to upsert; matching it would not
// be a collision. Returns nil when no other CR claims the FQDN.
//
// Cloudflare-side collisions (a record at fqdn owned outside the
// operator) are handled by the CloudflareDNSRecord controller's
// TXT-registry decision logic. This helper only prevents the operator
// from racing two of its own CRs at the same name in the same namespace.
func findCollidingApexCR(ctx context.Context, c client.Client, namespace, fqdn, ourCRName string) (*cloudflarev1alpha1.CloudflareDNSRecord, error) {
	var list cloudflarev1alpha1.CloudflareDNSRecordList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list CloudflareDNSRecord in %q: %w", namespace, err)
	}
	for i := range list.Items {
		r := &list.Items[i]
		if r.Name == ourCRName {
			continue
		}
		if r.Spec.Name == fqdn {
			return r, nil
		}
	}
	return nil, nil
}

// desiredApexRecord builds the CloudflareDNSRecord the operator wants to
// exist for tunnel.spec.apexHostname. Caller is responsible for ensuring
// tunnel.Spec.ApexHostname != nil, tunnel.Status.TunnelCNAME != "", and
// validation has passed; this helper just translates a validated spec
// into the desired record.
func desiredApexRecord(tunnel *cloudflarev1alpha1.CloudflareTunnel) *cloudflarev1alpha1.CloudflareDNSRecord {
	apex := tunnel.Spec.ApexHostname
	// Proxied defaults to true when unset; explicit false is honored.
	proxied := true
	if apex.Proxied != nil {
		proxied = *apex.Proxied
	}

	return &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      apexRecordName(tunnel),
			Namespace: tunnel.Namespace,
			Labels: map[string]string{
				LabelSourceKind:      "CloudflareTunnel",
				LabelSourceNamespace: tunnel.Namespace,
				LabelSourceName:      tunnel.Name,
				LabelManagedBy:       "cloudflare-operator",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         tunnel.APIVersion,
					Kind:               tunnel.Kind,
					Name:               tunnel.Name,
					UID:                tunnel.UID,
					Controller:         boolPtr(true),
					BlockOwnerDeletion: boolPtr(true),
				},
			},
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			Name:      apex.Name,
			Type:      "CNAME",
			Content:   strPtr(tunnel.Status.TunnelCNAME),
			Proxied:   boolPtr(proxied),
			ZoneRef:   &cloudflarev1alpha1.ZoneReference{Name: apex.ZoneRef.Name, Namespace: apex.ZoneRef.Namespace},
			SecretRef: tunnel.Spec.SecretRef,
		},
	}
}

// deleteApexRecordIfPresent best-effort deletes the apex CloudflareDNSRecord
// owned by tunnel. NotFound on either Get or Delete is treated as success
// (idempotent). Used on the spec.apexHostname-removal path; on tunnel
// deletion, owner-reference GC handles cleanup automatically and this
// helper is not invoked.
func deleteApexRecordIfPresent(ctx context.Context, c client.Client, tunnel *cloudflarev1alpha1.CloudflareTunnel) error {
	rec := &cloudflarev1alpha1.CloudflareDNSRecord{}
	key := types.NamespacedName{Name: apexRecordName(tunnel), Namespace: tunnel.Namespace}
	if err := c.Get(ctx, key, rec); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get apex CloudflareDNSRecord %s: %w", key, err)
	}
	if err := c.Delete(ctx, rec); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete apex CloudflareDNSRecord %s: %w", key, err)
	}
	return nil
}

// reconcileApexHostname reconciles the apex CloudflareDNSRecord owned by
// tunnel.spec.apexHostname. Caller (main Reconcile) invokes this AFTER
// provisioning has populated Status.TunnelCNAME. See design D4 / D7.
//
// Returns:
//   - (zero ctrl.Result, nil) on success/steady-state, validation failure,
//     or collision-refuse — these are user-fixable and don't benefit from
//     a forced requeue beyond the tunnel's own interval.
//   - (RequeueAfter: 30s, nil) when the referenced CloudflareZone isn't Ready.
//   - (zero ctrl.Result, err) on plumbing errors (cache list, Update).
func reconcileApexHostname(ctx context.Context, c client.Client, tunnel *cloudflarev1alpha1.CloudflareTunnel) (ctrl.Result, error) {
	// Self-heal TypeMeta. controller-runtime's typed client strips TypeMeta
	// on cached reads, so when the controller fetched this tunnel via
	// r.Get(...) APIVersion/Kind are empty. desiredApexRecord copies those
	// fields into the owner-ref; an empty TypeMeta produces a malformed
	// ownerRef. Restore them once at the top so all downstream code sees
	// a fully-populated object.
	if tunnel.APIVersion == "" || tunnel.Kind == "" {
		tunnel.TypeMeta = metav1.TypeMeta{
			APIVersion: cloudflarev1alpha1.GroupVersion.String(),
			Kind:       "CloudflareTunnel",
		}
	}

	if tunnel.Spec.ApexHostname == nil {
		// Spec absent: GC any owned apex CR, clear status, drop condition.
		if err := deleteApexRecordIfPresent(ctx, c, tunnel); err != nil {
			return ctrl.Result{}, err
		}
		tunnel.Status.ApexHostname = nil
		meta.RemoveStatusCondition(&tunnel.Status.Conditions, cloudflarev1alpha1.ConditionTypeApexHostnameReady)
		return ctrl.Result{}, nil
	}

	apex := tunnel.Spec.ApexHostname

	// Step (a): fetch the referenced zone.
	zone, zoneReady, err := fetchApexZone(ctx, c, tunnel.Namespace, apex.ZoneRef)
	if err != nil {
		return ctrl.Result{}, err
	}
	if zone == nil || !zoneReady {
		setApexCondition(tunnel, metav1.ConditionFalse, cloudflarev1alpha1.ReasonZoneRefNotReady,
			fmt.Sprintf("zone %q not Ready", apex.ZoneRef.Name), tunnel.Generation)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Step (b): validate name + suffix-match. Map sentinel errors to
	// condition reasons via errors.Is so wrapping with %w is preserved.
	if vErr := validateApexSpec(apex.Name, zone.Spec.Name); vErr != nil {
		reason := cloudflarev1alpha1.ReasonInvalidSpec
		switch {
		case errors.Is(vErr, ErrApexZoneNotReady):
			// Defensive: zone-Ready was checked above, but if validateApexSpec
			// reports an empty zoneFQDN we still surface the right reason.
			reason = cloudflarev1alpha1.ReasonZoneRefNotReady
		case errors.Is(vErr, ErrApexInvalidName), errors.Is(vErr, ErrApexZoneMismatch):
			reason = cloudflarev1alpha1.ReasonInvalidSpec
		}
		setApexCondition(tunnel, metav1.ConditionFalse, reason, vErr.Error(), tunnel.Generation)
		return ctrl.Result{}, nil
	}

	// Step (c): collision check.
	collide, err := findCollidingApexCR(ctx, c, tunnel.Namespace, apex.Name, apexRecordName(tunnel))
	if err != nil {
		return ctrl.Result{}, err
	}
	if collide != nil {
		setApexCondition(tunnel, metav1.ConditionFalse,
			cloudflarev1alpha1.ReasonRecordOwnershipConflict,
			fmt.Sprintf("CloudflareDNSRecord %s/%s already claims %q", collide.Namespace, collide.Name, apex.Name),
			tunnel.Generation)
		return ctrl.Result{}, nil
	}

	// Step (d): upsert apex CR.
	desired := desiredApexRecord(tunnel)
	if err := upsertDNSRecord(ctx, c, desired); err != nil {
		return ctrl.Result{}, fmt.Errorf("upsert apex CloudflareDNSRecord: %w", err)
	}

	// Step (e): reflect. upsertDNSRecord returns only error and discards the
	// post-write object, so re-fetch to read RecordID + Ready condition.
	current := &cloudflarev1alpha1.CloudflareDNSRecord{}
	if err := c.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, current); err != nil {
		return ctrl.Result{}, fmt.Errorf("get apex CloudflareDNSRecord after upsert: %w", err)
	}
	tunnel.Status.ApexHostname = &cloudflarev1alpha1.ApexHostnameStatus{
		Name:     apex.Name,
		RecordID: current.Status.RecordID,
	}
	if isDNSRecordReady(current) {
		setApexCondition(tunnel, metav1.ConditionTrue, cloudflarev1alpha1.ReasonReconcileSuccess,
			"apex CloudflareDNSRecord Ready", tunnel.Generation)
	} else {
		setApexCondition(tunnel, metav1.ConditionFalse, cloudflarev1alpha1.ReasonApexRecordPending,
			"apex CloudflareDNSRecord not yet Ready", tunnel.Generation)
	}
	return ctrl.Result{}, nil
}

// fetchApexZone returns (zone, ready, err). The fallback namespace for
// ZoneRef.Namespace is the tunnel's own namespace, matching the existing
// CloudflareZone reference pattern in this codebase. NotFound is reported
// as (nil, false, nil) so the caller can treat it as "not yet" rather
// than a plumbing error.
func fetchApexZone(ctx context.Context, c client.Client, tunnelNs string, ref cloudflarev1alpha1.ZoneReference) (*cloudflarev1alpha1.CloudflareZone, bool, error) {
	ns := ref.Namespace
	if ns == "" {
		ns = tunnelNs
	}
	var zone cloudflarev1alpha1.CloudflareZone
	if err := c.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ns}, &zone); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get CloudflareZone %s/%s: %w", ns, ref.Name, err)
	}
	ready := meta.IsStatusConditionTrue(zone.Status.Conditions, cloudflarev1alpha1.ConditionTypeReady)
	return &zone, ready, nil
}

// setApexCondition writes a single ApexHostnameReady condition with the
// supplied status, reason, and message. The tunnel's overall Ready
// condition is intentionally NOT updated here — apex problems must not
// take down a working tunnel (design D5). generation is threaded through
// to ObservedGeneration so the condition reflects the spec revision it
// was evaluated against, matching the rest of the codebase.
func setApexCondition(tunnel *cloudflarev1alpha1.CloudflareTunnel, condStatus metav1.ConditionStatus, reason, message string, generation int64) {
	status.SetCondition(&tunnel.Status.Conditions, cloudflarev1alpha1.ConditionTypeApexHostnameReady,
		condStatus, reason, message, generation)
}

// isDNSRecordReady reports whether rec has a Ready=True condition.
func isDNSRecordReady(rec *cloudflarev1alpha1.CloudflareDNSRecord) bool {
	return meta.IsStatusConditionTrue(rec.Status.Conditions, cloudflarev1alpha1.ConditionTypeReady)
}
