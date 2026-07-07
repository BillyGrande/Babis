package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fleetv1alpha1 "github.com/BillyGrande/edge-deployer/api/v1alpha1"
	"github.com/BillyGrande/edge-deployer/internal/wire"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := fleetv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&fleetv1alpha1.Edge{}).
		Build()
	return &Server{Client: c, Namespace: "fleet", Token: "sekret"}
}

func post(t *testing.T, s *Server, token string, report wire.Report) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha1/reports", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.handleReport(rec, req)
	return rec
}

func TestHandleReportRegistersAndUpdatesEdge(t *testing.T) {
	s := newTestServer(t)
	report := wire.Report{
		Edge:      "shop-042",
		Labels:    map[string]string{"region": "gr-north"},
		Timestamp: time.Now(),
		Apps: []wire.App{
			{Name: "infra-apps", Sync: "Synced", Health: "Healthy"},
			{Name: "workloads", Sync: "OutOfSync", Health: "Degraded"},
		},
		Store: &wire.Store{Version: "1.2.0", RegistryHealthy: true},
	}

	if rec := post(t, s, "sekret", report); rec.Code != http.StatusAccepted {
		t.Fatalf("first report: status %d, body %s", rec.Code, rec.Body)
	}

	var edge fleetv1alpha1.Edge
	key := types.NamespacedName{Namespace: "fleet", Name: "shop-042"}
	if err := s.Client.Get(context.Background(), key, &edge); err != nil {
		t.Fatalf("edge not auto-registered: %v", err)
	}
	st := edge.Status
	if st.AppsTotal != 2 || st.AppsSynced != 1 || st.AppsHealthy != 1 {
		t.Errorf("app summary = total %d synced %d healthy %d, want 2/1/1", st.AppsTotal, st.AppsSynced, st.AppsHealthy)
	}
	if st.StoreVersion != "1.2.0" || st.RegistryHealthy == nil || !*st.RegistryHealthy {
		t.Errorf("store status not recorded: %+v", st)
	}
	if st.LastReportTime == nil {
		t.Error("lastReportTime not set")
	}
	if st.Labels["region"] != "gr-north" {
		t.Errorf("labels not recorded: %v", st.Labels)
	}

	// A second report updates in place rather than failing on AlreadyExists.
	if rec := post(t, s, "sekret", report); rec.Code != http.StatusAccepted {
		t.Fatalf("second report: status %d, body %s", rec.Code, rec.Body)
	}
}

func TestHandleReportRejects(t *testing.T) {
	s := newTestServer(t)
	valid := wire.Report{Edge: "shop-042", Timestamp: time.Now()}

	tests := []struct {
		name   string
		token  string
		report wire.Report
		want   int
	}{
		{"wrong token", "nope", valid, http.StatusUnauthorized},
		{"missing token", "", valid, http.StatusUnauthorized},
		{"invalid report", "sekret", wire.Report{Timestamp: time.Now()}, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := post(t, s, tt.token, tt.report); rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}
