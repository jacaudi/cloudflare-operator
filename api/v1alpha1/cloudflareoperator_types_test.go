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
