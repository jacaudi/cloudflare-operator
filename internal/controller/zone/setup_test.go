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

package zone

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
)

func TestAddToManager_RegistersAllFour(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, v2alpha1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	mgr, err := ctrl.NewManager(&rest.Config{Host: "localhost"}, ctrl.Options{
		Scheme:  s,
		Metrics: server.Options{BindAddress: "0"},
	})
	require.NoError(t, err)
	require.NoError(t, AddToManager(mgr, Options{}))
}
