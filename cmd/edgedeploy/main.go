// edgedeploy is the edge-side CLI: bootstrap ArgoCD, apply manifests,
// inspect and report status. See the repository README.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/BillyGrande/edge-deployer/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.Execute(ctx); err != nil {
		os.Exit(1)
	}
}
