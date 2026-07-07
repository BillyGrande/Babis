// Package wire defines the report format edges send to the central
// operator's ingest endpoint. It is the contract between the edgedeploy CLI
// (producer, typically from a cron/systemd timer) and the operator
// (consumer) — keep it backward compatible once released.
package wire

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Report is one heartbeat from one edge.
type Report struct {
	// Edge is the unique edge name; it becomes the Edge object's name in
	// the central cluster, so it must be a valid DNS-1123 subdomain.
	Edge string `json:"edge"`
	// Labels describe the edge (site, region, hardware, ...).
	Labels map[string]string `json:"labels,omitempty"`
	// Timestamp is when the report was generated on the edge.
	Timestamp time.Time `json:"timestamp"`
	// Apps is the state of every ArgoCD Application on the edge.
	Apps []App `json:"apps,omitempty"`
	// Store is present on air-gapped edges running a local store/registry.
	Store *Store `json:"store,omitempty"`
}

// App mirrors v1alpha1.AppSummary; duplicated so edges never import
// Kubernetes API machinery.
type App struct {
	Name     string `json:"name"`
	Sync     string `json:"sync,omitempty"`
	Health   string `json:"health,omitempty"`
	Revision string `json:"revision,omitempty"`
}

// Store describes the local air-gap artifact store on the edge.
type Store struct {
	Version         string `json:"version,omitempty"`
	RegistryHealthy bool   `json:"registryHealthy"`
}

var dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// Validate checks the invariants the ingest endpoint relies on.
func (r *Report) Validate() error {
	if r.Edge == "" {
		return errors.New("edge name is required")
	}
	if len(r.Edge) > 253 || !dns1123.MatchString(r.Edge) {
		return fmt.Errorf("edge name %q is not a valid DNS-1123 subdomain", r.Edge)
	}
	if r.Timestamp.IsZero() {
		return errors.New("timestamp is required")
	}
	for i, a := range r.Apps {
		if a.Name == "" {
			return fmt.Errorf("apps[%d]: name is required", i)
		}
	}
	return nil
}
