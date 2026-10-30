package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/BillyGrande/babis/central/api/v1alpha1"
	"github.com/BillyGrande/babis/central/internal/wire"
)

const (
	ns    = "fleet"
	token = "s3cret"
	empty = `{"generation":0,"desired":{},"extensions":{}}`
)

var clock = time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)

func newServer(t *testing.T, objs ...client.Object) (*Server, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fakeAPI(scheme, objs...)
	s, err := New(Config{Token: token, Namespace: ns, Client: c, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

// fakeAPI is controller-runtime's fake client with server-side apply on the
// status subresource. The fake creates a missing object on a status apply; the
// real API server answers NotFound, so this wrapper does the same.
func fakeAPI(scheme *runtime.Scheme, objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.Edge{}).WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
				if p.Type() == types.ApplyPatchType {
					var cur v1alpha1.Edge
					if err := c.Get(ctx, client.ObjectKeyFromObject(obj), &cur); err != nil {
						return err
					}
				}
				return c.SubResource(sub).Patch(ctx, obj, p, opts...)
			},
		}).Build()
}

func post(s *Server, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, CheckinPath, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func getEdge(t *testing.T, c client.Client, name string) v1alpha1.Edge {
	t.Helper()
	var e v1alpha1.Edge
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestRejects(t *testing.T) {
	big := `{"edge":"e1","labels":{"a":"` + strings.Repeat("x", MaxBodyBytes) + `"}}`
	tests := []struct {
		name string
		auth string
		body string
		code int
		msg  string
	}{
		{"no token", "", `{"edge":"e1"}`, 401, "bearer"},
		{"wrong token", "Bearer nope", `{"edge":"e1"}`, 401, "bearer"},
		{"wrong scheme", "Basic " + token, `{"edge":"e1"}`, 401, "bearer"},
		{"too large", "Bearer " + token, big, 413, "exceeds"},
		{"invalid json", "Bearer " + token, `{"edge":`, 400, "invalid JSON"},
		{"invalid payload", "Bearer " + token, `{"edge":"A"}`, 400, "edge:"},
		{"component missing ready", "Bearer " + token, `{"edge":"e1","argocd":{"components":[{"name":"x","desired":1}]}}`, 400, "ready"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, c := newServer(t)
			rec := post(s, tc.auth, tc.body)
			if rec.Code != tc.code {
				t.Fatalf("code %d, want %d (%s)", rec.Code, tc.code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.msg) || !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("body %q lacks %q", rec.Body, tc.msg)
			}
			var l v1alpha1.EdgeList
			if err := c.List(context.Background(), &l); err != nil || len(l.Items) != 0 {
				t.Errorf("edges created on rejected request: %v %v", l.Items, err)
			}
		})
	}
}

const full = `{
 "edge":"e1","labels":{"region":"gr"},
 "argocd":{"version":"v3.2.0","components":[{"name":"repo","ready":1,"desired":1},{"name":"ctl","ready":0,"desired":1}]},
 "apps":[
  {"name":"a","sync":"Synced","health":"Healthy","revision":"r1"},
  {"name":"b","sync":"OutOfSync","health":"Healthy"},
  {"name":"c","sync":"Synced","health":"Degraded"}]}`

func TestFirstCheckinCreatesEdge(t *testing.T) {
	s, c := newServer(t)
	rec := post(s, "Bearer "+token, full)
	if rec.Code != 200 || rec.Body.String() != empty {
		t.Fatalf("got %d %q", rec.Code, rec.Body)
	}
	e := getEdge(t, c, "e1")
	st := e.Status
	if st.LastCheckinTime == nil || !st.LastCheckinTime.Time.Equal(clock) {
		t.Errorf("lastCheckinTime %v", st.LastCheckinTime)
	}
	if st.Telemetry.Source != "checkin" || st.Labels["region"] != "gr" || st.ArgoCD.Version != "v3.2.0" {
		t.Errorf("status %+v", st)
	}
	if len(st.ArgoCD.Components) != 2 || st.ArgoCD.Components[1] != (v1alpha1.Component{Name: "ctl", Ready: 0, Desired: 1}) {
		t.Errorf("components %+v", st.ArgoCD.Components)
	}
	if len(st.Apps) != 3 || st.Apps[0].Revision != "r1" {
		t.Errorf("apps %+v", st.Apps)
	}
	if st.AppsTotal != 3 || st.AppsSynced != 2 || st.AppsHealthy != 2 {
		t.Errorf("counts %d/%d/%d", st.AppsTotal, st.AppsSynced, st.AppsHealthy)
	}
	if len(st.Conditions) != 0 {
		t.Errorf("ingest wrote conditions: %+v", st.Conditions)
	}
}

func TestSecondCheckinReplacesStatus(t *testing.T) {
	s, c := newServer(t)
	post(s, "Bearer "+token, full)
	clock2 := clock.Add(time.Minute)
	s.cfg.Now = func() time.Time { return clock2 }
	rec := post(s, "Bearer "+token, `{"edge":"e1","apps":[{"name":"a","sync":"Synced","health":"Healthy"}]}`)
	if rec.Code != 200 || rec.Body.String() != empty {
		t.Fatalf("got %d %q", rec.Code, rec.Body)
	}
	st := getEdge(t, c, "e1").Status
	if !st.LastCheckinTime.Time.Equal(clock2) {
		t.Errorf("lastCheckinTime %v", st.LastCheckinTime)
	}
	if st.AppsTotal != 1 || st.AppsSynced != 1 || st.AppsHealthy != 1 || len(st.Apps) != 1 {
		t.Errorf("apps not replaced: %+v", st)
	}
	if st.ArgoCD.Version != "" || len(st.ArgoCD.Components) != 0 || len(st.Labels) != 0 {
		t.Errorf("stale argocd/labels kept: %+v", st)
	}
}

func TestExistingEdgeUntouched(t *testing.T) {
	cond := metav1.Condition{
		Type: "Reachable", Status: metav1.ConditionTrue, Reason: "HeartbeatFresh",
		LastTransitionTime: metav1.NewTime(clock.Add(-time.Hour)),
	}
	existing := &v1alpha1.Edge{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "e1", Labels: map[string]string{"customer": "acme"}},
		Spec: v1alpha1.EdgeSpec{
			DisplayName:      "Athens",
			HeartbeatTimeout: &metav1.Duration{Duration: 5 * time.Minute},
		},
		Status: v1alpha1.EdgeStatus{Conditions: []metav1.Condition{cond}},
	}
	s, c := newServer(t, existing)
	if rec := post(s, "Bearer "+token, full); rec.Code != 200 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	e := getEdge(t, c, "e1")
	if e.Labels["customer"] != "acme" || len(e.Labels) != 1 {
		t.Errorf("metadata.labels changed: %v", e.Labels)
	}
	if e.Spec.DisplayName != "Athens" || e.Spec.HeartbeatTimeout == nil || e.Spec.HeartbeatTimeout.Duration != 5*time.Minute {
		t.Errorf("spec changed: %+v", e.Spec)
	}
	if len(e.Status.Conditions) != 1 || e.Status.Conditions[0].Reason != "HeartbeatFresh" {
		t.Errorf("conditions changed: %+v", e.Status.Conditions)
	}
	if e.Status.AppsTotal != 3 {
		t.Errorf("status not patched: %+v", e.Status)
	}
}

func TestHealthz(t *testing.T) {
	s, _ := newServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != 200 {
		t.Errorf("code %d", rec.Code)
	}
}

func TestNewRequiresToken(t *testing.T) {
	scheme := runtime.NewScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	if _, err := New(Config{Client: c, Namespace: ns}); err == nil {
		t.Error("empty token accepted")
	}
	s, err := New(Config{Client: c, Namespace: ns, InsecureNoAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.NeedLeaderElection() {
		t.Error("ingest must not need leader election")
	}
}

func TestApplyBodyHasNoConditions(t *testing.T) {
	var req wire.CheckinRequest
	if err := json.Unmarshal([]byte(full), &req); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(buildApply(ns, &req, clock))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["apiVersion"] != "fleet.babis.dev/v1alpha1" || m["kind"] != "Edge" {
		t.Errorf("type meta: %v", m)
	}
	md := m["metadata"].(map[string]any)
	if md["name"] != "e1" || md["namespace"] != ns || len(md) != 2 {
		t.Errorf("metadata: %v", md)
	}
	if _, ok := m["spec"]; ok {
		t.Error("spec in apply body")
	}
	st := m["status"].(map[string]any)
	if _, ok := st["conditions"]; ok {
		t.Error("conditions in apply body")
	}
	for _, k := range []string{"lastCheckinTime", "telemetry", "labels", "argocd", "apps", "appsTotal", "appsSynced", "appsHealthy"} {
		if _, ok := st[k]; !ok {
			t.Errorf("status.%s missing", k)
		}
	}
	if l := st["labels"].(map[string]any); len(l) != 1 || l["region"] != "gr" {
		t.Errorf("labels: %v", l)
	}
	if a := st["apps"].([]any); len(a) != 3 {
		t.Errorf("apps: %v", a)
	}
}

func TestRemovedLabelAndAppDisappear(t *testing.T) {
	s, c := newServer(t)
	post(s, "Bearer "+token, `{"edge":"e1","labels":{"a":"1","b":"2"},"apps":[{"name":"x","sync":"Synced","health":"Healthy"},{"name":"y"}]}`)
	rec := post(s, "Bearer "+token, `{"edge":"e1","labels":{"a":"1"},"apps":[{"name":"x","sync":"Synced","health":"Healthy"}]}`)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	st := getEdge(t, c, "e1").Status
	if len(st.Labels) != 1 || st.Labels["a"] != "1" {
		t.Errorf("labels %v", st.Labels)
	}
	if len(st.Apps) != 1 || st.Apps[0].Name != "x" {
		t.Errorf("apps %v", st.Apps)
	}
	post(s, "Bearer "+token, `{"edge":"e1"}`)
	st = getEdge(t, c, "e1").Status
	if len(st.Labels) != 0 || len(st.Apps) != 0 || st.AppsTotal != 0 {
		t.Errorf("not cleared: %+v", st)
	}
}

func TestConditionsSurviveCheckin(t *testing.T) {
	s, c := newServer(t)
	post(s, "Bearer "+token, full)
	e := getEdge(t, c, "e1")
	cond := metav1.Condition{
		Type: "Reachable", Status: metav1.ConditionTrue, Reason: "HeartbeatFresh", Message: "m",
		LastTransitionTime: metav1.NewTime(clock),
	}
	e.Status.Conditions = []metav1.Condition{cond}
	if err := c.Status().Update(context.Background(), &e); err != nil {
		t.Fatal(err)
	}
	post(s, "Bearer "+token, `{"edge":"e1","labels":{"z":"1"}}`)
	got := getEdge(t, c, "e1").Status
	if len(got.Conditions) != 1 || got.Conditions[0].Reason != "HeartbeatFresh" {
		t.Errorf("conditions: %+v", got.Conditions)
	}
	if got.Labels["z"] != "1" {
		t.Errorf("labels: %v", got.Labels)
	}
}

func TestConcurrentCheckins(t *testing.T) {
	s, c := newServer(t)
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := fmt.Sprintf(`{"edge":"e1","labels":{"n":"%d"}}`, i)
			codes[i] = post(s, "Bearer "+token, body).Code
		}()
	}
	wg.Wait()
	for i, code := range codes {
		if code != 200 {
			t.Errorf("request %d: %d", i, code)
		}
	}
	if len(getEdge(t, c, "e1").Status.Labels) != 1 {
		t.Error("labels not exactly one")
	}
}

type fakeIndex struct {
	known map[string]bool
	n     int
}

func (f fakeIndex) Has(_, name string) bool { return f.known[name] }
func (f fakeIndex) Count() int              { return f.n }

func TestMaxEdges(t *testing.T) {
	tests := []struct {
		name string
		edge string
		code int
	}{
		{"new edge at limit", "new", 429},
		{"known edge at limit", "old", 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, c := newServer(t)
			s.cfg.Index = fakeIndex{known: map[string]bool{"old": true}, n: 2}
			s.cfg.MaxEdges = 2
			rec := post(s, "Bearer "+token, fmt.Sprintf(`{"edge":%q}`, tc.edge))
			if rec.Code != tc.code {
				t.Fatalf("code %d, want %d (%s)", rec.Code, tc.code, rec.Body)
			}
			var l v1alpha1.EdgeList
			_ = c.List(context.Background(), &l)
			if tc.code == 429 && (len(l.Items) != 0 || !strings.Contains(rec.Body.String(), `"error"`)) {
				t.Errorf("429 must create nothing and be JSON: %v %s", l.Items, rec.Body)
			}
		})
	}
	t.Run("below limit", func(t *testing.T) {
		s, _ := newServer(t)
		s.cfg.Index = fakeIndex{n: 1}
		s.cfg.MaxEdges = 2
		if rec := post(s, "Bearer "+token, `{"edge":"new"}`); rec.Code != 200 {
			t.Errorf("code %d", rec.Code)
		}
	})
}

func TestInflightLimit(t *testing.T) {
	s, _ := newServer(t)
	s.cfg.InflightWait = 50 * time.Millisecond
	s.slots = make(chan struct{}, 1)
	s.slots <- struct{}{} // the only slot is taken
	rec := post(s, "Bearer "+token, `{"edge":"e1"}`)
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("code %d retry-after %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Auth is checked before waiting.
	if rec := post(s, "Bearer bad", `{"edge":"e1"}`); rec.Code != 401 {
		t.Errorf("unauthenticated got %d, want 401", rec.Code)
	}
	// A freed slot lets the request through.
	<-s.slots
	if rec := post(s, "Bearer "+token, `{"edge":"e1"}`); rec.Code != 200 {
		t.Errorf("code %d after slot freed", rec.Code)
	}
}

func TestInflightContextCancelled(t *testing.T) {
	s, _ := newServer(t)
	s.slots = make(chan struct{}, 1)
	s.slots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, CheckinPath, strings.NewReader(`{"edge":"e1"}`)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Errorf("code %d", rec.Code)
	}
}
