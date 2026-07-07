// Package argocd installs ArgoCD via Helm and reads Application state from
// the local cluster. Works with any conformant cluster — binaries are
// discovered on PATH and overridable by flags.
package argocd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/BillyGrande/babis/internal/execx"
	"github.com/BillyGrande/babis/internal/wire"
)

// DefaultChartRepo is the upstream Argo Helm repository, used unless the
// user points at a local chart or an (air-gapped) OCI registry.
const DefaultChartRepo = "https://argoproj.github.io/argo-helm"

type Client struct {
	Runner    *execx.Runner
	Kubectl   string // binary name/path, default "kubectl"
	Helm      string // binary name/path, default "helm"
	Namespace string // ArgoCD namespace, default "argocd"
}

// InstallOptions configure the Helm installation of ArgoCD.
type InstallOptions struct {
	// Chart is a chart reference: empty for the upstream repo chart, a
	// local .tgz path, or an oci:// URL (air-gapped local registry).
	Chart string
	// Version pins the chart version; empty means latest.
	Version string
	// ValuesFiles are passed to helm with -f in order.
	ValuesFiles []string
	// PlainHTTP adds --plain-http for http-only OCI registries.
	PlainHTTP bool
	// Wait blocks until the release is ready.
	Wait bool
}

// Install runs `helm upgrade --install` so it is idempotent: a re-run
// converges the release instead of failing.
func (c *Client) Install(ctx context.Context, opts InstallOptions) error {
	// helm upgrade --install NAME CHART [flags]
	args := []string{"upgrade", "--install", "argocd"}
	if opts.Chart != "" {
		args = append(args, opts.Chart)
	} else {
		args = append(args, "argo-cd", "--repo", DefaultChartRepo)
	}
	args = append(args, "--namespace", c.Namespace, "--create-namespace")
	if opts.Version != "" {
		args = append(args, "--version", opts.Version)
	}
	for _, f := range opts.ValuesFiles {
		args = append(args, "-f", f)
	}
	if opts.PlainHTTP {
		args = append(args, "--plain-http")
	}
	if opts.Wait {
		args = append(args, "--wait", "--timeout", "10m")
	}
	return c.Runner.Run(ctx, c.Helm, args...)
}

// applicationList mirrors only the fields we need from the ArgoCD
// Application CRD.
type applicationList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			Sync struct {
				Status   string `json:"status"`
				Revision string `json:"revision"`
			} `json:"sync"`
			Health struct {
				Status string `json:"status"`
			} `json:"health"`
		} `json:"status"`
	} `json:"items"`
}

// ListApps returns the compact state of every Application in the ArgoCD
// namespace — the same shape edges put in their reports.
func (c *Client) ListApps(ctx context.Context) ([]wire.App, error) {
	out, err := c.Runner.Output(ctx, c.Kubectl,
		"get", "applications.argoproj.io", "-n", c.Namespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	var list applicationList
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("parsing Application list: %w", err)
	}
	apps := make([]wire.App, 0, len(list.Items))
	for _, it := range list.Items {
		rev := it.Status.Sync.Revision
		if len(rev) > 8 {
			rev = rev[:8]
		}
		apps = append(apps, wire.App{
			Name:     it.Metadata.Name,
			Sync:     it.Status.Sync.Status,
			Health:   it.Status.Health.Status,
			Revision: rev,
		})
	}
	return apps, nil
}
