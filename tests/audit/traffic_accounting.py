#!/usr/bin/env python3
"""Loopback-only investigation rig; synthetic credentials, no production input."""
import argparse
import base64
import collections
import http.server
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import threading
import time
import urllib.parse
import urllib.request

ROOT = Path(os.environ.get("MMWXC_TRAFFIC_ARTIFACTS", "/root/mmwx-custom-artifacts/traffic-accounting"))
DIR = ROOT / "meter"
BIN = ROOT / "bin"
PROTOCOLS = ["ss2022", "vless", "trojan"]
EMAILS = {p: "localtest__" + p for p in PROTOCOLS}

def save(name, value):
    (DIR / name).write_text(json.dumps(value, indent=2) + "\n")

def configs():
    DIR.mkdir(exist_ok=True)
    key = base64.b64encode(os.urandom(16)).decode()
    userkey = base64.b64encode(os.urandom(16)).decode()
    uuid = "4169c5a1-8b46-44c2-a1d1-82e377be12cb"
    password = "local-only-traffic-investigation"
    s = {"log": {"loglevel": "info"}, "stats": {},
         "api": {"tag": "api", "listen": "127.0.0.1:28101", "services": ["StatsService", "HandlerService"]},
         "metrics": {"tag": "metrics", "listen": "127.0.0.1:28102"},
         "policy": {"levels": {"0": {"statsUserUplink": True, "statsUserDownlink": True}},
                    "system": {"statsInboundUplink": True, "statsInboundDownlink": True}},
         "inbounds": [], "outbounds": [{"protocol": "freedom", "tag": "direct"}],
         "routing": {"rules": [{"type": "field", "ip": ["127.0.0.1"], "outboundTag": "direct"}]}}
    c = {"log": {"loglevel": "info"}, "inbounds": [], "outbounds": [], "routing": {"rules": []}}
    for n, p in enumerate(PROTOCOLS):
        settings = {}
        outbound = {}
        if p == "ss2022":
            settings = {"method": "2022-blake3-aes-128-gcm", "password": key,
                        "network": "tcp,udp", "clients": [{"email": EMAILS[p], "password": userkey}] +
                        [{"email": "unused%d__ss2022" % x, "password": base64.b64encode(os.urandom(16)).decode()} for x in range(2)]}
            outbound = {"servers": [{"address": "127.0.0.1", "port": 28110+n, "method": settings["method"], "password": key+":"+userkey}]}
        elif p == "vless":
            settings = {"decryption": "none", "clients": [{"id": uuid, "email": EMAILS[p]}]}
            outbound = {"vnext": [{"address": "127.0.0.1", "port": 28110+n, "users": [{"id": uuid, "encryption": "none"}]}]}
        else:
            settings = {"clients": [{"password": password, "email": EMAILS[p]}]}
            outbound = {"servers": [{"address": "127.0.0.1", "port": 28110+n, "password": password}]}
        protocol = "shadowsocks" if p == "ss2022" else p
        s["inbounds"].append({"tag": p, "listen": "127.0.0.1", "port": 28110+n,
                               "protocol": protocol, "settings": settings})
        c["inbounds"].append({"tag": p, "listen": "127.0.0.1", "port": 28120+n,
                               "protocol": "socks", "settings": {"auth": "noauth", "udp": False}})
        c["outbounds"].append({"tag": p, "protocol": protocol, "settings": outbound})
        c["routing"]["rules"].append({"type": "field", "inboundTag": [p], "outboundTag": p})
    save("server.json", s)
    save("client.json", c)
    for f in ("server.json", "client.json"):
        (DIR / f).chmod(0o600)

class Target(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *args):
        pass
    def do_GET(self):
        q = urllib.parse.parse_qs(urllib.parse.urlsplit(self.path).query)
        rate = int(q.get("rate", [0])[0])
        total = int(q.get("bytes", [50*1024*1024])[0])
        mode = q.get("close", ["client"])[0]
        self.send_response(200)
        self.send_header("Content-Length", str(total))
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Connection", "close" if mode == "server" else "keep-alive")
        self.end_headers()
        sent = 0
        start = time.monotonic()
        try:
            with (DIR / "50MiB.bin").open("rb") as f:
                while sent < total:
                    chunk = f.read(min(65536, total-sent))
                    if not chunk:
                        f.seek(0)
                        continue
                    self.wfile.write(chunk)
                    self.wfile.flush()
                    sent += len(chunk)
                    if rate:
                        time.sleep(max(0, start + sent/rate - time.monotonic()))
        except (BrokenPipeError, ConnectionResetError):
            pass
        finally:
            with (DIR / "target.jsonl").open("a") as f:
                f.write(json.dumps({"at": time.time(), "query": q, "sent": sent, "elapsed": time.monotonic()-start})+"\n")
        if mode == "server":
            self.close_connection = True

def serve():
    configs()
    with (DIR / "50MiB.bin").open("wb") as f:
        f.truncate(50*1024*1024)
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 28100), Target)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    procs = []
    logs = []
    def stop(*_):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, stop)
    try:
        for role, binary in [("server", "fork-xray"), ("client", "official-xray")]:
            log = (DIR / (role+".log")).open("w")
            logs.append(log)
            env = dict(os.environ, GOMAXPROCS="2")
            if role == "server":
                env["MMWXC_CORE_CONTROL_SOCKET"] = "/tmp/mmwxc-meter.sock"
            procs.append(subprocess.Popen([str(BIN/binary), "run", "-c", str(DIR/(role+".json"))], env=env, stdout=log, stderr=log))
        save("pids.json", {"rig": os.getpid(), "server": procs[0].pid, "client": procs[1].pid})
        while all(p.poll() is None for p in procs):
            time.sleep(0.5)
    except KeyboardInterrupt:
        pass
    finally:
        for p in procs:
            if p.poll() is None:
                p.terminate()
        for p in procs:
            try:
                p.wait(timeout=5)
            except subprocess.TimeoutExpired:
                p.kill()
                p.wait()
        server.shutdown()
        server.server_close()
        for f in logs:
            f.close()
        Path("/tmp/mmwxc-meter.sock").unlink(missing_ok=True)
        save("stopped.json", {"at": time.time(), "pids": [p.pid for p in procs], "codes": [p.returncode for p in procs]})

def stats(reset=False):
    cmd = [str(BIN/"official-xray"), "api", "statsquery", "--server=127.0.0.1:28101"]
    if reset:
        cmd.append("-reset")
    result = json.loads(subprocess.check_output(cmd, stderr=subprocess.DEVNULL))
    return {s["name"]: int(s.get("value", 0)) for s in result.get("stat", [])}

def metrics():
    with urllib.request.urlopen("http://127.0.0.1:28102/debug/vars", timeout=3) as r:
        return json.load(r)["stats"]

def meter(args):
    tag = args.name or "%s-%s-%s-%s" % (args.protocol, args.close, args.interval, args.reset)
    path = DIR / (tag+".jsonl")
    baseline = stats(False)
    # Only reset in experiments without an official collector attached.
    if args.reset:
        stats(True)
        baseline = {}
    samples = []
    totals = collections.Counter()
    stopped = threading.Event()
    def poll():
        with path.open("w") as f:
            while True:
                sample = {"at": time.time(), "core": stats(args.reset), "metrics": metrics()}
                samples.append(sample)
                totals.update(sample["core"])
                f.write(json.dumps(sample)+"\n")
                f.flush()
                if stopped.wait(args.interval):
                    break
    thread = threading.Thread(target=poll)
    thread.start()
    downloads = []
    try:
        for i in range(args.count):
            url = "http://127.0.0.1:28100/50MiB.bin?" + urllib.parse.urlencode({"rate": args.rate, "close": args.close})
            cmd = ["curl", "--silent", "--show-error", "--noproxy", "", "--proxy", "socks5h://127.0.0.1:%d" % (28120+PROTOCOLS.index(args.protocol)),
                   "--max-time", "330", "-o", "/dev/null", "--write-out", "%{json}", url]
            start = time.time()
            p = subprocess.run(cmd, capture_output=True, text=True)
            result = json.loads(p.stdout)
            downloads.append({"start": start, "end": time.time(), "code": p.returncode, "stderr": p.stderr,
                              "size_download": result["size_download"], "http_code": result["http_code"], "time_total": result["time_total"]})
            time.sleep(args.gap)
        time.sleep(args.settle)
    finally:
        stopped.set()
        thread.join()
    last = stats(args.reset)
    totals.update(last)
    core = dict(totals) if args.reset else {k: v-baseline.get(k, 0) for k, v in last.items()}
    result = {"name": tag, "args": vars(args), "downloads": downloads, "client_bytes": sum(d["size_download"] for d in downloads),
              "core": core, "metrics_final": metrics(), "poll_count": len(samples), "final_at": time.time()}
    save(tag+"-result.json", result)
    print(json.dumps(result), flush=True)

if __name__ == "__main__":
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest="action", required=True)
    sub.add_parser("serve")
    m = sub.add_parser("meter")
    m.add_argument("--protocol", choices=PROTOCOLS, default="ss2022")
    m.add_argument("--close", choices=["client", "server"], default="client")
    m.add_argument("--interval", type=float, default=0.2)
    m.add_argument("--reset", action="store_true")
    m.add_argument("--count", type=int, default=3)
    m.add_argument("--rate", type=int, default=0)
    m.add_argument("--gap", type=float, default=0.1)
    m.add_argument("--settle", type=float, default=1)
    m.add_argument("--name")
    a = p.parse_args()
    serve() if a.action == "serve" else meter(a)
