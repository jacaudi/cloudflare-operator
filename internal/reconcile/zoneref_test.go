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

package reconcile

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

func zoneScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func TestResolveZoneID_LiteralID(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(zoneScheme(t)).Build()
	res, err := ResolveZoneID(context.Background(), c, ZoneRefInputs{ZoneID: "abc123"}, "default")
	require.NoError(t, err)
	require.Equal(t, "abc123", res.ZoneID)
	require.Empty(t, res.ZoneName)
	require.Nil(t, res.ZoneObject)
}

func TestResolveZoneID_FromRef(t *testing.T) {
	zone := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "media"},
	}
	c := fake.NewClientBuilder().WithScheme(zoneScheme(t)).WithObjects(zone).Build()

	res, err := ResolveZoneID(context.Background(), c, ZoneRefInputs{
		ZoneRef: &v1alpha1.ZoneReference{Name: "test", Namespace: "media"},
	}, "default")
	require.NoError(t, err)
	require.Equal(t, "test", res.ZoneName)
	require.NotNil(t, res.ZoneObject)
}

func TestResolveZoneID_BothSetRejected(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(zoneScheme(t)).Build()
	_, err := ResolveZoneID(context.Background(), c, ZoneRefInputs{
		ZoneID:  "abc",
		ZoneRef: &v1alpha1.ZoneReference{Name: "test"},
	}, "default")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrZoneRefXOR)
}

func TestResolveZoneID_NeitherSetRejected(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(zoneScheme(t)).Build()
	_, err := ResolveZoneID(context.Background(), c, ZoneRefInputs{}, "default")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrZoneRefXOR)
}

func TestResolveZoneID_RefNotFound(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(zoneScheme(t)).Build()
	_, err := ResolveZoneID(context.Background(), c, ZoneRefInputs{
		ZoneRef: &v1alpha1.ZoneReference{Name: "missing"},
	}, "media")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrZoneRefNotFound)
}

func TestResolveZoneID_FromRef_StatusZoneID(t *testing.T) {
	// Spec 2 contract: when status.zoneID is populated, ResolveZoneID returns it.
	zone := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "media"},
		Status:     v1alpha1.CloudflareZoneStatus{ZoneID: "abc123"},
	}
	c := fake.NewClientBuilder().WithScheme(zoneScheme(t)).WithObjects(zone).Build()
	res, err := ResolveZoneID(context.Background(), c, ZoneRefInputs{
		ZoneRef: &v1alpha1.ZoneReference{Name: "test", Namespace: "media"},
	}, "default")
	require.NoError(t, err)
	require.Equal(t, "abc123", res.ZoneID)
	require.NotNil(t, res.ZoneObject)
}

func TestResolveZoneID_FromRef_StatusZoneIDUnset(t *testing.T) {
	// Spec-2 reconciler not yet populated status — caller requeues.
	// ZoneObject must be non-nil so caller distinguishes from not-found.
	zone := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "media"},
	}
	c := fake.NewClientBuilder().WithScheme(zoneScheme(t)).WithObjects(zone).Build()
	res, err := ResolveZoneID(context.Background(), c, ZoneRefInputs{
		ZoneRef: &v1alpha1.ZoneReference{Name: "test", Namespace: "media"},
	}, "default")
	require.NoError(t, err)
	require.Empty(t, res.ZoneID, "status.zoneID not populated yet; caller should requeue")
	require.NotNil(t, res.ZoneObject, "ZoneObject must be non-nil so caller can distinguish from not-found")
}
