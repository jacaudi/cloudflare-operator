package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCloudflareOperator_DefaultName(t *testing.T) {
	require.Equal(t, "cluster", CloudflareOperatorSingletonName)
}

func TestControllerSpec_DefaultReplicas(t *testing.T) {
	s := ControllerSpec{}
	require.Equal(t, int32(0), s.Replicas, "replicas should default to zero so CEL/defaulting can fill it")
}

func TestBundleEnabled(t *testing.T) {
	op := CloudflareOperator{
		Spec: CloudflareOperatorSpec{
			Controllers: ControllersSpec{
				Zone:   ControllerSpec{Enabled: true},
				Tunnel: ControllerSpec{Enabled: false},
			},
		},
	}
	require.True(t, op.Spec.Controllers.Zone.Enabled)
	require.False(t, op.Spec.Controllers.Tunnel.Enabled)
}
