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
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

// ResolveZoneID resolves a zone ID from either a direct zoneID string or a zoneRef.
// Returns the zone ID or an error if it cannot be resolved.
func ResolveZoneID(ctx context.Context, k8sClient client.Client, namespace, zoneID string, zoneRef *cloudflarev1alpha1.ZoneReference) (string, error) {
	if zoneID != "" {
		return zoneID, nil
	}
	if zoneRef == nil {
		return "", fmt.Errorf("one of zoneID or zoneRef is required")
	}

	var zone cloudflarev1alpha1.CloudflareZone
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      zoneRef.Name,
		Namespace: namespace,
	}, &zone); err != nil {
		return "", fmt.Errorf("failed to get CloudflareZone %q: %w", zoneRef.Name, err)
	}

	if zone.Status.ZoneID == "" {
		return "", fmt.Errorf("CloudflareZone %q does not have a zone ID yet (status: %s)", zoneRef.Name, zone.Status.Status)
	}

	return zone.Status.ZoneID, nil
}
