package cli

import (
	"github.com/spf13/cobra"

	"github.com/BillyGrande/edge-deployer/internal/argocd"
)

// newBootstrapCmd installs ArgoCD on the local cluster. Online edges pull
// the chart from the upstream Argo Helm repo; air-gapped edges point
// --chart at a local .tgz or an oci:// reference on their local registry.
func newBootstrapCmd(g *globalOptions) *cobra.Command {
	var opts argocd.InstallOptions
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Install (or converge) ArgoCD on the local cluster",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return g.argocd(g.runner()).Install(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.Chart, "chart", "", "chart source: local .tgz or oci:// reference (default: upstream argo-helm repo)")
	cmd.Flags().StringVar(&opts.Version, "chart-version", "", "pin the chart version")
	cmd.Flags().StringArrayVarP(&opts.ValuesFiles, "values", "f", nil, "Helm values files (repeatable, applied in order)")
	cmd.Flags().BoolVar(&opts.PlainHTTP, "plain-http", false, "use plain HTTP for oci:// charts (local air-gap registries)")
	cmd.Flags().BoolVar(&opts.Wait, "wait", true, "wait for the release to become ready")
	return cmd
}
