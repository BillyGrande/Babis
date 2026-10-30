use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::env;

#[derive(Deserialize, Default)]
struct Meta { #[serde(default)] name: String, #[serde(default)] labels: BTreeMap<String, String> }
#[derive(Deserialize, Default)] struct SyncS { #[serde(default)] status: String, #[serde(default)] revision: String }
#[derive(Deserialize, Default)] struct HealthS { #[serde(default)] status: String }
#[derive(Deserialize, Default)] struct AppStatus { #[serde(default)] sync: SyncS, #[serde(default)] health: HealthS }
#[derive(Deserialize)] struct App { metadata: Meta, #[serde(default)] status: AppStatus }
#[derive(Deserialize)] struct AppList { items: Vec<App> }

#[derive(Deserialize, Default)] struct Container { image: String }
#[derive(Deserialize, Default)] struct PodSpec { containers: Vec<Container> }
#[derive(Deserialize, Default)] struct Tmpl { spec: PodSpec }
#[derive(Deserialize, Default)] struct WSpec { #[serde(default)] replicas: i32, template: Tmpl }
#[derive(Deserialize, Default)] struct WStatus { #[serde(default, rename = "readyReplicas")] ready_replicas: i32 }
#[derive(Deserialize)] struct Workload { metadata: Meta, spec: WSpec, #[serde(default)] status: WStatus }
#[derive(Deserialize)] struct WList { items: Vec<Workload> }
#[derive(Deserialize)] struct Secret { metadata: Meta }

#[derive(Serialize)] struct Component { name: String, ready: i32, desired: i32 }
#[derive(Serialize)] struct RepApp { name: String, sync: String, health: String, revision: String }
#[derive(Serialize)] struct Argocd { version: String, components: Vec<Component> }
#[derive(Serialize)] struct Report { edge: String, labels: BTreeMap<String, String>, argocd: Argocd, apps: Vec<RepApp> }

fn get<T: for<'de> Deserialize<'de>>(agent: &ureq::Agent, base: &str, tok: &str, path: &str) -> T {
    let mut resp = agent.get(&format!("{base}{path}")).header("Authorization", &format!("Bearer {tok}")).call().expect("GET");
    let rdr = std::io::BufReader::with_capacity(64 * 1024, resp.body_mut().as_reader());
    serde_json::from_reader(rdr).expect("decode")
}

fn main() {
    let hub = env::var("HUB_URL").unwrap();
    let kube = env::var("KUBE_URL").unwrap();
    let tok = env::var("KUBE_TOKEN").unwrap();
    let agent: ureq::Agent = ureq::Agent::config_builder().build().into();
    let sel = "?labelSelector=app.kubernetes.io%2Fpart-of%3Dargocd";
    let apps: AppList = get(&agent, &kube, &tok, "/apis/argoproj.io/v1alpha1/namespaces/argocd/applications");
    let deps: WList = get(&agent, &kube, &tok, &format!("/apis/apps/v1/namespaces/argocd/deployments{sel}"));
    let sts: WList = get(&agent, &kube, &tok, &format!("/apis/apps/v1/namespaces/argocd/statefulsets{sel}"));
    let sec: Secret = get(&agent, &kube, &tok, "/api/v1/namespaces/argocd/secrets/in-cluster");
    let mut version = String::new();
    let mut components = Vec::new();
    for w in deps.items.iter().chain(sts.items.iter()) {
        components.push(Component { name: w.metadata.name.clone(), ready: w.status.ready_replicas, desired: w.spec.replicas });
        if w.metadata.name == "argocd-application-controller" {
            if let Some(c) = w.spec.template.spec.containers.first() {
                version = c.image.rsplit(':').next().unwrap_or("").to_string();
            }
        }
    }
    let r = Report {
        edge: sec.metadata.labels.get("babis.dev/edge").cloned().unwrap_or_default(),
        labels: sec.metadata.labels,
        argocd: Argocd { version: version.clone(), components },
        apps: apps.items.into_iter().map(|a| RepApp { name: a.metadata.name, sync: a.status.sync.status, health: a.status.health.status, revision: a.status.sync.revision }).collect(),
    };
    let n = r.apps.len();
    let body = serde_json::to_vec(&r).unwrap();
    let resp = agent.post(&format!("{hub}/api/v1alpha1/checkin")).header("Content-Type", "application/json").send(&body[..]).expect("POST");
    println!("status: {} apps: {} version: {}", resp.status(), n, version);
}
