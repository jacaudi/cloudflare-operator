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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func gwv1NS(s string) *gwv1.Namespace { v := gwv1.Namespace(s); return &v }
func gwv1Kind(s string) *gwv1.Kind    { v := gwv1.Kind(s); return &v }
func gwv1Group(s string) *gwv1.Group  { v := gwv1.Group(s); return &v }

func TestIndexHTTPRouteByGatewayParent_MultiParent(t *testing.T) {
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "rt"},
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{
					{Name: "gw1"},                            // implicit Gateway kind, same ns
					{Name: "gw2", Namespace: gwv1NS("ns-b")}, // explicit cross-ns
					{Name: "svc", Kind: gwv1Kind("Service")}, // non-Gateway → skipped
				},
			},
		},
	}
	got := indexHTTPRouteByGatewayParent(rt)
	require.ElementsMatch(t, []string{"ns-a/gw1", "ns-b/gw2"}, got)
}

func TestIndexHTTPRouteByGatewayParent_NonGatewayGroupSkipped(t *testing.T) {
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "rt"},
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{
					{Name: "other", Group: gwv1Group("other.io"), Kind: gwv1Kind("Gateway")},
				},
			},
		},
	}
	require.Empty(t, indexHTTPRouteByGatewayParent(rt))
}

func TestIndexHTTPRouteByGatewayParent_WrongType(t *testing.T) {
	// Indexer receives client.Object; an unexpected type should produce nil.
	require.Nil(t, indexHTTPRouteByGatewayParent(&gwv1.Gateway{}))
}

func TestIndexTLSRouteByGatewayParent_Mirror(t *testing.T) {
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "rt"},
		Spec: gwv1a2.TLSRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw1"}},
			},
		},
	}
	require.Equal(t, []string{"ns-a/gw1"}, indexTLSRouteByGatewayParent(rt))
}

// boomRESTMapper embeds meta.RESTMapper (so it satisfies the interface) and
// overrides only RESTMapping to return a non-no-match error, simulating a
// transient discovery failure.
type boomRESTMapper struct{ meta.RESTMapper }

func (boomRESTMapper) RESTMapping(schema.GroupKind, ...string) (*meta.RESTMapping, error) {
	return nil, errors.New("boom")
}

func TestTLSRouteSupported(t *testing.T) {
	gk := schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "TLSRoute"}

	// Case 1: CRD genuinely absent → (false, nil). NewDefaultRESTMapper(nil)
	// returns a *meta.NoKindMatchError, which IsNoMatchError recognizes.
	empty := meta.NewDefaultRESTMapper(nil)
	ok, err := tlsRouteSupported(empty)
	require.NoError(t, err)
	require.False(t, ok, "expected (false, nil) when TLSRoute kind absent from RESTMapper")

	// Case 2: CRD present → (true, nil).
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: "gateway.networking.k8s.io", Version: "v1alpha2"}})
	m.Add(gk.WithVersion("v1alpha2"), meta.RESTScopeNamespace)
	ok, err = tlsRouteSupported(m)
	require.NoError(t, err)
	require.True(t, ok, "expected (true, nil) when TLSRoute kind present in RESTMapper")

	// Case 3: discovery failed with a non-no-match error → (false, non-nil).
	ok, err = tlsRouteSupported(boomRESTMapper{})
	require.Error(t, err, "expected discovery error to propagate")
	require.False(t, ok, "expected false when discovery fails")
}
