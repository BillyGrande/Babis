//! Check-in wire contract. Mirrors `central/internal/wire` and the JSON
//! Schemas in `/wire`. Unknown JSON fields are ignored (never
//! `deny_unknown_fields`) so newer peers stay compatible.

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use std::collections::BTreeMap;

const MAX_APPS: usize = 5000;
const RESERVED: [&str; 4] = ["argocd.argoproj.io", "babis.dev", "kubernetes.io", "k8s.io"];

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct CheckinRequest {
    pub edge: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub labels: Option<BTreeMap<String, String>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub argocd: Option<ArgoCd>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub apps: Option<Vec<App>>,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct ArgoCd {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub version: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub components: Option<Vec<Component>>,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Component {
    pub name: String,
    pub ready: u64,
    pub desired: u64,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct App {
    pub name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sync: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub health: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub revision: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct CheckinResponse {
    pub generation: u64,
    pub desired: Map<String, Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub extensions: Option<Map<String, Value>>,
}

/// DNS-1123 subdomain: dot-separated labels of lowercase alphanumerics and
/// '-', each starting and ending alphanumeric; 1..253 chars in total.
fn valid_subdomain(s: &str) -> bool {
    let ok = |c: u8| c.is_ascii_lowercase() || c.is_ascii_digit();
    !s.is_empty()
        && s.len() <= 253
        && s.split('.').all(|label| {
            let b = label.as_bytes();
            !b.is_empty() && ok(b[0]) && ok(b[b.len() - 1]) && b.iter().all(|&c| ok(c) || c == b'-')
        })
}

fn valid_label_name(s: &str) -> bool {
    let b = s.as_bytes();
    let alnum = |c: u8| c.is_ascii_alphanumeric();
    !b.is_empty()
        && alnum(b[0])
        && alnum(b[b.len() - 1])
        && b.iter()
            .all(|&c| alnum(c) || matches!(c, b'-' | b'_' | b'.'))
}

fn truncate(s: &str) -> String {
    if s.chars().count() > 40 {
        format!("{}...", s.chars().take(40).collect::<String>())
    } else {
        s.to_string()
    }
}

fn validate_label_key(k: &str) -> Result<(), String> {
    let (prefix, name) = match k.split_once('/') {
        Some((p, n)) => (p, n),
        None => ("", k),
    };
    if k.contains('/') && !valid_subdomain(prefix) {
        return Err(format!("invalid prefix {:?}", truncate(prefix)));
    }
    if name.len() > 63 || !valid_label_name(name) {
        return Err(format!("invalid name {:?}", truncate(name)));
    }
    if let Some(res) = reserved_prefix(prefix) {
        return Err(format!("reserved prefix {res}"));
    }
    Ok(())
}

fn reserved_prefix(prefix: &str) -> Option<&'static str> {
    RESERVED
        .into_iter()
        .find(|res| prefix == *res || prefix.ends_with(&format!(".{res}")))
}

/// True if the label key has a reserved prefix (exact or subdomain).
pub fn is_reserved_label(key: &str) -> bool {
    key.split_once('/')
        .is_some_and(|(prefix, _)| reserved_prefix(prefix).is_some())
}

impl CheckinRequest {
    /// Checks the wire rules and returns every violation, or `Ok`.
    pub fn validate(&self) -> Result<(), Vec<String>> {
        let mut errs = Vec::new();
        if !valid_subdomain(&self.edge) {
            errs.push(format!(
                "edge: must be a DNS-1123 subdomain of 1..253 chars, got {:?}",
                truncate(&self.edge)
            ));
        }
        for (k, v) in self.labels.iter().flatten() {
            if let Err(e) = validate_label_key(k) {
                errs.push(format!("labels[{k:?}]: {e}"));
            }
            if v.len() > 63 || (!v.is_empty() && !valid_label_name(v)) {
                errs.push(format!("labels[{k:?}]: invalid value {:?}", truncate(v)));
            }
        }
        for (i, c) in self
            .argocd
            .iter()
            .flat_map(|a| a.components.iter().flatten())
            .enumerate()
        {
            if c.name.is_empty() || c.name.len() > 253 {
                errs.push(format!("argocd.components[{i}].name: must be 1..253 chars"));
            }
        }
        let apps = self.apps.as_deref().unwrap_or(&[]);
        if apps.len() > MAX_APPS {
            errs.push(format!(
                "apps: at most {MAX_APPS} items, got {}",
                apps.len()
            ));
        }
        for (i, a) in apps.iter().enumerate() {
            if a.name.is_empty() || a.name.len() > 253 {
                errs.push(format!("apps[{i}].name: must be 1..253 chars"));
            }
        }
        if errs.is_empty() { Ok(()) } else { Err(errs) }
    }
}

// Response rules (generation >= 0, desired present) are enforced by the
// types: u64 rejects negatives and a missing `generation` or `desired`
// fails to decode.

#[cfg(test)]
mod tests {
    use super::*;

    fn req(edge: &str) -> CheckinRequest {
        CheckinRequest {
            edge: edge.into(),
            labels: None,
            argocd: None,
            apps: None,
        }
    }

    fn with_label(k: &str, v: &str) -> CheckinRequest {
        let mut r = req("e");
        r.labels = Some(BTreeMap::from([(k.to_string(), v.to_string())]));
        r
    }

    #[test]
    fn edge_names() {
        let cases: Vec<(String, bool)> = vec![
            ("e1".into(), true),
            ("".into(), false),
            ("E1".into(), false),
            ("a.b-c.d".into(), true),
            ("-a".into(), false),
            ("a.".into(), false),
            ("a..b".into(), false),
            ("a.-b".into(), false),
            ("a-.b".into(), false),
            ("a".repeat(253), true),
            ("a".repeat(254), false),
        ];
        for (edge, ok) in cases {
            assert_eq!(req(&edge).validate().is_ok(), ok, "edge {edge:?}");
        }
    }

    #[test]
    fn label_cases() {
        let cases: Vec<(String, String, Option<&str>)> = vec![
            ("region".into(), "gr-north".into(), None),
            ("k".into(), "".into(), None),
            ("example.com/site".into(), "a1".into(), None),
            ("-k".into(), "v".into(), Some("invalid name")),
            ("example.com/".into(), "v".into(), Some("invalid name")),
            ("/k".into(), "v".into(), Some("invalid prefix")),
            ("a/b/c".into(), "v".into(), Some("invalid name")),
            ("Example.com/k".into(), "v".into(), Some("invalid prefix")),
            ("a".repeat(63), "v".into(), None),
            ("a".repeat(64), "v".into(), Some("invalid name")),
            ("k".into(), "a".repeat(63), None),
            ("k".into(), "a".repeat(64), Some("invalid value")),
            ("k".into(), "a b".into(), Some("invalid value")),
            ("k".into(), "a-".into(), Some("invalid value")),
            (
                "kubernetes.io/x".into(),
                "v".into(),
                Some("reserved prefix kubernetes.io"),
            ),
            (
                "node.kubernetes.io/x".into(),
                "v".into(),
                Some("reserved prefix kubernetes.io"),
            ),
            (
                "argocd.argoproj.io/secret-type".into(),
                "cluster".into(),
                Some("reserved"),
            ),
            ("babis.dev/x".into(), "v".into(), Some("reserved")),
            ("foo.k8s.io/x".into(), "v".into(), Some("reserved")),
            ("notkubernetes.io/x".into(), "v".into(), None),
            ("mik8s.io/x".into(), "v".into(), None),
            ("kubernetes.io".into(), "v".into(), None),
        ];
        for (k, v, want) in cases {
            let got = with_label(&k, &v).validate();
            match want {
                None => assert!(got.is_ok(), "{k:?}={v:?}: {got:?}"),
                Some(sub) => {
                    let errs = got.expect_err(&format!("{k:?}={v:?} should fail"));
                    assert!(errs.iter().any(|e| e.contains(sub)), "{errs:?} lacks {sub}");
                }
            }
        }
    }

    #[test]
    fn collects_all_errors_with_paths() {
        let mut r = req("BAD");
        r.labels = Some(BTreeMap::from([("kubernetes.io/x".into(), "v".into())]));
        r.apps = Some(vec![App {
            name: String::new(),
            sync: None,
            health: None,
            revision: None,
        }]);
        r.argocd = Some(ArgoCd {
            version: None,
            components: Some(vec![Component {
                name: String::new(),
                ready: 1,
                desired: 1,
            }]),
        });
        let errs = r.validate().unwrap_err();
        assert_eq!(errs.len(), 4, "{errs:?}");
        assert!(errs.iter().any(|e| e.starts_with("edge:")));
        assert!(
            errs.iter()
                .any(|e| e.contains("labels[\"kubernetes.io/x\"]: reserved prefix kubernetes.io"))
        );
        assert!(
            errs.iter()
                .any(|e| e.starts_with("argocd.components[0].name"))
        );
        assert!(errs.iter().any(|e| e.starts_with("apps[0].name")));
    }

    #[test]
    fn app_limits() {
        let app = |n: &str| App {
            name: n.into(),
            sync: None,
            health: None,
            revision: None,
        };
        let mut r = req("e");
        r.apps = Some(vec![app("a"); 5000]);
        assert!(r.validate().is_ok());
        r.apps = Some(vec![app("a"); 5001]);
        assert!(r.validate().is_err());
        r.apps = Some(vec![app(&"a".repeat(254))]);
        assert!(r.validate().is_err());
    }

    #[test]
    fn response_rules() {
        assert!(
            serde_json::from_str::<CheckinResponse>(r#"{"generation":0,"desired":{}}"#).is_ok()
        );
        assert!(
            serde_json::from_str::<CheckinResponse>(r#"{"generation":-1,"desired":{}}"#).is_err()
        );
        assert!(serde_json::from_str::<CheckinResponse>(r#"{"desired":{}}"#).is_err());
        assert!(serde_json::from_str::<CheckinResponse>(r#"{"generation":0}"#).is_err());
    }
}
