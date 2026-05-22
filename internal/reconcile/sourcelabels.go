/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package reconcile

import (
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// ErrPartialSourceLabels is the hard-fail returned by VerifySourceLabels when
// some-but-not-all of the source-* labels are present (Foundation §7).
var ErrPartialSourceLabels = errors.New("partial source labels: all three of source-kind, source-name, source-namespace are required when any are set")

// StampSourceLabels sets the three required source labels on the object.
func StampSourceLabels(obj client.Object, kind, name, namespace string) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[conventions.LabelSourceKind] = kind
	labels[conventions.LabelSourceName] = name
	labels[conventions.LabelSourceNamespace] = namespace
	obj.SetLabels(labels)
}

// HasSourceLabels reports whether the object carries ALL THREE operator
// source labels (kind, name, namespace). Partial labelling → false (a
// half-labelled object is never treated as operator-managed; the all-or-none
// invariant is enforced on the write path by VerifySourceLabels). Used as the
// durable, orphan-state-surviving proof of operator authorship for
// cascade-GC eligibility (metadata.labels persist when ownerRefs /
// Status.AttachedSources are cleared by orphaning).
func HasSourceLabels(obj client.Object) bool {
	labels := obj.GetLabels()
	_, hasKind := labels[conventions.LabelSourceKind]
	_, hasName := labels[conventions.LabelSourceName]
	_, hasNs := labels[conventions.LabelSourceNamespace]
	return hasKind && hasName && hasNs
}

// VerifySourceLabels enforces the Foundation §7 hard-fail policy: an object
// either has all three source-* labels, or none. Partial labelling is rejected.
func VerifySourceLabels(obj client.Object) error {
	labels := obj.GetLabels()
	_, hasKind := labels[conventions.LabelSourceKind]
	_, hasName := labels[conventions.LabelSourceName]
	_, hasNs := labels[conventions.LabelSourceNamespace]

	count := 0
	for _, p := range []bool{hasKind, hasName, hasNs} {
		if p {
			count++
		}
	}
	if count == 0 || count == 3 {
		return nil
	}
	return fmt.Errorf("%w: kind=%v name=%v namespace=%v", ErrPartialSourceLabels, hasKind, hasName, hasNs)
}
