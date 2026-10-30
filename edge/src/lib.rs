pub mod cli;
pub mod hub;
pub mod kube;
pub mod observe;
pub mod wire;

use std::time::Instant;

/// Everything `checkin` needs. Tests build this directly; the CLI fills it
/// from flags and env.
pub struct Config {
    pub hub: String,
    pub hub_ca_pem: Option<Vec<u8>>,
    pub token: String,
    pub edge_name: String,
    pub namespace: String,
    pub page_size: u32,
    pub kube: kube::KubeConfig,
}

pub struct Sent {
    pub apps: usize,
    pub components: usize,
    pub duration_ms: u128,
}

/// Observes the local ArgoCD and posts one check-in. Read-only on the edge.
pub fn checkin(cfg: &Config) -> Result<Sent, String> {
    let start = Instant::now();
    let client = kube::Client::new(&cfg.kube)?;
    let req = observe::observe(&client, &cfg.namespace, &cfg.edge_name, cfg.page_size)
        .map_err(|e| format!("kubernetes: {e}"))?;
    if let Err(errs) = req.validate() {
        return Err(format!("invalid check-in: {}", errs.join("; ")));
    }
    hub::post(&cfg.hub, cfg.hub_ca_pem.as_deref(), &cfg.token, &req)?;
    Ok(Sent {
        apps: req.apps.as_ref().map_or(0, Vec::len),
        components: req
            .argocd
            .as_ref()
            .and_then(|a| a.components.as_ref())
            .map_or(0, Vec::len),
        duration_ms: start.elapsed().as_millis(),
    })
}
