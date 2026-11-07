package ingest

import (
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/BillyGrande/babis/central/api/v1alpha1"
	"github.com/BillyGrande/babis/central/internal/wire"
)

// at is clock plus d, as a lastSeen value.
func at(d time.Duration) *metav1.MicroTime { return &metav1.MicroTime{Time: clock.Add(d)} }

func st(sync, health, rev string, d time.Duration) entry {
	return entry{event: wire.EventState, sync: sync, health: health, revision: rev, received: clock.Add(d)}
}

func del(d time.Duration) entry { return entry{event: wire.EventDeleted, received: clock.Add(d)} }

func tomb(name string, d time.Duration) v1alpha1.DeletedApp {
	return v1alpha1.DeletedApp{Name: name, DeletedAt: *at(d)}
}

func TestMergeApps(t *testing.T) {
	const stale = 45 * time.Minute
	now := clock.Add(time.Minute)
	tests := []struct {
		name       string
		cur        []v1alpha1.App
		curDeleted []v1alpha1.DeletedApp
		entries    map[string]entry
		want       []v1alpha1.App
		wantDel    []v1alpha1.DeletedApp
		newest     time.Time
	}{
		{
			name:    "upsert new",
			entries: map[string]entry{"a": st("Synced", "Healthy", "r1", 0)},
			want:    []v1alpha1.App{{Name: "a", Sync: "Synced", Health: "Healthy", Revision: "r1", LastSeen: at(0)}},
			newest:  clock,
		},
		{
			name:    "update existing",
			cur:     []v1alpha1.App{{Name: "a", Sync: "Synced", Health: "Healthy", Revision: "r1", LastSeen: at(-time.Minute)}},
			entries: map[string]entry{"a": st("OutOfSync", "Progressing", "r2", 0)},
			want:    []v1alpha1.App{{Name: "a", Sync: "OutOfSync", Health: "Progressing", Revision: "r2", LastSeen: at(0)}},
			newest:  clock,
		},
		{
			name:    "older entry skipped",
			cur:     []v1alpha1.App{{Name: "a", Sync: "Synced", LastSeen: at(0)}},
			entries: map[string]entry{"a": st("OutOfSync", "", "", -time.Second)},
			want:    []v1alpha1.App{{Name: "a", Sync: "Synced", LastSeen: at(0)}},
		},
		{
			name:    "equal time skipped",
			cur:     []v1alpha1.App{{Name: "a", Sync: "Synced", LastSeen: at(0)}},
			entries: map[string]entry{"a": st("OutOfSync", "", "", 0)},
			want:    []v1alpha1.App{{Name: "a", Sync: "Synced", LastSeen: at(0)}},
		},
		{
			name:    "one microsecond newer wins",
			cur:     []v1alpha1.App{{Name: "a", Sync: "Synced", LastSeen: at(0)}},
			entries: map[string]entry{"a": st("OutOfSync", "", "", time.Microsecond)},
			want:    []v1alpha1.App{{Name: "a", Sync: "OutOfSync", LastSeen: at(time.Microsecond)}},
			newest:  clock.Add(time.Microsecond),
		},
		{
			name: "delete present",
			cur: []v1alpha1.App{
				{Name: "a", LastSeen: at(-time.Minute)},
				{Name: "b", LastSeen: at(-time.Minute)},
			},
			entries: map[string]entry{"a": del(0)},
			want:    []v1alpha1.App{{Name: "b", LastSeen: at(-time.Minute)}},
			wantDel: []v1alpha1.DeletedApp{tomb("a", 0)},
			newest:  clock,
		},
		{
			name:    "delete of an absent app writes a tombstone",
			cur:     []v1alpha1.App{{Name: "b", LastSeen: at(-time.Minute)}},
			entries: map[string]entry{"a": del(0)},
			want:    []v1alpha1.App{{Name: "b", LastSeen: at(-time.Minute)}},
			wantDel: []v1alpha1.DeletedApp{tomb("a", 0)},
			newest:  clock,
		},
		{
			name:       "newer delete moves the tombstone forward",
			curDeleted: []v1alpha1.DeletedApp{tomb("a", 0)},
			entries:    map[string]entry{"a": del(time.Second)},
			want:       []v1alpha1.App{},
			wantDel:    []v1alpha1.DeletedApp{tomb("a", time.Second)},
			newest:     clock.Add(time.Second),
		},
		{
			name:       "older delete keeps the tombstone",
			curDeleted: []v1alpha1.DeletedApp{tomb("a", 0)},
			entries:    map[string]entry{"a": del(-time.Second)},
			want:       []v1alpha1.App{},
			wantDel:    []v1alpha1.DeletedApp{tomb("a", 0)},
			newest:     clock.Add(-time.Second),
		},
		{
			name:       "older state skipped by the tombstone",
			curDeleted: []v1alpha1.DeletedApp{tomb("a", 0)},
			entries:    map[string]entry{"a": st("Synced", "Healthy", "", -time.Second)},
			want:       []v1alpha1.App{},
			wantDel:    []v1alpha1.DeletedApp{tomb("a", 0)},
		},
		{
			name:       "state at the tombstone time skipped",
			curDeleted: []v1alpha1.DeletedApp{tomb("a", 0)},
			entries:    map[string]entry{"a": st("Synced", "Healthy", "", 0)},
			want:       []v1alpha1.App{},
			wantDel:    []v1alpha1.DeletedApp{tomb("a", 0)},
		},
		{
			name:       "newer state creates the app again and clears the tombstone",
			curDeleted: []v1alpha1.DeletedApp{tomb("a", 0), tomb("b", 0)},
			entries:    map[string]entry{"a": st("Synced", "Healthy", "", time.Microsecond)},
			want:       []v1alpha1.App{{Name: "a", Sync: "Synced", Health: "Healthy", LastSeen: at(time.Microsecond)}},
			wantDel:    []v1alpha1.DeletedApp{tomb("b", 0)},
			newest:     clock.Add(time.Microsecond),
		},
		{
			name: "tombstone prune by age and sort",
			curDeleted: []v1alpha1.DeletedApp{
				tomb("z", time.Minute-stale),
				tomb("old", time.Minute-stale-time.Microsecond),
				tomb("b", 0),
			},
			want:    []v1alpha1.App{},
			wantDel: []v1alpha1.DeletedApp{tomb("b", 0), tomb("z", time.Minute-stale)},
		},
		{
			name:    "delete older than lastSeen skipped",
			cur:     []v1alpha1.App{{Name: "a", LastSeen: at(0)}},
			entries: map[string]entry{"a": del(-time.Second)},
			want:    []v1alpha1.App{{Name: "a", LastSeen: at(0)}},
		},
		{
			name: "prune by age",
			cur: []v1alpha1.App{
				{Name: "old", LastSeen: at(time.Minute - stale - time.Microsecond)},
				{Name: "edge", LastSeen: at(time.Minute - stale)},
			},
			want: []v1alpha1.App{{Name: "edge", LastSeen: at(time.Minute - stale)}},
		},
		{
			name:    "prune app without lastSeen from a check-in",
			cur:     []v1alpha1.App{{Name: "x", Sync: "Synced"}, {Name: "y", Sync: "Synced"}},
			entries: map[string]entry{"y": st("Synced", "", "", 0)},
			want:    []v1alpha1.App{{Name: "y", Sync: "Synced", LastSeen: at(0)}},
			newest:  clock,
		},
		{
			name: "sort order and newest",
			cur:  []v1alpha1.App{{Name: "m", LastSeen: at(-time.Minute)}},
			entries: map[string]entry{
				"z": st("", "", "", 2*time.Second),
				"a": st("", "", "", time.Second),
			},
			want: []v1alpha1.App{
				{Name: "a", LastSeen: at(time.Second)},
				{Name: "m", LastSeen: at(-time.Minute)},
				{Name: "z", LastSeen: at(2 * time.Second)},
			},
			newest: clock.Add(2 * time.Second),
		},
		{
			name: "empty",
			want: []v1alpha1.App{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotDel, newest := mergeApps(tc.cur, tc.curDeleted, tc.entries, now, stale)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("apps\n got %+v\nwant %+v", got, tc.want)
			}
			if !reflect.DeepEqual(gotDel, tc.wantDel) {
				t.Errorf("deletedApps\n got %+v\nwant %+v", gotDel, tc.wantDel)
			}
			if !newest.Equal(tc.newest) {
				t.Errorf("newest %v, want %v", newest, tc.newest)
			}
		})
	}
}

func TestCountApps(t *testing.T) {
	tests := []struct {
		name            string
		apps            []v1alpha1.App
		synced, healthy int
	}{
		{"none", nil, 0, 0},
		{"mixed", []v1alpha1.App{
			{Sync: "Synced", Health: "Healthy"},
			{Sync: "OutOfSync", Health: "Healthy"},
			{Sync: "Synced", Health: "Degraded"},
			{},
		}, 2, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, h := countApps(tc.apps)
			if s != tc.synced || h != tc.healthy {
				t.Errorf("got %d/%d, want %d/%d", s, h, tc.synced, tc.healthy)
			}
		})
	}
}

func TestMaterial(t *testing.T) {
	cur := []v1alpha1.App{
		{Name: "a", Sync: "Synced", Health: "Healthy", Revision: "r1", LastSeen: at(0)},
		{Name: "c", Sync: "Synced", Health: "Healthy"}, // from a check-in
	}
	curDeleted := []v1alpha1.DeletedApp{tomb("gone", 0)}
	tests := []struct {
		name    string
		entries map[string]entry
		want    bool
	}{
		{"no entries", nil, false},
		{"same state", map[string]entry{"a": st("Synced", "Healthy", "r1", time.Second)}, false},
		{"same state as a check-in app", map[string]entry{"c": st("Synced", "Healthy", "", time.Second)}, false},
		{"new app", map[string]entry{"b": st("Synced", "Healthy", "r1", time.Second)}, true},
		{"sync differs", map[string]entry{"a": st("OutOfSync", "Healthy", "r1", time.Second)}, true},
		{"health differs", map[string]entry{"a": st("Synced", "Degraded", "r1", time.Second)}, true},
		{"revision differs", map[string]entry{"a": st("Synced", "Healthy", "r2", time.Second)}, true},
		{"delete present", map[string]entry{"a": del(time.Second)}, true},
		{"delete of an absent app without tombstone", map[string]entry{"x": del(time.Second)}, true},
		{"delete newer than the tombstone", map[string]entry{"gone": del(time.Second)}, true},
		{"delete at the tombstone time", map[string]entry{"gone": del(0)}, false},
		{"delete older than the tombstone", map[string]entry{"gone": del(-time.Second)}, false},
		{"state older than the tombstone", map[string]entry{"gone": st("Synced", "Healthy", "", -time.Second)}, false},
		{"state at the tombstone time", map[string]entry{"gone": st("Synced", "Healthy", "", 0)}, false},
		{"state newer than the tombstone", map[string]entry{"gone": st("Synced", "Healthy", "", time.Second)}, true},
		{"change but skipped as older", map[string]entry{"a": st("OutOfSync", "Healthy", "r1", -time.Second)}, false},
		{"delete but skipped as equal", map[string]entry{"a": del(0)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := material(cur, curDeleted, tc.entries); got != tc.want {
				t.Errorf("material = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckinFresh(t *testing.T) {
	const timeout = 15 * time.Minute
	edge := func(source string, ago time.Duration, override *metav1.Duration) *v1alpha1.Edge {
		e := &v1alpha1.Edge{Spec: v1alpha1.EdgeSpec{HeartbeatTimeout: override}}
		e.Status.Telemetry.Source = source
		if ago >= 0 {
			last := metav1.NewTime(clock.Add(-ago))
			e.Status.LastCheckinTime = &last
		}
		return e
	}
	tests := []struct {
		name string
		edge *v1alpha1.Edge
		want bool
	}{
		{"fresh", edge(v1alpha1.TelemetrySourceCheckin, time.Minute, nil), true},
		{"stale", edge(v1alpha1.TelemetrySourceCheckin, 16*time.Minute, nil), false},
		{"at the deadline is stale", edge(v1alpha1.TelemetrySourceCheckin, timeout, nil), false},
		{"source notifications", edge(v1alpha1.TelemetrySourceNotifications, time.Minute, nil), false},
		{"no source (new edge)", edge("", time.Minute, nil), false},
		{"never checked in", edge(v1alpha1.TelemetrySourceCheckin, -1, nil), false},
		{"spec override shorter", edge(v1alpha1.TelemetrySourceCheckin, 3*time.Minute, &metav1.Duration{Duration: 2 * time.Minute}), false},
		{"spec override longer", edge(v1alpha1.TelemetrySourceCheckin, 20*time.Minute, &metav1.Duration{Duration: time.Hour}), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkinFresh(tc.edge, timeout, clock); got != tc.want {
				t.Errorf("checkinFresh = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildEventApply(t *testing.T) {
	last := metav1.NewTime(clock.Add(-time.Minute))
	later := metav1.NewTime(clock.Add(time.Minute))
	tests := []struct {
		name    string
		current *metav1.Time
		entries map[string]entry
		want    time.Time
	}{
		{"newest entry wins", &last, map[string]entry{"a": st("", "", "", 0)}, clock},
		{"never older than current", &later, map[string]entry{"a": st("", "", "", 0)}, later.Time},
		{"nothing applied keeps current", &last, nil, last.Time},
		{"new edge, nothing applied gets now", nil, nil, clock.Add(time.Hour)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &v1alpha1.Edge{ObjectMeta: metav1.ObjectMeta{Name: "e1", Namespace: ns, ResourceVersion: "7"}}
			e.Status.LastCheckinTime = tc.current
			e.Status.ArgoCD.Version = "v3.2.0"
			e.Status.Labels = map[string]string{"x": "y"}
			got := buildEventApply(e, tc.entries, clock.Add(time.Hour), 2*time.Hour)
			if !got.Status.LastCheckinTime.Time.Equal(tc.want) {
				t.Errorf("lastCheckinTime %v, want %v", got.Status.LastCheckinTime, tc.want)
			}
			if got.Metadata.ResourceVersion != "7" || got.Status.Telemetry.Source != v1alpha1.TelemetrySourceNotifications {
				t.Errorf("apply %+v", got)
			}
		})
	}
}
