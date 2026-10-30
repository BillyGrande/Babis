use std::process::ExitCode;

const USAGE: &str = "usage: babis --version";

enum Action {
    Version,
    Usage,
}

fn parse(args: &[String]) -> Action {
    match args {
        [flag] if flag == "--version" => Action::Version,
        _ => Action::Usage,
    }
}

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    match parse(&args) {
        Action::Version => {
            println!("babis {}", env!("CARGO_PKG_VERSION"));
            ExitCode::SUCCESS
        }
        Action::Usage => {
            eprintln!("{USAGE}");
            ExitCode::from(2)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_args() {
        let cases: [(&[&str], bool); 4] = [
            (&["--version"], true),
            (&[], false),
            (&["--bogus"], false),
            (&["--version", "extra"], false),
        ];
        for (args, want_version) in cases {
            let args: Vec<String> = args.iter().map(|s| s.to_string()).collect();
            assert_eq!(matches!(parse(&args), Action::Version), want_version);
        }
    }
}
