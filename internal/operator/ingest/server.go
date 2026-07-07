// Package ingest is the operator's HTTP endpoint that receives edge
// reports. Edges are auto-registered: the first report creates the Edge
// object, later reports update its status. Only the edges talk to this
// endpoint, always outbound from their side — the operator never dials an
// edge.
package ingest

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	fleetv1alpha1 "github.com/BillyGrande/edge-deployer/api/v1alpha1"
	"github.com/BillyGrande/edge-deployer/internal/wire"
)

const maxBodyBytes = 1 << 20 // 1 MiB is plenty for a report

// Server receives reports and writes them into Edge objects. It implements
// manager.Runnable so its lifecycle is tied to the controller manager.
type Server struct {
	Client client.Client
	// Addr to listen on, e.g. ":8090".
	Addr string
	// Namespace where Edge objects live.
	Namespace string
	// Token is the shared bearer token edges must present. Empty disables
	// auth (local development only). TODO: mTLS / per-edge tokens.
	Token string
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1alpha1/reports", s.handleReport)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	log := logf.FromContext(r.Context()).WithName("ingest")

	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var report wire.Report
	body := http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(body).Decode(&report); err != nil {
		http.Error(w, fmt.Sprintf("decoding report: %v", err), http.StatusBadRequest)
		return
	}
	if err := report.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.upsert(r.Context(), &report); err != nil {
		log.Error(err, "storing report", "edge", report.Edge)
		http.Error(w, "storing report failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) authorized(r *http.Request) bool {
	if s.Token == "" {
		return true
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) == 1
}

// upsert creates the Edge on first contact, then writes the report into its
// status subresource.
func (s *Server) upsert(ctx context.Context, report *wire.Report) error {
	key := types.NamespacedName{Namespace: s.Namespace, Name: report.Edge}

	var edge fleetv1alpha1.Edge
	if err := s.Client.Get(ctx, key, &edge); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		edge = fleetv1alpha1.Edge{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		}
		if err := s.Client.Create(ctx, &edge); err != nil {
			return err
		}
	}

	edge.Status = statusFrom(report, edge.Status)
	return s.Client.Status().Update(ctx, &edge)
}

// statusFrom maps a wire report onto EdgeStatus, preserving what the
// reconciler owns (conditions).
func statusFrom(report *wire.Report, prev fleetv1alpha1.EdgeStatus) fleetv1alpha1.EdgeStatus {
	now := metav1.Now()
	status := fleetv1alpha1.EdgeStatus{
		LastReportTime: &now,
		Labels:         report.Labels,
		Conditions:     prev.Conditions,
	}
	for _, a := range report.Apps {
		status.Apps = append(status.Apps, fleetv1alpha1.AppSummary(a))
		status.AppsTotal++
		if a.Sync == "Synced" {
			status.AppsSynced++
		}
		if a.Health == "Healthy" {
			status.AppsHealthy++
		}
	}
	if report.Store != nil {
		status.StoreVersion = report.Store.Version
		healthy := report.Store.RegistryHealthy
		status.RegistryHealthy = &healthy
	}
	return status
}
