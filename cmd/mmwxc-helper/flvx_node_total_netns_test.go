package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// 节点总连接数 (landing-node-total-v1) with the real FLVX Core driven by the
// Helper's own reconcile, in three private namespaces (the host network is
// never touched):
//
//	client 203.0.113.50 ── 203.0.113.1 landing 12.0.3.1 ── 12.0.3.2 target
//
// Every accepted client costs the landing 2 sockets (inbound + outbound):
//   - P: 总 8 the old way (max_connections, no conn_mode): 4 of 6 fit; after
//     they close, the Core's outbound sockets linger in TIME_WAIT, still count
//     (report tcp.time_wait) and keep new clients out;
//   - W: target half-closes, the CLOSE_WAIT sockets are counted too;
//   - T: 入站 3 + 总 5 → the total is hit first (2 clients);
//   - I: 入站 2 + 总 100 → the inbound limit is hit first;
//   - M: 入站+出站 10 + 总 4 (no conn_mode) → the smaller one (2 clients).
//
// Every sample is the Helper's own report (inbound + outbound == connections).
// Opt-in: root, MMWXC_TEST_FLVX_CORE_NODE_TOTAL (the FLVX Core);
// MMWXC_NT_E2E_OUT receives the evidence JSON, MMWXC_NT_E2E_DIR the lab dir.
func TestFLVXNodeTotalNetnsE2E(t *testing.T) {
	bin := os.Getenv("MMWXC_TEST_FLVX_CORE_NODE_TOTAL")
	if bin == "" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_TEST_FLVX_CORE_NODE_TOTAL and run as root")
	}
	for _, tool := range []string{"python3", "ip"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " missing")
		}
	}
	raiseNoFile()
	base := os.Getenv("MMWXC_NT_E2E_DIR")
	if base == "" {
		base = t.TempDir()
	}
	os.MkdirAll(base, 0755)
	n := flvxNetns{t, base}
	c, l, d := "mmwxnt-c", "mmwxnt-l", "mmwxnt-t"
	flvxNetnsCleanup(c, l, d)
	t.Cleanup(func() { flvxNetnsCleanup(c, l, d) })
	for _, ns := range []string{c, l, d} {
		n.sh("ip", "netns", "add", ns)
		n.sh("ip", "-n", ns, "link", "set", "lo", "up")
	}
	n.sh("ip", "link", "add", "mmwxnt-c0", "type", "veth", "peer", "name", "mmwxnt-l0")
	n.sh("ip", "link", "set", "mmwxnt-c0", "netns", c)
	n.sh("ip", "link", "set", "mmwxnt-l0", "netns", l)
	n.sh("ip", "link", "add", "mmwxnt-l1", "type", "veth", "peer", "name", "mmwxnt-t0")
	n.sh("ip", "link", "set", "mmwxnt-l1", "netns", l)
	n.sh("ip", "link", "set", "mmwxnt-t0", "netns", d)
	n.sh("ip", "-n", c, "addr", "add", "203.0.113.50/24", "dev", "mmwxnt-c0")
	n.sh("ip", "-n", l, "addr", "add", "203.0.113.1/24", "dev", "mmwxnt-l0")
	n.sh("ip", "-n", l, "addr", "add", "12.0.3.1/24", "dev", "mmwxnt-l1")
	n.sh("ip", "-n", d, "addr", "add", "12.0.3.2/24", "dev", "mmwxnt-t0")
	for _, link := range [][2]string{{c, "mmwxnt-c0"}, {l, "mmwxnt-l0"}, {l, "mmwxnt-l1"}, {d, "mmwxnt-t0"}} {
		n.sh("ip", "-n", link[0], "link", "set", link[1], "up")
	}

	// Target: 9100 greets and holds until the peer closes; 9101 greets and
	// half-closes at once (the landing's outbound socket enters CLOSE_WAIT).
	target := `import socket, threading
def hold(c, half):
    try:
        c.sendall(b"ok\n")
        if half:
            c.shutdown(socket.SHUT_WR)
        c.settimeout(300)
        while c.recv(64):
            pass
    except Exception:
        pass
    c.close()
def serve(port, half):
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("0.0.0.0", port)); s.listen(256)
    while True:
        c, _ = s.accept()
        threading.Thread(target=hold, args=(c, half), daemon=True).start()
threading.Thread(target=serve, args=(9101, True), daemon=True).start()
serve(9100, False)
`
	os.WriteFile(filepath.Join(base, "target.py"), []byte(target), 0644)
	srv := exec.Command("ip", "netns", "exec", d, "python3", filepath.Join(base, "target.py"))
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Process.Kill(); srv.Wait() })

	key := func() string {
		var b [16]byte
		rand.Read(b[:])
		return base64.StdEncoding.EncodeToString(b[:])
	}
	node := func(id string, port int, mode string, conns, total int64) flvx.Node {
		x := netnsNode(id, port, conns)
		x.ConnMode, x.MaxTotalConnections = mode, total
		x.SS2022 = &flvx.SS2022{Method: "2022-blake3-aes-128-gcm", ServerKey: key(), UserKey: key()}
		return x
	}
	nodes := []flvx.Node{
		node("P", 31201, "", 8, 0),
		node("W", 31202, "", 100, 0),
		node("T", 31203, flvx.ConnModeInbound, 3, 5),
		node("I", 31204, flvx.ConnModeInbound, 2, 100),
		node("M", 31205, "", 10, 4),
	}
	m, _ := flvxNetnsManager(t, l, bin, base, 39874, nodes)
	ctx := context.Background()
	pass := func() {
		t.Helper()
		if err := m.reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	pass()
	pass()

	// Client Core: dokodemo 127.0.0.1:2200x → node x → target.
	targets := map[string]int{"W": 9101}
	var inbounds, outbounds, rules []any
	for i, x := range nodes {
		port := 9100
		if p := targets[x.ID]; p != 0 {
			port = p
		}
		inbounds = append(inbounds, map[string]any{"tag": "in-" + x.ID, "listen": "127.0.0.1", "port": 22001 + i, "protocol": "dokodemo-door", "settings": map[string]any{"address": "12.0.3.2", "port": port, "network": "tcp"}})
		outbounds = append(outbounds, map[string]any{"tag": "out-" + x.ID, "protocol": "shadowsocks", "settings": map[string]any{"servers": []any{map[string]any{"address": "203.0.113.1", "port": x.Port, "method": x.SS2022.Method, "password": x.SS2022.ServerKey + ":" + x.SS2022.UserKey}}}})
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{"in-" + x.ID}, "outboundTag": "out-" + x.ID})
	}
	flvxNetnsCore(t, c, bin, filepath.Join(base, "client.json"), "", map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": inbounds, "outbounds": outbounds, "routing": map[string]any{"rules": rules}}, true)
	time.Sleep(2 * time.Second)

	holdPy := `import json, socket, sys, time
port, n = int(sys.argv[1]), int(sys.argv[2])
held, results = [], []
for i in range(n):
    s = socket.create_connection(("127.0.0.1", port), timeout=5)
    s.settimeout(4)
    try:
        ok = s.recv(3) == b"ok\n"
    except Exception:
        ok = False
    results.append(ok)
    if ok:
        held.append(s)
    else:
        s.close()
    time.sleep(0.3)
print(json.dumps({"ok": len(held), "results": results}), flush=True)
sys.stdin.read()
for s in held:
    s.close()
`
	os.WriteFile(filepath.Join(base, "hold.py"), []byte(holdPy), 0644)
	type sample struct {
		Connections int64         `json:"connections"`
		Inbound     int64         `json:"inbound_connections"`
		TCP         *flvx.NodeTCP `json:"tcp"`
	}
	report := func(id string) sample {
		t.Helper()
		pass()
		for _, s := range m.state.Report.Nodes {
			if s.ID == id {
				out := sample{Connections: s.Connections, TCP: s.TCP}
				if s.InboundConnections != nil {
					out.Inbound = *s.InboundConnections
				}
				return out
			}
		}
		t.Fatalf("node %s not reported", id)
		return sample{}
	}
	type phase struct {
		Name     string `json:"name"`
		Opened   int    `json:"opened"`
		Accepted int    `json:"accepted"`
		Results  []bool `json:"results"`
		Held     sample `json:"while_held"`
	}
	ports := map[string]int{}
	for i, x := range nodes {
		ports[x.ID] = 22001 + i
	}
	run := func(name, id string, count int) (phase, func()) {
		t.Helper()
		cmd := exec.Command("ip", "netns", "exec", c, "python3", filepath.Join(base, "hold.py"), strconv.Itoa(ports[id]), strconv.Itoa(count))
		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var out struct {
			OK      int    `json:"ok"`
			Results []bool `json:"results"`
		}
		if err := json.Unmarshal([]byte(line), &out); err != nil {
			t.Fatalf("%s: %v %q", name, err, line)
		}
		time.Sleep(300 * time.Millisecond)
		p := phase{Name: name, Opened: count, Accepted: out.OK, Results: out.Results, Held: report(id)}
		t.Logf("%s: accepted %d/%d %v, while held: connections=%d inbound=%d tcp=%+v", name, p.Accepted, count, p.Results, p.Held.Connections, p.Held.Inbound, p.Held.TCP)
		return p, func() { stdin.Close(); cmd.Wait() }
	}

	evidence := map[string]any{"core_sha256": mustHash(t, bin), "started_at": time.Now().UTC().Format(time.RFC3339)}
	defer func() {
		if out := os.Getenv("MMWXC_NT_E2E_OUT"); out != "" {
			raw, _ := json.MarshalIndent(evidence, "", "  ")
			os.WriteFile(out, raw, 0644)
		}
	}()
	check := func(name string, ok bool, detail any) {
		t.Helper()
		evidence[name] = detail
		if !ok {
			t.Errorf("%s: %v", name, detail)
		}
	}
	consistent := func(s sample) bool { return s.TCP != nil && s.TCP.Inbound+s.TCP.Outbound == s.Connections }

	a, closeA := run("总 8 (old field), open 6", "P", 6)
	closeA()
	// After the clients close, the landing closes its outbound sockets
	// first → TIME_WAIT (60 s on Linux), still counted.
	var tw sample
	for i := 0; i < 20; i++ {
		tw = report("P")
		if tw.TCP != nil && tw.TCP.TimeWait > 0 && tw.Inbound == 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("after close: %+v tcp=%+v", tw, tw.TCP)
	b, closeB := run("总 8 while TIME_WAIT lingers, open 4", "P", 4)
	closeB()
	cw, closeW := run("half-closing target, open 3", "W", 3)
	closeW()
	tt, closeT := run("入站 3 + 总 5, open 4", "T", 4)
	closeT()
	ii, closeI := run("入站 2 + 总 100, open 4", "I", 4)
	closeI()
	mm, closeM := run("入站+出站 10 + 总 4, open 4", "M", 4)
	closeM()
	// The port limits the Core holds (per identity once it has traffic).
	snap, err := m.core.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	limits := map[string][2]any{}
	for _, u := range snap.Users {
		limits[u.Identity.InboundTag] = [2]any{flvxNTDeref(u.MaxPortInboundConnections), flvxNTDeref(u.MaxPortTotalConnections)}
	}
	check("core_port_limits_inbound_total", limits["flvxl-P"] == [2]any{nil, int64(8)} && limits["flvxl-T"] == [2]any{int64(3), int64(5)} && limits["flvxl-I"] == [2]any{int64(2), int64(100)} && limits["flvxl-M"] == [2]any{nil, int64(4)}, limits)
	evidence["phases"] = []phase{a, b, cw, tt, ii, mm}
	evidence["after_close"] = tw

	check("total8_four_fit", a.Accepted == 4 && a.Held.Connections == 8 && consistent(a.Held) && a.Held.TCP.Inbound == 4 && a.Held.TCP.Outbound == 4, a)
	check("time_wait_counted", tw.TCP != nil && tw.TCP.TimeWait > 0 && tw.Inbound == 0 && consistent(tw) && tw.Connections >= tw.TCP.TimeWait, tw)
	check("time_wait_keeps_clients_out", b.Accepted <= (8-int(tw.Connections)+1)/2 && b.Accepted < 4, map[string]any{"accepted": b.Accepted, "lingering": tw.Connections})
	check("close_wait_counted", cw.Accepted == 3 && consistent(cw.Held) && cw.Held.TCP.CloseWait > 0, cw)
	// A refused client's socket may linger in TIME_WAIT too: never above 5.
	check("inbound3_total5_total_wins", tt.Accepted == 2 && tt.Held.Inbound == 2 && tt.Held.Connections <= 5 && consistent(tt.Held), tt)
	check("inbound2_total100_inbound_wins", ii.Accepted == 2 && ii.Held.Inbound == 2 && consistent(ii.Held), ii)
	check("total10_total4_min", mm.Accepted == 2 && mm.Held.Connections == 4 && consistent(mm.Held), mm)
}

func flvxNTDeref(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
