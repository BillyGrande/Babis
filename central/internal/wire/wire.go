// Package wire holds the check-in contract between the edge and the operator.
// The JSON Schemas in /wire are the source of truth; Validate enforces the
// same rules in code. Unknown JSON fields are ignored on decode.
package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const maxApps = 5000

// CheckinRequest is the body of POST /api/v1alpha1/checkin. Optional
// collections use omitzero so empty-but-present values round-trip.
type CheckinRequest struct {
	Edge   string            `json:"edge"`
	Labels map[string]string `json:"labels,omitzero"`
	ArgoCD *ArgoCD           `json:"argocd,omitempty"`
	Apps   []App             `json:"apps,omitzero"`
}

// ArgoCD describes the observed ArgoCD install.
type ArgoCD struct {
	Version    string      `json:"version,omitempty"`
	Components []Component `json:"components,omitzero"`
}

// Component is one ArgoCD workload. Ready and Desired are pointers so that
// a missing field can be told apart from zero.
type Component struct {
	Name    string `json:"name"`
	Ready   *int64 `json:"ready"`
	Desired *int64 `json:"desired"`
}

// App is one ArgoCD Application. Sync, Health and Revision are ArgoCD's own
// values, passed through untranslated.
type App struct {
	Name     string `json:"name"`
	Sync     string `json:"sync,omitempty"`
	Health   string `json:"health,omitempty"`
	Revision string `json:"revision,omitempty"`
}

// CheckinResponse is the 200 body of a check-in. Generation is a pointer so
// that a missing field can be told apart from zero.
type CheckinResponse struct {
	Generation *int64                     `json:"generation"`
	Desired    map[string]json.RawMessage `json:"desired"`
	Extensions map[string]json.RawMessage `json:"extensions,omitzero"`
}

var (
	subdomainRe  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	labelNameRe  = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	reservedBase = []string{"argocd.argoproj.io", "babis.dev", "kubernetes.io", "k8s.io"}
)

func validSubdomain(s string) bool {
	return len(s) >= 1 && len(s) <= 253 && subdomainRe.MatchString(s)
}

// Validate checks the request against the wire contract and returns all
// violations joined, or nil.
func (r *CheckinRequest) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if !validSubdomain(r.Edge) {
		add("edge: must be a DNS-1123 subdomain of 1..253 chars, got %q", truncate(r.Edge))
	}
	for k, v := range r.Labels {
		if err := validateLabelKey(k); err != nil {
			add("labels[%q]: %v", k, err)
		}
		if len(v) > 63 || (v != "" && !labelNameRe.MatchString(v)) {
			add("labels[%q]: invalid value %q", k, truncate(v))
		}
	}
	if r.ArgoCD != nil {
		for i, c := range r.ArgoCD.Components {
			p := fmt.Sprintf("argocd.components[%d]", i)
			if c.Name == "" || len(c.Name) > 253 {
				add("%s.name: must be 1..253 chars", p)
			}
			if c.Ready == nil {
				add("%s.ready: required", p)
			} else if *c.Ready < 0 {
				add("%s.ready: must be >= 0", p)
			}
			if c.Desired == nil {
				add("%s.desired: required", p)
			} else if *c.Desired < 0 {
				add("%s.desired: must be >= 0", p)
			}
		}
	}
	if len(r.Apps) > maxApps {
		add("apps: at most %d items, got %d", maxApps, len(r.Apps))
	}
	for i, a := range r.Apps {
		if a.Name == "" || len(a.Name) > 253 {
			add("apps[%d].name: must be 1..253 chars", i)
		}
	}
	return errors.Join(errs...)
}

func validateLabelKey(k string) error {
	prefix, name := "", k
	if i := strings.IndexByte(k, '/'); i >= 0 {
		prefix, name = k[:i], k[i+1:]
		if !validSubdomain(prefix) {
			return fmt.Errorf("invalid prefix %q", truncate(prefix))
		}
	}
	if len(name) > 63 || !labelNameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q", truncate(name))
	}
	for _, res := range reservedBase {
		if prefix == res || strings.HasSuffix(prefix, "."+res) {
			return fmt.Errorf("reserved prefix %s", res)
		}
	}
	return nil
}

// Validate checks the response against the wire contract.
func (r *CheckinResponse) Validate() error {
	var errs []error
	if r.Generation == nil {
		errs = append(errs, errors.New("generation: required"))
	} else if *r.Generation < 0 {
		errs = append(errs, errors.New("generation: must be >= 0"))
	}
	if r.Desired == nil {
		errs = append(errs, errors.New("desired: required"))
	}
	return errors.Join(errs...)
}

func truncate(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}
