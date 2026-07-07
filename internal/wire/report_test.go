package wire

import (
	"strings"
	"testing"
	"time"
)

func TestReportValidate(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		report  Report
		wantErr string // substring; empty = valid
	}{
		{
			name:   "minimal valid",
			report: Report{Edge: "shop-athens-042", Timestamp: now},
		},
		{
			name:   "valid with apps and store",
			report: Report{Edge: "edge-1", Timestamp: now, Apps: []App{{Name: "infra-apps", Sync: "Synced"}}, Store: &Store{Version: "1.2.0", RegistryHealthy: true}},
		},
		{
			name:    "missing edge name",
			report:  Report{Timestamp: now},
			wantErr: "edge name is required",
		},
		{
			name:    "invalid edge name",
			report:  Report{Edge: "Shop_Athens", Timestamp: now},
			wantErr: "DNS-1123",
		},
		{
			name:    "missing timestamp",
			report:  Report{Edge: "edge-1"},
			wantErr: "timestamp",
		},
		{
			name:    "app without name",
			report:  Report{Edge: "edge-1", Timestamp: now, Apps: []App{{Sync: "Synced"}}},
			wantErr: "apps[0]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.report.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}
