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

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func pruneScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func makeEmittedRecord(svcName, hostname string) *v1alpha1.CloudflareDNSRecord {
	return &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      emittedDNSRecordName(svcName, hostname),
			Namespace: "ns",
			Labels: map[string]string{
				conventions.LabelSourceKind:      "Service",
				conventions.LabelSourceName:      svcName,
				conventions.LabelSourceNamespace: "ns",
			},
		},
		Spec: v1alpha1.CloudflareDNSRecordSpec{
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
	var got v1alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(keep), &got))

	// drop.example.com CR must be gone.
	var gone v1alpha1.CloudflareDNSRecord
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
	var surviving v1alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(theirs), &surviving))
}

func TestPruneOrphanedDNSRecords_EmptyExistingIsNoOp(t *testing.T) {
	s := pruneScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	desired := map[string]struct{}{"some.example.com": {}}
	deleted, err := pruneOrphanedDNSRecords(context.Background(), c, "Service", "svc", "ns", desired)
	require.NoError(t, err)
	require.Empty(t, deleted)
}

func TestPruneOrphanedDNSRecords_IgnoresConcurrentlyDeletedRecord(t *testing.T) {
	s := pruneScheme(t)
	record := makeEmittedRecord("svc", "race.example.com")

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(record).Build()

	// Simulate an out-of-band deletion racing our prune — delete the target CR
	// before calling the helper.
	require.NoError(t, c.Delete(context.Background(), record))

	// Desired set excludes race.example.com — the helper would want to delete it,
	// but it's already gone. Must not error.
	desired := map[string]struct{}{}
	deleted, err := pruneOrphanedDNSRecords(context.Background(), c, "Service", "svc", "ns", desired)
	require.NoError(t, err, "a racing deletion must not cause an error")
	// The record is already gone; its hostname may or may not appear in deleted —
	// we only assert no error and that the client state is consistent.
	_ = deleted
}
