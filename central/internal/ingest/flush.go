package ingest

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/BillyGrande/babis/central/api/v1alpha1"
)

const (
	// flushAttempts is the number of applies per edge and flush when the
	// API server answers 409 Conflict.
	flushAttempts = 3
	// shutdownFlushTimeout bounds the last flush when the operator stops.
	shutdownFlushTimeout = 5 * time.Second
)

// flushResult is what one flush of one edge did.
type flushResult int

const (
	flushWritten flushResult = iota // the status was applied
	flushKept                       // nothing material, status fresh: entries stay
	flushDropped                    // a fresh check-in owns the status: entries dropped
)

// eventApply is the server-side apply body of a flush. It has no argocd, no
// labels and no conditions: SSA removes the argocd and labels fields that
// babis-ingest owned from a check-in, and conditions stay with the reconciler.
// resourceVersion makes the apply conditional: the API server answers 409 if
// the Edge changed after it was read.
type eventApply struct {
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	Metadata   eventApplyMeta   `json:"metadata"`
	Status     eventApplyStatus `json:"status"`
}

type eventApplyMeta struct {
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	ResourceVersion string `json:"resourceVersion"`
}

type eventApplyStatus struct {
	LastCheckinTime metav1.Time        `json:"lastCheckinTime"`
	Telemetry       v1alpha1.Telemetry `json:"telemetry"`
	Apps            []v1alpha1.App     `json:"apps,omitempty"`
	// DeletedApps are the tombstones. The check-in apply has none, so a
	// check-in (a full snapshot) removes them.
	DeletedApps []v1alpha1.DeletedApp `json:"deletedApps,omitempty"`
	AppsTotal   int                   `json:"appsTotal"`
	AppsSynced  int                   `json:"appsSynced"`
	AppsHealthy int                   `json:"appsHealthy"`
}

// runFlusher flushes due edges every FlushTick until ctx ends, then does one
// last flush of every edge with entries.
func (s *Server) runFlusher(ctx context.Context) {
	t := time.NewTicker(s.cfg.FlushTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Stop()
			s.flushAll(ctx)
			return
		case <-t.C:
			s.flushOnce(ctx)
		}
	}
}

// flushOnce flushes the edges that are due and waits for them.
func (s *Server) flushOnce(ctx context.Context) {
	s.flushEdges(ctx, s.buf.due(s.cfg.Now(), s.cfg.EventFlushInterval), false)
}

// flushAll is the best-effort flush at shutdown. It ignores the flush and the
// touch interval and is bounded by shutdownFlushTimeout. ctx may already be
// cancelled; only its values are used.
func (s *Server) flushAll(ctx context.Context) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownFlushTimeout)
	defer cancel()
	s.flushEdges(fctx, s.buf.due(s.cfg.Now(), 0), true)
}

// flushEdges flushes the named edges (already marked busy) on at most
// FlushWorkers goroutines and waits for all of them.
func (s *Server) flushEdges(ctx context.Context, names []string, force bool) {
	sem := make(chan struct{}, s.cfg.FlushWorkers)
	var wg sync.WaitGroup
	for _, name := range names {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			s.flushEdge(ctx, name, force)
		}()
	}
	wg.Wait()
}

// flushEdge writes the buffered entries of one edge into its status (spec
// "Central / Flush"). Entries that are not written go back to the buffer. A
// failure also waits one flush interval before the next try, so a broken API
// server is not hit every second for every edge.
func (s *Server) flushEdge(ctx context.Context, name string, force bool) {
	log := s.cfg.Log.WithValues("edge", name)
	entries := s.buf.take(name)
	defer func() { s.buf.done(name, s.cfg.Now()) }()
	if len(entries) == 0 {
		return
	}
	key := types.NamespacedName{Namespace: s.cfg.Namespace, Name: name}
	reader := s.cfg.Reader // the cache first; after a 409 the API server
	for attempt := 1; ; attempt++ {
		edge, err := s.readEdge(ctx, reader, key)
		if err != nil {
			s.buf.restore(name, entries)
			log.Error(err, "flush: read edge")
			return
		}
		res, err := s.applyEntries(ctx, edge, entries, force)
		switch {
		case err == nil && res == flushKept:
			s.buf.restore(name, entries)
			return
		case err == nil && res == flushDropped:
			log.V(1).Info("app events dropped: the edge has a fresh check-in", "entries", len(entries))
			return
		case err == nil:
			return
		case apierrors.IsConflict(err) && attempt < flushAttempts:
			reader = s.cfg.Client
			continue
		case apierrors.IsConflict(err):
			// Another replica or the reconciler keeps writing the Edge. The
			// entries stay, and the next flush interval tries again.
			s.buf.restore(name, entries)
			log.Info("flush: conflict on every attempt; retry at the next interval", "attempts", attempt)
			return
		default:
			s.buf.restore(name, entries)
			log.Error(err, "flush: apply status", "attempt", attempt)
			return
		}
	}
}

// readEdge gets the Edge with r. A missing Edge is created with the direct
// client (one that another replica created meanwhile is fine) and read again
// with it, so the resourceVersion is current.
func (s *Server) readEdge(ctx context.Context, r client.Reader, key types.NamespacedName) (*v1alpha1.Edge, error) {
	var edge v1alpha1.Edge
	err := r.Get(ctx, key, &edge)
	if !apierrors.IsNotFound(err) {
		return &edge, err
	}
	created := &v1alpha1.Edge{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}
	if err := s.cfg.Client.Create(ctx, created); err == nil {
		return created, nil
	} else if !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	if err := s.cfg.Client.Get(ctx, key, &edge); err != nil {
		return nil, err
	}
	return &edge, nil
}

// applyEntries decides what to do with the entries for this read of the Edge
// and applies the merged status when needed. force skips the touch check.
func (s *Server) applyEntries(ctx context.Context, edge *v1alpha1.Edge, entries map[string]entry, force bool) (flushResult, error) {
	now := s.cfg.Now()
	st := &edge.Status
	if checkinFresh(edge, s.cfg.CheckinHeartbeatTimeout, now) {
		return flushDropped, nil
	}
	touchFresh := st.LastCheckinTime != nil && st.LastCheckinTime.After(now.Add(-s.cfg.EventTouchInterval))
	if !force && touchFresh && !material(st.Apps, st.DeletedApps, entries) {
		return flushKept, nil
	}
	body, err := json.Marshal(buildEventApply(edge, entries, now, s.cfg.EventAppStale))
	if err != nil {
		return 0, err
	}
	err = s.cfg.Client.Status().Patch(ctx, edge.DeepCopy(),
		client.RawPatch(types.ApplyPatchType, body),
		client.FieldOwner(fieldManager), client.ForceOwnership)
	return flushWritten, err
}

// buildEventApply merges the entries into the Edge's apps and builds the apply
// body. It is pure. lastCheckinTime moves to the newest applied receipt time
// and never back; a new Edge with nothing applied gets now.
func buildEventApply(edge *v1alpha1.Edge, entries map[string]entry, now time.Time, stale time.Duration) eventApply {
	apps, deleted, newest := mergeApps(edge.Status.Apps, edge.Status.DeletedApps, entries, now, stale)
	last := newest
	if cur := edge.Status.LastCheckinTime; cur != nil && cur.After(last) {
		last = cur.Time
	}
	if last.IsZero() {
		last = now
	}
	synced, healthy := countApps(apps)
	return eventApply{
		APIVersion: v1alpha1.GroupVersion.String(),
		Kind:       "Edge",
		Metadata: eventApplyMeta{
			Name: edge.Name, Namespace: edge.Namespace, ResourceVersion: edge.ResourceVersion,
		},
		Status: eventApplyStatus{
			LastCheckinTime: metav1.NewTime(last),
			Telemetry:       v1alpha1.Telemetry{Source: v1alpha1.TelemetrySourceNotifications},
			Apps:            apps,
			DeletedApps:     deleted,
			AppsTotal:       len(apps),
			AppsSynced:      synced,
			AppsHealthy:     healthy,
		},
	}
}
