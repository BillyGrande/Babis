#!/usr/bin/env python3
"""Fake k8s API + fake hub. Usage: fakeserver.py gen <outdir> | serve <fixturedir> <port>"""
import json, sys, random, os, http.server, urllib.parse

def app(i, r):
    n = f"app-{i:04d}"
    rev = "%040x" % r.getrandbits(160)
    res = [{"group": "apps" if k % 3 == 0 else "", "version": "v1", "kind": ["Deployment","Service","ConfigMap"][k % 3],
            "namespace": "prod", "name": f"{n}-res-{k}", "status": "Synced", "health": {"status": "Healthy"}} for k in range(30)]
    hist = [{"revision": "%040x" % r.getrandbits(160), "deployedAt": "2026-10-%02dT10:00:00Z" % (j+1), "id": j,
             "source": {"repoURL": "https://git.example.com/org/repo.git", "path": f"apps/{n}", "targetRevision": "main"},
             "deployStartedAt": "2026-10-%02dT09:59:50Z" % (j+1), "initiatedBy": {"automated": True}} for j in range(10)]
    return {
      "apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
      "metadata": {"name": n, "namespace": "argocd", "uid": "%032x" % r.getrandbits(128), "resourceVersion": str(r.randint(10**6, 10**7)),
        "generation": 123, "creationTimestamp": "2026-01-05T08:00:00Z",
        "labels": {"app.kubernetes.io/instance": n, "team": "platform"},
        "annotations": {"kubectl.kubernetes.io/last-applied-configuration": "{" + "x"*600 + "}"},
        "finalizers": ["resources-finalizer.argocd.argoproj.io"],
        "managedFields": [{"manager": m, "operation": "Update", "apiVersion": "argoproj.io/v1alpha1", "time": "2026-10-01T10:00:00Z",
           "fieldsType": "FieldsV1", "fieldsV1": {"f:metadata": {"f:labels": {".": {}, "f:team": {}}}, "f:spec": {".": {}, "f:project": {}, "f:source": {".": {}, "f:path": {}, "f:repoURL": {}, "f:targetRevision": {}}, "f:syncPolicy": {".": {}, "f:automated": {".": {}, "f:prune": {}, "f:selfHeal": {}}}}}}
           for m in ("argocd-controller", "kubectl-client-side-apply", "argocd-application-controller")]},
      "spec": {"project": "default", "source": {"repoURL": "https://git.example.com/org/repo.git", "path": f"apps/{n}", "targetRevision": "main",
               "helm": {"valueFiles": ["values.yaml"], "parameters": [{"name": "replicas", "value": "2"}]}},
               "destination": {"server": "https://kubernetes.default.svc", "namespace": "prod"},
               "syncPolicy": {"automated": {"prune": True, "selfHeal": True}, "syncOptions": ["CreateNamespace=true"]}},
      "status": {"sync": {"status": r.choice(["Synced","Synced","OutOfSync"]), "comparedTo": {"source": {"repoURL": "https://git.example.com/org/repo.git", "path": f"apps/{n}", "targetRevision": "main"},
                          "destination": {"server": "https://kubernetes.default.svc", "namespace": "prod"}}, "revision": rev},
                 "health": {"status": r.choice(["Healthy","Healthy","Progressing","Degraded"])},
                 "history": hist,
                 "operationState": {"operation": {"sync": {"revision": rev, "prune": True}, "initiatedBy": {"automated": True}, "retry": {"limit": 5}},
                    "phase": "Succeeded", "message": "successfully synced (all tasks run)",
                    "syncResult": {"resources": [{"group": "", "version": "v1", "kind": "ConfigMap", "namespace": "prod", "name": f"{n}-cm-{k}", "status": "Synced", "message": "configmap/x unchanged", "hookPhase": "Running", "syncPhase": "Sync"} for k in range(10)],
                                   "revision": rev, "source": {"repoURL": "https://git.example.com/org/repo.git", "path": f"apps/{n}", "targetRevision": "main"}},
                    "startedAt": "2026-10-01T10:00:00Z", "finishedAt": "2026-10-01T10:00:05Z"},
                 "reconciledAt": "2026-10-30T10:00:00Z", "observedAt": "2026-10-30T10:00:00Z",
                 "resources": res, "sourceType": "Helm",
                 "summary": {"images": [f"registry.example.com/org/{n}:v1.{k}.0" for k in range(4)], "externalURLs": [f"https://{n}.example.com"]},
                 "controllerNamespace": "argocd"}}

def workload(kind, name, ver, replicas, ready):
    return {"apiVersion": "apps/v1", "kind": kind,
      "metadata": {"name": name, "namespace": "argocd", "uid": "%032x" % random.getrandbits(128), "resourceVersion": "12345", "generation": 3,
        "creationTimestamp": "2026-01-05T08:00:00Z", "labels": {"app.kubernetes.io/part-of": "argocd", "app.kubernetes.io/name": name},
        "annotations": {"deployment.kubernetes.io/revision": "1", "meta.helm.sh/release-name": "argocd"},
        "managedFields": [{"manager": "helm", "operation": "Update", "apiVersion": "apps/v1", "time": "2026-01-05T08:00:00Z", "fieldsType": "FieldsV1", "fieldsV1": {"f:spec": {"f:replicas": {}, "f:template": {"f:spec": {"f:containers": {"k:{\"name\":\"c\"}": {".": {}, "f:image": {}}}}}}}}]},
      "spec": {"replicas": replicas, "revisionHistoryLimit": 3, "selector": {"matchLabels": {"app.kubernetes.io/name": name}},
        "template": {"metadata": {"labels": {"app.kubernetes.io/name": name}},
          "spec": {"containers": [{"name": name, "image": f"quay.io/argoproj/argocd:{ver}", "imagePullPolicy": "Always",
             "command": ["argocd-" + name], "env": [{"name": f"E{k}", "valueFrom": {"configMapKeyRef": {"name": "argocd-cmd-params-cm", "key": f"k{k}", "optional": True}}} for k in range(15)],
             "ports": [{"containerPort": 8080, "protocol": "TCP"}], "resources": {}, "securityContext": {"runAsNonRoot": True, "readOnlyRootFilesystem": True},
             "volumeMounts": [{"name": "v", "mountPath": "/tmp"}]}], "volumes": [{"name": "v", "emptyDir": {}}], "serviceAccountName": name, "terminationGracePeriodSeconds": 30}}},
      "status": {"observedGeneration": 3, "replicas": replicas, "updatedReplicas": replicas, "readyReplicas": ready, "availableReplicas": ready,
        "conditions": [{"type": "Available", "status": "True", "lastUpdateTime": "2026-10-01T10:00:00Z", "lastTransitionTime": "2026-10-01T10:00:00Z", "reason": "MinimumReplicasAvailable", "message": "ok"}]}}

def gen(out):
    r = random.Random(42)
    os.makedirs(out, exist_ok=True)
    for name, n in (("small", 10), ("large", 300)):
        d = {"apiVersion": "argoproj.io/v1alpha1", "kind": "ApplicationList", "metadata": {"resourceVersion": "999"}, "items": [app(i, r) for i in range(n)]}
        open(f"{out}/apps-{name}.json", "w").write(json.dumps(d))
    dep = {"apiVersion": "apps/v1", "kind": "DeploymentList", "metadata": {"resourceVersion": "9"}, "items":
        [workload("Deployment", n, "v2.13.2", 1, 1) for n in ("argocd-server", "argocd-repo-server", "argocd-redis", "argocd-applicationset-controller", "argocd-notifications-controller")]}
    open(f"{out}/deployments.json", "w").write(json.dumps(dep))
    sts = {"apiVersion": "apps/v1", "kind": "StatefulSetList", "metadata": {"resourceVersion": "9"}, "items": [workload("StatefulSet", "argocd-application-controller", "v2.13.2", 1, 1)]}
    open(f"{out}/statefulsets.json", "w").write(json.dumps(sts))
    sec = {"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "in-cluster", "namespace": "argocd", "labels": {"argocd.argoproj.io/secret-type": "cluster", "babis.dev/edge": "edge-001", "region": "eu-west", "tier": "edge"}},
           "data": {"name": "aW4tY2x1c3Rlcg==", "server": "aHR0cHM6Ly9rdWJlcm5ldGVzLmRlZmF1bHQuc3Zj"}, "type": "Opaque"}
    open(f"{out}/secret.json", "w").write(json.dumps(sec))
    for f in sorted(os.listdir(out)): print(f, os.path.getsize(f"{out}/{f}"))

def serve(fx, port):
    cache = {f: open(f"{fx}/{f}", "rb").read() for f in os.listdir(fx)}
    apps = cache["apps.json"]
    class H(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"
        def log_message(self, *a): pass
        def send(self, code, body):
            self.send_response(code); self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
        def do_GET(self):
            if self.headers.get("Authorization") != "Bearer test": return self.send(401, b'{"kind":"Status","code":401}')
            p = urllib.parse.urlparse(self.path); q = urllib.parse.parse_qs(p.query)
            if p.path == "/apis/argoproj.io/v1alpha1/namespaces/argocd/applications": return self.send(200, apps)
            if p.path in ("/apis/apps/v1/namespaces/argocd/deployments", "/apis/apps/v1/namespaces/argocd/statefulsets"):
                if q.get("labelSelector") != ["app.kubernetes.io/part-of=argocd"]: return self.send(400, b"{}")
                return self.send(200, cache["deployments.json" if p.path.endswith("deployments") else "statefulsets.json"])
            if p.path == "/api/v1/namespaces/argocd/secrets/in-cluster": return self.send(200, cache["secret.json"])
            self.send(404, b'{"kind":"Status","code":404}')
        def do_POST(self):
            n = int(self.headers.get("Content-Length", 0)); body = self.rfile.read(n)
            if self.path != "/api/v1alpha1/checkin": return self.send(404, b"{}")
            json.loads(body)  # validate
            self.send(200, b'{"generation":0,"desired":{},"extensions":{}}')
    http.server.ThreadingHTTPServer.daemon_threads = True
    http.server.ThreadingHTTPServer(("127.0.0.1", port), H).serve_forever()

if __name__ == "__main__":
    if sys.argv[1] == "gen": gen(sys.argv[2])
    else: serve(sys.argv[2], int(sys.argv[3]))
