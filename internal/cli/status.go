package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/BillyGrande/edge-deployer/internal/wire"
)

// newStatusCmd shows the local ArgoCD Application state — the same data a
// report carries, for humans.
func newStatusCmd(g *globalOptions) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show sync/health of the local ArgoCD Applications",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			apps, err := g.argocd(g.runner()).ListApps(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(apps)
			}
			return printApps(apps)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func printApps(apps []wire.App) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "APP\tSYNC\tHEALTH\tREVISION")
	for _, a := range apps {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.Name, a.Sync, a.Health, a.Revision)
	}
	return w.Flush()
}
