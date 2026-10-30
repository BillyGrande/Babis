// Command babis-operator is the central Babis fleet operator.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1alpha1 "github.com/BillyGrande/babis/central/api/v1alpha1"
	"github.com/BillyGrande/babis/central/internal/controller"
	"github.com/BillyGrande/babis/central/internal/ingest"
)

// Version is stamped at build time via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "babis-operator:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		fleetNS     = flag.String("fleet-namespace", "babis-fleet", "namespace that holds Edge objects")
		ingestAddr  = flag.String("ingest-addr", ":8090", "listen address of the check-in API")
		heartbeat   = flag.Duration("heartbeat-timeout", 15*time.Minute, "default time without check-in before an edge is unreachable")
		metricsAddr = flag.String("metrics-addr", ":8080", "metrics listen address")
		healthAddr  = flag.String("health-addr", ":8081", "health probe listen address")
		leaderElect = flag.Bool("leader-elect", true, "enable leader election")
		insecure    = flag.Bool("insecure-no-auth", false, "accept check-ins without a token (development only)")
	)
	zapOpts := zap.Options{Development: false}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	log := ctrl.Log.WithName("setup")
	log.Info("starting", "version", Version)

	token := os.Getenv("BABIS_INGEST_TOKEN")
	if token == "" && !*insecure {
		return fmt.Errorf("BABIS_INGEST_TOKEN is empty; set it or pass --insecure-no-auth (development only)")
	}
	if token == "" {
		log.Info("WARNING: running without ingest authentication")
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}

	cfg := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: *metricsAddr},
		HealthProbeBindAddress:  *healthAddr,
		LeaderElection:          *leaderElect,
		LeaderElectionID:        "babis-operator.fleet.babis.dev",
		LeaderElectionNamespace: *fleetNS,
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{*fleetNS: {}},
		},
	})
	if err != nil {
		return fmt.Errorf("manager: %w", err)
	}

	if err := (&controller.EdgeReconciler{
		Client:                  mgr.GetClient(),
		DefaultHeartbeatTimeout: *heartbeat,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("controller: %w", err)
	}

	// Ingest reads straight from the API server so a fresh create is visible.
	direct, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("ingest client: %w", err)
	}
	srv, err := ingest.New(ingest.Config{
		Addr:           *ingestAddr,
		Token:          token,
		InsecureNoAuth: *insecure,
		Namespace:      *fleetNS,
		Client:         direct,
		Log:            ctrl.Log.WithName("ingest"),
	})
	if err != nil {
		return err
	}
	if err := mgr.Add(srv); err != nil {
		return err
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return mgr.Start(ctrl.SetupSignalHandler())
}
