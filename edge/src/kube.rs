//! Minimal Kubernetes API client: GET one object, paginated LIST, streaming
//! decode into caller-defined minimal structs.

use serde::Deserialize;
use serde::de::DeserializeOwned;
use std::fmt;
use std::io::BufReader;
use std::time::Duration;

const SA_DIR: &str = "/var/run/secrets/kubernetes.io/serviceaccount";

#[derive(Debug, Clone)]
pub struct KubeConfig {
    /// Base URL, no trailing slash. `http` or `https`.
    pub api: String,
    pub token: String,
    /// PEM bundle that signs the API server certificate. `None` -> webpki roots.
    pub ca_pem: Option<Vec<u8>>,
}

impl KubeConfig {
    /// In-cluster config. The testing-only overrides `BABIS_KUBE_API`,
    /// `BABIS_KUBE_TOKEN_FILE` and `BABIS_KUBE_CA_FILE` replace the
    /// service-account defaults. The token is read here, so call this once per run.
    pub fn from_env() -> Result<Self, String> {
        let var = |k: &str| std::env::var(k).ok().filter(|v| !v.is_empty());
        let (api, in_cluster) = match var("BABIS_KUBE_API") {
            Some(api) => (api.trim_end_matches('/').to_string(), false),
            None => {
                let host = var("KUBERNETES_SERVICE_HOST")
                    .ok_or("not in a cluster: KUBERNETES_SERVICE_HOST is not set (run as a pod)")?;
                let port = var("KUBERNETES_SERVICE_PORT").unwrap_or_else(|| "443".into());
                let host = if host.contains(':') {
                    format!("[{host}]")
                } else {
                    host
                };
                (format!("https://{host}:{port}"), true)
            }
        };
        let token_file = var("BABIS_KUBE_TOKEN_FILE").unwrap_or_else(|| format!("{SA_DIR}/token"));
        let token = std::fs::read_to_string(&token_file)
            .map_err(|e| format!("read token {token_file}: {e}"))?
            .trim()
            .to_string();
        let ca_file = match var("BABIS_KUBE_CA_FILE") {
            Some(f) => Some(f),
            None if in_cluster => Some(format!("{SA_DIR}/ca.crt")),
            None => None,
        };
        let ca_pem = match ca_file {
            Some(f) if api.starts_with("https://") => {
                Some(std::fs::read(&f).map_err(|e| format!("read CA {f}: {e}"))?)
            }
            _ => None,
        };
        Ok(Self { api, token, ca_pem })
    }
}

/// Builds an agent with the edge timeouts. `ca_pem` replaces the built-in
/// webpki roots; non-2xx statuses are returned, not raised, so callers can
/// tell 404 and 401 apart.
pub(crate) fn agent(ca_pem: Option<&[u8]>) -> Result<ureq::Agent, String> {
    let mut tls = ureq::tls::TlsConfig::builder();
    if let Some(pem) = ca_pem {
        let mut certs = Vec::new();
        for item in ureq::tls::parse_pem(pem) {
            if let ureq::tls::PemItem::Certificate(c) = item.map_err(|e| format!("CA PEM: {e}"))? {
                certs.push(c);
            }
        }
        if certs.is_empty() {
            return Err("CA PEM: no certificate found".into());
        }
        tls = tls.root_certs(ureq::tls::RootCerts::new_with_certs(&certs));
    }
    Ok(ureq::Agent::config_builder()
        .timeout_connect(Some(Duration::from_secs(10)))
        .timeout_global(Some(Duration::from_secs(30)))
        .http_status_as_error(false)
        .tls_config(tls.build())
        .build()
        .into())
}

#[derive(Debug)]
pub enum KubeError {
    NotFound,
    Status(u16, String),
    Transport(String),
    Decode(String),
}

impl fmt::Display for KubeError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::NotFound => write!(f, "not found (404)"),
            Self::Status(c, b) => write!(f, "API returned {c}: {b}"),
            Self::Transport(e) => write!(f, "request failed: {e}"),
            Self::Decode(e) => write!(f, "decode: {e}"),
        }
    }
}

#[derive(Deserialize)]
#[serde(bound(deserialize = "T: Deserialize<'de>"))]
struct Page<T> {
    #[serde(default = "Vec::new", deserialize_with = "null_as_empty")]
    items: Vec<T>,
    #[serde(default)]
    metadata: PageMeta,
}

#[derive(Deserialize, Default)]
struct PageMeta {
    #[serde(default, rename = "continue")]
    cont: Option<String>,
}

fn null_as_empty<'de, D, T>(d: D) -> Result<Vec<T>, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de>,
{
    Ok(Option::<Vec<T>>::deserialize(d)?.unwrap_or_default())
}

pub struct Client {
    agent: ureq::Agent,
    base: String,
    auth: String,
}

impl Client {
    pub fn new(cfg: &KubeConfig) -> Result<Self, String> {
        Ok(Self {
            agent: agent(cfg.ca_pem.as_deref())?,
            base: cfg.api.clone(),
            auth: format!("Bearer {}", cfg.token),
        })
    }

    fn call<T: DeserializeOwned>(&self, url: &str) -> Result<T, KubeError> {
        let mut resp = self
            .agent
            .get(url)
            .header("Authorization", &self.auth)
            .header("Accept", "application/json")
            .call()
            .map_err(|e| KubeError::Transport(e.to_string()))?;
        let code = resp.status().as_u16();
        if code == 404 {
            return Err(KubeError::NotFound);
        }
        if code != 200 {
            let body = resp.body_mut().read_to_string().unwrap_or_default();
            return Err(KubeError::Status(code, crate::hub::truncate(&body)));
        }
        let rdr = BufReader::with_capacity(64 * 1024, resp.body_mut().as_reader());
        serde_json::from_reader(rdr).map_err(|e| KubeError::Decode(e.to_string()))
    }

    /// GET one object, e.g. `/api/v1/namespaces/argocd/secrets/in-cluster`.
    pub fn get<T: DeserializeOwned>(&self, path: &str) -> Result<T, KubeError> {
        self.call(&format!("{}{path}", self.base))
    }

    /// LIST all items, `limit` per page, following `continue`.
    pub fn list<T: DeserializeOwned>(
        &self,
        path: &str,
        label_selector: Option<&str>,
        limit: u32,
    ) -> Result<Vec<T>, KubeError> {
        let mut out = Vec::new();
        let mut cont: Option<String> = None;
        loop {
            let mut url = format!("{}{path}?limit={limit}", self.base);
            if let Some(sel) = label_selector {
                url.push_str("&labelSelector=");
                url.push_str(&encode(sel));
            }
            if let Some(c) = &cont {
                url.push_str("&continue=");
                url.push_str(&encode(c));
            }
            let page: Page<T> = self.call(&url)?;
            out.extend(page.items);
            match page.metadata.cont {
                Some(c) if !c.is_empty() => cont = Some(c),
                _ => return Ok(out),
            }
        }
    }
}

/// Percent-encodes everything except RFC 3986 unreserved characters.
fn encode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b'~') {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn encodes_query_values() {
        assert_eq!(
            encode("app.kubernetes.io/part-of=argocd"),
            "app.kubernetes.io%2Fpart-of%3Dargocd"
        );
        assert_eq!(encode("a+b c=="), "a%2Bb%20c%3D%3D");
    }
}
