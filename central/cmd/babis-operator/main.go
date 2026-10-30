// Command babis-operator is the central Babis fleet operator.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	toolscache "k8s.io/client-go/tools/cache"
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

type options struct {
	fleetNS, ingestAddr, metricsAddr, healthAddr string
	heartbeat                                    time.Duration
	leaderElect, insecure                        bool
	qps                                          float64
	burst, maxEdges, maxInflight                 int
}

func parseOptions(args []string) (*options, *zap.Options, error) {
	var o options
	fs := flag.NewFlagSet("babis-operator", flag.ContinueOnError)
	fs.StringVar(&o.fleetNS, "fleet-namespace", "babis-fleet", "namespace that holds Edge objects")
	fs.StringVar(&o.ingestAddr, "ingest-addr", ":8090", "listen address of the check-in API")
	fs.DurationVar(&o.heartbeat, "heartbeat-timeout", 15*time.Minute, "default time without check-in before an edge is unreachable")
	fs.StringVar(&o.metricsAddr, "metrics-addr", ":8080", "metrics listen address")
	fs.StringVar(&o.healthAddr, "health-addr", ":8081", "health probe listen address")
	fs.BoolVar(&o.leaderElect, "leader-elect", true, "enable leader election")
	fs.BoolVar(&o.insecure, "insecure-no-auth", false, "accept check-ins without a token (development only)")
	fs.Float64Var(&o.qps, "kube-api-qps", -1, "client-side Kubernetes API rate limit (queries per second) for the manager and the ingest client; negative disables client-side limiting (API Priority and Fairness applies), a positive value enables it")
	fs.IntVar(&o.burst, "kube-api-burst", 0, "client-side Kubernetes API burst, used only when --kube-api-qps is positive")
	fs.IntVar(&o.maxEdges, "max-edges", 2000, "refuse check-ins from new edges (429) when the fleet already has this many Edges")
	fs.IntVar(&o.maxInflight, "max-inflight", 64, "maximum check-ins handled at once; others wait up to 20s, then get 503")
	zapOpts := zap.Options{Development: false}
	zapOpts.BindFlags(fs)
	if err := fs.Parse(args); err != nil {
		return nil, nil, err
	}
	return &o, &zapOpts, nil
}

func run() error {
	o, zapOpts, err := parseOptions(os.Args[1:])
	if err != nil {
		return err
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(zapOpts)))
	log := ctrl.Log.WithName("setup")
	log.Info("starting", "version", Version)

	token := os.Getenv("BABIS_INGEST_TOKEN")
	if token == "" && !o.insecure {
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
	cfg.QPS, cfg.Burst = float32(o.qps), o.burst
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: o.metricsAddr},
		HealthProbeBindAddress:  o.healthAddr,
		LeaderElection:          o.leaderElect,
		LeaderElectionID:        "babis-operator.fleet.babis.dev",
		LeaderElectionNamespace: o.fleetNS,
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{o.fleetNS: {}},
		},
	})
	if err != nil {
		return fmt.Errorf("manager: %w", err)
	}

	if err := (&controller.EdgeReconciler{
		Client:                  mgr.GetClient(),
		DefaultHeartbeatTimeout: o.heartbeat,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("controller: %w", err)
	}

	// Ingest reads straight from the API server so a fresh create is visible.
	direct, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("ingest client: %w", err)
	}
	srv, err := ingest.New(ingest.Config{
		Addr:           o.ingestAddr,
		Token:          token,
		InsecureNoAuth: o.insecure,
		Namespace:      o.fleetNS,
		Client:         direct,
		Index:          cacheIndex{c: mgr.GetCache()},
		MaxEdges:       o.maxEdges,
		MaxInflight:    o.maxInflight,
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

// cacheIndex answers ingest's questions from the manager's Edge informer, so
// no request lists the API server. Before the cache has started it reports an
// empty fleet.
type cacheIndex struct{ c cache.Cache }

func (i cacheIndex) store() toolscache.Store {
	inf, err := i.c.GetInformer(context.Background(), &v1alpha1.Edge{}, cache.BlockUntilSynced(false))
	if err != nil {
		return nil
	}
	g, ok := inf.(interface{ GetStore() toolscache.Store })
	if !ok {
		return nil
	}
	return g.GetStore()
}

func (i cacheIndex) Has(namespace, name string) bool {
	st := i.store()
	if st == nil {
		return false
	}
	_, ok, _ := st.GetByKey(namespace + "/" + name)
	return ok
}

func (i cacheIndex) Count() int {
	if st := i.store(); st != nil {
		return len(st.ListKeys())
	}
	return 0
}
