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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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
	tn := &v1alpha1.CloudflareTunnel{
		Status: v1alpha1.CloudflareTunnelStatus{
			AttachedSources: []v1alpha1.AttachedSource{{Kind: "Service", Name: "a", Namespace: "ns"}},
		},
	}
	require.True(t, needsOwnerTransfer(tn))
}

func TestNeedsOwnerTransfer_OwnerExists(t *testing.T) {
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{{Kind: "Service", Name: "a", UID: "uid"}},
		},
		Status: v1alpha1.CloudflareTunnelStatus{
			AttachedSources: []v1alpha1.AttachedSource{{Kind: "Service", Name: "a", Namespace: "ns"}},
		},
	}
	require.False(t, needsOwnerTransfer(tn))
}

func TestNeedsOwnerTransfer_OwnerGoneNoSources(t *testing.T) {
	tn := &v1alpha1.CloudflareTunnel{}
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
		// 4th case: auto-created + sources + no owners — the only state where a
		// regression dropping isOrphaned's AttachedSources==0 clause would make
		// both predicates return true simultaneously.
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
