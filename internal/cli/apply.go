package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/BillyGrande/edge-deployer/internal/manifests"
)

// newApplyCmd renders and applies manifests — typically the ArgoCD
// Applications that bootstrap the rest of the edge. Plain .yaml applies
// as-is; .yaml.tmpl files are Go templates rendered with --values.
func newApplyCmd(g *globalOptions) *cobra.Command {
	var valuesFile string
	cmd := &cobra.Command{
		Use:   "apply <dir-or-file>...",
		Short: "Render (.yaml.tmpl) and apply manifests to the local cluster",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			files, err := manifests.Collect(args)
			if err != nil {
				return err
			}
			if manifests.HasTemplates(files) && valuesFile == "" {
				return fmt.Errorf("templates found but no --values file given")
			}
			values, err := manifests.LoadValues(valuesFile)
			if err != nil {
				return err
			}
			return manifests.Apply(cmd.Context(), g.runner(), g.kubectl, os.Stdout, files, values)
		},
	}
	cmd.Flags().StringVar(&valuesFile, "values", "", "YAML values file for .yaml.tmpl templates")
	return cmd
}
