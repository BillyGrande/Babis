// Command babis-loadgen simulates many edges checking in to the hub.
// It is a test tool: it is not shipped in any image.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/BillyGrande/babis/central/internal/wire"
)

const checkinPath = "/api/v1alpha1/checkin"

type config struct {
	hub, token, prefix  string
	edges, apps, rounds int
	spread, timeout     time.Duration
	asJSON              bool
}

// result is the outcome of one request.
type result struct {
	latency time.Duration
	kind    string // "ok", "http_<code>", "timeout", "transport"
}

// RoundSummary is the per-round report.
type RoundSummary struct {
	Round    int            `json:"round"`
	Requests int            `json:"requests"`
	OK       int            `json:"ok"`
	Errors   map[string]int `json:"errors"`
	P50ms    float64        `json:"p50_ms"`
	P95ms    float64        `json:"p95_ms"`
	P99ms    float64        `json:"p99_ms"`
	MaxMs    float64        `json:"max_ms"`
	WallSec  float64        `json:"wall_s"`
	ReqPerS  float64        `json:"req_per_s"`
}

func main() {
	var c config
	flag.StringVar(&c.hub, "hub", "http://127.0.0.1:8090", "hub base URL")
	flag.IntVar(&c.edges, "edges", 1000, "number of simulated edges")
	flag.IntVar(&c.apps, "apps", 300, "ArgoCD Applications per edge")
	flag.IntVar(&c.rounds, "rounds", 2, "check-in rounds")
	flag.DurationVar(&c.spread, "spread", 0, "start each edge at a random offset in [0, spread); 0 = all at once")
	flag.DurationVar(&c.timeout, "timeout", 30*time.Second, "per-request timeout")
	flag.StringVar(&c.prefix, "prefix", "load", "edge name prefix")
	flag.BoolVar(&c.asJSON, "json", false, "print a machine-readable summary")
	flag.Parse()
	c.token = os.Getenv("BABIS_TOKEN")

	if c.edges < 1 || c.apps < 0 || c.rounds < 1 {
		fmt.Fprintln(os.Stderr, "babis-loadgen: edges and rounds must be >= 1, apps >= 0")
		os.Exit(2)
	}
	bodies, err := buildBodies(c.prefix, c.edges, c.apps)
	if err != nil {
		fmt.Fprintln(os.Stderr, "babis-loadgen:", err)
		os.Exit(1)
	}
	sums := run(context.Background(), c, bodies)
	if c.asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(sums)
	}
}

func run(ctx context.Context, c config, bodies [][]byte) []RoundSummary {
	tr := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: c.timeout}).DialContext,
		MaxIdleConnsPerHost: c.edges,
		DisableKeepAlives:   true, // each edge is its own client, as with a CronJob
	}
	client := &http.Client{Transport: tr, Timeout: c.timeout}
	var sums []RoundSummary
	for r := 1; r <= c.rounds; r++ {
		s := runRound(ctx, c, client, bodies, r)
		sums = append(sums, s)
		logf(c, "round %d: requests=%d ok=%d errors=%v p50=%.0fms p95=%.0fms p99=%.0fms max=%.0fms wall=%.1fs %.0f req/s\n",
			s.Round, s.Requests, s.OK, s.Errors, s.P50ms, s.P95ms, s.P99ms, s.MaxMs, s.WallSec, s.ReqPerS)
	}
	return sums
}

func logf(c config, format string, a ...any) {
	out := io.Writer(os.Stdout)
	if c.asJSON {
		out = os.Stderr
	}
	fmt.Fprintf(out, format, a...)
}

func runRound(ctx context.Context, c config, client *http.Client, bodies [][]byte, round int) RoundSummary {
	results := make([]result, len(bodies))
	var wg sync.WaitGroup
	start := time.Now()
	for i := range bodies {
		var delay time.Duration
		if c.spread > 0 {
			delay = time.Duration(rand.Int64N(int64(c.spread)))
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					results[i] = result{kind: "transport"}
					return
				}
			}
			results[i] = send(ctx, client, c, bodies[i])
		}()
	}
	wg.Wait()
	return summarize(round, results, time.Since(start))
}

func send(ctx context.Context, client *http.Client, c config, body []byte) result {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.hub+checkinPath, bytes.NewReader(body))
	if err != nil {
		return result{kind: "transport"}
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	t0 := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return result{latency: time.Since(t0), kind: "timeout"}
		}
		return result{latency: time.Since(t0), kind: "transport"}
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	lat := time.Since(t0)
	if resp.StatusCode == http.StatusOK {
		return result{latency: lat, kind: "ok"}
	}
	return result{latency: lat, kind: fmt.Sprintf("http_%d", resp.StatusCode)}
}

func summarize(round int, rs []result, wall time.Duration) RoundSummary {
	s := RoundSummary{Round: round, Requests: len(rs), Errors: map[string]int{}, WallSec: wall.Seconds()}
	lats := make([]time.Duration, 0, len(rs))
	for _, r := range rs {
		if r.kind == "ok" {
			s.OK++
		} else {
			s.Errors[r.kind]++
		}
		lats = append(lats, r.latency)
	}
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	s.P50ms, s.P95ms, s.P99ms = ms(percentile(lats, 50)), ms(percentile(lats, 95)), ms(percentile(lats, 99))
	if len(lats) > 0 {
		s.MaxMs = ms(lats[len(lats)-1])
	}
	if wall > 0 {
		s.ReqPerS = float64(len(rs)) / wall.Seconds()
	}
	return s
}

// percentile returns the nearest-rank percentile p (0..100) of sorted.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(p/100*float64(len(sorted)) + 0.999999999)
	rank = min(max(rank, 1), len(sorted))
	return sorted[rank-1]
}

var (
	syncVals   = []string{"Synced", "Synced", "Synced", "OutOfSync", "Unknown"}
	healthVals = []string{"Healthy", "Healthy", "Healthy", "Progressing", "Degraded", "Missing", "Suspended"}
	components = []string{
		"argocd-server", "argocd-repo-server", "argocd-application-controller",
		"argocd-applicationset-controller", "argocd-redis", "argocd-notifications-controller",
	}
)

// buildBodies returns one pre-encoded, valid check-in body per edge.
func buildBodies(prefix string, edges, apps int) ([][]byte, error) {
	out := make([][]byte, 0, edges)
	for i := range edges {
		req := buildRequest(prefix, i, apps)
		if err := req.Validate(); err != nil {
			return nil, fmt.Errorf("edge %d: generated invalid request: %w", i, err)
		}
		b, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func buildRequest(prefix string, i, apps int) *wire.CheckinRequest {
	rng := rand.New(rand.NewPCG(uint64(i), 0xbab15))
	one := int64(1)
	comps := make([]wire.Component, len(components))
	for j, n := range components {
		comps[j] = wire.Component{Name: n, Ready: &one, Desired: &one}
	}
	list := make([]wire.App, apps)
	for j := range list {
		list[j] = wire.App{
			Name:     fmt.Sprintf("app-%04d", j),
			Sync:     syncVals[rng.IntN(len(syncVals))],
			Health:   healthVals[rng.IntN(len(healthVals))],
			Revision: fmt.Sprintf("%07x", rng.Uint32()&0xfffffff),
		}
	}
	return &wire.CheckinRequest{
		Edge: fmt.Sprintf("%s-%05d", prefix, i),
		Labels: map[string]string{
			"region": []string{"eu", "us", "ap"}[i%3],
			"site":   fmt.Sprintf("site-%d", i%50),
		},
		ArgoCD: &wire.ArgoCD{Version: "v3.5.6", Components: comps},
		Apps:   list,
	}
}
