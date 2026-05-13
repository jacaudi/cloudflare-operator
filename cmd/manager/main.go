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
// on --mode=meta|zone|tunnel: meta runs the bootstrap reconciler, zone and
// tunnel run stub no-ops (specs 2 and 3 fill them in).
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

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

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/bootstrap"
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
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	logger := zapr.NewLogger(newProductionLogger(opts.LogLevel))
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
		runStub(opts)
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

// runStub is the placeholder for zone/tunnel modes in Foundation. It binds the
// health/readyz HTTP server (so spawned controller pods can pass their probes)
// and blocks on SIGTERM. Specs 2 and 3 replace this with the real reconcilers.
func runStub(opts Options) {
	logger := log.FromContext(context.Background()).WithValues("mode", opts.Mode)
	logger.Info("stub controller starting", "healthAddress", opts.HealthAddress)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              opts.HealthAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx := ctrl.SetupSignalHandler()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error(err, "health server failed")
		os.Exit(1)
	}
	logger.Info("stub controller stopped")
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
