package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/BillyGrande/babis/central/internal/wire"
)

func TestPercentile(t *testing.T) {
	ms := func(n int) []time.Duration {
		out := make([]time.Duration, n)
		for i := range out {
			out[i] = time.Duration(i+1) * time.Millisecond
		}
		return out
	}
	tests := []struct {
		name string
		in   []time.Duration
		p    float64
		want time.Duration
	}{
		{"empty", nil, 50, 0},
		{"single", ms(1), 99, time.Millisecond},
		{"p50 of 100", ms(100), 50, 50 * time.Millisecond},
		{"p95 of 100", ms(100), 95, 95 * time.Millisecond},
		{"p99 of 100", ms(100), 99, 99 * time.Millisecond},
		{"p100", ms(10), 100, 10 * time.Millisecond},
		{"p0 clamps to first", ms(10), 0, time.Millisecond},
		{"p99 of 10", ms(10), 99, 10 * time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := percentile(tc.in, tc.p); got != tc.want {
				t.Errorf("percentile(%v) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

func TestBuildBodies(t *testing.T) {
	tests := []struct {
		name        string
		edges, apps int
	}{
		{"no apps", 3, 0},
		{"small", 5, 10},
		{"full", 20, 300},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bodies, err := buildBodies("load", tc.edges, tc.apps)
			if err != nil {
				t.Fatal(err)
			}
			if len(bodies) != tc.edges {
				t.Fatalf("got %d bodies, want %d", len(bodies), tc.edges)
			}
			seen := map[string]bool{}
			for _, b := range bodies {
				var req wire.CheckinRequest
				if err := json.Unmarshal(b, &req); err != nil {
					t.Fatal(err)
				}
				if err := req.Validate(); err != nil {
					t.Errorf("invalid body: %v", err)
				}
				if seen[req.Edge] {
					t.Errorf("duplicate edge %s", req.Edge)
				}
				seen[req.Edge] = true
				if len(req.Apps) != tc.apps || len(req.ArgoCD.Components) != 6 {
					t.Errorf("apps=%d components=%d", len(req.Apps), len(req.ArgoCD.Components))
				}
				for _, a := range req.Apps {
					if len(a.Revision) != 7 {
						t.Errorf("revision %q is not 7 chars", a.Revision)
					}
				}
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	rs := []result{
		{10 * time.Millisecond, "ok"}, {20 * time.Millisecond, "ok"},
		{30 * time.Millisecond, "http_500"}, {40 * time.Millisecond, "timeout"},
	}
	s := summarize(1, rs, 2*time.Second)
	if s.OK != 2 || s.Errors["http_500"] != 1 || s.Errors["timeout"] != 1 || s.ReqPerS != 2 || s.MaxMs != 40 {
		t.Errorf("unexpected summary: %+v", s)
	}
}
