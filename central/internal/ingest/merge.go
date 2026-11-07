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

// mergeApps applies the entries to the current app list, then removes stale
// apps and sorts by name. An app is stale when it has no lastSeen (it came
// from a check-in) or its lastSeen is older than now-stale. newest is the
// latest receipt time of the entries that were applied, or zero.
func mergeApps(cur []v1alpha1.App, entries map[string]entry, now time.Time, stale time.Duration) (apps []v1alpha1.App, newest time.Time) {
	byName := make(map[string]v1alpha1.App, len(cur)+len(entries))
	for _, a := range cur {
		byName[a.Name] = a
	}
	for name, e := range entries {
		a, found := byName[name]
		if skipped(a, found, e) {
			continue
		}
		if e.event == wire.EventDeleted {
			delete(byName, name)
		} else {
			byName[name] = v1alpha1.App{
				Name: name, Sync: e.sync, Health: e.health, Revision: e.revision,
				LastSeen: &metav1.MicroTime{Time: e.received},
			}
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
	return apps, newest
}

// material reports whether the entries change the app list: a new app, a
// delete of a present app, or a different sync, health or revision. Entries
// that mergeApps would skip do not count. A changed lastSeen alone is not
// material; the touch interval covers it.
func material(cur []v1alpha1.App, entries map[string]entry) bool {
	byName := make(map[string]v1alpha1.App, len(cur))
	for _, a := range cur {
		byName[a.Name] = a
	}
	for name, e := range entries {
		a, found := byName[name]
		if skipped(a, found, e) {
			continue
		}
		if e.event == wire.EventDeleted {
			if found {
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
