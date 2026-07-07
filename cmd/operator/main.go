// operator is the central controller: it serves the report ingest endpoint
// and reconciles Edge objects (fleet inventory + reachability). It runs in
// one central cluster; edges only ever dial in, never the reverse.
package main

import (
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	fleetv1alpha1 "github.com/BillyGrande/edge-deployer/api/v1alpha1"
	"github.com/BillyGrande/edge-deployer/internal/operator/controller"
	"github.com/BillyGrande/edge-deployer/internal/operator/ingest"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(fleetv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr      string
		probeAddr        string
		ingestAddr       string
		fleetNamespace   string
		heartbeatTimeout time.Duration
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "metrics endpoint address")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "liveness/readiness probe address")
	flag.StringVar(&ingestAddr, "ingest-bind-address", ":8090", "edge report ingest endpoint address")
	flag.StringVar(&fleetNamespace, "fleet-namespace", "edge-fleet", "namespace where Edge objects live")
	flag.DurationVar(&heartbeatTimeout, "heartbeat-timeout", 15*time.Minute, "default time after the last report before an edge is unreachable")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
	})
	if err != nil {
		setupLog.Error(err, "creating manager")
		os.Exit(1)
	}

	reconciler := &controller.EdgeReconciler{
		Client:                  mgr.GetClient(),
		DefaultHeartbeatTimeout: heartbeatTimeout,
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "setting up Edge controller")
		os.Exit(1)
	}

	if err := mgr.Add(&ingest.Server{
		Client:    mgr.GetClient(),
		Addr:      ingestAddr,
		Namespace: fleetNamespace,
		Token:     os.Getenv("EDGEDEPLOY_INGEST_TOKEN"),
	}); err != nil {
		setupLog.Error(err, "adding ingest server")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "adding healthz check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "adding readyz check")
		os.Exit(1)
	}

	setupLog.Info("starting operator", "ingest", ingestAddr, "fleetNamespace", fleetNamespace)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "running manager")
		os.Exit(1)
	}
}
