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
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// pruneOrphanedDNSRecords deletes operator-emitted CloudflareDNSRecord CRs that
// a source previously emitted but no longer wants. It lists all CRs in
// sourceNamespace that carry the three source-identity labels
// (conventions.LabelSourceKind, conventions.LabelSourceName,
// conventions.LabelSourceNamespace) matching the provided values, then deletes
// any whose Spec.Name is not a key in desired.
//
// Label scope: the three-key selector means a CR owned by a different source
// kind, name, or namespace is invisible to this helper and will never be
// touched. Likewise, a user-authored CR missing these labels is invisible.
//
// Racing deletions: Delete is wrapped in client.IgnoreNotFound, so a CR that
// another actor already removed between List and Delete is treated as already
// gone — not as an error.
//
// On error, the pruned slice accumulated so far is returned alongside the
// error so the caller has partial progress information.
func pruneOrphanedDNSRecords(
	ctx context.Context,
	c client.Client,
	sourceKind, sourceName, sourceNamespace string,
	desired map[string]struct{},
) ([]string, error) {
	var existing v2alpha1.CloudflareDNSRecordList
	if err := c.List(ctx, &existing,
		client.InNamespace(sourceNamespace),
		client.MatchingLabels{
			conventions.LabelSourceKind:      sourceKind,
			conventions.LabelSourceName:      sourceName,
			conventions.LabelSourceNamespace: sourceNamespace,
		},
	); err != nil {
		return nil, fmt.Errorf("list emitted DNSRecord CRs for prune: %w", err)
	}

	var pruned []string
	for i := range existing.Items {
		cr := &existing.Items[i]
		if _, ok := desired[cr.Spec.Name]; ok {
			continue
		}
		if err := client.IgnoreNotFound(c.Delete(ctx, cr)); err != nil {
			return pruned, fmt.Errorf("delete orphaned DNSRecord %s: %w", cr.Name, err)
		}
		pruned = append(pruned, cr.Spec.Name)
	}
	return pruned, nil
}
