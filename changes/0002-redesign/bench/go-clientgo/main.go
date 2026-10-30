package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type appT struct {
	Metadata struct{ Name string } `json:"metadata"`
	Status   struct {
		Sync   struct{ Status, Revision string } `json:"sync"`
		Health struct{ Status string }           `json:"health"`
	} `json:"status"`
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

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func main() {
	hub := os.Getenv("HUB_URL")
	cfg := &rest.Config{Host: os.Getenv("KUBE_URL"), BearerToken: os.Getenv("KUBE_TOKEN")}
	ctx := context.Background()
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		fail(err)
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		fail(err)
	}
	sel := metav1.ListOptions{LabelSelector: "app.kubernetes.io/part-of=argocd"}
	deps, err := cs.AppsV1().Deployments("argocd").List(ctx, sel)
	if err != nil {
		fail(err)
	}
	sts, err := cs.AppsV1().StatefulSets("argocd").List(ctx, sel)
	if err != nil {
		fail(err)
	}
	sec, err := cs.CoreV1().Secrets("argocd").Get(ctx, "in-cluster", metav1.GetOptions{})
	if err != nil {
		fail(err)
	}
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	ul, err := dc.Resource(gvr).Namespace("argocd").List(ctx, metav1.ListOptions{})
	if err != nil {
		fail(err)
	}
	var r report
	r.Edge = sec.Labels["babis.dev/edge"]
	r.Labels = sec.Labels
	r.Apps = make([]repApp, 0, len(ul.Items))
	for i := range ul.Items {
		var a appT
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(ul.Items[i].Object, &a); err != nil {
			fail(err)
		}
		r.Apps = append(r.Apps, repApp{a.Metadata.Name, a.Status.Sync.Status, a.Status.Health.Status, a.Status.Sync.Revision})
	}
	for _, d := range deps.Items {
		r.Argocd.Components = append(r.Argocd.Components, component{d.Name, d.Status.ReadyReplicas, *d.Spec.Replicas})
	}
	for _, s := range sts.Items {
		r.Argocd.Components = append(r.Argocd.Components, component{s.Name, s.Status.ReadyReplicas, *s.Spec.Replicas})
		if s.Name == "argocd-application-controller" && len(s.Spec.Template.Spec.Containers) > 0 {
			img := s.Spec.Template.Spec.Containers[0].Image
			r.Argocd.Version = img[strings.LastIndex(img, ":")+1:]
		}
	}
	body, _ := json.Marshal(r)
	resp, err := http.Post(hub+"/api/v1alpha1/checkin", "application/json", bytes.NewReader(body))
	if err != nil {
		fail(err)
	}
	resp.Body.Close()
	fmt.Println("status:", resp.Status, "apps:", len(r.Apps), "version:", r.Argocd.Version)
}
