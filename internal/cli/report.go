package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/BillyGrande/babis/internal/wire"
)

// newReportCmd gathers local state and pushes one report to the central
// operator. Designed to run from cron or a systemd timer — a periodic
// one-shot instead of a resident agent, so an edge has nothing extra to
// keep alive and the connection is always outbound.
func newReportCmd(g *globalOptions) *cobra.Command {
	var (
		hub          string
		edgeName     string
		labels       []string
		storeVersion string
		registryURL  string
	)
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Send one status report to the central operator (cron-friendly)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if edgeName == "" {
				hostname, err := os.Hostname()
				if err != nil {
					return fmt.Errorf("--edge-name not set and hostname unavailable: %w", err)
				}
				edgeName = strings.ToLower(hostname)
			}

			apps, err := g.argocd(g.runner()).ListApps(cmd.Context())
			if err != nil {
				return err
			}
			report := wire.Report{
				Edge:      edgeName,
				Labels:    parseLabels(labels),
				Timestamp: time.Now().UTC(),
				Apps:      apps,
			}
			if storeVersion != "" || registryURL != "" {
				report.Store = &wire.Store{
					Version:         storeVersion,
					RegistryHealthy: registryURL != "" && registryHealthy(cmd.Context(), registryURL),
				}
			}
			if err := report.Validate(); err != nil {
				return err
			}
			if g.dryRun {
				return json.NewEncoder(os.Stdout).Encode(report)
			}
			return send(cmd.Context(), hub, os.Getenv("BABIS_TOKEN"), &report)
		},
	}
	cmd.Flags().StringVar(&hub, "hub", "", "base URL of the central operator ingest endpoint (required)")
	cmd.Flags().StringVar(&edgeName, "edge-name", "", "unique edge name (default: lowercased hostname)")
	cmd.Flags().StringArrayVar(&labels, "label", nil, "edge label key=value (repeatable)")
	cmd.Flags().StringVar(&storeVersion, "store-version", "", "air-gap store version to report")
	cmd.Flags().StringVar(&registryURL, "registry-url", "", "local registry to health-check, e.g. http://localhost:5000")
	_ = cmd.MarkFlagRequired("hub")
	return cmd
}

func parseLabels(pairs []string) map[string]string {
	if len(pairs) == 0 {
		return nil
	}
	labels := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, _ := strings.Cut(p, "=")
		labels[k] = v
	}
	return labels
}

func registryHealthy(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(url, "/")+"/v2/", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func send(ctx context.Context, hub, token string, report *wire.Report) error {
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	url := strings.TrimSuffix(hub, "/") + "/api/v1alpha1/reports"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending report to %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("hub rejected report: %s", resp.Status)
	}
	fmt.Printf("reported %d apps as %s\n", len(report.Apps), report.Edge)
	return nil
}
