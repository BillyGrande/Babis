package manifests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRender(t *testing.T) {
	dir := t.TempDir()
	values := map[string]any{"edge": map[string]any{"name": "shop-042"}}
	tests := []struct {
		name    string
		content string
		want    string
		wantErr string
	}{
		{
			name:    "substitutes values",
			content: "name: {{ .edge.name }}\n",
			want:    "name: shop-042\n",
		},
		{
			name:    "missing value is an error",
			content: "name: {{ .edge.typo }}\n",
			wantErr: "typo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := write(t, dir, strings.ReplaceAll(tt.name, " ", "-")+".yaml.tmpl", tt.content)
			got, err := Render(path, values)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCollectSortsAndFilters(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "10-app.yaml", "")
	write(t, dir, "00-ns.yaml", "")
	write(t, dir, "05-secret.yaml.tmpl", "")
	write(t, dir, "README.md", "")

	files, err := Collect([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	want := []string{"00-ns.yaml", "05-secret.yaml.tmpl", "10-app.yaml"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", names, want)
	}
	if !HasTemplates(files) {
		t.Error("expected HasTemplates to be true")
	}
}

func TestCollectEmptyErrors(t *testing.T) {
	if _, err := Collect([]string{t.TempDir()}); err == nil {
		t.Error("expected error for directory without manifests")
	}
}

func TestLoadValues(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "values.yaml", "edge:\n  name: shop-042\n")
	v, err := LoadValues(path)
	if err != nil {
		t.Fatal(err)
	}
	edge, ok := v["edge"].(map[string]any)
	if !ok || edge["name"] != "shop-042" {
		t.Errorf("unexpected values: %#v", v)
	}
	if v, err := LoadValues(""); err != nil || len(v) != 0 {
		t.Errorf("empty path should yield empty values, got %#v, %v", v, err)
	}
}
