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
	"k8s.io/apimachinery/pkg/types"
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
	// Index answers "is this edge known" and "how many edges". Normally the
	// manager cache. Nil disables the --max-edges check.
	Index EdgeIndex
	// MaxEdges caps the fleet; a check-in from a new edge beyond it gets 429.
	// Default 2000.
	MaxEdges int
	// MaxInflight bounds the check-ins that are being read or written at once.
	// Default 64.
	MaxInflight int
	// InflightWait is how long a request waits for a slot. Default 20 s.
	InflightWait time.Duration
	// Now is the clock. Defaults to time.Now.
	Now func() time.Time
	// Log is optional.
	Log logr.Logger
}

// Server is the ingest HTTP server. It implements manager.Runnable.
type Server struct {
	cfg       Config
	tokenHash [sha256.Size]byte
	slots     chan struct{}
}

// EdgeIndex is a cheap, cached view of the Edges that exist.
type EdgeIndex interface {
	Has(namespace, name string) bool
	Count() int
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
	if cfg.MaxEdges <= 0 {
		cfg.MaxEdges = 2000
	}
	if cfg.MaxInflight <= 0 {
		cfg.MaxInflight = 64
	}
	if cfg.InflightWait <= 0 {
		cfg.InflightWait = 20 * time.Second
	}
	return &Server{
		cfg: cfg, tokenHash: sha256.Sum256([]byte(cfg.Token)),
		slots: make(chan struct{}, cfg.MaxInflight),
	}, nil
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
	// Take a slot before the body is read, so at most MaxInflight bodies
	// are in memory.
	wait := time.NewTimer(s.cfg.InflightWait)
	defer wait.Stop()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-wait.C:
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "too many check-ins in flight; retry later")
		return
	case <-r.Context().Done():
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "request cancelled while waiting")
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
	if s.cfg.Index != nil && !s.cfg.Index.Has(s.cfg.Namespace, req.Edge) && s.cfg.Index.Count() >= s.cfg.MaxEdges {
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("fleet is full (%d edges); new edges are refused", s.cfg.MaxEdges))
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

// fieldManager owns every status field except conditions.
const fieldManager = "babis-ingest"

// applyEdge is the server-side apply body: identity plus the status fields
// ingest owns. It has no conditions, so the reconciler's fields are untouched.
type applyEdge struct {
	APIVersion string      `json:"apiVersion"`
	Kind       string      `json:"kind"`
	Metadata   applyMeta   `json:"metadata"`
	Status     applyStatus `json:"status"`
}

type applyMeta struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type applyStatus struct {
	LastCheckinTime metav1.Time           `json:"lastCheckinTime"`
	Telemetry       v1alpha1.Telemetry    `json:"telemetry"`
	Labels          map[string]string     `json:"labels,omitempty"`
	ArgoCD          v1alpha1.ArgoCDStatus `json:"argocd"`
	Apps            []v1alpha1.App        `json:"apps,omitempty"`
	AppsTotal       int                   `json:"appsTotal"`
	AppsSynced      int                   `json:"appsSynced"`
	AppsHealthy     int                   `json:"appsHealthy"`
}

// buildApply maps a check-in to the apply body. It is pure.
func buildApply(namespace string, req *wire.CheckinRequest, now time.Time) applyEdge {
	st := applyStatus{
		LastCheckinTime: metav1.NewTime(now),
		Telemetry:       v1alpha1.Telemetry{Source: v1alpha1.TelemetrySourceCheckin},
		Labels:          req.Labels,
		AppsTotal:       len(req.Apps),
	}
	if req.ArgoCD != nil {
		st.ArgoCD.Version = req.ArgoCD.Version
		for _, c := range req.ArgoCD.Components {
			st.ArgoCD.Components = append(st.ArgoCD.Components, v1alpha1.Component{
				Name: c.Name, Ready: clamp32(c.Ready), Desired: clamp32(c.Desired),
			})
		}
	}
	for _, a := range req.Apps {
		st.Apps = append(st.Apps, v1alpha1.App{Name: a.Name, Sync: a.Sync, Health: a.Health, Revision: a.Revision})
		if a.Sync == "Synced" {
			st.AppsSynced++
		}
		if a.Health == "Healthy" {
			st.AppsHealthy++
		}
	}
	return applyEdge{
		APIVersion: v1alpha1.GroupVersion.String(),
		Kind:       "Edge",
		Metadata:   applyMeta{Name: req.Edge, Namespace: namespace},
		Status:     st,
	}
}

// record applies the status with server-side apply: one call, no GET. When the
// Edge does not exist it creates it (an existing one is fine) and applies again.
// It never writes spec, metadata.labels or conditions.
func (s *Server) record(ctx context.Context, req *wire.CheckinRequest) error {
	body, err := json.Marshal(buildApply(s.cfg.Namespace, req, s.cfg.Now()))
	if err != nil {
		return err
	}
	edge := &v1alpha1.Edge{ObjectMeta: metav1.ObjectMeta{Namespace: s.cfg.Namespace, Name: req.Edge}}
	apply := func() error {
		return s.cfg.Client.Status().Patch(ctx, edge.DeepCopy(),
			client.RawPatch(types.ApplyPatchType, body),
			client.FieldOwner(fieldManager), client.ForceOwnership)
	}
	// The cache says the Edge is new: create first and skip an apply that would
	// get NotFound. A stale cache is harmless (AlreadyExists is fine).
	if s.cfg.Index == nil || s.cfg.Index.Has(s.cfg.Namespace, req.Edge) {
		err = apply()
		if !apierrors.IsNotFound(err) {
			return err
		}
	}
	if err := s.cfg.Client.Create(ctx, edge); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return apply()
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
