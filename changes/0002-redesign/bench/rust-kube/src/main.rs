use k8s_openapi::api::apps::v1::{Deployment, StatefulSet};
use k8s_openapi::api::core::v1::Secret;
use kube::api::{Api, DynamicObject, ListParams};
use kube::core::{ApiResource, GroupVersionKind};
use kube::{Client, Config};
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::env;

#[derive(Deserialize, Default)] struct SyncS { #[serde(default)] status: String, #[serde(default)] revision: String }
#[derive(Deserialize, Default)] struct HealthS { #[serde(default)] status: String }
#[derive(Deserialize, Default)] struct AppStatus { #[serde(default)] sync: SyncS, #[serde(default)] health: HealthS }

#[derive(Serialize)] struct Component { name: String, ready: i32, desired: i32 }
#[derive(Serialize)] struct RepApp { name: String, sync: String, health: String, revision: String }
#[derive(Serialize)] struct Argocd { version: String, components: Vec<Component> }
#[derive(Serialize)] struct Report { edge: String, labels: BTreeMap<String, String>, argocd: Argocd, apps: Vec<RepApp> }

#[tokio::main(flavor = "current_thread")]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    rustls::crypto::ring::default_provider().install_default().ok();
    let hub = env::var("HUB_URL")?;
    let kube_url = env::var("KUBE_URL")?;
    let tok = env::var("KUBE_TOKEN")?;
    let mut cfg = Config::new(kube_url.parse()?);
    cfg.auth_info.token = Some(tok.into());
    let client = Client::try_from(cfg)?;

    let lp = ListParams::default().labels("app.kubernetes.io/part-of=argocd");
    let deps = Api::<Deployment>::namespaced(client.clone(), "argocd").list(&lp).await?;
    let sts = Api::<StatefulSet>::namespaced(client.clone(), "argocd").list(&lp).await?;
    let sec = Api::<Secret>::namespaced(client.clone(), "argocd").get("in-cluster").await?;
    let ar = ApiResource::from_gvk_with_plural(&GroupVersionKind::gvk("argoproj.io", "v1alpha1", "Application"), "applications");
    let apps = Api::<DynamicObject>::namespaced_with(client.clone(), "argocd", &ar).list(&Default::default()).await?;

    let mut components = Vec::new();
    let mut version = String::new();
    for d in &deps.items {
        let name = d.metadata.name.clone().unwrap_or_default();
        components.push(Component { name, ready: d.status.as_ref().and_then(|s| s.ready_replicas).unwrap_or(0), desired: d.spec.as_ref().and_then(|s| s.replicas).unwrap_or(0) });
    }
    for s in &sts.items {
        let name = s.metadata.name.clone().unwrap_or_default();
        if name == "argocd-application-controller" {
            if let Some(img) = s.spec.as_ref().and_then(|sp| sp.template.spec.as_ref()).and_then(|p| p.containers.first()).and_then(|c| c.image.as_ref()) {
                version = img.rsplit(':').next().unwrap_or("").to_string();
            }
        }
        components.push(Component { name, ready: s.status.as_ref().and_then(|s| s.ready_replicas).unwrap_or(0), desired: s.spec.as_ref().and_then(|s| s.replicas).unwrap_or(0) });
    }
    let labels = sec.metadata.labels.unwrap_or_default();
    let mut rep_apps = Vec::with_capacity(apps.items.len());
    for a in apps.items {
        let st: AppStatus = a.data.get("status").map(|v| serde_json::from_value(v.clone())).transpose()?.unwrap_or_default();
        rep_apps.push(RepApp { name: a.metadata.name.unwrap_or_default(), sync: st.sync.status, health: st.health.status, revision: st.sync.revision });
    }
    let n = rep_apps.len();
    let r = Report { edge: labels.get("babis.dev/edge").cloned().unwrap_or_default(), labels, argocd: Argocd { version: version.clone(), components }, apps: rep_apps };
    let resp = reqwest::Client::new().post(format!("{hub}/api/v1alpha1/checkin")).header("Content-Type", "application/json").body(serde_json::to_vec(&r)?).send().await?;
    println!("status: {} apps: {} version: {}", resp.status(), n, version);
    Ok(())
}
