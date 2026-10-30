//! End-to-end checkin against a fake Kubernetes API and a fake hub, both
//! tiny_http servers on loopback threads.

use babis::kube::KubeConfig;
use babis::wire::CheckinRequest;
use babis::{Config, checkin};
use std::sync::{Arc, Mutex};
use tiny_http::{Header, Response, Server};

type Handler = Box<dyn Fn(&str, &str, Option<String>) -> (u16, String) + Send>;

/// Starts a server; `handler(method, url, auth_header)` returns (status, body).
/// Returns the base URL.
fn serve(handler: Handler) -> String {
    let server = Server::http("127.0.0.1:0").unwrap();
    let url = format!("http://{}", server.server_addr().to_ip().unwrap());
    std::thread::spawn(move || {
        for mut req in server.incoming_requests() {
            let auth = req
                .headers()
                .iter()
                .find(|h| h.field.equiv("Authorization"))
                .map(|h| h.value.to_string());
            let mut body = String::new();
            let _ = std::io::Read::read_to_string(req.as_reader(), &mut body);
            let (code, out) = handler(&req.method().to_string(), req.url(), auth);
            let ct = Header::from_bytes("Content-Type", "application/json").unwrap();
            let _ = req.respond(
                Response::from_string(out)
                    .with_status_code(code)
                    .with_header(ct),
            );
        }
    });
    url
}

fn app(name: &str) -> String {
    format!(
        r#"{{"metadata":{{"name":"{name}","managedFields":[{{"x":1}}]}},"spec":{{"big":"ignored"}},
        "status":{{"sync":{{"status":"Synced","revision":"rev-{name}"}},"health":{{"status":"Healthy"}}}}}}"#
    )
}

fn query(url: &str, key: &str) -> Option<String> {
    let q = url.split_once('?')?.1;
    q.split('&')
        .find_map(|kv| kv.strip_prefix(&format!("{key}=")))
        .map(String::from)
}

struct Fake {
    secret_404: bool,
    urls: Arc<Mutex<Vec<String>>>,
    api: String,
}

fn fake_kube(secret_404: bool) -> Fake {
    let urls = Arc::new(Mutex::new(Vec::new()));
    let seen = urls.clone();
    let api = serve(Box::new(move |_, url, auth| {
        seen.lock().unwrap().push(url.to_string());
        if auth.as_deref() != Some("Bearer ktok") {
            return (401, "{}".into());
        }
        let path = url.split('?').next().unwrap();
        match path {
            "/apis/argoproj.io/v1alpha1/namespaces/argocd/applications" => {
                let (items, next) = match query(url, "continue").as_deref() {
                    None => (vec!["a1", "a2"], "t1"),
                    Some("t1") => (vec!["a3", "a4"], "t2"),
                    Some("t2") => (vec!["a5"], ""),
                    Some(other) => panic!("bad continue {other}"),
                };
                let items: Vec<_> = items.into_iter().map(app).collect();
                (
                    200,
                    format!(
                        r#"{{"metadata":{{"continue":"{next}"}},"items":[{}]}}"#,
                        items.join(",")
                    ),
                )
            }
            "/apis/apps/v1/namespaces/argocd/deployments" => {
                assert_eq!(
                    query(url, "labelSelector").as_deref(),
                    Some("app.kubernetes.io%2Fpart-of%3Dargocd")
                );
                (
                    200,
                    r#"{"items":[{"metadata":{"name":"argocd-server"},"spec":{"replicas":2,"template":{"spec":{"containers":[{"image":"quay.io/argoproj/argocd:v9.9.9"}]}}},"status":{"readyReplicas":1}}]}"#.into(),
                )
            }
            "/apis/apps/v1/namespaces/argocd/statefulsets" => (
                200,
                r#"{"items":[{"metadata":{"name":"argocd-application-controller"},"spec":{"template":{"spec":{"containers":[{"image":"quay.io/argoproj/argocd:v3.2.0@sha256:abc"}]}}},"status":{"readyReplicas":1}}]}"#.into(),
            ),
            "/api/v1/namespaces/argocd/secrets/in-cluster" => {
                if secret_404 {
                    (404, r#"{"kind":"Status","code":404}"#.into())
                } else {
                    (
                        200,
                        r#"{"metadata":{"name":"in-cluster","labels":{"argocd.argoproj.io/secret-type":"cluster","region":"gr-north","tier":"retail"}},"data":{"config":"c2VjcmV0"}}"#.into(),
                    )
                }
            }
            _ => (404, "{}".into()),
        }
    }));
    Fake {
        secret_404,
        urls,
        api,
    }
}

type Received = Arc<Mutex<Vec<String>>>;

fn fake_hub(status: u16) -> (String, Received) {
    let got: Received = Arc::default();
    let sink = got.clone();
    let url = serve(Box::new(move |method, url, auth| {
        if method != "POST" || url != "/api/v1alpha1/checkin" {
            return (404, "{}".into());
        }
        if auth.as_deref() != Some("Bearer hubtok") {
            return (401, r#"{"error":"unauthorized"}"#.into());
        }
        sink.lock().unwrap().push(String::new());
        if status == 200 {
            (200, r#"{"generation":0,"desired":{}}"#.into())
        } else {
            (status, r#"{"error":"nope"}"#.into())
        }
    }));
    (url, got)
}

// The serve() closure cannot see the POST body (it is consumed there), so
// the hub variant below captures it through a dedicated server.
fn capturing_hub() -> (String, Arc<Mutex<Option<String>>>) {
    let body = Arc::new(Mutex::new(None));
    let sink = body.clone();
    let server = Server::http("127.0.0.1:0").unwrap();
    let url = format!("http://{}", server.server_addr().to_ip().unwrap());
    std::thread::spawn(move || {
        for mut req in server.incoming_requests() {
            let mut b = String::new();
            let _ = std::io::Read::read_to_string(req.as_reader(), &mut b);
            let auth_ok = req
                .headers()
                .iter()
                .any(|h| h.field.equiv("Authorization") && h.value.as_str() == "Bearer hubtok");
            if req.url() == "/api/v1alpha1/checkin" && auth_ok {
                *sink.lock().unwrap() = Some(b);
                let _ = req.respond(
                    Response::from_string(r#"{"generation":3,"desired":{},"future":1}"#)
                        .with_status_code(200),
                );
            } else {
                let _ = req.respond(Response::from_string("{}").with_status_code(401));
            }
        }
    });
    (url, body)
}

fn config(kube: &Fake, hub: &str, token: &str) -> Config {
    Config {
        hub: hub.to_string(),
        hub_ca_pem: None,
        token: token.to_string(),
        edge_name: "edge-001".into(),
        namespace: "argocd".into(),
        page_size: 2,
        kube: KubeConfig {
            api: kube.api.clone(),
            token: "ktok".into(),
            ca_pem: None,
        },
    }
}

#[test]
fn happy_path_three_pages() {
    let kube = fake_kube(false);
    let (hub, body) = capturing_hub();
    let sent = checkin(&config(&kube, &hub, "hubtok")).unwrap();
    assert_eq!((sent.apps, sent.components), (5, 2));

    let req: CheckinRequest = serde_json::from_str(body.lock().unwrap().as_ref().unwrap()).unwrap();
    req.validate().unwrap();
    assert_eq!(req.edge, "edge-001");
    let names: Vec<_> = req
        .apps
        .as_ref()
        .unwrap()
        .iter()
        .map(|a| a.name.as_str())
        .collect();
    assert_eq!(names, ["a1", "a2", "a3", "a4", "a5"]);
    let a = &req.apps.as_ref().unwrap()[2];
    assert_eq!(a.sync.as_deref(), Some("Synced"));
    assert_eq!(a.health.as_deref(), Some("Healthy"));
    assert_eq!(a.revision.as_deref(), Some("rev-a3"));
    let argocd = req.argocd.unwrap();
    assert_eq!(argocd.version.as_deref(), Some("v3.2.0"));
    let comps: Vec<_> = argocd
        .components
        .unwrap()
        .into_iter()
        .map(|c| (c.name, c.ready, c.desired))
        .collect();
    assert_eq!(
        comps,
        [
            ("argocd-server".to_string(), 1, 2),
            ("argocd-application-controller".to_string(), 1, 1)
        ]
    );
    let labels = req.labels.unwrap();
    assert_eq!(labels.len(), 2, "{labels:?}");
    assert_eq!(labels["region"], "gr-north");

    let urls = kube.urls.lock().unwrap();
    let app_urls: Vec<_> = urls
        .iter()
        .filter(|u| u.contains("/applications"))
        .collect();
    assert_eq!(app_urls.len(), 3, "{app_urls:?}");
    assert!(app_urls.iter().all(|u| u.contains("limit=2")));
    assert!(app_urls[1].contains("continue=t1") && app_urls[2].contains("continue=t2"));
}

#[test]
fn secret_404_means_no_labels() {
    let kube = fake_kube(true);
    assert!(kube.secret_404);
    let (hub, body) = capturing_hub();
    checkin(&config(&kube, &hub, "hubtok")).unwrap();
    let req: CheckinRequest = serde_json::from_str(body.lock().unwrap().as_ref().unwrap()).unwrap();
    assert_eq!(req.labels, None);
    req.validate().unwrap();
}

#[test]
fn hub_401_is_an_error() {
    let kube = fake_kube(false);
    let (hub, got) = fake_hub(200);
    let err = checkin(&config(&kube, &hub, "wrong")).err().unwrap();
    assert!(err.contains("401"), "{err}");
    assert!(got.lock().unwrap().is_empty());
}

#[test]
fn hub_500_is_an_error() {
    let kube = fake_kube(false);
    let (hub, _) = fake_hub(500);
    let err = checkin(&config(&kube, &hub, "hubtok")).err().unwrap();
    assert!(err.contains("500") && err.contains("nope"), "{err}");
}

#[test]
fn kube_401_is_an_error() {
    let kube = fake_kube(false);
    let (hub, _) = fake_hub(200);
    let mut cfg = config(&kube, &hub, "hubtok");
    cfg.kube.token = "bad".into();
    let err = checkin(&cfg).err().unwrap();
    assert!(
        err.starts_with("kubernetes:") && err.contains("401"),
        "{err}"
    );
}

#[test]
fn invalid_checkin_is_not_sent() {
    let kube = fake_kube(false);
    let (hub, got) = fake_hub(200);
    let mut cfg = config(&kube, &hub, "hubtok");
    cfg.edge_name = "Not_Valid".into();
    let err = checkin(&cfg).err().unwrap();
    assert!(
        err.contains("invalid check-in") && err.contains("edge:"),
        "{err}"
    );
    assert!(got.lock().unwrap().is_empty());
}
