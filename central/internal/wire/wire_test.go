package wire

import (
	"encoding/json"
	"strings"
	"testing"
)

func i64(n int64) *int64 { return &n }

func TestRequestValidate(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	tests := []struct {
		name    string
		req     CheckinRequest
		wantErr string // substring; empty means valid
	}{
		{"minimal", CheckinRequest{Edge: "e1"}, ""},
		{"edge empty", CheckinRequest{}, "edge:"},
		{"edge upper", CheckinRequest{Edge: "E1"}, "edge:"},
		{"edge dots ok", CheckinRequest{Edge: "a.b-c.d"}, ""},
		{"edge leading dash", CheckinRequest{Edge: "-a"}, "edge:"},
		{"edge trailing dot", CheckinRequest{Edge: "a."}, "edge:"},
		{"edge double dot", CheckinRequest{Edge: "a..b"}, "edge:"},
		{"edge dot dash", CheckinRequest{Edge: "a.-b"}, "edge:"},
		{"edge dash dot", CheckinRequest{Edge: "a-.b"}, "edge:"},
		{"edge 253", CheckinRequest{Edge: long(253)}, ""},
		{"edge 254", CheckinRequest{Edge: long(254)}, "edge:"},
		{"label plain", CheckinRequest{Edge: "e", Labels: map[string]string{"region": "gr-north"}}, ""},
		{"label empty value", CheckinRequest{Edge: "e", Labels: map[string]string{"k": ""}}, ""},
		{"label prefixed", CheckinRequest{Edge: "e", Labels: map[string]string{"example.com/site": "a1"}}, ""},
		{"label key leading dash", CheckinRequest{Edge: "e", Labels: map[string]string{"-k": "v"}}, `labels["-k"]`},
		{"label key empty name", CheckinRequest{Edge: "e", Labels: map[string]string{"example.com/": "v"}}, "invalid name"},
		{"label key empty prefix", CheckinRequest{Edge: "e", Labels: map[string]string{"/k": "v"}}, "invalid prefix"},
		{"label key two slashes", CheckinRequest{Edge: "e", Labels: map[string]string{"a/b/c": "v"}}, "invalid name"},
		{"label key prefix dot dash", CheckinRequest{Edge: "e", Labels: map[string]string{"a.-b/k": "v"}}, "invalid prefix"},
		{"label key uppercase prefix", CheckinRequest{Edge: "e", Labels: map[string]string{"Example.com/k": "v"}}, "invalid prefix"},
		{"label name 63", CheckinRequest{Edge: "e", Labels: map[string]string{long(63): "v"}}, ""},
		{"label name 64", CheckinRequest{Edge: "e", Labels: map[string]string{long(64): "v"}}, "invalid name"},
		{"label value 63", CheckinRequest{Edge: "e", Labels: map[string]string{"k": long(63)}}, ""},
		{"label value 64", CheckinRequest{Edge: "e", Labels: map[string]string{"k": long(64)}}, "invalid value"},
		{"label value bad char", CheckinRequest{Edge: "e", Labels: map[string]string{"k": "a b"}}, "invalid value"},
		{"label value trailing dash", CheckinRequest{Edge: "e", Labels: map[string]string{"k": "a-"}}, "invalid value"},
		{"reserved exact", CheckinRequest{Edge: "e", Labels: map[string]string{"kubernetes.io/x": "v"}}, "reserved prefix kubernetes.io"},
		{"reserved subdomain", CheckinRequest{Edge: "e", Labels: map[string]string{"node.kubernetes.io/x": "v"}}, "reserved prefix kubernetes.io"},
		{"reserved argocd", CheckinRequest{Edge: "e", Labels: map[string]string{"argocd.argoproj.io/secret-type": "cluster"}}, "reserved"},
		{"reserved babis", CheckinRequest{Edge: "e", Labels: map[string]string{"babis.dev/x": "v"}}, "reserved"},
		{"reserved k8s", CheckinRequest{Edge: "e", Labels: map[string]string{"foo.k8s.io/x": "v"}}, "reserved"},
		{"look-alike allowed", CheckinRequest{Edge: "e", Labels: map[string]string{"notkubernetes.io/x": "v"}}, ""},
		{"look-alike k8s allowed", CheckinRequest{Edge: "e", Labels: map[string]string{"mik8s.io/x": "v"}}, ""},
		{"unprefixed name equal to reserved allowed", CheckinRequest{Edge: "e", Labels: map[string]string{"kubernetes.io": "v"}}, ""},
		{"component ok", CheckinRequest{Edge: "e", ArgoCD: &ArgoCD{Components: []Component{{"c", i64(0), i64(1)}}}}, ""},
		{"component no name", CheckinRequest{Edge: "e", ArgoCD: &ArgoCD{Components: []Component{{"", i64(1), i64(1)}}}}, "argocd.components[0].name"},
		{"component negative", CheckinRequest{Edge: "e", ArgoCD: &ArgoCD{Components: []Component{{"c", i64(-1), i64(1)}}}}, "argocd.components[0].ready"},
		{"component desired missing", CheckinRequest{Edge: "e", ArgoCD: &ArgoCD{Components: []Component{{"c", i64(1), nil}}}}, "argocd.components[0].desired"},
		{"app no name", CheckinRequest{Edge: "e", Apps: []App{{Sync: "Synced"}}}, "apps[0].name"},
		{"app name 254", CheckinRequest{Edge: "e", Apps: []App{{Name: long(254)}}}, "apps[0].name"},
		{"apps 5000", CheckinRequest{Edge: "e", Apps: manyApps(5000)}, ""},
		{"apps 5001", CheckinRequest{Edge: "e", Apps: manyApps(5001)}, "apps:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("want error containing %q, got nil", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func manyApps(n int) []App {
	a := make([]App, n)
	for i := range a {
		a[i].Name = "a"
	}
	return a
}

func TestRequestValidateCollectsAll(t *testing.T) {
	r := CheckinRequest{Edge: "BAD", Labels: map[string]string{"kubernetes.io/x": "v"}, Apps: []App{{}}}
	err := r.Validate()
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"edge:", `labels["kubernetes.io/x"]: reserved prefix kubernetes.io`, "apps[0].name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestResponseValidate(t *testing.T) {
	tests := []struct {
		name    string
		resp    CheckinResponse
		wantErr string
	}{
		{"ok", CheckinResponse{Generation: i64(0), Desired: map[string]json.RawMessage{}}, ""},
		{"negative generation", CheckinResponse{Generation: i64(-1), Desired: map[string]json.RawMessage{}}, "generation"},
		{"nil desired", CheckinResponse{Generation: i64(0)}, "desired"},
		{"nil generation", CheckinResponse{Desired: map[string]json.RawMessage{}}, "generation: required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.resp.Validate()
			if (err == nil) != (tt.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("got %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}
