/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package envtest_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
)

// sharedClient is set up once in TestMain and shared across all tests in the package.
var sharedClient client.Client

// sharedScheme is the *runtime.Scheme registered in TestMain and shared across
// all tests in the package. Tests that call tunnel.EmitDNSRecord directly (which
// requires a scheme for SetControllerOwner) use this instead of building their own.
var sharedScheme *runtime.Scheme

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
			[]string{filepath.Join("..", "..", "bin", "crd-staging")},
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
	utilruntime.Must(v2alpha1.AddToScheme(scheme))
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
	sharedScheme = scheme

	code := m.Run()

	_ = env.Stop()
	os.Exit(code)
}
