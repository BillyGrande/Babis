package wire

import (
	"errors"
	"fmt"
)

// Values of AppEventRequest.Event.
const (
	// EventState says the Application exists with the given state.
	EventState = "state"
	// EventDeleted says the Application has a deletion timestamp.
	EventDeleted = "deleted"
)

const maxAppEventField = 253

// AppEventRequest is the body of POST /api/v1alpha1/app-events. The ArgoCD
// notifications controller on the edge sends one per Application. A JSON
// null in Sync, Health or Revision decodes to "".
type AppEventRequest struct {
	Edge  string      `json:"edge"`
	Event string      `json:"event"`
	App   AppEventApp `json:"app"`
}

// AppEventApp is the Application part of an app event.
type AppEventApp struct {
	Name     string `json:"name"`
	Sync     string `json:"sync,omitempty"`
	Health   string `json:"health,omitempty"`
	Revision string `json:"revision,omitempty"`
}

// Validate checks the request against the wire contract and returns all
// violations joined, or nil. A missing app object shows as an empty name.
func (r *AppEventRequest) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if !validSubdomain(r.Edge) {
		add("edge: must be a DNS-1123 subdomain of 1..253 chars, got %q", truncate(r.Edge))
	}
	if r.Event != EventState && r.Event != EventDeleted {
		add("event: must be %q or %q, got %q", EventState, EventDeleted, truncate(r.Event))
	}
	if r.App.Name == "" || len(r.App.Name) > 253 {
		add("app.name: must be 1..253 chars")
	}
	for _, f := range []struct{ name, val string }{
		{"sync", r.App.Sync}, {"health", r.App.Health}, {"revision", r.App.Revision},
	} {
		if len(f.val) > maxAppEventField {
			add("app.%s: at most %d chars", f.name, maxAppEventField)
		}
	}
	return errors.Join(errs...)
}
