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
