package ingest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/BillyGrande/babis/central/api/v1alpha1"
	"github.com/BillyGrande/babis/central/internal/wire"
)

// testClock is a clock that tests move by hand. The flusher reads it from
// several goroutines.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *testClock { return &testClock{t: clock} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

// newEventServer builds a Server on client c with a controllable clock.
// mod may change the Config before New.
func newEventServer(t *testing.T, c client.Client, mod func(*Config)) (*Server, *testClock) {
	t.Helper()
	clk := newClock()
	cfg := Config{Token: token, Namespace: ns, Client: c, Now: clk.Now}
	if mod != nil {
		mod(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, clk
}

// countingAPI is a fake API server that counts every call.
func countingAPI(t *testing.T, calls *atomic.Int32) client.Client {
	inc := func() { calls.Add(1) }
	return fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&v1alpha1.Edge{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
				inc()
				return c.Get(ctx, k, o, opts...)
			},
			List: func(ctx context.Context, c client.WithWatch, l client.ObjectList, opts ...client.ListOption) error {
				inc()
				return c.List(ctx, l, opts...)
			},
			Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				inc()
				return c.Create(ctx, o, opts...)
			},
			Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
				inc()
				return c.Update(ctx, o, opts...)
			},
			Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
				inc()
				return c.Patch(ctx, o, p, opts...)
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
				inc()
				return c.SubResource(sub).Patch(ctx, o, p, opts...)
			},
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
				inc()
				return c.SubResource(sub).Update(ctx, o, opts...)
			},
		}).Build()
}

func postEvent(s *Server, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, AppEventsPath, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func event(edge, ev, app, sync, health, rev string) string {
	return fmt.Sprintf(`{"edge":%q,"event":%q,"app":{"name":%q,"sync":%q,"health":%q,"revision":%q}}`,
		edge, ev, app, sync, health, rev)
}

func TestAppEventAccepted(t *testing.T) {
	var calls atomic.Int32
	s, clk := newEventServer(t, countingAPI(t, &calls), nil)
	clk.Add(1500 * time.Nanosecond) // the receipt time is cut to microseconds
	tests := []struct {
		name, body, app string
		want            entry
	}{
		{"state", event("e1", "state", "a", "Synced", "Healthy", "r1"), "a",
			entry{event: "state", sync: "Synced", health: "Healthy", revision: "r1"}},
		{"null fields", `{"edge":"e1","event":"state","app":{"name":"b","sync":null,"health":null,"revision":null}}`, "b",
			entry{event: "state"}},
		{"deleted", `{"edge":"e1","event":"deleted","app":{"name":"c"}}`, "c",
			entry{event: "deleted"}},
		{"unknown fields", `{"edge":"e1","event":"state","x":1,"app":{"name":"d","y":"z"}}`, "d",
			entry{event: "state"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := postEvent(s, "Bearer "+token, tc.body)
			if rec.Code != http.StatusAccepted || rec.Body.String() != "{}" {
				t.Fatalf("got %d %q", rec.Code, rec.Body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("content type %q", ct)
			}
			got := s.buf.take("e1")[tc.app]
			tc.want.received = clock.Add(time.Microsecond)
			if got != tc.want {
				t.Errorf("entry %+v, want %+v", got, tc.want)
			}
		})
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("handler made %d API calls, want 0", n)
	}
}

func TestAppEventRejects(t *testing.T) {
	big := `{"edge":"e1","event":"state","app":{"name":"a","x":"` + strings.Repeat("x", MaxEventBodyBytes) + `"}}`
	tests := []struct {
		name, auth, body string
		code             int
		msg              string
	}{
		{"no token", "", event("e1", "state", "a", "", "", ""), 401, "bearer"},
		{"wrong token", "Bearer nope", event("e1", "state", "a", "", "", ""), 401, "bearer"},
		{"too large", "Bearer " + token, big, 413, "exceeds"},
		{"invalid json", "Bearer " + token, `{"edge":`, 400, "invalid JSON"},
		{"edge missing", "Bearer " + token, `{"event":"state","app":{"name":"a"}}`, 400, "edge:"},
		{"edge uppercase", "Bearer " + token, event("E1", "state", "a", "", "", ""), 400, "edge:"},
		{"event unknown", "Bearer " + token, event("e1", "created", "a", "", "", ""), 400, "event:"},
		{"app missing", "Bearer " + token, `{"edge":"e1","event":"state"}`, 400, "app.name"},
		{"sync too long", "Bearer " + token, event("e1", "state", "a", strings.Repeat("s", 254), "", ""), 400, "app.sync"},
		{"health a number", "Bearer " + token, `{"edge":"e1","event":"state","app":{"name":"a","health":3}}`, 400, "invalid JSON"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s, _ := newEventServer(t, countingAPI(t, &calls), nil)
			rec := postEvent(s, tc.auth, tc.body)
			if rec.Code != tc.code {
				t.Fatalf("code %d, want %d (%s)", rec.Code, tc.code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.msg) || !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("body %q lacks %q", rec.Body, tc.msg)
			}
			if s.buf.pending("e1") != 0 || s.buf.pending("E1") != 0 {
				t.Error("rejected event was buffered")
			}
			if n := calls.Load(); n != 0 {
				t.Errorf("handler made %d API calls", n)
			}
		})
	}
}

func TestAppEventMaxEdges(t *testing.T) {
	tests := []struct {
		name string
		edge string
		n    int
		code int
	}{
		{"unknown edge, fleet full", "new", 2, 429},
		{"known edge, fleet full", "old", 2, 202},
		{"unknown edge, below the limit", "new", 1, 202},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newEventServer(t, fakeAPI(testScheme(t)), func(c *Config) {
				c.Index = fakeIndex{known: map[string]bool{"old": true}, n: tc.n}
				c.MaxEdges = 2
			})
			rec := postEvent(s, "Bearer "+token, event(tc.edge, "state", "a", "", "", ""))
			if rec.Code != tc.code {
				t.Fatalf("code %d, want %d (%s)", rec.Code, tc.code, rec.Body)
			}
			if want := map[bool]int{true: 0, false: 1}[tc.code == 429]; s.buf.pending(tc.edge) != want {
				t.Errorf("pending %d, want %d", s.buf.pending(tc.edge), want)
			}
		})
	}
}

func TestAppEventInflight(t *testing.T) {
	s, _ := newEventServer(t, fakeAPI(testScheme(t)), func(c *Config) {
		c.MaxInflight = 1
		c.EventInflightWait = 20 * time.Millisecond
	})
	s.slots <- struct{}{} // the only slot is taken, for example by a check-in
	rec := postEvent(s, "Bearer "+token, event("e1", "state", "a", "", "", ""))
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("code %d retry-after %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := postEvent(s, "Bearer bad", event("e1", "state", "a", "", "", "")); rec.Code != 401 {
		t.Errorf("unauthenticated got %d, want 401 before the wait", rec.Code)
	}
	<-s.slots
	if rec := postEvent(s, "Bearer "+token, event("e1", "state", "a", "", "", "")); rec.Code != 202 {
		t.Errorf("code %d after slot freed", rec.Code)
	}
	if len(s.slots) != 0 {
		t.Error("slot not given back")
	}
}

func TestAppEventDefaultInflightWait(t *testing.T) {
	s, _ := newEventServer(t, fakeAPI(testScheme(t)), nil)
	if s.cfg.EventInflightWait != 2*time.Second || s.cfg.InflightWait != 20*time.Second {
		t.Errorf("waits %s / %s", s.cfg.EventInflightWait, s.cfg.InflightWait)
	}
}

func TestBufferAdd(t *testing.T) {
	b := newBuffer(2, 1)
	tests := []struct {
		name, edge, app string
		e               entry
		ok              bool
		wantSync        string
	}{
		{"first", "e1", "a", st("S1", "", "", 0), true, "S1"},
		{"newer replaces", "e1", "a", st("S2", "", "", time.Second), true, "S2"},
		{"equal ignored", "e1", "a", st("S3", "", "", time.Second), true, "S2"},
		{"older ignored", "e1", "a", st("S4", "", "", 0), true, "S2"},
		{"second app", "e1", "b", st("S1", "", "", 0), true, ""},
		{"app cap drops", "e1", "c", st("S1", "", "", 0), false, ""},
		{"edge cap drops", "e2", "a", st("S1", "", "", 0), false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if ok := b.add(tc.edge, tc.app, tc.e); ok != tc.ok {
				t.Fatalf("add = %v, want %v", ok, tc.ok)
			}
			if tc.wantSync != "" && b.edges["e1"].entries["a"].sync != tc.wantSync {
				t.Errorf("sync %q, want %q", b.edges["e1"].entries["a"].sync, tc.wantSync)
			}
		})
	}
	if b.pending("e1") != 2 || b.pending("e2") != 0 {
		t.Errorf("pending %d %d", b.pending("e1"), b.pending("e2"))
	}
}

func TestBufferTakeRestoreDue(t *testing.T) {
	b := newBuffer(wire.MaxApps, 10)
	b.add("e1", "a", st("Old", "", "", 0))
	b.add("e1", "b", st("Old", "", "", 0))
	if got := b.due(clock, time.Minute); len(got) != 1 || got[0] != "e1" {
		t.Fatalf("due %v, want [e1] (a zero lastCheck is due at once)", got)
	}
	if got := b.due(clock, 0); len(got) != 0 {
		t.Errorf("busy edge due again: %v", got)
	}
	taken := b.take("e1")
	// An event arrives while the flush runs.
	b.add("e1", "a", st("New", "", "", time.Second))
	b.restore("e1", taken)
	b.done("e1", clock)
	if e := b.edges["e1"].entries; e["a"].sync != "New" || e["b"].sync != "Old" {
		t.Errorf("after restore %+v", e)
	}
	if got := b.due(clock.Add(59*time.Second), time.Minute); len(got) != 0 {
		t.Errorf("due before the interval: %v", got)
	}
	if got := b.due(clock.Add(time.Minute), time.Minute); len(got) != 1 {
		t.Errorf("not due after the interval: %v", got)
	}
	b.take("e1")
	b.done("e1", clock.Add(time.Minute))
	// An empty edge stays (its lastCheck holds the interval), then goes.
	b.due(clock.Add(time.Minute+time.Second), time.Minute)
	if _, ok := b.edges["e1"]; !ok {
		t.Error("empty edge forgotten before its interval ended")
	}
	b.due(clock.Add(2*time.Minute), time.Minute)
	if _, ok := b.edges["e1"]; ok {
		t.Error("empty edge kept after its interval")
	}
}
