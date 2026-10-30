//! Argument parsing (lexopt). Pure: the caller passes args and the token.

use lexopt::prelude::*;

pub const USAGE: &str = "\
usage:
  babis checkin --hub URL --edge-name NAME [--namespace argocd]
                [--hub-ca FILE] [--page-size 100]
  babis --version

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
}

pub fn parse<I: IntoIterator<Item = String>>(args: I) -> Result<Command, String> {
    let mut p = lexopt::Parser::from_args(args);
    let (mut hub, mut edge, mut hub_ca) = (None, None, None);
    let mut namespace = "argocd".to_string();
    let mut page_size = 100u32;
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
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn run(args: &str) -> Result<Command, String> {
        parse(args.split_whitespace().map(String::from))
    }

    #[test]
    fn parsing() {
        let ok = Command::Checkin(CheckinArgs {
            hub: "http://h".into(),
            edge_name: "e1".into(),
            namespace: "argocd".into(),
            hub_ca: None,
            page_size: 100,
        });
        assert_eq!(run("checkin --hub http://h --edge-name e1"), Ok(ok));
        assert_eq!(
            run("checkin --edge-name=e --hub=h --namespace ns --hub-ca c.pem --page-size 7"),
            Ok(Command::Checkin(CheckinArgs {
                hub: "h".into(),
                edge_name: "e".into(),
                namespace: "ns".into(),
                hub_ca: Some("c.pem".into()),
                page_size: 7,
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
        ];
        for (args, want) in errors {
            let err = run(args).expect_err(args);
            assert!(err.contains(want), "{args}: {err}");
        }
    }
}
