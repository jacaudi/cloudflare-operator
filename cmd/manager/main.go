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
// the zone-bundle reconcilers, and tunnel runs the tunnel-bundle reconcilers
// (CloudflareTunnel + Service/Gateway/HTTPRoute/TLSRoute sources).
package main

import (
	"errors"
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
	rest "k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
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

var errMissingCloudflareEnv = errors.New("zone/tunnel mode requires CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID env vars")

// version, commit, date are injected via -ldflags at build time
// (-X main.version=... etc.); the defaults apply for `go run` / `go build`
// without ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// versionString is the single formatter for --version and the startup log.
func versionString() string {
	return fmt.Sprintf("cloudflare-operator %s (commit %s, built %s)", version, commit, date)
}

// Options is the parsed flag set.
type Options struct {
	Mode              Mode
	LogLevel          string
	MetricsAddress    string
	HealthAddress     string
	LeaderElection    bool
	OperatorNamespace string
	OperatorImage     string
	Version           bool
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
	versionFlag := fs.Bool("version", false, "print build version and exit")
	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}
	// --version short-circuits before mode validation so it is
	// unconditional (conventional CLI behaviour): `--version` works
	// regardless of --mode. main() prints versionString() and exits 0.
	if *versionFlag {
		return Options{Version: true}, nil
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
		Version:           *versionFlag,
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
	zl, lerr := newProductionLogger("info")
	if lerr != nil {
		// No logger yet — this one raw stderr line is unavoidable (the
		// logger itself failed to build).
		fmt.Fprintln(os.Stderr, "init logger:", lerr)
		os.Exit(2)
	}
	logger := zapr.NewLogger(zl)
	ctrl.SetLogger(logger)
	log.SetLogger(logger)

	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		logger.Error(err, "flag parse failed")
		os.Exit(2)
	}

	if opts.Version {
		fmt.Println(versionString())
		os.Exit(0)
	}

	// Reconfigure to the user-requested level. On failure keep the
	// already-installed info-level logger (degraded but functional) and
	// surface it structurally rather than aborting.
	if zl2, rerr := newProductionLogger(opts.LogLevel); rerr != nil {
		logger.Error(rerr, "reconfigure logger to requested level; keeping info-level logger", "level", opts.LogLevel)
	} else {
		logger = zapr.NewLogger(zl2)
		ctrl.SetLogger(logger)
		log.SetLogger(logger)
	}

	logger.Info("starting", "version", version, "commit", commit, "date", date, "mode", opts.Mode)

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v2alpha1.AddToScheme(scheme))
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

// startManager builds a controller-runtime manager from cfg, runs register
// to wire the mode-specific reconcilers, adds the health/ready probes, and
// blocks on Start. Returns the first error (wrapped) instead of os.Exit so
// callers control fatal reporting. cfg is a parameter (not GetConfigOrDie
// internally) so the register/wiring path is unit-testable without a cluster.
func startManager(opts Options, scheme *runtime.Scheme, cfg *rest.Config, register func(ctrl.Manager) error) error {
	leaderID := "cloudflare-operator-" + string(opts.Mode)
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: opts.MetricsAddress},
		HealthProbeBindAddress: opts.HealthAddress,
		LeaderElection:         opts.LeaderElection,
		LeaderElectionID:       leaderID,
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}
	if err := register(mgr); err != nil {
		return fmt.Errorf("register reconcilers: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("add healthz check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("add readyz check: %w", err)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("manager exited with error: %w", err)
	}
	return nil
}

// runMeta starts the controller-runtime manager with the bootstrap reconciler.
func runMeta(opts Options, scheme *runtime.Scheme) {
	log := ctrl.Log.WithName(string(opts.Mode))
	err := startManager(opts, scheme, ctrl.GetConfigOrDie(), func(mgr ctrl.Manager) error {
		return (&bootstrap.Reconciler{
			Client:            mgr.GetClient(),
			Scheme:            mgr.GetScheme(),
			OperatorNamespace: opts.OperatorNamespace,
			OperatorImage:     opts.OperatorImage,
		}).SetupWithManager(mgr)
	})
	if err != nil {
		log.Error(err, "fatal")
		os.Exit(1)
	}
}

// runZone starts the controller-runtime manager with the zone-bundle reconcilers
// (CloudflareZone, CloudflareZoneConfig, CloudflareDNSRecord, CloudflareRuleset).
// Per-reconcile credentials are resolved via reconcile.LoadCredentialsHierarchical;
// the env vars below are smoke-checked here as a fail-fast.
func runZone(opts Options, scheme *runtime.Scheme) {
	log := ctrl.Log.WithName(string(opts.Mode))
	if os.Getenv("CLOUDFLARE_API_TOKEN") == "" || os.Getenv("CLOUDFLARE_ACCOUNT_ID") == "" {
		log.Error(errMissingCloudflareEnv, "preflight failed")
		os.Exit(1)
	}
	if err := startManager(opts, scheme, ctrl.GetConfigOrDie(), func(mgr ctrl.Manager) error {
		return zone.AddToManager(mgr, zone.Options{})
	}); err != nil {
		log.Error(err, "fatal")
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
	log := ctrl.Log.WithName(string(opts.Mode))
	if os.Getenv("CLOUDFLARE_API_TOKEN") == "" || os.Getenv("CLOUDFLARE_ACCOUNT_ID") == "" {
		log.Error(errMissingCloudflareEnv, "preflight failed")
		os.Exit(1)
	}
	if err := startManager(opts, scheme, ctrl.GetConfigOrDie(), func(mgr ctrl.Manager) error {
		return tunnel.AddToManager(mgr, tunnel.Options{})
	}); err != nil {
		log.Error(err, "fatal")
		os.Exit(1)
	}
}

func newProductionLogger(level string) (*zap.Logger, error) {
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
	return cfg.Build()
}
