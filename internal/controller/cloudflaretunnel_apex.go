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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
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
