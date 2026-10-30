# Edge check-in benchmark (2026-10-30)

Evidence for the language decision in [../spec.md](../spec.md#language-decision).
Each program does the P1 `checkin` job against a fake Kubernetes API and hub:
list ArgoCD Applications, list ArgoCD Deployments/StatefulSets, get the
`in-cluster` secret, POST one report.

```bash
./make-fixtures.sh   # SMALL = 10 apps (137 KB), LARGE = 300 apps (4.1 MB)
./build-all.sh       # Go natively; Rust in docker (rust:1-alpine, musl, static); launcher.c
python3 harness.py 7                # median of 7 runs; peak RSS via wait4 in launcher.c
```

| Variant | Binary | Peak RSS SMALL | Peak RSS LARGE | Wall LARGE |
|---|---|---|---|---|
| rust-ureq (ureq + serde, streaming) | 2.2 MB | ~1.4 MB | ~1.8 MB | 62 ms |
| go-stream (stdlib, streaming decode) | 6.7 MB | 7.2 MB | 7.6 MB | 70 ms |
| go-stdlib (stdlib, buffered decode) | 6.7 MB | 7.4 MB | 19.5 MB | 77 ms |
| rust-kube (kube-rs 4.2, DynamicObject) | 7.0 MB | 6.0 MB | 45 MB | 174 ms |
| go-clientgo (client-go 0.37.2) | 26.8 MB | 23.5 MB | 58 MB | 198 ms |

Caveats: WSL2 amd64; plain HTTP (TLS compiled in, handshake not measured);
the ~50 ms wall-time floor comes from the Python fake server; rust-ureq RSS
sits at the launcher's ~1.4 MB measurement floor. Go 1.27.3, Rust 1.99.1.
