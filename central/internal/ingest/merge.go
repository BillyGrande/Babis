package ingest

import (
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/BillyGrande/babis/central/api/v1alpha1"
	"github.com/BillyGrande/babis/central/internal/controller"
	"github.com/BillyGrande/babis/central/internal/wire"
)

// The functions in this file are pure. They hold the merge rules of the
// flush (spec "Central / Flush", steps 2-5).

// skipped reports whether the entry is not newer than what the status already
// has for the app. Another replica wrote a newer state, so the entry loses.
// An app without lastSeen (from a check-in) never wins over an entry.
func skipped(app v1alpha1.App, found bool, e entry) bool {
	return found && app.LastSeen != nil && !e.received.After(app.LastSeen.Time)
}

// buried reports whether a state entry is not newer than the app's tombstone.
// The app was deleted after this state, maybe through another replica, so the
// state must not add it again.
func buried(tombs map[string]time.Time, name string, e entry) bool {
	at, ok := tombs[name]
	return ok && e.event != wire.EventDeleted && !e.received.After(at)
}

// mergeApps applies the entries to the current app list and tombstones:
//   - a `deleted` entry removes the app and moves its tombstone to the
//     receipt time (also for an app that is not in the list);
//   - a `state` entry not newer than the tombstone is skipped; a newer one
//     adds or updates the app and removes the tombstone;
//   - an entry not newer than the app's lastSeen is skipped.
//
// Then it removes stale apps (no lastSeen, which means from a check-in, or
// lastSeen older than now-stale) and stale tombstones, and sorts both lists
// by name. newest is the latest receipt time of the applied entries, or zero.
func mergeApps(cur []v1alpha1.App, curDeleted []v1alpha1.DeletedApp, entries map[string]entry, now time.Time, stale time.Duration) (apps []v1alpha1.App, deleted []v1alpha1.DeletedApp, newest time.Time) {
	byName := make(map[string]v1alpha1.App, len(cur)+len(entries))
	for _, a := range cur {
		byName[a.Name] = a
	}
	tombs := tombstones(curDeleted)
	for name, e := range entries {
		a, found := byName[name]
		if skipped(a, found, e) || buried(tombs, name, e) {
			continue
		}
		if e.event == wire.EventDeleted {
			delete(byName, name)
			if e.received.After(tombs[name]) {
				tombs[name] = e.received
			}
		} else {
			byName[name] = v1alpha1.App{
				Name: name, Sync: e.sync, Health: e.health, Revision: e.revision,
				LastSeen: &metav1.MicroTime{Time: e.received},
			}
			delete(tombs, name)
		}
		if e.received.After(newest) {
			newest = e.received
		}
	}
	cutoff := now.Add(-stale)
	apps = make([]v1alpha1.App, 0, len(byName))
	for _, a := range byName {
		if a.LastSeen == nil || a.LastSeen.Time.Before(cutoff) {
			continue
		}
		apps = append(apps, a)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	for name, at := range tombs {
		if !at.Before(cutoff) {
			deleted = append(deleted, v1alpha1.DeletedApp{Name: name, DeletedAt: metav1.MicroTime{Time: at}})
		}
	}
	sort.Slice(deleted, func(i, j int) bool { return deleted[i].Name < deleted[j].Name })
	return apps, deleted, newest
}

// tombstones maps app name to deletion time. A duplicate name keeps the latest.
func tombstones(list []v1alpha1.DeletedApp) map[string]time.Time {
	m := make(map[string]time.Time, len(list))
	for _, d := range list {
		if d.DeletedAt.After(m[d.Name]) {
			m[d.Name] = d.DeletedAt.Time
		}
	}
	return m
}

// material reports whether the entries change the status in a way that must
// be written soon: a new app, a different sync, health or revision, or a
// delete that removes a present app or moves its tombstone forward. Entries
// that mergeApps would skip do not count. A changed lastSeen alone is not
// material; the touch interval covers it.
func material(cur []v1alpha1.App, curDeleted []v1alpha1.DeletedApp, entries map[string]entry) bool {
	byName := make(map[string]v1alpha1.App, len(cur))
	for _, a := range cur {
		byName[a.Name] = a
	}
	tombs := tombstones(curDeleted)
	for name, e := range entries {
		a, found := byName[name]
		if skipped(a, found, e) || buried(tombs, name, e) {
			continue
		}
		if e.event == wire.EventDeleted {
			if at, ok := tombs[name]; found || !ok || e.received.After(at) {
				return true
			}
			continue
		}
		if !found || a.Sync != e.sync || a.Health != e.health || a.Revision != e.revision {
			return true
		}
	}
	return false
}

// countApps counts the synced and the healthy apps, with ArgoCD's values.
func countApps(apps []v1alpha1.App) (synced, healthy int) {
	for _, a := range apps {
		if a.Sync == "Synced" {
			synced++
		}
		if a.Health == "Healthy" {
			healthy++
		}
	}
	return synced, healthy
}

// checkinFresh reports whether the edge reports with `babis checkin` and its
// last check-in is within its check-in heartbeat timeout. The check-in then
// owns the status and app events are dropped (spec R7). The timeout rule is
// the reconciler's, so both agree on "fresh".
func checkinFresh(e *v1alpha1.Edge, checkinTimeout time.Duration, now time.Time) bool {
	st := e.Status
	if st.Telemetry.Source != v1alpha1.TelemetrySourceCheckin || st.LastCheckinTime == nil {
		return false
	}
	timeout := controller.HeartbeatTimeout(e.Spec.HeartbeatTimeout, v1alpha1.TelemetrySourceCheckin, checkinTimeout, 0)
	return now.Before(st.LastCheckinTime.Add(timeout))
}
