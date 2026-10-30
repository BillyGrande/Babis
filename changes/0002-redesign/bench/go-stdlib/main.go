package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

type meta struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}
type app struct {
	Metadata meta `json:"metadata"`
	Status   struct {
		Sync   struct{ Status, Revision string } `json:"sync"`
		Health struct{ Status string }           `json:"health"`
	} `json:"status"`
}
type appList struct {
	Items []app `json:"items"`
}
type workload struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		Replicas int32 `json:"replicas"`
		Template struct {
			Spec struct {
				Containers []struct {
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		ReadyReplicas int32 `json:"readyReplicas"`
	} `json:"status"`
}
type wlList struct {
	Items []workload `json:"items"`
}
type secret struct {
	Metadata meta `json:"metadata"`
}

type component struct {
	Name    string `json:"name"`
	Ready   int32  `json:"ready"`
	Desired int32  `json:"desired"`
}
type repApp struct {
	Name     string `json:"name"`
	Sync     string `json:"sync"`
	Health   string `json:"health"`
	Revision string `json:"revision"`
}
type report struct {
	Edge   string            `json:"edge"`
	Labels map[string]string `json:"labels"`
	Argocd struct {
		Version    string      `json:"version"`
		Components []component `json:"components"`
	} `json:"argocd"`
	Apps []repApp `json:"apps"`
}

var kube, token string

func get(path string, out any) error {
	req, _ := http.NewRequest("GET", kube+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func main() {
	hub := os.Getenv("HUB_URL")
	kube = os.Getenv("KUBE_URL")
	token = os.Getenv("KUBE_TOKEN")
	var apps appList
	var deps, sts wlList
	var sec secret
	sel := "?labelSelector=app.kubernetes.io%2Fpart-of%3Dargocd"
	for _, e := range []struct {
		p string
		o any
	}{
		{"/apis/argoproj.io/v1alpha1/namespaces/argocd/applications", &apps},
		{"/apis/apps/v1/namespaces/argocd/deployments" + sel, &deps},
		{"/apis/apps/v1/namespaces/argocd/statefulsets" + sel, &sts},
		{"/api/v1/namespaces/argocd/secrets/in-cluster", &sec},
	} {
		if err := get(e.p, e.o); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	var r report
	r.Edge = sec.Metadata.Labels["babis.dev/edge"]
	r.Labels = sec.Metadata.Labels
	r.Apps = make([]repApp, 0, len(apps.Items))
	for _, a := range apps.Items {
		r.Apps = append(r.Apps, repApp{a.Metadata.Name, a.Status.Sync.Status, a.Status.Health.Status, a.Status.Sync.Revision})
	}
	for _, w := range append(deps.Items, sts.Items...) {
		r.Argocd.Components = append(r.Argocd.Components, component{w.Metadata.Name, w.Status.ReadyReplicas, w.Spec.Replicas})
		if w.Metadata.Name == "argocd-application-controller" && len(w.Spec.Template.Spec.Containers) > 0 {
			img := w.Spec.Template.Spec.Containers[0].Image
			r.Argocd.Version = img[strings.LastIndex(img, ":")+1:]
		}
	}
	body, _ := json.Marshal(r)
	resp, err := http.Post(hub+"/api/v1alpha1/checkin", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	resp.Body.Close()
	fmt.Println("status:", resp.Status, "apps:", len(r.Apps), "version:", r.Argocd.Version)
}
