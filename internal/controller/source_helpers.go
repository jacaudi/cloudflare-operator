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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

// strPtr returns a pointer to the given string value.
func strPtr(s string) *string { return &s }

// boolPtr returns a pointer to the given bool value.
func boolPtr(b bool) *bool { return &b }

// splitAndTrim splits s on commas, trims whitespace from each element,
// and discards empty strings.
func splitAndTrim(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

var (
	// invalidCRNameChars matches characters that are not valid in a DNS1123 subdomain label.
	invalidCRNameChars = regexp.MustCompile(`[^a-z0-9-]`)
	// multipleDashes collapses consecutive dashes.
	multipleDashes = regexp.MustCompile(`-{2,}`)
)

// sanitizeDNSForCRName converts a DNS hostname into a valid Kubernetes
// DNS1123 subdomain name suitable for use as a CR metadata.name.
//
// Transformation rules (applied in order):
//  1. Lowercase.
//  2. Replace leading wildcard "*" with "wild".
//  3. Replace dots with dashes.
//  4. Strip any remaining characters that are not [a-z0-9-].
//  5. Collapse consecutive dashes.
//  6. Trim leading/trailing dashes.
func sanitizeDNSForCRName(s string) string {
	s = strings.ToLower(s)
	// Replace wildcard leaf with "wild".
	if strings.HasPrefix(s, "*.") {
		s = "wild" + s[1:]
	} else if s == "*" {
		s = "wild"
	}
	// Replace dots with dashes.
	s = strings.ReplaceAll(s, ".", "-")
	// Strip invalid characters.
	s = invalidCRNameChars.ReplaceAllString(s, "")
	// Collapse consecutive dashes.
	s = multipleDashes.ReplaceAllString(s, "-")
	// Trim leading/trailing dashes.
	s = strings.Trim(s, "-")
	return s
}

// isValidDNSName reports whether hostname is a valid DNS name for use as a
// Cloudflare DNS record name. It accepts FQDNs and wildcard labels of the form
// "*.parent.tld".
//
// Constraints checked:
//   - Not empty.
//   - Total length ≤ 253 characters.
//   - Each label is 1–63 characters.
//   - Labels contain only [a-zA-Z0-9-].
//   - Labels do not start or end with a dash.
//   - Wildcard is allowed only as the first label ("*.rest").
func isValidDNSName(hostname string) bool {
	if hostname == "" {
		return false
	}
	// Strip wildcard prefix for label validation.
	rest := hostname
	if strings.HasPrefix(hostname, "*.") {
		rest = hostname[2:]
	} else if hostname == "*" {
		return false // bare wildcard is not a valid DNS name
	}
	if len(rest) == 0 || len(rest) > 253 {
		return false
	}
	for label := range strings.SplitSeq(rest, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '-' {
				return false
			}
		}
	}
	return true
}

// maxK8sName is the Kubernetes maximum length for resource names (DNS-1123 subdomain).
const maxK8sName = 253

// capCRName ensures a generated CR name does not exceed maxK8sName characters.
// When the name is already within the limit, it is returned unchanged.
// When it exceeds the limit, it is truncated and a 9-character hash suffix
// ("-" + 8 hex chars from sha256 of the original full name) is appended to
// preserve uniqueness. The result always satisfies DNS-1123 subdomain rules
// (no trailing dashes — the truncation point trims any trailing dashes before
// appending the hash).
func capCRName(name string) string {
	if len(name) <= maxK8sName {
		return name
	}
	h := sha256.Sum256([]byte(name))
	suffix := "-" + hex.EncodeToString(h[:4]) // "-" + 8 hex chars = 9 chars
	maxHead := maxK8sName - len(suffix)
	head := name[:maxHead]
	// Trim any trailing dash introduced by truncation.
	head = strings.TrimRight(head, "-")
	return head + suffix
}

// firstNonEmpty returns the first non-empty string among a and b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ownerRefsFor returns a single-element slice containing an OwnerReference
// for obj with Controller=true and BlockOwnerDeletion=true.
// The caller must ensure obj has its TypeMeta set (GroupVersionKind populated)
// so that the APIVersion and Kind fields are meaningful.
func ownerRefsFor(obj client.Object) []metav1.OwnerReference {
	gvk := obj.GetObjectKind().GroupVersionKind()
	return []metav1.OwnerReference{
		{
			APIVersion:         gvk.GroupVersion().String(),
			Kind:               gvk.Kind,
			Name:               obj.GetName(),
			UID:                obj.GetUID(),
			Controller:         boolPtr(true),
			BlockOwnerDeletion: boolPtr(true),
		},
	}
}

// upsertDNSRecord creates desired if absent, otherwise updates the existing
// record to match. Spec, Labels, Annotations, and OwnerReferences are fully
// replaced — the operator is the sole authority for these fields. Callers
// that want to preserve foreign labels/annotations should use a different
// pattern.
func upsertDNSRecord(ctx context.Context, c client.Client, desired *cloudflarev1alpha1.CloudflareDNSRecord) error {
	existing := &cloudflarev1alpha1.CloudflareDNSRecord{}
	err := c.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		return c.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("get CloudflareDNSRecord %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	existing.Spec = desired.Spec
	existing.Labels = desired.Labels
	existing.Annotations = desired.Annotations
	existing.OwnerReferences = desired.OwnerReferences
	return c.Update(ctx, existing)
}

// upsertTunnelRule creates desired if absent, otherwise updates the existing
// rule to match. Spec, Labels, Annotations, and OwnerReferences are fully
// replaced — the operator is the sole authority for these fields. Callers
// that want to preserve foreign labels/annotations should use a different
// pattern.
func upsertTunnelRule(ctx context.Context, c client.Client, desired *cloudflarev1alpha1.CloudflareTunnelRule) error {
	existing := &cloudflarev1alpha1.CloudflareTunnelRule{}
	err := c.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		return c.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("get CloudflareTunnelRule %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	existing.Spec = desired.Spec
	existing.Labels = desired.Labels
	existing.Annotations = desired.Annotations
	existing.OwnerReferences = desired.OwnerReferences
	return c.Update(ctx, existing)
}

// resolveZoneRefFromAnnotations determines the CloudflareZone to use for a
// given hostname. If the annotation cloudflare.io/zone-ref is set, the named
// zone is fetched from sourceNs (or from the namespace specified by
// cloudflare.io/zone-ref-namespace). Otherwise the zone with the longest
// suffix-match against representativeHost is selected from all cluster zones.
func resolveZoneRefFromAnnotations(
	ctx context.Context,
	c client.Client,
	sourceNs string,
	ann map[string]string,
	representativeHost string,
) (*cloudflarev1alpha1.CloudflareZone, error) {
	if zoneName := ann[AnnotationZoneRef]; zoneName != "" {
		ns := firstNonEmpty(ann[AnnotationZoneRefNamespace], sourceNs)
		var zone cloudflarev1alpha1.CloudflareZone
		if err := c.Get(ctx, types.NamespacedName{Name: zoneName, Namespace: ns}, &zone); err != nil {
			return nil, fmt.Errorf("CloudflareZone %s/%s: %w", ns, zoneName, err)
		}
		return &zone, nil
	}
	// Fall back to longest-suffix match.
	zones, err := ListZonesClusterWide(ctx, c)
	if err != nil {
		return nil, err
	}
	return ResolveZoneForHostname(representativeHost, zones)
}

// resolveTunnelCNAME looks up the CloudflareTunnel named by the annotation
// cloudflare.io/target (tunnel:<name>) in the given namespace (overridable via
// cloudflare.io/tunnel-ref-namespace). It returns the tunnel's CNAME, the
// tunnel's ready state, and any error.
//
// Returns (cname, ready, nil) on success.
// Returns ("", false, nil) when the tunnel exists but has no CNAME yet (not ready).
// Returns ("", false, err) when the tunnel cannot be fetched.
func resolveTunnelCNAME(
	ctx context.Context,
	c client.Client,
	sourceNs string,
	ann map[string]string,
	tunnelName string,
) (string, bool, error) {
	ns := firstNonEmpty(ann[AnnotationTunnelRefNamespace], sourceNs)
	var tunnel cloudflarev1alpha1.CloudflareTunnel
	if err := c.Get(ctx, types.NamespacedName{Name: tunnelName, Namespace: ns}, &tunnel); err != nil {
		if errors.IsNotFound(err) {
			return "", false, fmt.Errorf("CloudflareTunnel %s/%s not found", ns, tunnelName)
		}
		return "", false, fmt.Errorf("get CloudflareTunnel %s/%s: %w", ns, tunnelName, err)
	}
	if tunnel.Status.TunnelCNAME == "" {
		return "", false, nil
	}
	return tunnel.Status.TunnelCNAME, true, nil
}

// ttlFromAnnotation parses the cloudflare.io/ttl annotation value as an int.
// Returns 1 (Cloudflare's "automatic" TTL) when the annotation is absent or
// the value is not a valid integer.
func ttlFromAnnotation(ann map[string]string) int {
	raw, ok := ann[AnnotationTTL]
	if !ok || raw == "" {
		return 1
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return 1
	}
	return v
}
