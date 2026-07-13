/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
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

	"github.com/stretchr/testify/require"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
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

// resolveGatewayAPICRDPaths returns the gateway-api CRD directory from the
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
// We install ONLY the Experimental channel, never Standard alongside it.
// Experimental is a strict superset: it ships every CRD Standard does (same
// names) and serves a superset of their versions, plus TCPRoute / UDPRoute and
// the x-k8s.io CRDs. Installing both directories is actively unsafe, because
// the two channels declare the SAME CRD names with different served versions
// (Standard's tlsroutes serves v1 only; Experimental's serves v1 + v1alpha2 +
// v1alpha3). envtest resolves such duplicates as "last path wins", and
// envtest.Environment.Start pushes CRDDirectoryPaths through mergePaths, which
// launders them through a Go map and so randomizes their order on every run.
// Passing both channels therefore installed the Standard tlsroutes — with
// v1alpha2 NOT served — in a random ~1-in-4 of runs, permanently removing the
// gateway.networking.k8s.io/v1alpha2 group-version and failing every TLSRoute
// test with `no matches for kind "TLSRoute"`. One directory, no duplicate
// names, no order dependence.
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

	return []string{filepath.Join(dir, "config", "crd", "experimental")}, nil
}

// purgeCloudflareCRs deletes every cloudflare.io CR in the shared apiserver and
// blocks until they are gone. Call it before building a test's manager, so each
// test starts against an empty cluster.
//
// The whole package shares ONE envtest apiserver, and envtest runs no namespace
// GC — so a test's CRs outlive it. Every test manager watches cluster-wide, so
// without this a later test's controllers pick up earlier tests' abandoned CRs
// and reconcile them against the later test's mock, creating and deleting
// records in it. That is how the zone bundle's §10.4 lost the TXT companion it
// had just seeded: adoption is then refused permanently by design (never
// backfill a TXT for a pre-existing record), so Status.RecordID stays empty and
// the assertion can never pass — no matter how long it waits.
//
// Finalizers are stripped first: the manager that owned these CRs is gone, so
// nothing is left to remove them and Delete would block forever.
func purgeCloudflareCRs(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	// Fresh list objects per pass — List does not clear a previous result.
	lists := func() []client.ObjectList {
		return []client.ObjectList{
			&v2alpha1.CloudflareDNSRecordList{},
			&v2alpha1.CloudflareTunnelList{},
			&v2alpha1.CloudflareZoneList{},
			&v2alpha1.CloudflareZoneConfigList{},
			&v2alpha1.CloudflareRulesetList{},
		}
	}

	for _, l := range lists() {
		require.NoError(t, sharedClient.List(ctx, l))
		objs, err := apimeta.ExtractList(l)
		require.NoError(t, err)
		for _, o := range objs {
			obj, ok := o.(client.Object)
			if !ok {
				continue
			}
			if len(obj.GetFinalizers()) > 0 {
				obj.SetFinalizers(nil)
				_ = sharedClient.Update(ctx, obj)
			}
			_ = sharedClient.Delete(ctx, obj)
		}
	}

	require.Eventually(t, func() bool {
		for _, l := range lists() {
			if err := sharedClient.List(ctx, l); err != nil {
				return false
			}
			objs, err := apimeta.ExtractList(l)
			if err != nil || len(objs) > 0 {
				return false
			}
		}
		return true
	}, 30*time.Second, 50*time.Millisecond, "cloudflare.io CRs from an earlier test never cleared")
}

func TestMain(m *testing.M) {
	// Skip envtest when KUBEBUILDER_ASSETS isn't set (so unit-test CI without
	// envtest still passes); the `make test` target sets it correctly.
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		fmt.Fprintln(os.Stderr, "skipping envtest suite: KUBEBUILDER_ASSETS unset (run `make envtest` to set it)")
		os.Exit(0)
	}

	// Resolve the gateway-api CRD directory from the Go module cache so the
	// envtest API server can install Gateway / HTTPRoute / TLSRoute CRDs without
	// vendoring them into the repo. Only the Experimental channel is installed —
	// see resolveGatewayAPICRDPaths for why installing Standard alongside it is
	// unsafe.
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
