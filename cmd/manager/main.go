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

// Package main is the cloudflare-operator binary entrypoint. It dispatches
// on --mode=meta|zone|tunnel: meta runs the bootstrap reconciler, zone runs
// the zone-bundle reconcilers, tunnel runs a stub no-op (spec 3 fills it in).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/bootstrap"
	"github.com/jacaudi/cloudflare-operator/internal/controller/tunnel"
	"github.com/jacaudi/cloudflare-operator/internal/controller/zone"
)

// Mode is the controller role this binary plays.
type Mode string

const (
	ModeMeta   Mode = "meta"
	ModeZone   Mode = "zone"
	ModeTunnel Mode = "tunnel"
)

// Options is the parsed flag set.
type Options struct {
	Mode              Mode
	LogLevel          string
	MetricsAddress    string
	HealthAddress     string
	LeaderElection    bool
	OperatorNamespace string
	OperatorImage     string
}

func parseFlags(args []string) (Options, error) {
	fs := flag.NewFlagSet("manager", flag.ContinueOnError)
	mode := fs.String("mode", string(ModeMeta), "controller mode: meta|zone|tunnel")
	logLevel := fs.String("log-level", "info", "log level: debug|info|warn|error")
	metricsAddr := fs.String("metrics-address", ":8080", "metrics bind address")
	healthAddr := fs.String("health-address", ":8081", "health/readyz bind address")
	leaderElection := fs.Bool("leader-election", true, "enable leader election")
	opNamespace := fs.String("operator-namespace", envOr("OPERATOR_NAMESPACE", "cloudflare-system"), "namespace the operator runs in")
	opImage := fs.String("operator-image", envOr("OPERATOR_IMAGE", ""), "image used for spawned controller Deployments")
	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}
	m := Mode(*mode)
	switch m {
	case ModeMeta, ModeZone, ModeTunnel:
	default:
		return Options{}, fmt.Errorf("invalid --mode %q (want meta|zone|tunnel)", *mode)
	}
	return Options{
		Mode:              m,
		LogLevel:          *logLevel,
		MetricsAddress:    *metricsAddr,
		HealthAddress:     *healthAddr,
		LeaderElection:    *leaderElection,
		OperatorNamespace: *opNamespace,
		OperatorImage:     *opImage,
	}, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	// Init logger first (default info level) so flag-parse errors emit
	// structured JSON via logger.Error rather than raw text on stderr.
	logger := zapr.NewLogger(newProductionLogger("info"))
	ctrl.SetLogger(logger)
	log.SetLogger(logger)

	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		logger.Error(err, "flag parse failed")
		os.Exit(2)
	}

	// Replace with a logger at the user-requested level (no-op when the level
	// already matches the default).
	logger = zapr.NewLogger(newProductionLogger(opts.LogLevel))
	ctrl.SetLogger(logger)
	log.SetLogger(logger)

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	utilruntime.Must(apiextv1.AddToScheme(scheme))

	switch opts.Mode {
	case ModeMeta:
		runMeta(opts, scheme)
	case ModeZone:
		runZone(opts, scheme)
	case ModeTunnel:
		utilruntime.Must(gwv1.Install(scheme))
		utilruntime.Must(gwv1a2.Install(scheme))
		runTunnel(opts, scheme)
	}
}

// runMeta starts the controller-runtime manager with the bootstrap reconciler.
func runMeta(opts Options, scheme *runtime.Scheme) {
	leaderID := "cloudflare-operator-" + string(opts.Mode)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: opts.MetricsAddress},
		HealthProbeBindAddress: opts.HealthAddress,
		LeaderElection:         opts.LeaderElection,
		LeaderElectionID:       leaderID,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "create manager:", err)
		os.Exit(1)
	}
	if err := (&bootstrap.Reconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		OperatorNamespace: opts.OperatorNamespace,
		OperatorImage:     opts.OperatorImage,
	}).SetupWithManager(mgr); err != nil {
		fmt.Fprintln(os.Stderr, "setup bootstrap reconciler:", err)
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		fmt.Fprintln(os.Stderr, "add healthz check:", err)
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		fmt.Fprintln(os.Stderr, "add readyz check:", err)
		os.Exit(1)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		fmt.Fprintln(os.Stderr, "manager exited with error:", err)
		os.Exit(1)
	}
}

// runZone starts the controller-runtime manager with the zone-bundle reconcilers
// (CloudflareZone, CloudflareZoneConfig, CloudflareDNSRecord, CloudflareRuleset).
// Per-reconcile credentials are resolved via reconcile.LoadCredentialsHierarchical;
// the env vars below are smoke-checked here as a fail-fast.
func runZone(opts Options, scheme *runtime.Scheme) {
	if os.Getenv("CLOUDFLARE_API_TOKEN") == "" || os.Getenv("CLOUDFLARE_ACCOUNT_ID") == "" {
		fmt.Fprintln(os.Stderr, "zone mode requires CLOUDFLARE_API_TOKEN + CLOUDFLARE_ACCOUNT_ID env vars")
		os.Exit(1)
	}

	leaderID := "cloudflare-operator-" + string(opts.Mode)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: opts.MetricsAddress},
		HealthProbeBindAddress: opts.HealthAddress,
		LeaderElection:         opts.LeaderElection,
		LeaderElectionID:       leaderID,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "create manager:", err)
		os.Exit(1)
	}
	if err := zone.AddToManager(mgr, zone.Options{}); err != nil {
		fmt.Fprintln(os.Stderr, "register zone bundle:", err)
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		fmt.Fprintln(os.Stderr, "add healthz check:", err)
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		fmt.Fprintln(os.Stderr, "add readyz check:", err)
		os.Exit(1)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		fmt.Fprintln(os.Stderr, "manager exited with error:", err)
		os.Exit(1)
	}
}

// runTunnel starts the controller-runtime manager with the tunnel-bundle
// reconcilers (CloudflareTunnel + Service / Gateway / HTTPRoute / TLSRoute
// sources). Mirrors runZone: per-reconcile credentials resolve via
// reconcile.LoadCredentialsHierarchical; the env vars below are smoke-checked
// here as a fail-fast (the bootstrap reconciler injects them into the tunnel
// controller Deployment, but a hand-rolled deployment could omit them).
func runTunnel(opts Options, scheme *runtime.Scheme) {
	if os.Getenv("CLOUDFLARE_API_TOKEN") == "" || os.Getenv("CLOUDFLARE_ACCOUNT_ID") == "" {
		fmt.Fprintln(os.Stderr, "tunnel mode requires CLOUDFLARE_API_TOKEN + CLOUDFLARE_ACCOUNT_ID env vars")
		os.Exit(1)
	}

	leaderID := "cloudflare-operator-" + string(opts.Mode)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: opts.MetricsAddress},
		HealthProbeBindAddress: opts.HealthAddress,
		LeaderElection:         opts.LeaderElection,
		LeaderElectionID:       leaderID,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "create manager:", err)
		os.Exit(1)
	}
	if err := tunnel.AddToManager(mgr, tunnel.Options{}); err != nil {
		fmt.Fprintln(os.Stderr, "register tunnel bundle:", err)
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		fmt.Fprintln(os.Stderr, "add healthz check:", err)
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		fmt.Fprintln(os.Stderr, "add readyz check:", err)
		os.Exit(1)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		fmt.Fprintln(os.Stderr, "manager exited with error:", err)
		os.Exit(1)
	}
}

func newProductionLogger(level string) *zap.Logger {
	cfg := zap.NewProductionConfig()
	switch level {
	case "debug":
		cfg.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
	case "warn":
		cfg.Level = zap.NewAtomicLevelAt(zapcore.WarnLevel)
	case "error":
		cfg.Level = zap.NewAtomicLevelAt(zapcore.ErrorLevel)
	default:
		cfg.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	}
	l, _ := cfg.Build()
	return l
}
