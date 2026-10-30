use babis::wire::{CheckinRequest, CheckinResponse};
use std::fs;
use std::path::Path;

const DIR: &str = concat!(env!("CARGO_MANIFEST_DIR"), "/../wire/testdata");

fn round_trip<T>(name: &str, data: &str, validate: impl Fn(&T) -> Result<(), Vec<String>>)
where
    T: serde::Serialize + serde::de::DeserializeOwned + PartialEq + std::fmt::Debug,
{
    let first: T = serde_json::from_str(data).unwrap_or_else(|e| panic!("{name}: decode: {e}"));
    if let Err(e) = validate(&first) {
        panic!("{name}: validate: {e:?}");
    }
    let enc = serde_json::to_string(&first).unwrap();
    let second: T = serde_json::from_str(&enc).unwrap();
    assert_eq!(first, second, "{name}: round trip differs");
}

fn rejected<T: serde::de::DeserializeOwned>(
    data: &str,
    validate: impl Fn(&T) -> Result<(), Vec<String>>,
) -> bool {
    match serde_json::from_str::<T>(data) {
        Err(_) => true,
        Ok(v) => validate(&v).is_err(),
    }
}

#[test]
fn contract() {
    let mut seen = 0;
    for entry in fs::read_dir(Path::new(DIR)).expect("read testdata") {
        let path = entry.unwrap().path();
        let name = path.file_name().unwrap().to_string_lossy().into_owned();
        let data = fs::read_to_string(&path).unwrap();
        let req_ok = |r: &CheckinRequest| r.validate();
        let resp_ok = |_: &CheckinResponse| Ok(());
        if !name.ends_with(".json") {
            panic!("fixture {name} matches no category");
        }
        if name.starts_with("request-") {
            round_trip::<CheckinRequest>(&name, &data, req_ok);
        } else if name.starts_with("response-") {
            round_trip::<CheckinResponse>(&name, &data, resp_ok);
        } else if name.starts_with("invalid-request-") {
            assert!(
                rejected::<CheckinRequest>(&data, req_ok),
                "{name}: accepted"
            );
        } else if name.starts_with("invalid-response-") {
            assert!(
                rejected::<CheckinResponse>(&data, resp_ok),
                "{name}: accepted"
            );
        } else {
            panic!("fixture {name} matches no category");
        }
        seen += 1;
    }
    assert!(seen > 0, "no fixtures found");
}
