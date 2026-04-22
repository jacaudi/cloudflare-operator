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

	"sigs.k8s.io/controller-runtime/pkg/client"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

// ErrNoMatchingZone is returned by ResolveZoneForHostname when no
// CloudflareZone's spec.name is a suffix of the hostname.
var ErrNoMatchingZone = errors.New("no CloudflareZone matches hostname")

// ResolveZoneForHostname picks the CloudflareZone with the longest spec.name
// suffix match against hostname. If two zones are equally-specific, returns
// an error (expected in practice only when zones are literally identical; CR
// names are unique by namespace so this is rare but guarded).
func ResolveZoneForHostname(hostname string, zones []cloudflarev1alpha1.CloudflareZone) (*cloudflarev1alpha1.CloudflareZone, error) {
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	var best *cloudflarev1alpha1.CloudflareZone
	bestLen := -1
	ambiguous := false
	for i := range zones {
		z := &zones[i]
		zn := strings.ToLower(z.Spec.Name)
		if hostname == zn || strings.HasSuffix(hostname, "."+zn) {
			switch {
			case len(zn) > bestLen:
				best = z
				bestLen = len(zn)
				ambiguous = false
			case len(zn) == bestLen:
				ambiguous = true
			}
		}
	}
	if best == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoMatchingZone, hostname)
	}
	if ambiguous {
		return nil, fmt.Errorf("ambiguous zone for hostname %s", hostname)
	}
	return best, nil
}

// ListZonesClusterWide returns all CloudflareZone CRs in the cluster.
func ListZonesClusterWide(ctx context.Context, c client.Client) ([]cloudflarev1alpha1.CloudflareZone, error) {
	var list cloudflarev1alpha1.CloudflareZoneList
	if err := c.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list CloudflareZone: %w", err)
	}
	return list.Items, nil
}
