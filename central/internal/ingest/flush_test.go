package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/BillyGrande/babis/central/api/v1alpha1"
)

// newFlushServer is a Server on fakeAPI with a controllable clock.
func newFlushServer(t *testing.T, mod func(*Config), objs ...client.Object) (*Server, client.Client, *testClock) {
	t.Helper()
	c := fakeAPI(testScheme(t), objs...)
	s, clk := newEventServer(t, c, mod)
	return s, c, clk
}

func mustEvent(t *testing.T, s *Server, body string) {
	t.Helper()
	if rec := postEvent(s, "Bearer "+token, body); rec.Code != 202 {
		t.Fatalf("event: %d %s", rec.Code, rec.Body)
	}
}

func mustCheckin(t *testing.T, s *Server, body string) {
	t.Helper()
	if rec := post(s, "Bearer "+token, body); rec.Code != 200 {
		t.Fatalf("check-in: %d %s", rec.Code, rec.Body)
	}
}

// appNames lists the app names with their sync value.
func appNames(apps []v1alpha1.App) map[string]string {
	m := map[string]string{}
	for _, a := range apps {
		m[a.Name] = a.Sync
	}
	return m
}

func lastSeenOf(t *testing.T, apps []v1alpha1.App, name string) time.Time {
	t.Helper()
	for _, a := range apps {
		if a.Name == name {
			if a.LastSeen == nil {
				t.Fatalf("app %s has no lastSeen", name)
			}
			return a.LastSeen.Time
		}
	}
	t.Fatalf("app %s missing", name)
	return time.Time{}
}

var conflict = apierrors.NewConflict(schema.GroupResource{Group: "fleet.babis.dev", Resource: "edges"}, "e1", errors.New("object was modified"))

func TestFlushCreatesEdge(t *testing.T) {
	s, c, _ := newFlushServer(t, nil)
	mustEvent(t, s, event("e1", "state", "b", "OutOfSync", "Healthy", "r2"))
	mustEvent(t, s, event("e1", "state", "a", "Synced", "Healthy", "r1"))
	s.flushOnce(context.Background())

	st := getEdge(t, c, "e1").Status
	if st.Telemetry.Source != v1alpha1.TelemetrySourceNotifications {
		t.Errorf("source %q", st.Telemetry.Source)
	}
	if len(st.Apps) != 2 || st.Apps[0].Name != "a" || st.Apps[1].Name != "b" || st.Apps[0].Revision != "r1" {
		t.Errorf("apps %+v", st.Apps)
	}
	if !lastSeenOf(t, st.Apps, "a").Equal(clock) {
		t.Errorf("lastSeen %v", st.Apps[0].LastSeen)
	}
	if st.AppsTotal != 2 || st.AppsSynced != 1 || st.AppsHealthy != 2 {
		t.Errorf("counts %d/%d/%d", st.AppsTotal, st.AppsSynced, st.AppsHealthy)
	}
	if st.LastCheckinTime == nil || !st.LastCheckinTime.Time.Equal(clock) {
		t.Errorf("lastCheckinTime %v", st.LastCheckinTime)
	}
	if len(st.Conditions) != 0 {
		t.Errorf("flush wrote conditions: %+v", st.Conditions)
	}
	if s.buf.pending("e1") != 0 {
		t.Errorf("pending %d after flush", s.buf.pending("e1"))
	}
}

func TestFlushMergesAndRespectsInterval(t *testing.T) {
	s, c, clk := newFlushServer(t, nil)
	ctx := context.Background()
	mustEvent(t, s, event("e1", "state", "a", "Synced", "Healthy", "r1"))
	s.flushOnce(ctx)

	clk.Add(10 * time.Second)
	mustEvent(t, s, event("e1", "state", "b", "Synced", "Healthy", "r1"))
	mustEvent(t, s, event("e1", "state", "a", "OutOfSync", "Healthy", "r2"))
	s.flushOnce(ctx)
	if n := len(getEdge(t, c, "e1").Status.Apps); n != 1 || s.buf.pending("e1") != 2 {
		t.Fatalf("flushed inside the flush interval: %d apps, %d pending", n, s.buf.pending("e1"))
	}

	clk.Add(20 * time.Second) // 30 s after the first flush
	s.flushOnce(ctx)
	st := getEdge(t, c, "e1").Status
	if got := appNames(st.Apps); len(got) != 2 || got["a"] != "OutOfSync" || got["b"] != "Synced" {
		t.Errorf("apps %v", got)
	}
	if st.AppsTotal != 2 || st.AppsSynced != 1 {
		t.Errorf("counts %d/%d", st.AppsTotal, st.AppsSynced)
	}
	if !st.LastCheckinTime.Time.Equal(clock.Add(10 * time.Second)) {
		t.Errorf("lastCheckinTime %v", st.LastCheckinTime)
	}
}

func TestFlushTouch(t *testing.T) {
	s, c, clk := newFlushServer(t, nil)
	ctx := context.Background()
	same := event("e1", "state", "a", "Synced", "Healthy", "r1")
	mustEvent(t, s, same)
	s.flushOnce(ctx)
	rv := getEdge(t, c, "e1").ResourceVersion

	clk.Add(31 * time.Second)
	mustEvent(t, s, same)
	s.flushOnce(ctx)
	e := getEdge(t, c, "e1")
	if e.ResourceVersion != rv || s.buf.pending("e1") != 1 {
		t.Fatalf("unchanged state written while fresh (rv %s -> %s, pending %d)", rv, e.ResourceVersion, s.buf.pending("e1"))
	}

	clk.Add(5 * time.Minute) // lastCheckinTime is now older than the touch interval
	s.flushOnce(ctx)
	st := getEdge(t, c, "e1").Status
	want := clock.Add(31 * time.Second)
	if !st.LastCheckinTime.Time.Equal(want) || !lastSeenOf(t, st.Apps, "a").Equal(want) {
		t.Errorf("touch not written: lastCheckinTime %v apps %+v", st.LastCheckinTime, st.Apps)
	}
	if s.buf.pending("e1") != 0 {
		t.Errorf("pending %d", s.buf.pending("e1"))
	}
}

func TestFlushCheckinWins(t *testing.T) {
	s, c, clk := newFlushServer(t, nil)
	mustCheckin(t, s, full)
	rv := getEdge(t, c, "e1").ResourceVersion
	clk.Add(time.Minute)
	mustEvent(t, s, event("e1", "state", "z", "OutOfSync", "Missing", ""))
	s.flushOnce(context.Background())

	e := getEdge(t, c, "e1")
	if e.ResourceVersion != rv || e.Status.Telemetry.Source != v1alpha1.TelemetrySourceCheckin || e.Status.AppsTotal != 3 {
		t.Errorf("check-in status changed: %+v", e.Status)
	}
	if s.buf.pending("e1") != 0 {
		t.Errorf("entries kept for a fresh check-in edge: %d", s.buf.pending("e1"))
	}
}

func TestFlushTakesOverStaleCheckin(t *testing.T) {
	s, c, clk := newFlushServer(t, nil)
	mustCheckin(t, s, full)
	clk.Add(16 * time.Minute) // past the 15 min check-in heartbeat timeout
	mustEvent(t, s, event("e1", "state", "a", "Synced", "Healthy", "r9"))
	s.flushOnce(context.Background())

	e := getEdge(t, c, "e1")
	st := e.Status
	if st.Telemetry.Source != v1alpha1.TelemetrySourceNotifications {
		t.Errorf("source %q", st.Telemetry.Source)
	}
	if st.ArgoCD.Version != "" || len(st.ArgoCD.Components) != 0 || len(st.Labels) != 0 {
		t.Errorf("argocd/labels from the check-in kept: %+v %v", st.ArgoCD, st.Labels)
	}
	// b and c came from the check-in (no lastSeen) and are pruned.
	if len(st.Apps) != 1 || st.Apps[0].Revision != "r9" || st.AppsTotal != 1 || st.AppsSynced != 1 || st.AppsHealthy != 1 {
		t.Errorf("apps %+v counts %d/%d/%d", st.Apps, st.AppsTotal, st.AppsSynced, st.AppsHealthy)
	}
	if !st.LastCheckinTime.Time.Equal(clock.Add(16 * time.Minute)) {
		t.Errorf("lastCheckinTime %v", st.LastCheckinTime)
	}
}

func TestFlushCheckinAfterNotifications(t *testing.T) {
	s, c, clk := newFlushServer(t, nil)
	mustEvent(t, s, event("e1", "state", "x", "Synced", "Healthy", ""))
	s.flushOnce(context.Background())
	clk.Add(time.Minute)
	mustCheckin(t, s, full)

	st := getEdge(t, c, "e1").Status
	if st.Telemetry.Source != v1alpha1.TelemetrySourceCheckin || st.ArgoCD.Version != "v3.2.0" {
		t.Errorf("status %+v", st)
	}
	if got := appNames(st.Apps); len(got) != 3 || got["x"] != "" {
		t.Errorf("apps not replaced: %v", got)
	}
	for _, a := range st.Apps {
		if a.LastSeen != nil {
			t.Errorf("check-in app %s has lastSeen", a.Name)
		}
	}
}

func TestFlushKeepsReconcilerConditions(t *testing.T) {
	s, c, clk := newFlushServer(t, nil)
	ctx := context.Background()
	mustEvent(t, s, event("e1", "state", "a", "Synced", "Healthy", ""))
	s.flushOnce(ctx)

	cond := metav1.Condition{
		Type: "Reachable", Status: metav1.ConditionTrue, Reason: "HeartbeatFresh", Message: "m",
		LastTransitionTime: metav1.NewTime(clock),
	}
	body, _ := json.Marshal(map[string]any{
		"apiVersion": v1alpha1.GroupVersion.String(), "kind": "Edge",
		"metadata": map[string]string{"name": "e1", "namespace": ns},
		"status":   map[string]any{"conditions": []metav1.Condition{cond}},
	})
	target := &v1alpha1.Edge{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "e1"}}
	if err := c.Status().Patch(ctx, target, client.RawPatch(types.ApplyPatchType, body),
		client.FieldOwner("babis-reconciler"), client.ForceOwnership); err != nil {
		t.Fatal(err)
	}

	clk.Add(31 * time.Second)
	mustEvent(t, s, event("e1", "state", "a", "OutOfSync", "Healthy", ""))
	s.flushOnce(ctx)
	st := getEdge(t, c, "e1").Status
	if len(st.Conditions) != 1 || st.Conditions[0].Reason != "HeartbeatFresh" {
		t.Errorf("conditions: %+v", st.Conditions)
	}
	if st.Apps[0].Sync != "OutOfSync" {
		t.Errorf("flush not written: %+v", st.Apps)
	}
}

// countingReader counts the reads that go to the cache.
type countingReader struct {
	client.Reader
	gets atomic.Int32
}

func (r *countingReader) Get(ctx context.Context, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
	r.gets.Add(1)
	return r.Reader.Get(ctx, k, o, opts...)
}

func TestFlushConflict(t *testing.T) {
	tests := []struct {
		name      string
		failures  int32 // applies that answer 409
		wantPatch int32
		written   bool
	}{
		{"409 once, then success", 1, 2, true},
		{"409 three times, entries restored", 3, 3, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var patches, directGets atomic.Int32
			var s *Server
			base := fakeAPI(testScheme(t))
			c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
					directGets.Add(1)
					return c.Get(ctx, k, o, opts...)
				},
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
					n := patches.Add(1)
					if n == 1 {
						// A newer event for the same app arrives during the flush.
						s.buf.add("e1", "a", entry{event: "state", sync: "Newer", received: clock.Add(time.Second)})
					}
					if n <= tc.failures {
						return conflict
					}
					return c.SubResource(sub).Patch(ctx, o, p, opts...)
				},
			})
			reader := &countingReader{Reader: base}
			s, _ = newEventServer(t, c, func(cfg *Config) { cfg.Reader = reader })
			mustEvent(t, s, event("e1", "state", "a", "Synced", "Healthy", ""))
			s.flushOnce(context.Background())

			if patches.Load() != tc.wantPatch {
				t.Errorf("applies %d, want %d", patches.Load(), tc.wantPatch)
			}
			if reader.gets.Load() != 1 {
				t.Errorf("cache reads %d, want 1 (retries read the API server)", reader.gets.Load())
			}
			// The Edge is created on the first flush, so the direct client
			// reads only on the retries.
			if want := tc.wantPatch - 1; directGets.Load() != want {
				t.Errorf("direct reads %d, want %d", directGets.Load(), want)
			}
			e := getEdge(t, base, "e1")
			if tc.written {
				if len(e.Status.Apps) != 1 || e.Status.Apps[0].Sync != "Synced" {
					t.Errorf("apps %+v", e.Status.Apps)
				}
				if got := s.buf.edges["e1"].entries["a"].sync; got != "Newer" {
					t.Errorf("event that arrived during the flush lost: %q", got)
				}
				return
			}
			if len(e.Status.Apps) != 0 {
				t.Errorf("status written after 3 conflicts: %+v", e.Status.Apps)
			}
			if s.buf.pending("e1") != 1 || s.buf.edges["e1"].entries["a"].sync != "Newer" {
				t.Errorf("restore lost the newer event: %+v", s.buf.edges["e1"].entries)
			}
		})
	}
}

// TestFlushStaleCache uses the fake's own resourceVersion check: the Reader
// has an old copy of the Edge, the apply with its resourceVersion fails with
// 409, and the retry reads the API server.
func TestFlushStaleCache(t *testing.T) {
	scheme := testScheme(t)
	old := &v1alpha1.Edge{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "e1"}}
	c := fakeAPI(scheme, old.DeepCopy())
	stale := fake.NewClientBuilder().WithScheme(scheme).WithObjects(old.DeepCopy()).Build()
	cur := getEdge(t, c, "e1")
	cur.Spec.DisplayName = "changed"
	if err := c.Update(context.Background(), &cur); err != nil {
		t.Fatal(err)
	}
	var conflicts atomic.Int32
	counted := interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
			err := c.SubResource(sub).Patch(ctx, o, p, opts...)
			if apierrors.IsConflict(err) {
				conflicts.Add(1)
			}
			return err
		},
	})
	s, _ := newEventServer(t, counted, func(cfg *Config) { cfg.Reader = stale })
	mustEvent(t, s, event("e1", "state", "a", "Synced", "Healthy", ""))
	s.flushOnce(context.Background())
	if conflicts.Load() != 1 {
		t.Errorf("conflicts %d, want 1 (the fake checks resourceVersion on apply)", conflicts.Load())
	}
	if st := getEdge(t, c, "e1").Status; len(st.Apps) != 1 {
		t.Errorf("not written after a stale read: %+v", st)
	}
}

func TestFlushOtherErrorRestores(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	base := fakeAPI(testScheme(t))
	c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
			if fail.Load() {
				return apierrors.NewServiceUnavailable("down")
			}
			return c.SubResource(sub).Patch(ctx, o, p, opts...)
		},
	})
	s, clk := newEventServer(t, c, nil)
	mustEvent(t, s, event("e1", "state", "a", "Synced", "Healthy", ""))
	s.flushOnce(context.Background())
	if s.buf.pending("e1") != 1 {
		t.Fatalf("entries not restored after an error")
	}
	fail.Store(false)
	s.flushOnce(context.Background())
	if s.buf.pending("e1") != 1 {
		t.Error("retried before the flush interval")
	}
	clk.Add(30 * time.Second)
	s.flushOnce(context.Background())
	if len(getEdge(t, base, "e1").Status.Apps) != 1 || s.buf.pending("e1") != 0 {
		t.Error("not written after the API came back")
	}
}

func TestShutdownFlush(t *testing.T) {
	s, c, clk := newFlushServer(t, nil)
	same := event("e1", "state", "a", "Synced", "Healthy", "")
	mustEvent(t, s, same)
	s.flushOnce(context.Background())
	clk.Add(time.Second)
	// Inside the flush interval and not material: a normal flush keeps it.
	mustEvent(t, s, same)
	mustEvent(t, s, event("e2", "state", "b", "Synced", "Healthy", ""))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.runFlusher(ctx) // returns after the last flush

	if got := lastSeenOf(t, getEdge(t, c, "e1").Status.Apps, "a"); !got.Equal(clock.Add(time.Second)) {
		t.Errorf("e1 lastSeen %v", got)
	}
	if len(getEdge(t, c, "e2").Status.Apps) != 1 {
		t.Error("e2 not written at shutdown")
	}
}

func TestStartStopsAndFlushes(t *testing.T) {
	s, c, _ := newFlushServer(t, func(cfg *Config) { cfg.Addr = "127.0.0.1:0" })
	mustEvent(t, s, event("e1", "state", "a", "Synced", "Healthy", ""))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Start did not return")
	}
	if len(getEdge(t, c, "e1").Status.Apps) != 1 {
		t.Error("pending event not written on stop")
	}
}

func TestEventApplyBody(t *testing.T) {
	e := &v1alpha1.Edge{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "e1", ResourceVersion: "42"}}
	e.Status.ArgoCD.Version = "v3.2.0"
	e.Status.Labels = map[string]string{"a": "b"}
	b, err := json.Marshal(buildEventApply(e, map[string]entry{"a": st("Synced", "", "", 0)}, clock, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var m struct{ Metadata, Status map[string]any }
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if md := m.Metadata; md["resourceVersion"] != "42" || md["name"] != "e1" || md["namespace"] != ns || len(md) != 3 {
		t.Errorf("metadata %v", md)
	}
	if bytes.Contains(b, []byte(`"spec"`)) {
		t.Error("spec in apply body")
	}
	for _, k := range []string{"argocd", "labels", "conditions"} {
		if _, ok := m.Status[k]; ok {
			t.Errorf("status.%s in apply body", k)
		}
	}
	for _, k := range []string{"lastCheckinTime", "telemetry", "apps", "appsTotal", "appsSynced", "appsHealthy"} {
		if _, ok := m.Status[k]; !ok {
			t.Errorf("status.%s missing", k)
		}
	}
}
