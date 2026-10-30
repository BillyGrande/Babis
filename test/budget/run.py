#!/usr/bin/env python3
"""Edge budget: run <binary> RUNS times against the fake API and fail if the
median peak RSS exceeds MAX_RSS_KB. Usage: run.py <binary> <workdir> [runs] [max_rss_kb]"""
import os, statistics, subprocess, sys, time

HERE = os.path.dirname(os.path.abspath(__file__))
binary, work = os.path.abspath(sys.argv[1]), os.path.abspath(sys.argv[2])
runs = int(sys.argv[3]) if len(sys.argv) > 3 else 5
max_rss = int(sys.argv[4]) if len(sys.argv) > 4 else 5 * 1024
PORT = 18082

# LARGE fixture: 300 Applications (~4 MB), laid out as the fake server serves it.
fx = os.path.join(work, "fx-large")
if not os.path.exists(os.path.join(fx, "apps.json")):
    subprocess.run([sys.executable, "-I", f"{HERE}/fakeserver.py", "gen", f"{work}/fixtures"],
                   check=True, stdout=subprocess.DEVNULL)
    os.makedirs(fx, exist_ok=True)
    for f in ("deployments.json", "statefulsets.json", "secret.json"):
        os.replace(f"{work}/fixtures/{f}", f"{fx}/{f}")
    os.replace(f"{work}/fixtures/apps-large.json", f"{fx}/apps.json")

# launcher: fork/exec/wait4 and report ru_maxrss on fd 3 (see launcher.c)
launcher = f"{work}/launcher"
subprocess.run(["cc", "-O2", "-static", "-o", launcher, f"{HERE}/launcher.c"], check=True)
token = f"{work}/token"
with open(token, "w") as f:
    f.write("test")  # the fake server accepts "Bearer test"

server = subprocess.Popen([sys.executable, "-I", f"{HERE}/fakeserver.py", "serve", fx, str(PORT)])
time.sleep(1)
env = {"PATH": os.environ["PATH"], "BABIS_TOKEN": "x",
       "BABIS_KUBE_API": f"http://127.0.0.1:{PORT}", "BABIS_KUBE_TOKEN_FILE": token}
args = ["checkin", "--hub", f"http://127.0.0.1:{PORT}", "--edge-name", "edge-001"]


def once():
    r, w = os.pipe()
    p = subprocess.Popen([launcher, binary, *args], env=env, stdout=subprocess.PIPE,
                         stderr=subprocess.PIPE, pass_fds=(3,), preexec_fn=lambda: os.dup2(w, 3))
    out, err = p.communicate()
    os.close(w)
    rss, ms, code = os.read(r, 100).decode().split()
    os.close(r)
    return int(rss), float(ms), int(code), (out or err).decode().strip()


try:
    once()  # warm-up
    res = [once() for _ in range(runs)]
finally:
    server.terminate()
bad = [r for r in res if r[2] != 0]
if bad:
    sys.exit(f"FAIL: exit {bad[0][2]}: {bad[0][3]}")
med = statistics.median(r[0] for r in res)
print(f"LARGE (300 apps): median peak RSS {med:.0f} KB over {runs} runs "
      f"(limit {max_rss}), wall {statistics.median(r[1] for r in res):.0f} ms")
print(res[0][3])
if med > max_rss:
    sys.exit(f"FAIL: median peak RSS {med:.0f} KB > {max_rss} KB")
