package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReachability(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	timeout := 15 * time.Minute
	at := func(d time.Duration) *metav1.Time {
		t := metav1.NewTime(now.Add(d))
		return &t
	}

	tests := []struct {
		name          string
		lastReport    *metav1.Time
		wantReachable bool
		wantRecheck   time.Duration
	}{
		{"never reported", nil, false, timeout},
		{"fresh report", at(-5 * time.Minute), true, 10 * time.Minute},
		{"just inside deadline", at(-14 * time.Minute), true, 1 * time.Minute},
		{"exactly at deadline", at(-15 * time.Minute), false, timeout},
		{"long silent", at(-2 * time.Hour), false, timeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reachable, recheck := Reachability(tt.lastReport, timeout, now)
			if reachable != tt.wantReachable {
				t.Errorf("reachable = %v, want %v", reachable, tt.wantReachable)
			}
			if recheck != tt.wantRecheck {
				t.Errorf("recheck = %v, want %v", recheck, tt.wantRecheck)
			}
		})
	}
}
