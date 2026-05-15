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

package envtest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/bootstrap"
)

// sharedClient is set up once in TestMain and shared across all tests in the package.
var sharedClient client.Client

// sharedConfig is the envtest *rest.Config, exported so per-test files can
// build their own managers (e.g. zone bundle wiring with mock-backed clients).
var sharedConfig *rest.Config

// resolveGatewayAPICRDPaths returns the gateway-api CRD directories from the
// local Go module cache, keyed by the sigs.k8s.io/gateway-api version pinned
// in this repo's go.mod. Returns an error if `go list -m` fails — a hard
// prerequisite that we surface clearly rather than silently skipping the
// gateway-api CRDs.
//
// Resolution source: `go list -m -json sigs.k8s.io/gateway-api` against the
// surrounding module. The .Dir field gives the on-disk module location
// directly, honoring `replace` directives and any caching the toolchain does.
// We use this over runtime/debug.BuildInfo because `go test` produces test
// binaries whose embedded BuildInfo.Deps is unreliable across toolchain
// versions (observed empty under this repo's `make test` invocation, even
// though the build resolved the gateway-api dependency cleanly).
//
// The Standard channel ships Gateway, HTTPRoute, GatewayClass, GRPCRoute, and
// ReferenceGrant. TLSRoute (v1alpha2) lives only in the experimental channel,
// so we install BOTH directories. The experimental channel is a superset of
// Standard for the CRDs we install — envtest tolerates duplicate CRD declarations
// across paths because each CRD's Kubernetes object is name-keyed and applied
// idempotently. The experimental directory also ships TCPRoute / UDPRoute /
// GRPCRoute / ReferenceGrant / BackendLBPolicy / BackendTLSPolicy — none of
// which the reconcilers touch, but installing them is harmless.
func resolveGatewayAPICRDPaths() ([]string, error) {
	listOut, err := exec.Command("go", "list", "-m", "-json", "sigs.k8s.io/gateway-api").Output()
	if err != nil {
		return nil, fmt.Errorf("go list -m sigs.k8s.io/gateway-api: %w", err)
	}
	var meta struct {
		Path    string
		Version string
		Dir     string
		Replace *struct {
			Path    string
			Version string
			Dir     string
		}
	}
	if err := json.Unmarshal(listOut, &meta); err != nil {
		return nil, fmt.Errorf("parse go list output: %w", err)
	}
	dir := meta.Dir
	if meta.Replace != nil && meta.Replace.Dir != "" {
		dir = meta.Replace.Dir
	}
	if dir == "" {
		return nil, fmt.Errorf("sigs.k8s.io/gateway-api module Dir empty (cache hydrated?)")
	}

	base := filepath.Join(dir, "config", "crd")
	return []string{
		filepath.Join(base, "standard"),
		filepath.Join(base, "experimental"),
	}, nil
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout after %s", timeout)
}

func TestMain(m *testing.M) {
	// Skip envtest when KUBEBUILDER_ASSETS isn't set (so unit-test CI without
	// envtest still passes); the `make test` target sets it correctly.
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		fmt.Fprintln(os.Stderr, "skipping envtest suite: KUBEBUILDER_ASSETS unset (run `make envtest` to set it)")
		os.Exit(0)
	}

	// Resolve the gateway-api CRD paths from the Go module cache so the envtest
	// API server can install Gateway / HTTPRoute / TLSRoute CRDs without
	// vendoring them into the repo. The Standard channel ships Gateway +
	// HTTPRoute; the Experimental channel ships TLSRoute (v1alpha2). Both
	// directories are installed — see resolveGatewayAPICRDPaths. Resolution
	// sources:
	//   - $GOMODCACHE (via `go env GOMODCACHE`) — the local module cache root.
	//   - sigs.k8s.io/gateway-api version (via runtime/debug.ReadBuildInfo) —
	//     the version actually compiled into the test binary, so the CRDs on
	//     disk match the Go types in scope. No risk of skew with go.mod.
	gwCRDPaths, err := resolveGatewayAPICRDPaths()
	if err != nil {
		panic("resolve gateway-api CRD paths: " + err.Error())
	}

	env := &envtest.Environment{
		CRDDirectoryPaths: append(
			[]string{filepath.Join("..", "..", "internal", "bootstrap", "crds")},
			gwCRDPaths...,
		),
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		panic("envtest Start: " + err.Error())
	}
	sharedConfig = cfg

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	utilruntime.Must(apiextv1.AddToScheme(scheme))
	// gateway-api types — Install registers gwv1.Gateway / gwv1.HTTPRoute /
	// gwv1a2.TLSRoute so envtest tests can construct + Get these objects.
	utilruntime.Must(gwv1.Install(scheme))
	utilruntime.Must(gwv1a2.Install(scheme))

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		panic("client.New: " + err.Error())
	}
	sharedClient = k8sClient

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme})
	if err != nil {
		panic("ctrl.NewManager: " + err.Error())
	}

	if err := (&bootstrap.Reconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		OperatorNamespace: "cloudflare-system",
		OperatorImage:     "ghcr.io/test/manager:test",
	}).SetupWithManager(mgr); err != nil {
		panic("SetupWithManager: " + err.Error())
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()

	code := m.Run()

	cancel()
	_ = env.Stop()
	os.Exit(code)
}
