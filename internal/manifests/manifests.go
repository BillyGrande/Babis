// Package manifests collects, renders, and applies Kubernetes manifests.
// Plain .yaml files apply as-is; .yaml.tmpl files are Go templates rendered
// with a user-supplied values file. Rendering is strict — a reference to a
// missing value is an error, never a silent empty string.
package manifests

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"sigs.k8s.io/yaml"

	"github.com/BillyGrande/babis/internal/execx"
)

const tmplExt = ".yaml.tmpl"

// LoadValues reads a YAML values file into the map handed to templates as
// {{ .foo.bar }}.
func LoadValues(path string) (map[string]any, error) {
	if path == "" {
		return map[string]any{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err := yaml.Unmarshal(b, &values); err != nil {
		return nil, fmt.Errorf("parsing values file %s: %w", path, err)
	}
	return values, nil
}

// Collect returns the manifests under each path (file or directory),
// sorted so apply order is deterministic. Numeric prefixes (00-ns.yaml,
// 10-app.yaml) are the intended way to control ordering.
func Collect(paths []string) ([]string, error) {
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, tmplExt) {
				files = append(files, filepath.Join(p, name))
			}
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no manifests found in %s", strings.Join(paths, ", "))
	}
	sort.Strings(files)
	return files, nil
}

// Render executes a manifest template with the given values.
// missingkey=error makes unknown references fail loudly.
func Render(path string, values map[string]any) (string, error) {
	tpl, err := template.New(filepath.Base(path)).Option("missingkey=error").ParseFiles(path)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tpl.Execute(&b, values); err != nil {
		return "", fmt.Errorf("rendering %s: %w", filepath.Base(path), err)
	}
	return b.String(), nil
}

// Apply renders (where needed) and applies each file in order via kubectl.
// Rendered output goes through stdin, never to disk.
func Apply(ctx context.Context, r *execx.Runner, kubectl string, w io.Writer, files []string, values map[string]any) error {
	for _, f := range files {
		fmt.Fprintln(w, "applying", filepath.Base(f))
		if !strings.HasSuffix(f, tmplExt) {
			if err := r.Run(ctx, kubectl, "apply", "-f", f); err != nil {
				return err
			}
			continue
		}
		rendered, err := Render(f, values)
		if err != nil {
			return err
		}
		if err := r.Stdin(ctx, rendered, kubectl, "apply", "-f", "-"); err != nil {
			return err
		}
	}
	return nil
}

// HasTemplates reports whether any file needs a values context.
func HasTemplates(files []string) bool {
	for _, f := range files {
		if strings.HasSuffix(f, tmplExt) {
			return true
		}
	}
	return false
}
