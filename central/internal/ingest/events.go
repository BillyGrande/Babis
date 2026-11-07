package ingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/BillyGrande/babis/central/internal/wire"
)

const (
	// AppEventsPath is the endpoint for ArgoCD notification events.
	AppEventsPath = "/api/v1alpha1/app-events"
	// MaxEventBodyBytes caps an app-event body. One event is below 1 KB.
	MaxEventBodyBytes = 64 << 10
)

// handleAppEvent puts one app event into the buffer and answers 202. It makes
// no API server call: the flush writes the Edge, and creates it if needed.
func (s *Server) handleAppEvent(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
		return
	}
	body, code, msg := s.readEventBody(w, r)
	if code != 0 {
		if code == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "5")
		}
		writeError(w, code, msg)
		return
	}
	var req wire.AppEventRequest
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
	e := entry{
		event: req.Event, sync: req.App.Sync, health: req.App.Health, revision: req.App.Revision,
		received: s.cfg.Now().UTC().Truncate(time.Microsecond),
	}
	if !s.buf.add(req.Edge, req.App.Name, e) {
		// The sender gains nothing from an error, and an error costs it retries.
		s.cfg.Log.V(1).Info("app event dropped: buffer cap reached", "edge", req.Edge, "app", req.App.Name)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("{}"))
}

// readEventBody reads the body while it holds an in-flight slot (shared with
// the check-in). It waits at most EventInflightWait for a slot, so the
// notifications controller on the edge is not held long. A non-zero code is
// the error answer.
func (s *Server) readEventBody(w http.ResponseWriter, r *http.Request) (body []byte, code int, msg string) {
	wait := time.NewTimer(s.cfg.EventInflightWait)
	defer wait.Stop()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-wait.C:
		return nil, http.StatusServiceUnavailable, "too many requests in flight; retry later"
	case <-r.Context().Done():
		return nil, http.StatusServiceUnavailable, "request cancelled while waiting"
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxEventBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, http.StatusRequestEntityTooLarge, fmt.Sprintf("body exceeds %d bytes", MaxEventBodyBytes)
		}
		return nil, http.StatusBadRequest, "cannot read body: " + err.Error()
	}
	return body, 0, ""
}
