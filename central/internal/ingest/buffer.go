package ingest

import (
	"sync"
	"time"
)

// entry is the last app event this replica received for one app.
type entry struct {
	event    string // wire.EventState or wire.EventDeleted
	sync     string
	health   string
	revision string
	received time.Time // replica clock, UTC, microseconds
}

// edgeBuf holds the pending events of one edge.
type edgeBuf struct {
	entries map[string]entry // app name -> newest entry
	// lastCheck is when this replica last looked at the edge in a flush.
	// Zero means "never": the edge is due at once.
	lastCheck time.Time
	// busy is true while a flush works on the edge.
	busy bool
}

// buffer is the per-replica store of app events that wait for a flush.
// The Server owns one; all methods are safe for concurrent use.
type buffer struct {
	mu       sync.Mutex
	edges    map[string]*edgeBuf
	maxApps  int
	maxEdges int
}

func newBuffer(maxApps, maxEdges int) *buffer {
	return &buffer{edges: map[string]*edgeBuf{}, maxApps: maxApps, maxEdges: maxEdges}
}

// add stores e for the app unless the buffer already has an equal or newer
// entry for it. It returns false when a cap drops the event.
func (b *buffer) add(edge, app string, e entry) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	eb, ok := b.edges[edge]
	if !ok {
		if len(b.edges) >= b.maxEdges {
			return false
		}
		eb = &edgeBuf{entries: map[string]entry{}}
		b.edges[edge] = eb
	}
	return eb.put(app, e, b.maxApps)
}

// put keeps the newer entry for the app. A new app above maxApps is dropped.
// An equal or older entry is ignored, which is not a drop.
func (eb *edgeBuf) put(app string, e entry, maxApps int) bool {
	old, ok := eb.entries[app]
	if !ok {
		if len(eb.entries) >= maxApps {
			return false
		}
		eb.entries[app] = e
		return true
	}
	if e.received.After(old.received) {
		eb.entries[app] = e
	}
	return true
}

// due marks as busy, and returns, every edge that has entries, is not busy and
// was last checked at least interval ago. An interval of zero returns every
// such edge. It also forgets idle edges whose last check is older than
// interval, so the map does not grow with edges that stopped sending.
func (b *buffer) due(now time.Time, interval time.Duration) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var names []string
	for name, eb := range b.edges {
		if eb.busy || now.Sub(eb.lastCheck) < interval {
			continue
		}
		if len(eb.entries) == 0 {
			delete(b.edges, name)
			continue
		}
		eb.busy = true
		names = append(names, name)
	}
	return names
}

// take removes and returns the entries of the edge.
func (b *buffer) take(edge string) map[string]entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	eb, ok := b.edges[edge]
	if !ok {
		return nil
	}
	out := eb.entries
	eb.entries = map[string]entry{}
	return out
}

// restore puts entries back after a flush that did not write them. An entry
// that arrived during the flush is newer and wins.
func (b *buffer) restore(edge string, entries map[string]entry) {
	if len(entries) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	eb, ok := b.edges[edge]
	if !ok {
		// A busy edge is never forgotten by due(), so this only guards
		// against a restore outside a flush.
		eb = &edgeBuf{entries: map[string]entry{}}
		b.edges[edge] = eb
	}
	for app, e := range entries {
		eb.put(app, e, b.maxApps)
	}
}

// done ends a flush of the edge: it clears busy and records the check time.
// The edge stays in the map (with its lastCheck) until due() forgets it, so
// the flush interval holds for the next event.
func (b *buffer) done(edge string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if eb, ok := b.edges[edge]; ok {
		eb.busy = false
		eb.lastCheck = now
	}
}

// pending reports the number of buffered entries of the edge. For tests and logs.
func (b *buffer) pending(edge string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if eb, ok := b.edges[edge]; ok {
		return len(eb.entries)
	}
	return 0
}
