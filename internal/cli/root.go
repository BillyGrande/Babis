// Package cli wires the edgedeploy CLI. Commands are built by constructors
// that close over their own flag state — no package-level variables.
// Business logic lives in the other internal packages.
package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/BillyGrande/edge-deployer/internal/argocd"
	"github.com/BillyGrande/edge-deployer/internal/execx"
)

// Version is stamped at build time via -ldflags "-X ...cli.Version=v0.1.0".
var Version = "dev"

// globalOptions holds the persistent flags shared by every subcommand.
type globalOptions struct {
	dryRun    bool
	verbose   bool
	kubectl   string
	helm      string
	namespace string
}

func (g *globalOptions) runner() *execx.Runner {
	return &execx.Runner{DryRun: g.dryRun, Verbose: g.verbose}
}

func (g *globalOptions) argocd(r *execx.Runner) *argocd.Client {
	return &argocd.Client{Runner: r, Kubectl: g.kubectl, Helm: g.helm, Namespace: g.namespace}
}

// NewRootCmd builds the full command tree. Exported so tests can run the
// CLI in-process.
func NewRootCmd() *cobra.Command {
	g := &globalOptions{}
	root := &cobra.Command{
		Use:   "edgedeploy",
		Short: "Deploy and observe autonomous ArgoCD edges, including air-gapped ones",
		Long: `edgedeploy runs locally on an edge cluster: it installs ArgoCD (from the
internet or from a local air-gap registry), applies the ArgoCD manifests
that drive the rest of the stack, and reports application status to a
central edge-deployer operator over an outbound-only connection.`,
		Version:      Version,
		SilenceUsage: true,
	}
	root.PersistentFlags().BoolVar(&g.dryRun, "dry-run", false, "print mutating commands instead of executing them")
	root.PersistentFlags().BoolVarP(&g.verbose, "verbose", "v", false, "echo every external command")
	root.PersistentFlags().StringVar(&g.kubectl, "kubectl", "kubectl", "kubectl binary to use")
	root.PersistentFlags().StringVar(&g.helm, "helm", "helm", "helm binary to use")
	root.PersistentFlags().StringVar(&g.namespace, "argocd-namespace", "argocd", "namespace of the ArgoCD installation")

	root.AddCommand(
		newBootstrapCmd(g),
		newApplyCmd(g),
		newStatusCmd(g),
		newReportCmd(g),
	)
	return root
}

// Execute runs the CLI with the given context.
func Execute(ctx context.Context) error {
	return NewRootCmd().ExecuteContext(ctx)
}
