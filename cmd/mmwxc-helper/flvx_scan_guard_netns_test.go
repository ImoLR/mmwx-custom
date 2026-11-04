package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// 禁止端口扫描 (landing-scan-guard-v1 §9) with the real FLVX Core driven by
// the Helper's own reconcile, in three private namespaces (the host network
// is never touched; every scanned address is local to the target namespace):
//
//	client 203.0.113.50 ── 203.0.113.1 landing 11.77.0.1 ── target 11.77.0.0/16 (all local)
//
// Nodes A (31001) and B (31002) are SS2022; the client reaches them through
// one SOCKS inbound each. The target accepts on every address of the /16 and
// logs each (address, port) it was dialed on, so "reached" is counted where
// the scan lands. Opt-in: root, MMWXC_TEST_FLVX_CORE_SCAN (a Core with
// scan_guard.v1); MMWXC_SG_E2E_OUT receives the evidence JSON.
const sgTargetServer = `import socket, sys, threading
log = open(sys.argv[1], "a", buffering=1)
lock = threading.Lock()
def serve(port):
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("0.0.0.0", port)); s.listen(1024)
    while True:
        c, a = s.accept()
        local = c.getsockname()
        with lock:
            log.write("%s %d\n" % (local[0], local[1]))
        threading.Thread(target=handle, args=(c, a), daemon=True).start()
def handle(c, a):
    try:
        c.sendall(("peer=%s\n" % a[0]).encode())
        c.settimeout(30)
        while c.recv(64):
            pass
    except Exception:
        pass
    c.close()
for p in [22, 8080] + list(range(1001, 1041)):
    threading.Thread(target=serve, args=(p,), daemon=True).start()
threading.Event().wait()
`

// sgClientDriver: one JSON command per line on a unix socket. "probe" opens
// one SOCKS connection and reports whether the banner came back; "scan"
// connects to every target (parallel workers), sends a byte so the SS2022
// request leaves, and waits briefly; "hold" keeps one banner connection open.
const sgClientDriver = `import json, os, socket, struct, sys, threading, queue
path = sys.argv[1]
held = []
def socks(port, host, dport, timeout):
    c = socket.create_connection(("127.0.0.1", port), timeout=timeout)
    c.sendall(b"\x05\x01\x00")
    if c.recv(2) != b"\x05\x00":
        raise Exception("socks auth")
    c.sendall(b"\x05\x01\x00\x01" + socket.inet_aton(host) + struct.pack(">H", dport))
    r = b""
    while len(r) < 10:
        d = c.recv(10 - len(r))
        if not d:
            raise Exception("socks reply")
        r += d
    if r[1] != 0:
        raise Exception("socks refused")
    return c
def probe(port, host, dport, keep):
    try:
        c = socks(port, host, dport, 5)
        c.sendall(b"hi\n")
        c.settimeout(4)
        data = b""
        while not data.endswith(b"\n"):
            d = c.recv(256)
            if not d:
                break
            data += d
        if data.startswith(b"peer="):
            if keep:
                c.settimeout(None); held.append(c)
            else:
                c.close()
            return True
        c.close()
    except Exception:
        pass
    return False
def alive():
    out = []
    for c in held:
        try:
            c.setblocking(False)
            try:
                d = c.recv(1)
                out.append(d != b"")
            except BlockingIOError:
                out.append(True)
            c.setblocking(True)
        except Exception:
            out.append(False)
    return out
def scan(port, targets, workers):
    q = queue.Queue()
    for t in targets:
        q.put(t)
    def work():
        while True:
            try:
                host, dport = q.get_nowait()
            except queue.Empty:
                return
            try:
                c = socks(port, host, dport, 5)
                c.sendall(b"x")
                c.settimeout(1.5)
                try:
                    c.recv(64)
                except Exception:
                    pass
                c.close()
            except Exception:
                pass
    ts = [threading.Thread(target=work) for _ in range(workers)]
    [t.start() for t in ts]; [t.join() for t in ts]
try:
    os.unlink(path)
except OSError:
    pass
s = socket.socket(socket.AF_UNIX); s.bind(path); s.listen(4)
while True:
    conn, _ = s.accept()
    f = conn.makefile("rw")
    for line in f:
        cmd = json.loads(line)
        if cmd["op"] == "probe":
            res = {"ok": probe(cmd["port"], cmd["host"], cmd["dport"], cmd.get("keep", False))}
        elif cmd["op"] == "alive":
            res = {"alive": alive()}
        elif cmd["op"] == "release":
            for c in held:
                try:
                    c.close()
                except Exception:
                    pass
            held.clear(); res = {}
        else:
            scan(cmd["port"], [tuple(t) for t in cmd["targets"]], cmd.get("workers", 16))
            res = {}
        f.write(json.dumps(res) + "\n"); f.flush()
    conn.close()
`

type sgLab struct {
	t       *testing.T
	base    string
	targets string // target log
	conn    net.Conn
	r       *bufio.Reader
}

func (d *sgLab) call(cmd map[string]any, out any) {
	d.t.Helper()
	raw, _ := json.Marshal(cmd)
	d.conn.SetDeadline(time.Now().Add(5 * time.Minute))
	if _, err := d.conn.Write(append(raw, '\n')); err != nil {
		d.t.Fatal(err)
	}
	line, err := d.r.ReadString('\n')
	if err != nil || json.Unmarshal([]byte(line), out) != nil {
		d.t.Fatalf("driver %v: %v %q", cmd, err, line)
	}
}

func (d *sgLab) probe(port int, keep bool) bool {
	d.t.Helper()
	var res struct{ OK bool }
	d.call(map[string]any{"op": "probe", "port": port, "host": "11.77.0.2", "dport": 8080, "keep": keep}, &res)
	return res.OK
}

func (d *sgLab) alive() []bool {
	d.t.Helper()
	var res struct{ Alive []bool }
	d.call(map[string]any{"op": "alive"}, &res)
	return res.Alive
}

// scan dials targets through the SOCKS port and returns how many distinct
// (address, port) pairs among them the target namespace accepted.
func (d *sgLab) scan(port int, targets [][2]any) int {
	d.t.Helper()
	before := d.reached()
	var res map[string]any
	d.call(map[string]any{"op": "scan", "port": port, "targets": targets, "workers": 16}, &res)
	time.Sleep(300 * time.Millisecond)
	after := d.reached()
	n := 0
	for _, t := range targets {
		key := fmt.Sprintf("%v %v", t[0], t[1])
		if after[key] && !before[key] {
			n++
		}
	}
	return n
}

func (d *sgLab) reached() map[string]bool {
	raw, _ := os.ReadFile(d.targets)
	out := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" {
			out[line] = true
		}
	}
	return out
}

func horizontalTargets(third, count, port int) [][2]any {
	out := [][2]any{}
	for i := 1; i <= count; i++ {
		out = append(out, [2]any{fmt.Sprintf("11.77.%d.%d", third, i), port})
	}
	return out
}

func TestFLVXScanGuardNetnsE2E(t *testing.T) {
	bin := os.Getenv("MMWXC_TEST_FLVX_CORE_SCAN")
	if bin == "" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_TEST_FLVX_CORE_SCAN and run as root")
	}
	for _, tool := range []string{"python3", "ip"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " missing")
		}
	}
	raiseNoFile()
	base := os.Getenv("MMWXC_SG_E2E_DIR")
	if base == "" {
		base = t.TempDir()
	}
	os.MkdirAll(base, 0755)
	n := flvxNetns{t, base}
	c, l, d := "mmwxsg-c", "mmwxsg-l", "mmwxsg-t"
	flvxNetnsCleanup(c, l, d)
	t.Cleanup(func() { flvxNetnsCleanup(c, l, d) })
	for _, ns := range []string{c, l, d} {
		n.sh("ip", "netns", "add", ns)
		n.sh("ip", "-n", ns, "link", "set", "lo", "up")
	}
	n.sh("ip", "link", "add", "mmwxsg-c0", "type", "veth", "peer", "name", "mmwxsg-l0")
	n.sh("ip", "link", "set", "mmwxsg-c0", "netns", c)
	n.sh("ip", "link", "set", "mmwxsg-l0", "netns", l)
	n.sh("ip", "link", "add", "mmwxsg-l1", "type", "veth", "peer", "name", "mmwxsg-t0")
	n.sh("ip", "link", "set", "mmwxsg-l1", "netns", l)
	n.sh("ip", "link", "set", "mmwxsg-t0", "netns", d)
	n.sh("ip", "-n", c, "addr", "add", "203.0.113.50/24", "dev", "mmwxsg-c0")
	n.sh("ip", "-n", l, "addr", "add", "203.0.113.1/24", "dev", "mmwxsg-l0")
	n.sh("ip", "-n", l, "addr", "add", "11.77.0.1/24", "dev", "mmwxsg-l1")
	n.sh("ip", "-n", d, "addr", "add", "11.77.0.2/24", "dev", "mmwxsg-t0")
	for _, link := range [][2]string{{c, "mmwxsg-c0"}, {l, "mmwxsg-l0"}, {l, "mmwxsg-l1"}, {d, "mmwxsg-t0"}} {
		n.sh("ip", "-n", link[0], "link", "set", link[1], "up")
	}
	// The whole /16 lives in the target namespace (AnyIP).
	n.sh("ip", "-n", l, "route", "add", "11.77.0.0/16", "via", "11.77.0.2")
	n.sh("ip", "-n", d, "route", "add", "local", "11.77.0.0/16", "dev", "lo")
	n.sh("ip", "-n", d, "route", "add", "203.0.113.0/24", "via", "11.77.0.1")
	targetLog := filepath.Join(base, "reached.log")
	os.WriteFile(filepath.Join(base, "target.py"), []byte(sgTargetServer), 0644)
	target := exec.Command("ip", "netns", "exec", d, "python3", filepath.Join(base, "target.py"), targetLog)
	if err := target.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Process.Kill(); target.Wait() })

	key := func() string {
		var b [16]byte
		rand.Read(b[:])
		return base64.StdEncoding.EncodeToString(b[:])
	}
	a, b := netnsNode("A", 31001, 0), netnsNode("B", 31002, 0)
	a.SS2022 = &flvx.SS2022{Method: "2022-blake3-aes-128-gcm", ServerKey: key(), UserKey: key()}
	b.SS2022 = &flvx.SS2022{Method: "2022-blake3-aes-128-gcm", ServerKey: key(), UserKey: key()}
	nodes := []flvx.Node{a, b}
	m, core := flvxNetnsManager(t, l, bin, base, 39873, nodes)
	m.state.Payload.Artifact = flvx.Artifact{SHA256: mustHash(t, bin), Version: "scan"}
	ctx := context.Background()
	pass := func() time.Duration {
		t.Helper()
		start := time.Now()
		if err := m.reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	setGuard := func(g *flvx.ScanGuard, enabled map[string]bool) {
		nodes := append([]flvx.Node(nil), m.state.Payload.Desired.Nodes...)
		for i := range nodes {
			on := enabled[nodes[i].ID]
			nodes[i].Enabled = &on
		}
		m.state.Payload.Version++
		m.state.Payload.Desired.Revision++
		m.state.Payload.Desired.Nodes = nodes
		m.state.Payload.Desired.ScanGuard = g
	}
	both := map[string]bool{"A": true, "B": true}

	// Client Core: SOCKS 127.0.0.1:23001 → A, :23002 → B.
	ss := func(n flvx.Node) string { return n.SS2022.ServerKey + ":" + n.SS2022.UserKey }
	var inbounds, outbounds, rules []any
	for i, node := range nodes {
		tag := node.ID
		inbounds = append(inbounds, map[string]any{"tag": "in-" + tag, "listen": "127.0.0.1", "port": 23001 + i, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}})
		outbounds = append(outbounds, map[string]any{"tag": "out-" + tag, "protocol": "shadowsocks", "settings": map[string]any{"servers": []any{map[string]any{"address": "203.0.113.1", "port": node.Port, "method": node.SS2022.Method, "password": ss(node)}}}})
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{"in-" + tag}, "outboundTag": "out-" + tag})
	}
	flvxNetnsCore(t, c, bin, filepath.Join(base, "client.json"), "", map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": inbounds, "outbounds": outbounds, "routing": map[string]any{"rules": rules}}, true)
	os.WriteFile(filepath.Join(base, "driver.py"), []byte(sgClientDriver), 0644)
	ctl := filepath.Join(base, "driver.sock")
	driver := exec.Command("ip", "netns", "exec", c, "python3", filepath.Join(base, "driver.py"), ctl)
	driverLog, _ := os.Create(filepath.Join(base, "driver.log"))
	driver.Stdout, driver.Stderr = driverLog, driverLog
	if err := driver.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { driver.Process.Kill(); driver.Wait() })
	var dconn net.Conn
	flvxWait(t, func() bool {
		var err error
		dconn, err = net.Dial("unix", ctl)
		return err == nil
	})
	lab := &sgLab{t: t, base: base, targets: targetLog, conn: dconn, r: bufio.NewReader(dconn)}
	const portA, portB = 23001, 23002

	evidence := map[string]any{"core_sha256": mustHash(t, bin), "started_at": time.Now().UTC().Format(time.RFC3339)}
	check := func(name string, ok bool, detail any) {
		t.Helper()
		evidence[name] = detail
		if !ok {
			t.Errorf("%s: %v", name, detail)
		}
	}
	defer func() {
		if out := os.Getenv("MMWXC_SG_E2E_OUT"); out != "" {
			raw, _ := json.MarshalIndent(evidence, "", "  ")
			os.WriteFile(out, raw, 0644)
		}
	}()

	// 0. Guard off (FLVX sends none): a 120-host scan reaches every host and
	// /v1/scan is never read.
	pass()
	check("off_features", m.scanCapable(), m.state.CoreFeatures)
	check("off_scan_120_reached", lab.scan(portA, horizontalTargets(9, 120, 22)) == 120, "120 hosts")
	pass()
	check("off_no_reads", m.scan.reads == 0, m.scan.reads)
	check("off_state", m.state.Report.ScanGuard.State == "off" && m.state.Report.ScanGuard.CoreReady, m.state.Report.ScanGuard)

	// 1. Guard on (FLVX's defaults): normal use stays fine.
	guard := &flvx.ScanGuard{Enabled: true, Identities: []string{"flvxl-A", "flvxl-B"}, Horizontal: 100, HorizontalWeb: 500, WebPorts: []int{80, 443}, WindowS: 60, Vertical: 30, Total: 1000, TotalWindowS: 300}
	setGuard(guard, both)
	pass()
	check("on_state", m.state.Report.ScanGuard.State == "on", m.state.Report.ScanGuard)
	check("on_probe_A", lab.probe(portA, false), true)
	check("on_probe_B_held", lab.probe(portB, true), true)

	// 2. Horizontal scan through A: the 100th host is refused and A is
	// blocked by the Core at once; the next pass reports the hit and holds A.
	reached := lab.scan(portA, horizontalTargets(1, 120, 22))
	check("horizontal_reached", reached == 99, reached)
	took := pass()
	st := m.state.Report.ScanGuard
	evidence["pass_after_hit_ms"] = took.Milliseconds()
	check("horizontal_reported", len(st.Hits) == 1 && st.Hits[0].ID == "A" && st.Hits[0].Type == "horizontal" && st.Hits[0].Count == 100 && st.Hits[0].Port == 22 && strings.Join(st.Blocked, ",") == "flvxl-A", st)
	hitA := st.Hits[0].Seq
	check("horizontal_A_refused", !lab.probe(portA, false), "A refused")
	check("horizontal_B_works", lab.probe(portB, false), "B ok")
	check("horizontal_B_held_conn_alive", strings.Trim(fmt.Sprint(lab.alive()), "[]") == "true", lab.alive())
	pass()
	pass()
	check("repush_keeps_block", !lab.probe(portA, false), "A still refused after re-pushes")

	// 3. FLVX records the hit and disables A in the same step.
	recorded := *guard
	recorded.Seen = hitA
	setGuard(&recorded, map[string]bool{"A": false, "B": true})
	pass()
	check("recorded_hits_cleared", len(m.state.Report.ScanGuard.Hits) == 0, m.state.Report.ScanGuard)
	check("recorded_A_off", !lab.probe(portA, false), "A off")
	pass()
	check("recorded_core_block_kept", strings.Join(m.state.Report.ScanGuard.Blocked, ",") == "flvxl-A", m.state.Report.ScanGuard.Blocked)

	// 4. 解除禁用: the matching scan_unblock lifts the Core block; A serves.
	setGuard(&recorded, both)
	pass()
	pass()
	check("unblock_core_clear", len(m.state.Report.ScanGuard.Blocked) == 0, m.state.Report.ScanGuard.Blocked)
	check("unblock_A_works", lab.probe(portA, false), "A ok")

	// 5. Vertical scan through B (30th port refused), then a Core restart
	// before FLVX recorded it: the Helper's hold keeps B blocked.
	vertical := [][2]any{}
	for p := 1001; p <= 1040; p++ {
		vertical = append(vertical, [2]any{"11.77.5.5", p})
	}
	reached = lab.scan(portB, vertical)
	check("vertical_reached", reached == 29, reached)
	pass()
	st = m.state.Report.ScanGuard
	check("vertical_reported", len(st.Hits) == 1 && st.Hits[0].ID == "B" && st.Hits[0].Type == "vertical" && st.Hits[0].Host == "11.77.5.5" && st.Hits[0].Count == 30, st)
	// The landing Core drops B's open client socket at once; the client's
	// own Core passes the close on to its SOCKS socket a little later.
	landingClosed := time.Duration(-1)
	for start := time.Now(); time.Since(start) < 10*time.Second; time.Sleep(50 * time.Millisecond) {
		snap, _ := m.core.snapshot(ctx)
		open := int64(0)
		for _, u := range snap.Users {
			if u.Identity.InboundTag == "flvxl-B" {
				open += u.InboundCurrent
			}
		}
		if open == 0 {
			landingClosed = time.Since(start)
			break
		}
	}
	check("vertical_landing_B_sockets_gone_after", landingClosed >= 0, landingClosed.String())
	closedAfter := time.Duration(-1)
	for start := time.Now(); time.Since(start) < 10*time.Second; time.Sleep(100 * time.Millisecond) {
		if strings.Trim(fmt.Sprint(lab.alive()), "[]") == "false" {
			closedAfter = time.Since(start)
			break
		}
	}
	check("vertical_held_conn_closed_at_client", closedAfter >= 0, closedAfter.String())
	core.Process.Kill()
	core.Wait()
	core = flvxNetnsCore(t, l, bin, filepath.Join(base, "core2.json"), filepath.Join(base, "control.sock"), flvx.BaseConfig(39873), true)
	flvxWait(t, func() bool { _, err := m.core.snapshot(ctx); return err == nil })
	pass()
	check("restart_new_epoch_no_core_block", len(m.state.Report.ScanGuard.Blocked) == 0, m.state.Report.ScanGuard.Blocked)
	check("restart_B_still_refused", !lab.probe(portB, false), "B held by the Helper")
	check("restart_A_works", lab.probe(portA, false), "A ok")
	check("restart_hit_still_pending", len(m.state.Report.ScanGuard.Hits) == 1, m.state.Report.ScanGuard.Hits)
	// FLVX records it and lifts it later: B serves again.
	recorded.Seen = m.state.Report.ScanGuard.Hits[0].Seq
	setGuard(&recorded, map[string]bool{"A": true, "B": false})
	pass()
	setGuard(&recorded, both)
	pass()
	check("restart_B_works_after_unblock", lab.probe(portB, false), "B ok")

	// 6. After 解除禁用 the windows start empty: a new scan through A is
	// caught again at its 100th host.
	reached = lab.scan(portA, horizontalTargets(20, 150, 22))
	check("second_scan_caught", reached == 99, reached)
	pass()
	evidence["final_status"] = m.state.Report.ScanGuard
	evidence["scan_reads"] = m.scan.reads
}
