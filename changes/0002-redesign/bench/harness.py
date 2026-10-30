#!/usr/bin/env python3
"""Usage: harness.py [runs]. Starts fake servers per fixture, runs each program N times, reports median peak RSS (KB) & wall (ms)."""
import os, subprocess, sys, time, statistics, json, signal
B = os.path.dirname(os.path.abspath(__file__))
RUNS = int(sys.argv[1]) if len(sys.argv) > 1 else 7
PROGS = [  # (label, binary, extra env)
 ("baseline /bin/true", "/bin/true", {}),
 ("go-stdlib", "bin-go-stdlib", {}),
 ("go-clientgo", "bin-go-clientgo", {}),
 ("go-stream", "bin-go-stream", {}),
 ("go-stream GOMEMLIMIT=8MiB", "bin-go-stream", {"GOMEMLIMIT": "8MiB"}),
 ("rust-ureq-unbuf (no BufReader)", "bin-rust-ureq-unbuf", {}),
 ("rust-ureq", "bin-rust-ureq", {}),
 ("rust-ureq-z", "bin-rust-ureq-z", {}),
 ("rust-kube", "bin-rust-kube", {}),
 ("go-stdlib GOGC=50", "bin-go-stdlib", {"GOGC": "50"}),
 ("go-stdlib GOMEMLIMIT=16MiB", "bin-go-stdlib", {"GOMEMLIMIT": "16MiB"}),
 ("go-clientgo GOGC=50", "bin-go-clientgo", {"GOGC": "50"}),
 ("go-clientgo GOMEMLIMIT=16MiB", "bin-go-clientgo", {"GOMEMLIMIT": "16MiB"}),
]
PORTS = {"small": 18081, "large": 18082}
servers = {}
for fx, port in PORTS.items():
    servers[fx] = subprocess.Popen([sys.executable, f"{B}/fakeserver.py", "serve", f"{B}/fx-{fx}", str(port)])
time.sleep(1)
def run(binary, env, port):
    e = {"PATH": os.environ["PATH"], "HUB_URL": f"http://127.0.0.1:{port}", "KUBE_URL": f"http://127.0.0.1:{port}", "KUBE_TOKEN": "test", **env}
    r, w = os.pipe()
    # launcher (tiny static C) does fork/exec/wait4; result comes back on fd 3. See launcher.c for why.
    p = subprocess.Popen([f"{B}/launcher", binary if binary.startswith("/") else f"{B}/{binary}"], env=e, stdout=subprocess.PIPE, stderr=subprocess.PIPE, pass_fds=(3,),
                         preexec_fn=lambda: os.dup2(w, 3))
    out, err = p.communicate(); os.close(w)
    rss, ms, code = os.read(r, 100).decode().split(); os.close(r)
    return int(rss), float(ms), int(code), (out or err).decode().strip()
res = []
try:
    for label, binary, env in PROGS:
        if not os.path.exists(binary if binary.startswith("/") else f"{B}/{binary}"): continue
        row = {"variant": label, "size": os.path.getsize(binary if binary.startswith("/") else f"{B}/{binary}")}
        for fx in ("small", "large"):
            rs = [run(binary, env, PORTS[fx]) for _ in range(RUNS + 1)][1:]  # drop 1 warmup
            bad = [r for r in rs if r[2] != 0]
            if bad: print("FAIL", label, fx, bad[0]); continue
            row[fx + "_rss"] = statistics.median(r[0] for r in rs)
            row[fx + "_wall"] = statistics.median(r[1] for r in rs)
            row[fx + "_out"] = rs[0][3]
        res.append(row)
finally:
    for s in servers.values(): s.terminate()
print(json.dumps(res, indent=1))
print("\n| variant | size | RSS SMALL (KB) | RSS LARGE (KB) | wall SMALL (ms) | wall LARGE (ms) |\n|---|---|---|---|---|---|")
for r in res:
    print(f"| {r['variant']} | {r['size']} ({r['size']/1e6:.2f} MB) | {r.get('small_rss','-')} | {r.get('large_rss','-')} | {r.get('small_wall',0):.1f} | {r.get('large_wall',0):.1f} |")
