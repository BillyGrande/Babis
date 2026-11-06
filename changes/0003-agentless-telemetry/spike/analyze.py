#!/usr/bin/env python3
"""Spike evidence helper. Usage: analyze.py PARTS_DIR COMMAND [ARGS]"""
import collections
import glob
import json
import os
import re
import sys
from datetime import datetime


def load(d):
    events = []
    for f in sorted(glob.glob(os.path.join(d, "*.jsonl"))):
        with open(f) as fh:
            for line in fh:
                if not line.startswith("{"):
                    continue
                try:
                    e = json.loads(line)
                except ValueError:
                    continue
                e["trigger"] = (re.search(r"[?&]trigger=(\w+)", e["path"]) or [None, "?"])[1]
                try:
                    e["parsed"] = json.loads(e["body"])
                    e["app"] = e["parsed"]["app"]["name"]
                except (ValueError, KeyError, TypeError):
                    e["parsed"] = None
                    m = re.search(r'"name":\s*"?([\w-]+)', e["body"])
                    e["app"] = m.group(1) if m else "?"
                events.append(e)
    events.sort(key=lambda e: e["time"])
    return events


def ts(s):
    return datetime.strptime(s, "%Y-%m-%dT%H:%M:%S.%fZ")


def fmt(e):
    return "%s %-9s %s" % (e["time"], e["trigger"], e["body"])


def main():
    d, cmd, args = sys.argv[1], sys.argv[2], sys.argv[3:]
    ev = load(d)
    if cmd == "count":  # APP TRIGGER [SINCE]
        since = args[2] if len(args) > 2 else ""
        print(sum(1 for e in ev if e["app"] == args[0] and e["trigger"] == args[1] and e["time"] >= since))
    elif cmd == "apps":  # prints apps seen
        print(" ".join(sorted({e["app"] for e in ev})))
    elif cmd == "summary":
        c = collections.Counter((e["app"], e["trigger"]) for e in ev)
        print("total requests: %d" % len(ev))
        for (a, t), n in sorted(c.items()):
            print("  %-14s %-10s %d" % (a, t, n))
    elif cmd == "bodies":
        bad = [e for e in ev if e["parsed"] is None]
        print("bodies: %d total, %d valid JSON, %d invalid" % (len(ev), len(ev) - len(bad), len(bad)))
        for e in bad[:5]:
            print("  INVALID: " + fmt(e))
    elif cmd == "lines":  # APP [SINCE] [MAX]
        since = args[1] if len(args) > 1 else ""
        mx = int(args[2]) if len(args) > 2 else 10
        for e in [e for e in ev if e["app"] == args[0] and e["time"] >= since][:mx]:
            print(fmt(e))
    elif cmd == "first":  # APP : full first raw line (with headers)
        for e in ev:
            if e["app"] == args[0]:
                print(json.dumps({k: e[k] for k in ("time", "method", "path", "headers", "body")}))
                break
    elif cmd == "auth":  # TOKEN
        want = "Bearer " + args[0]
        got = [e for e in ev if {k.lower(): v for k, v in e["headers"].items()}.get("authorization") == want]
        print("requests with Authorization == 'Bearer <token>': %d of %d" % (len(got), len(ev)))
        ctypes = collections.Counter({k.lower(): v for k, v in e["headers"].items()}.get("content-type") for e in ev)
        print("Content-Type values: %s" % dict(ctypes))
        if ev:
            print("headers of first request: %s" % json.dumps(ev[0]["headers"]))
        sys.exit(0 if ev and len(got) == len(ev) else 1)
    elif cmd in ("hb", "hbbuckets"):  # APP SINCE
        rows = [(e["time"], (e["parsed"] or {}).get("app", {}).get("reconciledAt")) for e in ev
                if e["app"] == args[0] and e["trigger"] == "heartbeat" and e["time"] >= args[1]]
        buckets = {r[:15] for _, r in rows if r}
        if cmd == "hbbuckets":
            print(len(buckets))
        else:
            for t, r in rows:
                print("%s reconciledAt=%s bucket[0:15]=%s" % (t, r, r[:15] if r else None))
            print("distinct non-null [0:15] buckets: %d" % len(buckets))
    elif cmd == "deleted":
        for e in ev:
            if e["parsed"] and e["parsed"].get("event") == "deleted":
                print(fmt(e))
    elif cmd == "after":  # APP SYNC SINCE
        print(sum(1 for e in ev if e["app"] == args[0] and e["parsed"] and
                  e["parsed"]["app"].get("sync") == args[1] and e["time"] >= args[2]))
    elif cmd == "intervals":  # TRIGGER SINCE
        by = collections.defaultdict(list)
        for e in ev:
            if e["trigger"] == args[0] and e["time"] >= args[1]:
                by[e["app"]].append(ts(e["time"]))
        for a, t in sorted(by.items()):
            gaps = [(t[i + 1] - t[i]).total_seconds() for i in range(len(t) - 1)]
            mean = sum(gaps) / len(gaps) if gaps else 0
            print("%-14s events=%d gaps(s)=%s mean=%.0fs" % (a, len(t), [round(g) for g in gaps], mean))
    elif cmd == "rv":  # WATCHFILE (kubectl get -w -o json stream)
        text = open(args[0]).read()
        dec, i, objs = json.JSONDecoder(), 0, []
        while i < len(text):
            while i < len(text) and text[i].isspace():
                i += 1
            if i >= len(text):
                break
            try:
                o, i = dec.raw_decode(text, i)
            except ValueError:
                break
            objs.append(o)
        key = "notified.notifications.argoproj.io"
        n_ann, n_rec, n_other, writers = 0, 0, 0, collections.Counter()
        prev = None
        for o in objs:
            ann = o["metadata"].get("annotations", {}).get(key)
            rec = o.get("status", {}).get("reconciledAt")
            mf = {(m["manager"], m.get("operation")): m.get("time") for m in o["metadata"].get("managedFields", [])}
            if prev is not None:
                n_ann += ann != prev[0]
                n_rec += rec != prev[1] and ann == prev[0]
                n_other += rec == prev[1] and ann == prev[0]
                for m, t in mf.items():
                    if prev[2].get(m) != t:
                        writers[m[0]] += 1
            prev = (ann, rec, mf)
        print("watch objects: %d (first is the initial state), resourceVersion changes: %d" % (len(objs), len(objs) - 1))
        print("  changes where the notified annotation changed: %d" % n_ann)
        print("  changes where only status.reconciledAt changed: %d" % n_rec)
        print("  other changes: %d" % n_other)
        print("  managedFields entries that moved, per manager: %s" % dict(writers))
        last = json.loads(objs[-1]["metadata"]["annotations"].get(key, "{}"))
        print("  notified annotation at the end: %d keys, %d bytes" % (len(last), len(json.dumps(last))))
        for k2 in sorted(last)[:3]:
            print("    key: %s -> %s" % (k2, last[k2]))
    elif cmd == "annot":  # prints size of notified annotation from a JSON Application on stdin
        a = json.load(sys.stdin)["metadata"].get("annotations", {}).get("notified.notifications.argoproj.io", "{}")
        print("notified annotation: %d keys, %d bytes" % (len(json.loads(a)), len(a)))
    else:
        sys.exit("unknown command " + cmd)


main()
