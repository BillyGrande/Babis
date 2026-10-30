use babis::cli::{self, Command, USAGE};
use babis::{Config, kube};
use std::process::ExitCode;

fn main() -> ExitCode {
    let args = match cli::parse(std::env::args().skip(1)) {
        Ok(Command::Version) => {
            println!("babis {}", env!("CARGO_PKG_VERSION"));
            return ExitCode::SUCCESS;
        }
        Ok(Command::Help) => {
            println!("{USAGE}");
            return ExitCode::SUCCESS;
        }
        Ok(Command::Checkin(a)) => a,
        Err(e) => {
            eprintln!("babis: {e}\n{USAGE}");
            return ExitCode::from(2);
        }
    };
    let Some(token) = std::env::var("BABIS_TOKEN").ok().filter(|t| !t.is_empty()) else {
        eprintln!("babis: BABIS_TOKEN is not set\n{USAGE}");
        return ExitCode::from(2);
    };
    let fail = |msg: String| {
        eprintln!("babis: {msg}");
        ExitCode::from(1)
    };
    let hub_ca_pem = match args.hub_ca.as_deref().map(std::fs::read).transpose() {
        Ok(pem) => pem,
        Err(e) => return fail(format!("read --hub-ca: {e}")),
    };
    let kube = match kube::KubeConfig::from_env() {
        Ok(k) => k,
        Err(e) => return fail(e),
    };
    let wait = babis::delay(args.jitter, babis::random_u64());
    std::thread::sleep(wait);
    let cfg = Config {
        hub: args.hub,
        hub_ca_pem,
        token,
        edge_name: args.edge_name.clone(),
        namespace: args.namespace,
        page_size: args.page_size,
        kube,
    };
    match babis::checkin(&cfg) {
        Ok(sent) => {
            println!(
                "{}",
                serde_json::json!({
                    "level": "info",
                    "msg": "checkin sent",
                    "edge": args.edge_name,
                    "apps": sent.apps,
                    "components": sent.components,
                    "durationMs": sent.duration_ms,
                    "jitterMs": wait.as_millis() as u64,
                })
            );
            ExitCode::SUCCESS
        }
        Err(e) => fail(e),
    }
}
