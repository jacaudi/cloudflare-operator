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
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func pruneScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v2alpha1.AddToScheme(s))
	return s
}

func makeEmittedRecord(svcName, hostname string) *v2alpha1.CloudflareDNSRecord {
	return makeEmittedRecordInSourceNS(svcName, hostname, "ns")
}

// makeEmittedRecordInSourceNS builds a record whose Kubernetes namespace is
// always "ns" (so client.InNamespace("ns") never filters it) but whose
// conventions.LabelSourceNamespace label is sourceNS. This isolates the
// label-key behaviour of the prune selector from the InNamespace behaviour.
func makeEmittedRecordInSourceNS(svcName, hostname, sourceNS string) *v2alpha1.CloudflareDNSRecord {
	return &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      emittedDNSRecordName(svcName, hostname),
			Namespace: "ns",
			Labels: map[string]string{
				conventions.LabelSourceKind:      "Service",
				conventions.LabelSourceName:      svcName,
				conventions.LabelSourceNamespace: sourceNS,
			},
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Type: "CNAME",
			Name: hostname,
		},
	}
}

func TestPruneOrphanedDNSRecords_DeletesNonDesired(t *testing.T) {
	s := pruneScheme(t)
	keep := makeEmittedRecord("svc", "keep.example.com")
	drop := makeEmittedRecord("svc", "drop.example.com")

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(keep, drop).Build()

	desired := map[string]struct{}{"keep.example.com": {}}
	deleted, err := pruneOrphanedDNSRecords(context.Background(), c, "Service", "svc", "ns", desired)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"drop.example.com"}, deleted)

	// keep.example.com CR must still exist.
	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(keep), &got))

	// drop.example.com CR must be gone.
	var gone v2alpha1.CloudflareDNSRecord
	err = c.Get(context.Background(), client.ObjectKeyFromObject(drop), &gone)
	require.True(t, apierrors.IsNotFound(err), "expected NotFound, got %v", err)
}

func TestPruneOrphanedDNSRecords_RespectsLabelScope(t *testing.T) {
	s := pruneScheme(t)
	mine := makeEmittedRecord("svc", "mine.example.com")
	// Record owned by a different source (LabelSourceName=other).
	theirs := makeEmittedRecord("other", "theirs.example.com")

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(mine, theirs).Build()

	// Empty desired — prune everything owned by "svc".
	desired := map[string]struct{}{}
	deleted, err := pruneOrphanedDNSRecords(context.Background(), c, "Service", "svc", "ns", desired)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"mine.example.com"}, deleted)

	// theirs.example.com must still exist — it belongs to a different source.
	var surviving v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(theirs), &surviving))
}

// TestPruneOrphanedDNSRecords_RespectsNamespaceLabelScope locks in that the
// conventions.LabelSourceNamespace selector key is load-bearing. Both seeded
// records live in Kubernetes namespace "ns" (so client.InNamespace("ns") does
// NOT filter either out); they differ only on the LabelSourceNamespace label
// value. If a regression dropped that key from the production MatchingLabels
// selector, the cross-namespace-labelled CR would be wrongly pruned and this
// test would fail.
func TestPruneOrphanedDNSRecords_RespectsNamespaceLabelScope(t *testing.T) {
	s := pruneScheme(t)
	// Same kind+name (Service/svc), same k8s namespace ("ns"), but the
	// source-namespace LABEL says it belongs to a source in "other-ns".
	crossNS := makeEmittedRecordInSourceNS("svc", "cross-ns.example.com", "other-ns")
	// In-scope record — should be pruned.
	inScope := makeEmittedRecordInSourceNS("svc", "in-scope.example.com", "ns")

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(crossNS, inScope).Build()

	desired := map[string]struct{}{}
	deleted, err := pruneOrphanedDNSRecords(context.Background(), c, "Service", "svc", "ns", desired)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"in-scope.example.com"}, deleted)

	// The cross-namespace-labelled CR must survive.
	var surviving v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(crossNS), &surviving))

	// The in-scope CR must be gone.
	var gone v2alpha1.CloudflareDNSRecord
	err = c.Get(context.Background(), client.ObjectKeyFromObject(inScope), &gone)
	require.True(t, apierrors.IsNotFound(err), "expected NotFound, got %v", err)
}

func TestPruneOrphanedDNSRecords_EmptyExistingIsNoOp(t *testing.T) {
	s := pruneScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	desired := map[string]struct{}{"some.example.com": {}}
	deleted, err := pruneOrphanedDNSRecords(context.Background(), c, "Service", "svc", "ns", desired)
	require.NoError(t, err)
	require.Empty(t, deleted)
}

func TestPruneOrphanedDNSRecords_IgnoresRecordDeletedBetweenListAndDelete(t *testing.T) {
	s := pruneScheme(t)
	// racing is present at List time (the helper WILL see it and decide to
	// delete it), but a concurrent actor removes it in the window between the
	// helper's List and its Delete. keep proves the iteration over the
	// remaining records is unaffected by the swallowed NotFound.
	racing := makeEmittedRecord("svc", "race.example.com")
	keep := makeEmittedRecord("svc", "keep.example.com")

	base := fake.NewClientBuilder().WithScheme(s).WithObjects(racing, keep).Build()
	c := interceptor.NewClient(base, interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if obj.GetName() == racing.Name {
				// First delegated Delete actually removes it (the concurrent
				// actor's deletion taking effect); the second returns the
				// genuine apierrors NotFound the store now produces — exactly
				// what the helper's c.Delete observes mid-iteration.
				_ = cl.Delete(ctx, obj, opts...)
				return cl.Delete(ctx, obj, opts...)
			}
			return cl.Delete(ctx, obj, opts...)
		},
	})

	// Desired excludes race.example.com, so the helper lists it, attempts the
	// Delete, and must tolerate the NotFound surfaced by the interceptor.
	desired := map[string]struct{}{"keep.example.com": {}}
	_, err := pruneOrphanedDNSRecords(context.Background(), c, "Service", "svc", "ns", desired)
	require.NoError(t, err, "a deletion racing between List and Delete must be swallowed")

	// The desired record was untouched by the prune and is still Gettable.
	var surviving v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(keep), &surviving))

	// The racing record is gone (the concurrent actor removed it; the helper
	// did not resurrect it).
	var gone v2alpha1.CloudflareDNSRecord
	err = c.Get(context.Background(), client.ObjectKeyFromObject(racing), &gone)
	require.True(t, apierrors.IsNotFound(err), "expected NotFound, got %v", err)
}
