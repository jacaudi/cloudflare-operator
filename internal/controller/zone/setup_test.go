/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
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
