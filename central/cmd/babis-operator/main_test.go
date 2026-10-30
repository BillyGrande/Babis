package main

import "testing"

func TestParseOptions(t *testing.T) {
	tests := []struct {
		name                         string
		args                         []string
		qps                          float64
		burst, maxEdges, maxInflight int
		wantErr                      bool
	}{
		{"defaults", nil, -1, 0, 2000, 64, false},
		{"overrides", []string{"--kube-api-qps", "100", "--kube-api-burst=200", "--max-edges=5", "--max-inflight=3"}, 100, 200, 5, 3, false},
		{"bad value", []string{"--max-edges=x"}, 0, 0, 0, 0, true},
		{"unknown flag", []string{"--nope"}, 0, 0, 0, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, _, err := parseOptions(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tc.wantErr {
				return
			}
			if o.qps != tc.qps || o.burst != tc.burst || o.maxEdges != tc.maxEdges || o.maxInflight != tc.maxInflight {
				t.Errorf("got %+v", o)
			}
		})
	}
}
