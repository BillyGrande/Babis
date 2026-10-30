//! Argument parsing (lexopt). Pure: the caller passes args and the token.

use lexopt::prelude::*;
use std::time::Duration;

const MAX_JITTER: Duration = Duration::from_secs(600);

/// Parses `500ms`, `30s`, `2m`, `1m30s` (units in falling order). Rejects
/// anything else, and values over 10m.
pub fn parse_duration(s: &str) -> Result<Duration, String> {
    let bad = || format!("invalid duration {s:?} (use e.g. 500ms, 30s, 2m, 1m30s)");
    let mut rest = s;
    let mut total_ms: u64 = 0;
    let mut last_rank = 3u8;
    if rest.is_empty() {
        return Err(bad());
    }
    while !rest.is_empty() {
        let digits = rest.bytes().take_while(u8::is_ascii_digit).count();
        if digits == 0 || digits > 9 {
            return Err(bad());
        }
        let n: u64 = rest[..digits].parse().map_err(|_| bad())?;
        rest = &rest[digits..];
        let (unit_ms, rank, len) = if rest.starts_with("ms") {
            (1, 0, 2)
        } else if rest.starts_with('s') {
            (1000, 1, 1)
        } else if rest.starts_with('m') {
            (60_000, 2, 1)
        } else {
            return Err(bad());
        };
        if rank >= last_rank {
            return Err(bad());
        }
        last_rank = rank;
        rest = &rest[len..];
        total_ms = total_ms.saturating_add(n.saturating_mul(unit_ms));
    }
    let d = Duration::from_millis(total_ms);
    if d > MAX_JITTER {
        return Err(format!("duration {s:?} is over the maximum of 10m"));
    }
    Ok(d)
}

pub const USAGE: &str = "\
usage:
  babis checkin --hub URL --edge-name NAME [--namespace argocd]
                [--hub-ca FILE] [--page-size 100]
                [--jitter 0s]
  babis --version

--jitter sleeps a random time in [0, DURATION) before the check-in, so
many edges do not call the hub together. Examples: 500ms, 30s, 2m, 1m30s.
Maximum 10m. Default 0 (no delay).

environment:
  BABIS_TOKEN   bearer token for the hub (required)

testing only (override the in-cluster Kubernetes config):
  BABIS_KUBE_API         API server URL, http or https
  BABIS_KUBE_TOKEN_FILE  file with the API token
  BABIS_KUBE_CA_FILE     PEM CA for the API server

exit codes: 0 sent, 1 Kubernetes or hub error, 2 usage error";

#[derive(Debug, PartialEq)]
pub enum Command {
    Version,
    Help,
    Checkin(CheckinArgs),
}

#[derive(Debug, PartialEq)]
pub struct CheckinArgs {
    pub hub: String,
    pub edge_name: String,
    pub namespace: String,
    pub hub_ca: Option<String>,
    pub page_size: u32,
    pub jitter: Duration,
}

pub fn parse<I: IntoIterator<Item = String>>(args: I) -> Result<Command, String> {
    let mut p = lexopt::Parser::from_args(args);
    let (mut hub, mut edge, mut hub_ca) = (None, None, None);
    let mut namespace = "argocd".to_string();
    let mut page_size = 100u32;
    let mut jitter = Duration::ZERO;
    let mut checkin = false;
    let e = |e: lexopt::Error| e.to_string();
    while let Some(arg) = p.next().map_err(e)? {
        match arg {
            Long("version") => return Ok(Command::Version),
            Long("help") | Short('h') => return Ok(Command::Help),
            Value(v) if !checkin => {
                if v != "checkin" {
                    return Err(format!("unknown command {v:?}"));
                }
                checkin = true;
            }
            Long("hub") => hub = Some(p.value().map_err(e)?.string().map_err(e)?),
            Long("edge-name") => edge = Some(p.value().map_err(e)?.string().map_err(e)?),
            Long("namespace") => namespace = p.value().map_err(e)?.string().map_err(e)?,
            Long("hub-ca") => hub_ca = Some(p.value().map_err(e)?.string().map_err(e)?),
            Long("page-size") => {
                page_size = p
                    .value()
                    .map_err(e)?
                    .string()
                    .map_err(e)?
                    .parse()
                    .ok()
                    .filter(|n| *n > 0)
                    .ok_or("--page-size must be a positive integer")?;
            }
            Long("jitter") => {
                let v = p.value().map_err(e)?.string().map_err(e)?;
                jitter = parse_duration(&v).map_err(|m| format!("--jitter: {m}"))?;
            }
            _ => return Err(e(arg.unexpected())),
        }
    }
    if !checkin {
        return Err("missing command (checkin)".into());
    }
    Ok(Command::Checkin(CheckinArgs {
        hub: hub.ok_or("--hub is required")?,
        edge_name: edge.ok_or("--edge-name is required")?,
        namespace,
        hub_ca,
        page_size,
        jitter,
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn run(args: &str) -> Result<Command, String> {
        parse(args.split_whitespace().map(String::from))
    }

    #[test]
    fn durations() {
        let ok = [
            ("0s", 0),
            ("500ms", 500),
            ("30s", 30_000),
            ("2m", 120_000),
            ("1m30s", 90_000),
            ("1m500ms", 60_500),
            ("10m", 600_000),
        ];
        for (s, ms) in ok {
            assert_eq!(parse_duration(s), Ok(Duration::from_millis(ms)), "{s}");
        }
        for s in [
            "",
            "0",
            "5",
            "s",
            "-1s",
            "1.5s",
            "1h",
            "30s1m",
            "1m1m",
            "abc",
            " 5s",
            "10m1s",
            "11m",
            "601s",
            "999999999999s",
        ] {
            assert!(parse_duration(s).is_err(), "{s}");
        }
    }

    #[test]
    fn parsing() {
        let ok = Command::Checkin(CheckinArgs {
            hub: "http://h".into(),
            edge_name: "e1".into(),
            namespace: "argocd".into(),
            hub_ca: None,
            page_size: 100,
            jitter: Duration::ZERO,
        });
        assert_eq!(run("checkin --hub http://h --edge-name e1"), Ok(ok));
        assert_eq!(
            run(
                "checkin --edge-name=e --hub=h --namespace ns --hub-ca c.pem --page-size 7 --jitter 1m30s"
            ),
            Ok(Command::Checkin(CheckinArgs {
                hub: "h".into(),
                edge_name: "e".into(),
                namespace: "ns".into(),
                hub_ca: Some("c.pem".into()),
                page_size: 7,
                jitter: Duration::from_secs(90),
            }))
        );
        assert_eq!(run("--version"), Ok(Command::Version));
        assert_eq!(run("--help"), Ok(Command::Help));

        let errors = [
            ("checkin --hub http://h", "--edge-name is required"),
            ("checkin --edge-name e", "--hub is required"),
            ("", "missing command"),
            ("bogus", "unknown command"),
            ("checkin --bogus", "--bogus"),
            ("checkin --hub h --edge-name e --page-size 0", "--page-size"),
            ("checkin --hub h --edge-name e --page-size x", "--page-size"),
            ("checkin --hub", "missing argument"),
            ("checkin --hub h --edge-name e --jitter 11m", "--jitter"),
            ("checkin --hub h --edge-name e --jitter 5", "--jitter"),
        ];
        for (args, want) in errors {
            let err = run(args).expect_err(args);
            assert!(err.contains(want), "{args}: {err}");
        }
    }
}
