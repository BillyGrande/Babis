//! The hub over HTTPS. A fake hub (std TcpListener + rustls) uses a committed
//! test certificate. The edge must trust it only with the right `--hub-ca`.
//! Certificates: tests/testdata/tls/README.md.

use babis::hub;
use babis::wire::CheckinRequest;
use rustls::pki_types::pem::PemObject;
use rustls::pki_types::{CertificateDer, PrivateKeyDer};
use rustls::{ServerConfig, ServerConnection, StreamOwned};
use std::io::{Read, Write};
use std::net::TcpListener;
use std::sync::Arc;

const CA: &[u8] = include_bytes!("testdata/tls/ca.crt");
const OTHER_CA: &[u8] = include_bytes!("testdata/tls/other-ca.crt");
const SERVER_CRT: &[u8] = include_bytes!("testdata/tls/server.crt");
const SERVER_KEY: &[u8] = include_bytes!("testdata/tls/server.key");

/// Reads one HTTP request (headers and body) from `r`.
fn read_request(r: &mut impl Read) -> std::io::Result<Vec<u8>> {
    let mut buf = Vec::new();
    let mut chunk = [0u8; 4096];
    loop {
        let n = r.read(&mut chunk)?;
        if n == 0 {
            return Ok(buf);
        }
        buf.extend_from_slice(&chunk[..n]);
        let Some(end) = buf.windows(4).position(|w| w == b"\r\n\r\n") else {
            continue;
        };
        let head = String::from_utf8_lossy(&buf[..end]).to_lowercase();
        let len: usize = head
            .lines()
            .find_map(|l| l.strip_prefix("content-length:"))
            .and_then(|v| v.trim().parse().ok())
            .unwrap_or(0);
        if buf.len() >= end + 4 + len {
            return Ok(buf);
        }
    }
}

/// Starts the TLS fake hub on a loopback thread; returns its https URL.
/// It answers every request like the plain fake hub does.
fn tls_hub() -> String {
    let certs: Vec<CertificateDer<'static>> = CertificateDer::pem_slice_iter(SERVER_CRT)
        .collect::<Result<_, _>>()
        .unwrap();
    let key = PrivateKeyDer::from_pem_slice(SERVER_KEY).unwrap();
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let cfg = Arc::new(
        ServerConfig::builder_with_provider(provider)
            .with_safe_default_protocol_versions()
            .unwrap()
            .with_no_client_auth()
            .with_single_cert(certs, key)
            .unwrap(),
    );
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let url = format!(
        "https://localhost:{}",
        listener.local_addr().unwrap().port()
    );
    std::thread::spawn(move || {
        for stream in listener.incoming().flatten() {
            let conn = ServerConnection::new(cfg.clone()).unwrap();
            let mut tls = StreamOwned::new(conn, stream);
            // A failed handshake (the client rejects the certificate) ends here.
            let Ok(req) = read_request(&mut tls) else {
                continue;
            };
            let req = String::from_utf8_lossy(&req);
            let body = r#"{"generation":0,"desired":{}}"#;
            let ok =
                req.starts_with("POST /api/v1alpha1/checkin ") && req.contains("Bearer hubtok");
            let (code, body) = if ok {
                ("200 OK", body)
            } else {
                ("401 Unauthorized", r#"{"error":"unauthorized"}"#)
            };
            let resp = format!(
                "HTTP/1.1 {code}\r\nContent-Type: application/json\r\n\
                 Content-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            );
            let _ = tls.write_all(resp.as_bytes());
            let _ = tls.flush();
            tls.conn.send_close_notify();
            let _ = tls.flush();
        }
    });
    url
}

fn request() -> CheckinRequest {
    CheckinRequest {
        edge: "edge-001".into(),
        labels: None,
        argocd: None,
        apps: None,
    }
}

#[test]
fn hub_over_https() {
    type Case = (&'static str, Option<&'static [u8]>, bool);
    // (name, hub CA, check-in must fail)
    let cases: [Case; 3] = [
        ("right CA", Some(CA), false),
        ("no hub CA", None, true),
        ("wrong CA", Some(OTHER_CA), true),
    ];
    let url = tls_hub();
    for (name, ca, fails) in cases {
        let got = hub::post(&url, ca, "hubtok", &request());
        match (fails, got) {
            (false, Ok(resp)) => assert_eq!(resp.generation, 0, "{name}"),
            (true, Err(e)) => {
                assert!(e.contains("request failed"), "{name}: {e}");
                let e = e.to_lowercase();
                assert!(
                    e.contains("certificate") || e.contains("tls") || e.contains("unknown"),
                    "{name}: not a TLS error: {e}"
                );
            }
            (fails, got) => panic!("{name}: want failure {fails}, got {got:?}"),
        }
    }
}
