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

// TestEnvtest_AddToManager_AppliesConcurrency is the integration smoke for the
// issue #134 per-controller concurrency knob. It builds a real
// controller-runtime manager against the envtest apiserver and calls
// tunnel.AddToManager with a non-default ConcurrencyOptions on every field,
// then asserts the whole bundle wires without error.
//
// This exercises the exact production wiring path — all five source-controller
// builders now carry `.WithOptions(controllerOptions(opts.Concurrency.X))` — so
// a malformed option, a wrong controller.Options type, or a builder that
// rejects WithOptions would surface here as a non-nil AddToManager error. The
// manager is intentionally NOT started: registering the controllers is
// sufficient to prove the WithOptions plumbing integrates, and not starting
// avoids interfering with the reconcile loops of sibling tests in the shared
// envtest process (AddToManager registers the fixed controller names
// "httproute-source", "service-source", ... which no other test uses).
//
// End-to-end verification that the value is *honored* by controller-runtime
// (MaxConcurrentReconciles <= 0 -> 1) is controller-runtime's own contract,
// asserted at the unit level against the pinned version in
// internal/controller/tunnel/concurrency_test.go.
func TestEnvtest_AddToManager_AppliesConcurrency(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}

	sch := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(sch))
	utilruntime.Must(v2alpha1.AddToScheme(sch))
	utilruntime.Must(gwv1.Install(sch))
	utilruntime.Must(gwv1a2.Install(sch))

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
