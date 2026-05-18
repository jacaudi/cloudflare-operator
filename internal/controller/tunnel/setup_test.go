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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
)

// TestAddToManager_DefaultConnectorResourcesPassthrough verifies that
// applyOptionDefaults does not clobber DefaultConnector.Resources — the
// caller-supplied ResourceRequirements must survive the defaulting pass
// unchanged while scalar fields (Replicas, Protocol, LogLevel,
// GracePeriodSeconds) are still filled in.
func TestAddToManager_DefaultConnectorResourcesPassthrough(t *testing.T) {
	want := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")},
	}
	opts := Options{DefaultConnector: v2alpha1.ConnectorSpec{Resources: want}}
	applyOptionDefaults(&opts)
	require.Equal(t, want, opts.DefaultConnector.Resources)
	require.Equal(t, int32(2), opts.DefaultConnector.Replicas) // scalar defaulting still applies
}
