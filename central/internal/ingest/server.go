// Package ingest serves the check-in API and turns check-ins into Edge status.
package ingest

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/BillyGrande/babis/central/api/v1alpha1"
	"github.com/BillyGrande/babis/central/internal/wire"
)

const (
	// CheckinPath is the check-in endpoint.
	CheckinPath = "/api/v1alpha1/checkin"
	// MaxBodyBytes caps a request body.
	MaxBodyBytes = 1 << 20
)

// Config configures a Server.
type Config struct {
	// Addr is the listen address, for example ":8090".
	Addr string
	// Token is the shared bearer token.
	Token string
	// InsecureNoAuth allows an empty Token. For development only.
	InsecureNoAuth bool
	// Namespace is the fleet namespace where Edges are created.
	Namespace string
	// Client should read without a stale cache, so a create is visible at once.
	Client client.Client
	// Now is the clock. Defaults to time.Now.
	Now func() time.Time
	// Log is optional.
	Log logr.Logger
}

// Server is the ingest HTTP server. It implements manager.Runnable.
type Server struct {
	cfg       Config
	tokenHash [sha256.Size]byte
}

// New builds a Server. It refuses an empty token unless InsecureNoAuth is set.
func New(cfg Config) (*Server, error) {
	if cfg.Token == "" && !cfg.InsecureNoAuth {
		return nil, errors.New("ingest: empty token requires InsecureNoAuth")
	}
	if cfg.Client == nil {
		return nil, errors.New("ingest: client is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log.GetSink() == nil {
		cfg.Log = logr.Discard()
	}
	return &Server{cfg: cfg, tokenHash: sha256.Sum256([]byte(cfg.Token))}, nil
}

// NeedLeaderElection reports false: every replica accepts check-ins.
func (s *Server) NeedLeaderElection() bool { return false }

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST "+CheckinPath, s.handleCheckin)
	return mux
}

// Start serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	if s.cfg.Token == "" {
		return s.cfg.InsecureNoAuth
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	h := sha256.Sum256([]byte(got))
	return subtle.ConstantTimeCompare(h[:], s.tokenHash[:]) == 1
}

func (s *Server) handleCheckin(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body exceeds %d bytes", MaxBodyBytes))
			return
		}
		writeError(w, http.StatusBadRequest, "cannot read body: "+err.Error())
		return
	}
	var req wire.CheckinRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.record(r.Context(), &req); err != nil {
		s.cfg.Log.Error(err, "record check-in", "edge", req.Edge)
		writeError(w, http.StatusInternalServerError, "cannot record check-in")
		return
	}
	gen := int64(0)
	resp := wire.CheckinResponse{
		Generation: &gen,
		Desired:    map[string]json.RawMessage{},
		Extensions: map[string]json.RawMessage{},
	}
	out, err := json.Marshal(resp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot encode response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// record registers the Edge if needed, then patches every status field except
// conditions. It never writes spec or metadata.labels.
func (s *Server) record(ctx context.Context, req *wire.CheckinRequest) error {
	key := client.ObjectKey{Namespace: s.cfg.Namespace, Name: req.Edge}
	var edge v1alpha1.Edge
	err := s.cfg.Client.Get(ctx, key, &edge)
	if apierrors.IsNotFound(err) {
		edge = v1alpha1.Edge{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}
		err = s.cfg.Client.Create(ctx, &edge)
		if apierrors.IsAlreadyExists(err) {
			err = s.cfg.Client.Get(ctx, key, &edge)
		}
	}
	if err != nil {
		return err
	}

	base := edge.DeepCopy()
	now := metav1.NewTime(s.cfg.Now())
	st := &edge.Status
	st.LastCheckinTime = &now
	st.Telemetry.Source = v1alpha1.TelemetrySourceCheckin
	st.Labels = req.Labels
	st.ArgoCD = v1alpha1.ArgoCDStatus{}
	if req.ArgoCD != nil {
		st.ArgoCD.Version = req.ArgoCD.Version
		for _, c := range req.ArgoCD.Components {
			st.ArgoCD.Components = append(st.ArgoCD.Components, v1alpha1.Component{
				Name: c.Name, Ready: clamp32(c.Ready), Desired: clamp32(c.Desired),
			})
		}
	}
	st.Apps = nil
	st.AppsTotal, st.AppsSynced, st.AppsHealthy = len(req.Apps), 0, 0
	for _, a := range req.Apps {
		st.Apps = append(st.Apps, v1alpha1.App{Name: a.Name, Sync: a.Sync, Health: a.Health, Revision: a.Revision})
		if a.Sync == "Synced" {
			st.AppsSynced++
		}
		if a.Health == "Healthy" {
			st.AppsHealthy++
		}
	}
	return s.cfg.Client.Status().Patch(ctx, &edge, client.MergeFrom(base))
}

func clamp32(v *int64) int32 {
	if v == nil {
		return 0
	}
	return int32(min(max(*v, 0), math.MaxInt32))
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
