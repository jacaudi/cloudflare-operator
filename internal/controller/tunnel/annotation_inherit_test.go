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
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

func TestInheritAnnotation_RouteWins(t *testing.T) {
	route := map[string]string{conventions.AnnotationAdopt: "true"}
	gw := &gwv1.Gateway{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{conventions.AnnotationAdopt: "false"}}}
	require.Equal(t, "true", inheritAnnotation(route, gw, conventions.AnnotationAdopt))
}

func TestInheritAnnotation_FallsThroughToGateway(t *testing.T) {
	route := map[string]string{}
	gw := &gwv1.Gateway{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{conventions.AnnotationAdopt: "true"}}}
	require.Equal(t, "true", inheritAnnotation(route, gw, conventions.AnnotationAdopt))
}

func TestInheritAnnotation_EmptyRouteValueFallsThroughToGateway(t *testing.T) {
	// Route key present but explicitly empty must fall through to the
	// Gateway value (the "set AND non-empty" half of the precedence rule).
	route := map[string]string{conventions.AnnotationAdopt: ""}
	gw := &gwv1.Gateway{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{conventions.AnnotationAdopt: "true"}}}
	require.Equal(t, "true", inheritAnnotation(route, gw, conventions.AnnotationAdopt))
}

func TestInheritAnnotation_EmptyOnBothIsEmpty(t *testing.T) {
	require.Equal(t, "", inheritAnnotation(map[string]string{}, &gwv1.Gateway{}, conventions.AnnotationAdopt))
}

func TestInheritAnnotation_NilGatewayTolerated(t *testing.T) {
	route := map[string]string{conventions.AnnotationAdopt: "true"}
	require.Equal(t, "true", inheritAnnotation(route, nil, conventions.AnnotationAdopt))
}

func TestInheritedAnnotations_MergesFamily(t *testing.T) {
	route := map[string]string{conventions.AnnotationAdopt: "true"}
	gw := &gwv1.Gateway{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		conventions.AnnotationAdopt:   "false", // route wins
		conventions.AnnotationZoneRef: "shared-zone",
	}}}
	got := inheritedAnnotations(route, gw)
	require.Equal(t, "true", got[conventions.AnnotationAdopt], "route override wins")
	require.Equal(t, "shared-zone", got[conventions.AnnotationZoneRef], "gateway value inherited when route unset")
}

// T1.10 — `inheritedAnnotations` must now propagate cloudflare.io/scheme so the
// HTTPRoute source controller can read the effective scheme override from the
// merged map (route value wins, otherwise Gateway value).
func TestInheritedAnnotations_PropagatesScheme(t *testing.T) {
	t.Run("from_gateway", func(t *testing.T) {
		route := map[string]string{}
		gw := &gwv1.Gateway{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			conventions.AnnotationScheme: "https",
		}}}
		got := inheritedAnnotations(route, gw)
		require.Equal(t, "https", got[conventions.AnnotationScheme], "scheme inherited from Gateway")
	})
	t.Run("route_overrides_gateway", func(t *testing.T) {
		route := map[string]string{conventions.AnnotationScheme: "http"}
		gw := &gwv1.Gateway{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			conventions.AnnotationScheme: "https",
		}}}
		got := inheritedAnnotations(route, gw)
		require.Equal(t, "http", got[conventions.AnnotationScheme], "route scheme overrides Gateway")
	})
}

// T1.11 — empty annotation map yields a zero-value Defaults (both pointer
// fields nil, CAPoolPath untouched).
func TestDefaultsFromAnnotations_EmptyIsZero(t *testing.T) {
	got := defaultsFromAnnotations(map[string]string{})
	require.Equal(t, tunnelsynth.Defaults{}, got, "empty annotations -> zero Defaults")
	require.Nil(t, got.NoTLSVerifyDefault)
	require.Nil(t, got.OriginServerNameDefault)
	require.Equal(t, "", got.CAPoolPath, "defaultsFromAnnotations must never set CAPoolPath")
}

// T1.12 — defaultsFromAnnotations parses truthy values via conventions.ParseTruthy.
// Unrecognized values (per ParseTruthy contract: empty, "1", "0", arbitrary
// strings) leave NoTLSVerifyDefault nil.
func TestDefaultsFromAnnotations_ParsesNoTLSVerify(t *testing.T) {
	t.Run("true", func(t *testing.T) {
		got := defaultsFromAnnotations(map[string]string{conventions.AnnotationNoTLSVerify: "true"})
		require.NotNil(t, got.NoTLSVerifyDefault)
		require.True(t, *got.NoTLSVerifyDefault)
	})
	t.Run("yes_case_insensitive", func(t *testing.T) {
		got := defaultsFromAnnotations(map[string]string{conventions.AnnotationNoTLSVerify: "YES"})
		require.NotNil(t, got.NoTLSVerifyDefault)
		require.True(t, *got.NoTLSVerifyDefault)
	})
	t.Run("false", func(t *testing.T) {
		got := defaultsFromAnnotations(map[string]string{conventions.AnnotationNoTLSVerify: "false"})
		require.NotNil(t, got.NoTLSVerifyDefault)
		require.False(t, *got.NoTLSVerifyDefault)
	})
	t.Run("garbage_leaves_nil", func(t *testing.T) {
		got := defaultsFromAnnotations(map[string]string{conventions.AnnotationNoTLSVerify: "maybe"})
		require.Nil(t, got.NoTLSVerifyDefault, "unparseable value must NOT populate the default")
	})
	t.Run("numeric_1_leaves_nil", func(t *testing.T) {
		// ParseTruthy explicitly rejects "1"/"0" — see conventions.ParseTruthy doc.
		got := defaultsFromAnnotations(map[string]string{conventions.AnnotationNoTLSVerify: "1"})
		require.Nil(t, got.NoTLSVerifyDefault)
	})
}

// T1.13 — origin-server-name is read verbatim (no parsing, no trimming beyond
// what the user typed). Empty string leaves the default nil.
func TestDefaultsFromAnnotations_OriginServerNameVerbatim(t *testing.T) {
	t.Run("verbatim", func(t *testing.T) {
		got := defaultsFromAnnotations(map[string]string{conventions.AnnotationOriginServerName: "external.example.com"})
		require.NotNil(t, got.OriginServerNameDefault)
		require.Equal(t, "external.example.com", *got.OriginServerNameDefault)
	})
	t.Run("empty_leaves_nil", func(t *testing.T) {
		got := defaultsFromAnnotations(map[string]string{conventions.AnnotationOriginServerName: ""})
		require.Nil(t, got.OriginServerNameDefault)
	})
}
