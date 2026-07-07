// Package execx runs the external tools edgedeploy orchestrates (kubectl,
// helm, hauler). Shelling out to whatever is on PATH — instead of embedding
// client-go and the Helm SDK — keeps the binary small, respects the user's
// kubeconfig/context setup, and works with any distribution's tooling.
package execx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Runner executes external commands. With DryRun set, mutating commands are
// printed instead of executed.
type Runner struct {
	DryRun  bool
	Verbose bool
}

// Run executes a command, streaming output to the terminal.
func (r *Runner) Run(ctx context.Context, name string, args ...string) error {
	if r.skip(name, args...) {
		return nil
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// Output executes a read-only command and returns its stdout. It runs even
// under DryRun so state inspection keeps working.
func (r *Runner) Output(ctx context.Context, name string, args ...string) (string, error) {
	if r.Verbose {
		fmt.Fprintf(os.Stderr, "+ %s %s\n", name, strings.Join(args, " "))
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}

// Stdin executes a command with the given stdin. Used for
// `kubectl apply -f -` so rendered manifests never touch disk.
func (r *Runner) Stdin(ctx context.Context, stdin, name string, args ...string) error {
	if r.skip(name, args...) {
		return nil
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout = os.Stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, stderr.String())
	}
	return nil
}

func (r *Runner) skip(name string, args ...string) bool {
	if r.DryRun {
		fmt.Printf("[dry-run] %s %s\n", name, strings.Join(args, " "))
		return true
	}
	if r.Verbose {
		fmt.Fprintf(os.Stderr, "+ %s %s\n", name, strings.Join(args, " "))
	}
	return false
}
