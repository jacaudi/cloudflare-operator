/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package envtest_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/controller/tunnel"
)

// TestEnvtest_AddToManager_AppliesConcurrency is the integration smoke for issue #134: AddToManager must wire ConcurrencyOptions across all five builders without error. The manager is deliberately not started, so it can't disturb sibling tests in the shared envtest process.
func TestEnvtest_AddToManager_AppliesConcurrency(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}

	sch := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(sch))
	utilruntime.Must(v2alpha1.AddToScheme(sch))
	utilruntime.Must(gwv1.Install(sch))
	utilruntime.Must(gwv1a2.Install(sch))

	// Start from an empty cluster: earlier tests' CRs outlive them in the
	// shared apiserver and every manager watches cluster-wide.
	purgeCloudflareCRs(t)

	mgr, err := ctrl.NewManager(sharedConfig, ctrl.Options{
		Scheme:  sch,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)

	// Non-default on every field — the full-variant surface from #134.
	err = tunnel.AddToManager(mgr, tunnel.Options{
		Concurrency: tunnel.ConcurrencyOptions{
			Tunnel:    2,
			Service:   2,
			Gateway:   2,
			HTTPRoute: 4,
			TLSRoute:  2,
		},
	})
	require.NoError(t, err, "AddToManager must wire all five builders with per-controller concurrency")
}
