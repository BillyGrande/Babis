package main

import (
	"testing"
	"time"
)

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

func TestParseEventOptions(t *testing.T) {
	tests := []struct {
		name                   string
		args                   []string
		flush, touch, appStale time.Duration
	}{
		{"defaults", nil, 30 * time.Second, 5 * time.Minute, 45 * time.Minute},
		{"overrides", []string{"--event-flush-interval=10s", "--event-touch-interval=1m", "--event-app-stale=2h"}, 10 * time.Second, time.Minute, 2 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, _, err := parseOptions(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if o.eventFlush != tc.flush || o.eventTouch != tc.touch || o.eventAppStale != tc.appStale {
				t.Errorf("got flush %s touch %s stale %s", o.eventFlush, o.eventTouch, o.eventAppStale)
			}
		})
	}
}
