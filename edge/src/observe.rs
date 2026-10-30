//! Reads the ArgoCD install through the Kubernetes API and builds the
//! check-in. Read-only. Structs below hold only the fields we use; serde
//! skips the rest without keeping it.

use crate::kube::{Client, KubeError};
use crate::wire::{self, ArgoCd, CheckinRequest, Component};
use serde::Deserialize;
use std::collections::BTreeMap;

#[derive(Deserialize, Default)]
struct Meta {
    #[serde(default)]
    name: String,
    #[serde(default)]
    labels: Option<BTreeMap<String, String>>,
}

#[derive(Deserialize, Default)]
struct SyncStatus {
    status: Option<String>,
    revision: Option<String>,
    revisions: Option<Vec<String>>,
}

#[derive(Deserialize, Default)]
struct HealthStatus {
    status: Option<String>,
}

#[derive(Deserialize, Default)]
struct AppStatus {
    sync: Option<SyncStatus>,
    health: Option<HealthStatus>,
}

#[derive(Deserialize)]
struct Application {
    metadata: Meta,
    status: Option<AppStatus>,
}

#[derive(Deserialize, Default)]
struct Container {
    image: Option<String>,
}

#[derive(Deserialize, Default)]
struct PodSpec {
    #[serde(default)]
    containers: Vec<Container>,
}

#[derive(Deserialize, Default)]
struct PodTemplate {
    spec: Option<PodSpec>,
}

#[derive(Deserialize, Default)]
struct WorkloadSpec {
    replicas: Option<u64>,
    template: Option<PodTemplate>,
}

#[derive(Deserialize, Default)]
struct WorkloadStatus {
    #[serde(default, rename = "readyReplicas")]
    ready_replicas: Option<u64>,
}

#[derive(Deserialize)]
struct Workload {
    metadata: Meta,
    spec: Option<WorkloadSpec>,
    status: Option<WorkloadStatus>,
}

#[derive(Deserialize)]
struct Secret {
    metadata: Meta,
}

/// Tag of a container image reference, or `unknown`. A digest is stripped;
/// a ':' is a tag separator only after the last '/'.
pub fn image_tag(image: &str) -> String {
    let no_digest = image.split('@').next().unwrap_or("");
    let last = no_digest.rsplit('/').next().unwrap_or("");
    match last.rsplit_once(':') {
        Some((_, tag)) if !tag.is_empty() => tag.to_string(),
        _ => "unknown".to_string(),
    }
}

fn map_app(a: Application) -> wire::App {
    let status = a.status.unwrap_or_default();
    let sync = status.sync.unwrap_or_default();
    let revision = sync
        .revision
        .filter(|r| !r.is_empty())
        .or_else(|| sync.revisions.and_then(|r| r.into_iter().next()));
    wire::App {
        name: a.metadata.name,
        sync: sync.status,
        health: status.health.and_then(|h| h.status),
        revision,
    }
}

fn first_image(w: &Workload) -> Option<&str> {
    w.spec
        .as_ref()?
        .template
        .as_ref()?
        .spec
        .as_ref()?
        .containers
        .first()?
        .image
        .as_deref()
}

/// Components for every workload, and the ArgoCD version from the
/// application controller (fallback: server).
fn components(workloads: &[Workload]) -> (Vec<Component>, String) {
    let comps = workloads
        .iter()
        .map(|w| Component {
            name: w.metadata.name.clone(),
            ready: w
                .status
                .as_ref()
                .and_then(|s| s.ready_replicas)
                .unwrap_or(0),
            desired: w.spec.as_ref().and_then(|s| s.replicas).unwrap_or(1),
        })
        .collect();
    let version = ["argocd-application-controller", "argocd-server"]
        .iter()
        .find_map(|name| {
            workloads
                .iter()
                .find(|w| w.metadata.name == *name)
                .and_then(first_image)
        })
        .map_or_else(|| "unknown".to_string(), image_tag);
    (comps, version)
}

fn edge_labels(raw: Option<BTreeMap<String, String>>) -> Option<BTreeMap<String, String>> {
    let kept: BTreeMap<_, _> = raw?
        .into_iter()
        .filter(|(k, _)| !wire::is_reserved_label(k))
        .collect();
    (!kept.is_empty()).then_some(kept)
}

/// Builds the full check-in snapshot. Does not validate it.
pub fn observe(
    client: &Client,
    ns: &str,
    edge: &str,
    page_size: u32,
) -> Result<CheckinRequest, KubeError> {
    let apps: Vec<Application> = client.list(
        &format!("/apis/argoproj.io/v1alpha1/namespaces/{ns}/applications"),
        None,
        page_size,
    )?;
    let sel = Some("app.kubernetes.io/part-of=argocd");
    let mut workloads: Vec<Workload> = client.list(
        &format!("/apis/apps/v1/namespaces/{ns}/deployments"),
        sel,
        page_size,
    )?;
    workloads.extend(client.list::<Workload>(
        &format!("/apis/apps/v1/namespaces/{ns}/statefulsets"),
        sel,
        page_size,
    )?);
    let labels = match client.get::<Secret>(&format!("/api/v1/namespaces/{ns}/secrets/in-cluster"))
    {
        Ok(s) => edge_labels(s.metadata.labels),
        Err(KubeError::NotFound) => None,
        Err(e) => return Err(e),
    };
    let (comps, version) = components(&workloads);
    Ok(CheckinRequest {
        edge: edge.to_string(),
        labels,
        argocd: Some(ArgoCd {
            version: Some(version),
            components: Some(comps),
        }),
        apps: Some(apps.into_iter().map(map_app).collect()),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn workload(json: &str) -> Workload {
        serde_json::from_str(json).unwrap()
    }

    #[test]
    fn image_tags() {
        let cases = [
            ("quay.io/argoproj/argocd:v2.13.2", "v2.13.2"),
            ("quay.io/argoproj/argocd@sha256:abc123", "unknown"),
            ("quay.io/argoproj/argocd:v3.0.1@sha256:abc123", "v3.0.1"),
            ("localhost:5000/argoproj/argocd", "unknown"),
            ("localhost:5000/argoproj/argocd:v3.2.0", "v3.2.0"),
            ("localhost:5000/argoproj/argocd@sha256:ab", "unknown"),
            ("argocd:v1", "v1"),
            ("argocd", "unknown"),
            ("argocd:", "unknown"),
            ("", "unknown"),
        ];
        for (img, want) in cases {
            assert_eq!(image_tag(img), want, "image {img:?}");
        }
    }

    #[test]
    fn component_defaults() {
        let ws = vec![
            workload(r#"{"metadata":{"name":"a"}}"#),
            workload(
                r#"{"metadata":{"name":"b"},"spec":{"replicas":3},"status":{"readyReplicas":2}}"#,
            ),
            workload(r#"{"metadata":{"name":"c"},"spec":{"replicas":0},"status":{}}"#),
        ];
        let (c, v) = components(&ws);
        let got: Vec<_> = c
            .iter()
            .map(|c| (c.name.as_str(), c.ready, c.desired))
            .collect();
        assert_eq!(got, [("a", 0, 1), ("b", 2, 3), ("c", 0, 0)]);
        assert_eq!(v, "unknown");
    }

    fn img(name: &str, image: &str) -> Workload {
        workload(&format!(
            r#"{{"metadata":{{"name":"{name}"}},"spec":{{"template":{{"spec":{{"containers":[{{"image":"{image}"}},{{"image":"x:other"}}]}}}}}}}}"#
        ))
    }

    #[test]
    fn version_source() {
        let cases = [
            (
                vec![
                    img("argocd-server", "r/a:v1"),
                    img("argocd-application-controller", "r/a:v2"),
                ],
                "v2",
            ),
            (
                vec![img("argocd-server", "r/a:v1"), img("other", "r/a:v9")],
                "v1",
            ),
            (
                vec![img("argocd-application-controller", "r/a@sha256:ff")],
                "unknown",
            ),
            (vec![img("other", "r/a:v9")], "unknown"),
            (vec![], "unknown"),
        ];
        for (ws, want) in cases {
            assert_eq!(components(&ws).1, want);
        }
    }

    fn app(json: &str) -> wire::App {
        map_app(serde_json::from_str(json).unwrap())
    }

    #[test]
    fn app_mapping() {
        let a = app(
            r#"{"metadata":{"name":"x"},"status":{"sync":{"status":"OutOfSync","revision":"abc"},"health":{"status":"Degraded"}}}"#,
        );
        assert_eq!(
            (
                a.name.as_str(),
                a.sync.as_deref(),
                a.health.as_deref(),
                a.revision.as_deref()
            ),
            ("x", Some("OutOfSync"), Some("Degraded"), Some("abc"))
        );
        // multi-source: first of revisions
        let a = app(
            r#"{"metadata":{"name":"m"},"status":{"sync":{"status":"Synced","revisions":["r1","r2"]},"health":{"status":"Healthy"}}}"#,
        );
        assert_eq!(a.revision.as_deref(), Some("r1"));
        // single revision wins over revisions
        let a = app(
            r#"{"metadata":{"name":"m"},"status":{"sync":{"revision":"s","revisions":["r1"]}}}"#,
        );
        assert_eq!(a.revision.as_deref(), Some("s"));
        // no status yet
        let a = app(r#"{"metadata":{"name":"new"}}"#);
        assert_eq!((a.sync, a.health, a.revision), (None, None, None));
    }

    #[test]
    fn reserved_labels_dropped() {
        let raw = BTreeMap::from(
            [
                ("argocd.argoproj.io/secret-type", "cluster"),
                ("babis.dev/edge", "e"),
                ("node.kubernetes.io/x", "v"),
                ("foo.k8s.io/x", "v"),
                ("region", "gr"),
                ("example.com/tier", "retail"),
            ]
            .map(|(k, v)| (k.to_string(), v.to_string())),
        );
        let got = edge_labels(Some(raw)).unwrap();
        let keys: Vec<_> = got.keys().map(String::as_str).collect();
        assert_eq!(keys, ["example.com/tier", "region"]);
        assert_eq!(edge_labels(None), None);
        let only = BTreeMap::from([(
            "argocd.argoproj.io/secret-type".to_string(),
            "c".to_string(),
        )]);
        assert_eq!(edge_labels(Some(only)), None);
    }
}
