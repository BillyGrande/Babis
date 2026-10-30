pub mod cli;
pub mod hub;
pub mod kube;
pub mod observe;
pub mod wire;

use std::time::{Duration, Instant};

/// Picks a delay in `[0, jitter)` from a random `r` (multiply-shift, no
/// modulo). Zero jitter gives zero delay.
pub fn delay(jitter: Duration, r: u64) -> Duration {
    let d = (jitter.as_nanos() * u128::from(r)) >> 64;
    Duration::new((d / 1_000_000_000) as u64, (d % 1_000_000_000) as u32)
}

/// A random `u64` from the std hasher's per-process random keys.
pub fn random_u64() -> u64 {
    use std::hash::{BuildHasher, Hasher};
    std::collections::hash_map::RandomState::new()
        .build_hasher()
        .finish()
}

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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn delay_is_below_jitter() {
        let rs = [0, 1, u64::MAX, u64::MAX - 1, 1 << 63, 0xdead_beef];
        let rands = (0..10_000u64).map(|i| i.wrapping_mul(0x9E37_79B9_7F4A_7C15));
        for jitter in [
            Duration::from_nanos(1),
            Duration::from_millis(500),
            Duration::from_secs(60),
            Duration::from_secs(600),
        ] {
            for r in rs.into_iter().chain(rands.clone()) {
                assert!(delay(jitter, r) < jitter, "{jitter:?} {r}");
            }
        }
        for r in rs {
            assert_eq!(delay(Duration::ZERO, r), Duration::ZERO);
        }
        assert_eq!(delay(Duration::from_secs(10), 0), Duration::ZERO);
        assert!(delay(Duration::from_secs(10), u64::MAX) > Duration::from_millis(9_999));
    }
}
