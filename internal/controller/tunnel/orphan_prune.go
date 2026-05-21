/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnel

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// pruneOrphanedDNSRecords deletes operator-emitted CloudflareDNSRecord CRs
// that a source previously emitted but no longer wants, AND old-form CRs
// whose metadata.Name does not match emittedDNSRecordName(Spec.Name) — the
// latter is the S4 / #6 self-migration path from the legacy
// `<source>-<host>-<hash>` doubled shape to the new `<host>-<hash>` shape.
// Both deletion modes share the same provably-own filter (the three
// source-identity labels), so user-authored CRs and CRs owned by a
// different source are never touched.
//
// It lists all CRs in sourceNamespace that carry the three source-identity
// labels (conventions.LabelSourceKind, conventions.LabelSourceName,
// conventions.LabelSourceNamespace) matching the provided values, then
// deletes any whose (Spec.Name not in desired) OR
// (metadata.Name != emittedDNSRecordName(Spec.Name)).
//
// Label scope: the three-key selector means a CR owned by a different
// source kind, name, or namespace is invisible to this helper and will
// never be touched. Likewise, a user-authored CR missing these labels is
// invisible.
//
// Racing deletions: Delete is wrapped in client.IgnoreNotFound, so a CR
// that another actor already removed between List and Delete is treated as
// already gone — not as an error.
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
		if _, hostnameDesired := desired[cr.Spec.Name]; hostnameDesired && cr.Name == emittedDNSRecordName(cr.Spec.Name) {
			continue
		}
		// Either:
		//   - hostname no longer desired (existing orphan behavior), or
		//   - hostname desired but CR is old-form (S4 / #6 migration GC) —
		//     the new-form CR is emitted by the source reconciler in the
		//     same Reconcile pass (before this helper runs), so the
		//     replacement is already in flight. S1's 81053-as-relist-verify
		//     absorbs the brief CF-side overlap.
		if err := client.IgnoreNotFound(c.Delete(ctx, cr)); err != nil {
			return pruned, fmt.Errorf("delete orphaned DNSRecord %s: %w", cr.Name, err)
		}
		pruned = append(pruned, cr.Spec.Name)
	}
	return pruned, nil
}
