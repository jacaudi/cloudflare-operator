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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

func TestDeriveTunnelName_WithName(t *testing.T) {
	got, err := DeriveTunnelName("app-foo", "payments")
	require.NoError(t, err)
	require.Equal(t, "cf-app-foo-payments", got)
}

func TestDeriveTunnelName_WithoutName_PerNamespacePool(t *testing.T) {
	got, err := DeriveTunnelName("app-foo", "")
	require.NoError(t, err)
	require.Equal(t, "cf-app-foo", got)
}

func TestDeriveTunnelName_NameTooLong(t *testing.T) {
	// 52-char cap on the resulting CR name.
	_, err := DeriveTunnelName("very-very-long-namespace-name", "very-very-long-tunnel-name-here")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNameTooLong))
}

func TestDeriveTunnelName_InvalidNamespace(t *testing.T) {
	_, err := DeriveTunnelName("Bad_Namespace", "ok")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidName))
}

func TestDeriveTunnelName_InvalidAnnotation(t *testing.T) {
	_, err := DeriveTunnelName("ok", "Has Space")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidName))
}

func TestDeriveTunnelName_BoundaryAt52(t *testing.T) {
	// 52 exactly is OK; 53 is not. cf- (3) + ns (21) + - (1) + nm (27) = 52.
	ns := "namespace-name-twelve"       // 21
	nm := "tunnel-name-twenty-seven-ok" // 27
	got, err := DeriveTunnelName(ns, nm)
	require.NoError(t, err)
	require.Len(t, got, 52)

	// One char more pushes us to 53 → ErrNameTooLong.
	_, err = DeriveTunnelName(ns, nm+"k")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNameTooLong))
}

// TestParseGatewayServiceRef exercises every parse branch of the
// cloudflare.io/gateway-service annotation parser. The function lives in the
// tunnel package (T12 extraction); call it directly.
//
// Implementation reference (attach.go): port must be 1..65535 and numeric; the
// hostPart must be either "<name>" (uses defaultNS) or "<ns>/<name>" with both
// halves non-empty. Empty raw is rejected.
func TestParseGatewayServiceRef(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		defaultNS string
		wantNS    string
		wantName  string
		wantPort  int32
		wantErr   bool
	}{
		// Happy paths — every supported annotation form.
		{"bare name with defaultNS", "svc", "default", "default", "svc", 0, false},
		{"bare name with port", "svc:8080", "default", "default", "svc", 8080, false},
		{"ns slash name", "ns1/svc", "default", "ns1", "svc", 0, false},
		{"ns slash name with port", "ns1/svc:8080", "default", "ns1", "svc", 8080, false},
		{"port boundary 1", "svc:1", "default", "default", "svc", 1, false},
		{"port boundary 65535", "svc:65535", "default", "default", "svc", 65535, false},

		// Error paths — port validation.
		{"invalid port non-numeric", "ns1/svc:abc", "default", "", "", 0, true},
		{"invalid port out of range high", "ns1/svc:70000", "default", "", "", 0, true},
		{"invalid port zero", "ns1/svc:0", "default", "", "", 0, true},
		{"invalid port negative", "ns1/svc:-1", "default", "", "", 0, true},
		{"empty port after colon", "ns1/svc:", "default", "", "", 0, true},

		// Error paths — malformed host part.
		// strings.Cut splits at the FIRST occurrence, so "ns/" yields ns="ns",
		// nm="" which the parser rejects as malformed.
		{"trailing slash empty name", "ns1/", "default", "", "", 0, true},
		// "/<name>" yields ns="", nm="name" → malformed.
		{"leading slash empty namespace", "/svc", "default", "", "", 0, true},
		// Empty raw → strings.Cut returns hostPart="" with no port; the parser
		// rejects it via the explicit hostPart=="" guard.
		{"empty raw", "", "default", "", "", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ns, name, port, err := parseGatewayServiceRef(c.raw, c.defaultNS)
			if c.wantErr {
				require.Error(t, err)
				// On error, the parser returns the zero value for every
				// out-parameter — confirms the "half-set port" MINOR is fixed.
				require.Equal(t, "", ns)
				require.Equal(t, "", name)
				require.Equal(t, int32(0), port)
				return
			}
			require.NoError(t, err)
			require.Equal(t, c.wantNS, ns)
			require.Equal(t, c.wantName, name)
			require.Equal(t, c.wantPort, port)
		})
	}
}

func TestEnsureTunnelCR_StampsAutoCreatedAnnotation(t *testing.T) {
	s := tunnelScheme(t)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns", UID: "uid-svc"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(svc).Build()

	tn, err := EnsureTunnelCR(context.Background(), c, s, svc, "Service", "cf-ns", v1alpha1.ConnectorSpec{
		Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
	})
	require.NoError(t, err)
	require.Equal(t, "true", tn.Annotations[conventions.AnnotationAutoCreated],
		"newly-created tunnel CR must carry the auto-created marker")
}

func TestIsAutoCreated_AnnotationTrue(t *testing.T) {
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			conventions.AnnotationAutoCreated: "true",
		}},
	}
	require.True(t, isAutoCreated(tn))
}

func TestIsAutoCreated_AnnotationAbsent(t *testing.T) {
	tn := &v1alpha1.CloudflareTunnel{}
	require.False(t, isAutoCreated(tn), "absent annotation must default to direct-create (no GC)")
}

func TestIsAutoCreated_AnnotationOtherValue(t *testing.T) {
	for _, v := range []string{"false", "", "1", "yes", "gibberish"} {
		tn := &v1alpha1.CloudflareTunnel{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				conventions.AnnotationAutoCreated: v,
			}},
		}
		require.False(t, isAutoCreated(tn), "value %q should NOT count as auto-created", v)
	}
}

func TestEnsureTunnelCR_AdoptPathDoesNotStampAutoCreatedAnnotation(t *testing.T) {
	s := tunnelScheme(t)
	existing := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-ns", Namespace: "ns"},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns", UID: "uid-svc"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(existing, svc).Build()

	tn, err := EnsureTunnelCR(context.Background(), c, s, svc, "Service", "cf-ns", v1alpha1.ConnectorSpec{
		Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
	})
	require.NoError(t, err)
	require.Empty(t, tn.Annotations[conventions.AnnotationAutoCreated],
		"adopt path must NOT retroactively stamp the annotation (no backfill)")
}

func TestNeedsOwnerTransfer_OwnerGoneSourcesExist(t *testing.T) {
	// Auto-created marker present: needsOwnerTransfer is isAutoCreated-gated,
	// so the TRUE path requires the annotation in addition to no owner +
	// attaching sources (a direct-create CR is never owner-transferred —
	// see TestNeedsOwnerTransfer_DirectCreateNeverTransfers).
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
		},
		Status: v1alpha1.CloudflareTunnelStatus{
			AttachedSources: []v1alpha1.AttachedSource{{Kind: "Service", Name: "a", Namespace: "ns"}},
		},
	}
	require.True(t, needsOwnerTransfer(tn))
}

func TestNeedsOwnerTransfer_DirectCreateNeverTransfers(t *testing.T) {
	// No cloudflare.io/auto-created annotation: a user-authored direct-create
	// tunnel must never be owner-transferred (else a Service becomes its
	// k8s-controller-owner and GC deletes the user's CR — design §7).
	tn := &v1alpha1.CloudflareTunnel{
		Status: v1alpha1.CloudflareTunnelStatus{
			AttachedSources: []v1alpha1.AttachedSource{{Kind: "Service", Name: "a", Namespace: "ns"}},
		},
	}
	require.False(t, needsOwnerTransfer(tn),
		"direct-create CR (no auto-created marker) must never need owner-transfer")
}

func TestNeedsOwnerTransfer_OwnerExists(t *testing.T) {
	// Auto-created marker present so the isAutoCreated gate is satisfied —
	// the discriminating clause under test is "owner present blocks transfer",
	// not the auto-created gate.
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Annotations:     map[string]string{conventions.AnnotationAutoCreated: "true"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "Service", Name: "a", UID: "uid"}},
		},
		Status: v1alpha1.CloudflareTunnelStatus{
			AttachedSources: []v1alpha1.AttachedSource{{Kind: "Service", Name: "a", Namespace: "ns"}},
		},
	}
	require.False(t, needsOwnerTransfer(tn), "owner present → no transfer needed (auto-created gate satisfied)")
}

func TestNeedsOwnerTransfer_OwnerGoneNoSources(t *testing.T) {
	// Auto-created marker present so the isAutoCreated gate is satisfied —
	// the discriminating clause under test is "no sources → delegated to
	// isOrphaned", not the auto-created gate.
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
		},
	}
	require.False(t, needsOwnerTransfer(tn), "no sources → delegated to isOrphaned, not transfer")
}

func TestIsOrphaned_AutoCreatedAllConditionsMet(t *testing.T) {
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
		},
	}
	require.True(t, isOrphaned(tn))
}

func TestIsOrphaned_DirectCreateNeverOrphaned(t *testing.T) {
	tn := &v1alpha1.CloudflareTunnel{} // no annotation
	require.False(t, isOrphaned(tn),
		"direct-create CRs are never orphaned regardless of OwnerReferences or AttachedSources state")
}

func TestIsOrphaned_AutoCreatedWithSourcesNotOrphaned(t *testing.T) {
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
		},
		Status: v1alpha1.CloudflareTunnelStatus{
			AttachedSources: []v1alpha1.AttachedSource{{Kind: "Service", Name: "a", Namespace: "ns"}},
		},
	}
	require.False(t, isOrphaned(tn), "sources present → not orphaned, may need transfer")
}

func TestIsOrphaned_AutoCreatedWithOwnerNotOrphaned(t *testing.T) {
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Annotations:     map[string]string{conventions.AnnotationAutoCreated: "true"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "Service", Name: "a", UID: "uid"}},
		},
	}
	require.False(t, isOrphaned(tn), "owner ref present → not orphaned")
}

func TestPredicates_MutuallyExclusive(t *testing.T) {
	for _, tn := range []*v1alpha1.CloudflareTunnel{
		{Status: v1alpha1.CloudflareTunnelStatus{AttachedSources: []v1alpha1.AttachedSource{{Kind: "Service", Name: "a"}}}},
		{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"}}},
		{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"}, OwnerReferences: []metav1.OwnerReference{{UID: "u"}}}},
		// 4th case is THE discriminator: auto-created + sources + no owners —
		// the only state where needsOwnerTransfer is true (so nt&&io is not
		// vacuously false here), and the only state where a regression dropping
		// isOrphaned's AttachedSources==0 clause would make both predicates
		// return true simultaneously. Cases 1-3 yield needsOwnerTransfer=false
		// (case 1 lacks the auto-created marker; cases 2-3 have no sources), so
		// nt&&io is trivially false for them — keep this 4th case so the test
		// stays non-vacuous now that needsOwnerTransfer is isAutoCreated-gated.
		{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
			},
			Status: v1alpha1.CloudflareTunnelStatus{
				AttachedSources: []v1alpha1.AttachedSource{{Kind: "Service", Name: "a"}},
			},
		},
	} {
		nt, io := needsOwnerTransfer(tn), isOrphaned(tn)
		require.False(t, nt && io, "needsOwnerTransfer + isOrphaned cannot both be true: %+v", tn.Status)
	}
}

func makeAttachedSource(kind, ns, name string) v1alpha1.AttachedSource {
	return v1alpha1.AttachedSource{Kind: kind, Namespace: ns, Name: name}
}

func TestTransferOwnership_PicksLexSmallestLive(t *testing.T) {
	s := tunnelScheme(t)
	svcA := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "a-svc", Namespace: "ns", UID: "uid-a"}}
	svcB := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "b-svc", Namespace: "ns", UID: "uid-b"}}
	svcC := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "c-svc", Namespace: "ns", UID: "uid-c"}}
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", UID: "uid-tnl",
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"}},
		Status: v1alpha1.CloudflareTunnelStatus{AttachedSources: []v1alpha1.AttachedSource{
			makeAttachedSource("Service", "ns", "c-svc"),
			makeAttachedSource("Service", "ns", "a-svc"),
			makeAttachedSource("Service", "ns", "b-svc"),
		}},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(svcA, svcB, svcC, tn).Build()
	rec := record.NewFakeRecorder(10)
	transferred, err := TransferOwnershipIfNeeded(context.Background(), c, s, tn, rec)
	require.NoError(t, err)
	require.True(t, transferred)
	var got v1alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got))
	require.Len(t, got.OwnerReferences, 1)
	require.Equal(t, "a-svc", got.OwnerReferences[0].Name)
	require.Equal(t, types.UID("uid-a"), got.OwnerReferences[0].UID)
	require.NotNil(t, got.OwnerReferences[0].Controller)
	require.True(t, *got.OwnerReferences[0].Controller)
	// In-memory tn must reflect the post-patch owner refs for the caller.
	require.Len(t, tn.OwnerReferences, 1)
	require.Equal(t, "a-svc", tn.OwnerReferences[0].Name)
	require.Equal(t, types.UID("uid-a"), tn.OwnerReferences[0].UID)
}

func TestTransferOwnership_SkipsCandidatesWithDeletionTimestamp(t *testing.T) {
	s := tunnelScheme(t)
	now := metav1.Now()
	svcA := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "a-svc", Namespace: "ns", UID: "uid-a",
		DeletionTimestamp: &now, Finalizers: []string{"keep-alive-for-test"}}}
	svcB := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "b-svc", Namespace: "ns", UID: "uid-b"}}
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", UID: "uid-tnl",
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"}},
		Status: v1alpha1.CloudflareTunnelStatus{AttachedSources: []v1alpha1.AttachedSource{
			makeAttachedSource("Service", "ns", "a-svc"),
			makeAttachedSource("Service", "ns", "b-svc"),
		}},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(svcA, svcB, tn).Build()
	rec := record.NewFakeRecorder(10)
	transferred, err := TransferOwnershipIfNeeded(context.Background(), c, s, tn, rec)
	require.NoError(t, err)
	require.True(t, transferred)
	var got v1alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got))
	require.Equal(t, "b-svc", got.OwnerReferences[0].Name, "lex-smallest live (non-deleting) candidate")
}

func TestTransferOwnership_AllCandidatesNotFound(t *testing.T) {
	s := tunnelScheme(t)
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", UID: "uid-tnl"},
		Status: v1alpha1.CloudflareTunnelStatus{AttachedSources: []v1alpha1.AttachedSource{
			makeAttachedSource("Service", "ns", "a-svc"),
			makeAttachedSource("Service", "ns", "b-svc"),
		}},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).Build()
	rec := record.NewFakeRecorder(10)
	transferred, err := TransferOwnershipIfNeeded(context.Background(), c, s, tn, rec)
	require.NoError(t, err, "all-NotFound is not an error; next reconcile retries with stable state")
	require.False(t, transferred)
	var got v1alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got))
	require.Empty(t, got.OwnerReferences)
}

func TestTransferOwnership_EmitsOwnerTransferredEvent(t *testing.T) {
	s := tunnelScheme(t)
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "a-svc", Namespace: "ns", UID: "uid-a"}}
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", UID: "uid-tnl"},
		Status: v1alpha1.CloudflareTunnelStatus{AttachedSources: []v1alpha1.AttachedSource{
			makeAttachedSource("Service", "ns", "a-svc")}},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(svc, tn).Build()
	rec := record.NewFakeRecorder(10)
	_, err := TransferOwnershipIfNeeded(context.Background(), c, s, tn, rec)
	require.NoError(t, err)
	select {
	case ev := <-rec.Events:
		require.Contains(t, ev, conventions.ReasonOwnerTransferred)
		require.Contains(t, ev, "a-svc")
	default:
		t.Fatal("expected OwnerTransferred event")
	}
}

// --- branch-inventory tests beyond the 4 plan tests ---

// Conflict on the optimistic-lock Patch is the optimistic-lock contract, NOT
// an error: return (false, nil) so the next reconcile retries with a fresh
// ResourceVersion.
func TestTransferOwnership_ConflictReturnsFalseNil(t *testing.T) {
	s := tunnelScheme(t)
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "a-svc", Namespace: "ns", UID: "uid-a"}}
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", UID: "uid-tnl"},
		Status: v1alpha1.CloudflareTunnelStatus{AttachedSources: []v1alpha1.AttachedSource{
			makeAttachedSource("Service", "ns", "a-svc")}},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(svc, tn).Build()
	c := interceptor.NewClient(base, interceptor.Funcs{
		Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
			return apierrors.NewConflict(schema.GroupResource{Group: "cloudflare.io", Resource: "cloudflaretunnels"}, "tnl", errors.New("stale"))
		},
	})
	rec := record.NewFakeRecorder(10)
	transferred, err := TransferOwnershipIfNeeded(context.Background(), c, s, tn, rec)
	require.NoError(t, err, "Conflict is the optimistic-lock contract, not an error")
	require.False(t, transferred)
}

// A non-Conflict Patch error propagates as (false, err) wrapped with the
// "patch ownerReferences" context.
func TestTransferOwnership_GenericPatchErrorPropagates(t *testing.T) {
	s := tunnelScheme(t)
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "a-svc", Namespace: "ns", UID: "uid-a"}}
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", UID: "uid-tnl"},
		Status: v1alpha1.CloudflareTunnelStatus{AttachedSources: []v1alpha1.AttachedSource{
			makeAttachedSource("Service", "ns", "a-svc")}},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(svc, tn).Build()
	c := interceptor.NewClient(base, interceptor.Funcs{
		Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
			return errors.New("boom")
		},
	})
	rec := record.NewFakeRecorder(10)
	transferred, err := TransferOwnershipIfNeeded(context.Background(), c, s, tn, rec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "patch ownerReferences")
	require.False(t, transferred)
}

// A nil recorder must not panic on the success path (recorder is guarded).
func TestTransferOwnership_NilRecorderNoPanic(t *testing.T) {
	s := tunnelScheme(t)
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "a-svc", Namespace: "ns", UID: "uid-a"}}
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", UID: "uid-tnl"},
		Status: v1alpha1.CloudflareTunnelStatus{AttachedSources: []v1alpha1.AttachedSource{
			makeAttachedSource("Service", "ns", "a-svc")}},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(svc, tn).Build()
	transferred, err := TransferOwnershipIfNeeded(context.Background(), c, s, tn, nil)
	require.NoError(t, err)
	require.True(t, transferred)
}

// An unknown source Kind in AttachedSources is a config/programming error:
// getSourceObject returns an error which propagates as (false, err).
func TestTransferOwnership_UnknownKindErrors(t *testing.T) {
	s := tunnelScheme(t)
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", UID: "uid-tnl"},
		Status: v1alpha1.CloudflareTunnelStatus{AttachedSources: []v1alpha1.AttachedSource{
			makeAttachedSource("Widget", "ns", "w1")}},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).Build()
	rec := record.NewFakeRecorder(10)
	transferred, err := TransferOwnershipIfNeeded(context.Background(), c, s, tn, rec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown source kind")
	require.False(t, transferred)
}

// First candidate NotFound, second candidate live: confirms NotFound skips to
// the next candidate rather than aborting the whole transfer.
func TestTransferOwnership_NotFoundSkipsToNextLive(t *testing.T) {
	s := tunnelScheme(t)
	// "a-svc" is NOT created (NotFound); "b-svc" is live.
	svcB := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "b-svc", Namespace: "ns", UID: "uid-b"}}
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", UID: "uid-tnl",
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"}},
		Status: v1alpha1.CloudflareTunnelStatus{AttachedSources: []v1alpha1.AttachedSource{
			makeAttachedSource("Service", "ns", "a-svc"),
			makeAttachedSource("Service", "ns", "b-svc"),
		}},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(svcB, tn).Build()
	rec := record.NewFakeRecorder(10)
	transferred, err := TransferOwnershipIfNeeded(context.Background(), c, s, tn, rec)
	require.NoError(t, err)
	require.True(t, transferred)
	var got v1alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got))
	require.Equal(t, "b-svc", got.OwnerReferences[0].Name, "NotFound on a-svc skips to live b-svc")
}
