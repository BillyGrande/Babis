//! POST the check-in to the hub.

use crate::wire::{CheckinRequest, CheckinResponse};
use std::io::Read;

pub(crate) fn truncate(s: &str) -> String {
    let t: String = s.chars().take(200).collect();
    if t.len() < s.len() {
        format!("{t}...")
    } else {
        t
    }
}

/// Sends the request; returns the decoded response. A non-200 status is an error.
/// With `ca_pem` the hub certificate must chain to that CA only (it replaces
/// the built-in webpki roots).
pub fn post(
    hub: &str,
    ca_pem: Option<&[u8]>,
    token: &str,
    req: &CheckinRequest,
) -> Result<CheckinResponse, String> {
    let agent = crate::kube::agent(ca_pem)?;
    let body = serde_json::to_vec(req).map_err(|e| format!("encode: {e}"))?;
    let url = format!("{}/api/v1alpha1/checkin", hub.trim_end_matches('/'));
    let mut resp = agent
        .post(&url)
        .header("Authorization", &format!("Bearer {token}"))
        .header("Content-Type", "application/json")
        .send(&body[..])
        .map_err(|e| format!("hub: request failed: {e}"))?;
    let code = resp.status().as_u16();
    if code != 200 {
        let mut buf = String::new();
        let _ = resp
            .body_mut()
            .as_reader()
            .take(512)
            .read_to_string(&mut buf);
        return Err(format!("hub: returned {code}: {}", truncate(buf.trim())));
    }
    serde_json::from_reader(resp.body_mut().as_reader())
        .map_err(|e| format!("hub: decode response: {e}"))
}
