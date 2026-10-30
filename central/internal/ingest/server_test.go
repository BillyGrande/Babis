package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/BillyGrande/babis/central/api/v1alpha1"
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
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.Edge{}).WithObjects(objs...).Build()
	s, err := New(Config{Token: token, Namespace: ns, Client: c, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	return s, c
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
