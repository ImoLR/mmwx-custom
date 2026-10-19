package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

type flvxNetns struct {
	t    *testing.T
	base string
}

func (n flvxNetns) sh(args ...string) {
	n.t.Helper()
	if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		n.t.Fatalf("%v: %v %s", args, err, out)
	}
}

func flvxNetnsCleanup(names ...string) {
	for _, ns := range names {
		if out, err := exec.Command("ip", "netns", "pids", ns).Output(); err == nil {
			for _, pid := range strings.Fields(string(out)) {
				exec.Command("kill", "-9", pid).Run()
			}
		}
		exec.Command("ip", "netns", "del", ns).Run()
		os.RemoveAll("/etc/netns/" + ns)
	}
}

// flvxNetnsCore starts a Core inside a namespace; the control socket is a
// filesystem path, so the Helper manager reaches it from the test process.
func flvxNetnsCore(t *testing.T, ns, bin, path, socket string, config any, ulimit bool) *exec.Cmd {
	t.Helper()
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	script := "exec " + bin + " run -config " + path
	if ulimit {
		script = "ulimit -n 65535; " + script
	}
	cmd := exec.Command("ip", "netns", "exec", ns, "sh", "-c", script)
	cmd.Env = append(os.Environ(), "MMWXC_CORE_CONTROL_SOCKET="+socket)
	logFile, _ := os.Create(path + ".log")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait(); logFile.Close() })
	return cmd
}

// flvxNetnsManager drives a real Core in namespace ns through the Helper's
// own reconcile: every Core CLI call runs inside the namespace.
func flvxNetnsManager(t *testing.T, ns, bin, dir string, apiPort int, nodes []flvx.Node) (*flvxManager, *exec.Cmd) {
	t.Helper()
	wrapper := filepath.Join(dir, "core-in-netns")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec ip netns exec "+ns+" "+bin+" \"$@\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "control.sock")
	core := flvxNetnsCore(t, ns, bin, filepath.Join(dir, "core.json"), socket, flvx.BaseConfig(apiPort), true)
	client := newCoreClient(socket)
	ctx := context.Background()
	flvxWait(t, func() bool { _, err := client.snapshot(ctx); return err == nil })
	m := &flvxManager{dir: filepath.Join(dir, "state"), binary: wrapper, core: client, logs: newPeriodicLogLimiter()}
	m.state = flvxLocalState{Payload: flvx.Payload{Version: 1, Status: "active", Config: flvx.Config{PortMin: 1024, PortMax: 65535, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}, Desired: flvx.Desired{Revision: 1, Nodes: nodes}}, APIPort: apiPort, Counters: map[string]*flvx.Counter{}, Applied: map[string]string{}, Report: flvx.Report{Generation: "netns"}}
	m.apiCall = m.api
	m.setup = func(context.Context) error { return nil }
	m.stop = func(context.Context) error { return nil }
	m.readOfficial = func() ([]int, error) { return nil, nil }
	m.restartCore = func(context.Context) error { t.Fatal("unexpected Core restart"); return nil }
	m.memory = func() (int64, int64, bool) { return 40 << 20, 1 << 30, true }
	return m, core
}

func netnsNode(id string, port int, maxConnections int64) flvx.Node {
	n := helperFLVXNode(id, port)
	n.MaxConnections, n.MaxOnlineIPs = maxConnections, 0
	return n
}

// TestFLVXEgressNetnsDataPlane proves the guarantees with real traffic through
// the pinned Core driven by the Helper, in three network namespaces
// (client -> landing -> target):
//   - 强制 IPv4: IPv4 literals and A records work; IPv6 literals, AAAA-only
//     names and IPv6 datagrams inside an established UDP session never leave.
//   - 强制 IPv6: the mirror image.
//   - 禁止大陆直连: a mainland source is blocked, an exempt FLVX-node source
//     passes and keeps following later rules, a non-mainland source passes.
//   - a SOCKS upstream (hostname pinned by the Helper) carries traffic, and the
//     node connection cap and share traffic accounting still apply to it.
//
// Opt-in: root, MMWXC_TEST_FLVX_NETNS=1, MMWXC_TEST_FLVX_CORE, MMWXC_TEST_GEO_DIR.
func TestFLVXEgressNetnsDataPlane(t *testing.T) {
	if os.Getenv("MMWXC_TEST_FLVX_NETNS") != "1" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_TEST_FLVX_NETNS=1 and run as root")
	}
	bin := realEgressCore(t)
	base := t.TempDir()
	if !realGeo(t, filepath.Join(base, "state")) {
		t.Skip("MMWXC_TEST_GEO_DIR not supplied")
	}
	n := flvxNetns{t, base}
	names := []string{"mmwxeg-c", "mmwxeg-l", "mmwxeg-t"}
	flvxNetnsCleanup(names...)
	t.Cleanup(func() { flvxNetnsCleanup(names...) })
	for _, ns := range names {
		n.sh("ip", "netns", "add", ns)
		n.sh("ip", "-n", ns, "link", "set", "lo", "up")
	}
	n.sh("ip", "link", "add", "mmwxeg-c0", "type", "veth", "peer", "name", "mmwxeg-l0")
	n.sh("ip", "link", "set", "mmwxeg-c0", "netns", "mmwxeg-c")
	n.sh("ip", "link", "set", "mmwxeg-l0", "netns", "mmwxeg-l")
	n.sh("ip", "link", "add", "mmwxeg-l1", "type", "veth", "peer", "name", "mmwxeg-t0")
	n.sh("ip", "link", "set", "mmwxeg-l1", "netns", "mmwxeg-l")
	n.sh("ip", "link", "set", "mmwxeg-t0", "netns", "mmwxeg-t")
	// 1.0.1.0/24 is in geoip:cn; 1.0.1.3 plays an FLVX node address (exempt).
	for _, a := range []string{"1.0.1.2/24", "1.0.1.3/24", "203.0.113.50/24"} {
		n.sh("ip", "-n", "mmwxeg-c", "addr", "add", a, "dev", "mmwxeg-c0")
	}
	for _, a := range []string{"1.0.1.1/24", "203.0.113.1/24"} {
		n.sh("ip", "-n", "mmwxeg-l", "addr", "add", a, "dev", "mmwxeg-l0")
	}
	n.sh("ip", "-n", "mmwxeg-l", "addr", "add", "198.51.100.1/24", "dev", "mmwxeg-l1")
	n.sh("ip", "-n", "mmwxeg-l", "addr", "add", "2001:db8:2::1/64", "dev", "mmwxeg-l1", "nodad")
	n.sh("ip", "-n", "mmwxeg-t", "addr", "add", "198.51.100.2/24", "dev", "mmwxeg-t0")
	n.sh("ip", "-n", "mmwxeg-t", "addr", "add", "2001:db8:2::2/64", "dev", "mmwxeg-t0", "nodad")
	for _, l := range [][2]string{{"mmwxeg-c", "mmwxeg-c0"}, {"mmwxeg-l", "mmwxeg-l0"}, {"mmwxeg-l", "mmwxeg-l1"}, {"mmwxeg-t", "mmwxeg-t0"}} {
		n.sh("ip", "-n", l[0], "link", "set", l[1], "up")
	}
	os.MkdirAll("/etc/netns/mmwxeg-l", 0755)
	hosts := "127.0.0.1 localhost\n2001:db8:2::2 v6only.flvx.test\n198.51.100.2 v4only.flvx.test\n198.51.100.2 both.flvx.test\n2001:db8:2::2 both.flvx.test\n"
	if err := os.WriteFile("/etc/netns/mmwxeg-l/hosts", []byte(hosts), 0644); err != nil {
		t.Fatal(err)
	}

	// Target: dual-stack TCP (8080, 8081) and UDP (9000) servers logging peers,
	// plus a SOCKS5 upstream (a second Core) at 198.51.100.2:1080.
	targetLog := filepath.Join(base, "target.log")
	server := `import socket, sys, threading
log = open(sys.argv[1], "a", buffering=1)
def tcp(port):
    s = socket.socket(socket.AF_INET6, socket.SOCK_STREAM)
    s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("::", port)); s.listen(512)
    while True:
        c, a = s.accept()
        peer = a[0][7:] if a[0].startswith("::ffff:") else a[0]
        log.write("tcp %d %s\n" % (port, peer))
        def serve(c=c, peer=peer):
            try:
                c.sendall(("peer=%s\n" % peer).encode())
                c.settimeout(30)
                while c.recv(64):
                    pass
            except Exception:
                pass
            c.close()
        threading.Thread(target=serve, daemon=True).start()
def udp():
    s = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
    s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
    s.bind(("::", 9000))
    while True:
        d, a = s.recvfrom(2048)
        peer = a[0][7:] if a[0].startswith("::ffff:") else a[0]
        log.write("udp %s %s\n" % (peer, d.decode(errors="replace")))
        s.sendto(b"echo:" + d, a)
for p in (8080, 8081):
    threading.Thread(target=tcp, args=(p,), daemon=True).start()
udp()
`
	os.WriteFile(filepath.Join(base, "server.py"), []byte(server), 0644)
	srv := exec.Command("ip", "netns", "exec", "mmwxeg-t", "python3", filepath.Join(base, "server.py"), targetLog)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Process.Kill(); srv.Wait() })
	flvxNetnsCore(t, "mmwxeg-t", bin, filepath.Join(base, "socks.json"), "", map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": []any{map[string]any{"listen": "198.51.100.2", "port": 1080, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true}}}, "outbounds": []any{map[string]any{"protocol": "freedom"}}}, false)

	nodes := []flvx.Node{netnsNode("1", 31001, 0), netnsNode("2", 31002, 0), netnsNode("3", 31003, 0), netnsNode("4", 31004, 0), netnsNode("5", 31005, 2), netnsNode("6", 31006, 2), netnsNode("7", 31007, 0)}
	m, _ := flvxNetnsManager(t, "mmwxeg-l", bin, base, 39871, nodes)
	m.detectLocal = func() flvx.LocalAddrs {
		return flvx.LocalAddrs{V4: netip.MustParseAddr("198.51.100.1"), V6: netip.MustParseAddr("2001:db8:2::1")}
	}
	m.lookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host == "socks.flvx.test" {
			return []netip.Addr{netip.MustParseAddr("198.51.100.2")}, nil
		}
		return nil, fmt.Errorf("unknown %s", host)
	}
	m.state.Payload.Desired.Egress = &flvx.Egress{Version: 1, Outbounds: []flvx.EgressOutbound{
		{Tag: "flvxo-f4", Type: "direct", IPVersion: "force_ipv4"},
		{Tag: "flvxo-f6", Type: "direct", IPVersion: "force_ipv6"},
		{Tag: "flvxo-socks", Type: "socks", IPVersion: "prefer_ipv4", Server: "socks.flvx.test", Port: 1080},
	}, Rules: []flvx.EgressRule{
		{InboundTags: []string{"flvxl-1"}, Outbound: "flvxo-f4"},
		{InboundTags: []string{"flvxl-2"}, Outbound: "flvxo-f6"},
		// 「屏蔽中国大陆」 c) 禁止大陆直连, then an ordinary later rule.
		{InboundTags: []string{"flvxl-3"}, SourceIP: []string{"geoip:cn"}, Outbound: "block"},
		{InboundTags: []string{"flvxl-3"}, Port: "8081", Outbound: "block"},
		{InboundTags: []string{"flvxl-5", "flvxl-7"}, Outbound: "flvxo-socks"},
	}, ExemptSources: []string{"1.0.1.3"}}
	ctx := context.Background()
	if err := m.reconcile(ctx); err != nil || m.state.Report.Egress.State != "applied" {
		t.Fatalf("apply: %v %+v", err, m.state.Report.Egress)
	}

	// Client Core in mmwxeg-c: each dokodemo inbound is pinned to one target,
	// landing node and client source address.
	ss := func(n flvx.Node) string { return n.SS2022.ServerKey + ":" + n.SS2022.UserKey }
	type route struct{ name, target, node, server, source string }
	routes := []route{
		{"f4-v4lit", "198.51.100.2", "1", "203.0.113.1", "203.0.113.50"},
		{"f4-v6lit", "2001:db8:2::2", "1", "203.0.113.1", "203.0.113.50"},
		{"f4-v6only", "v6only.flvx.test", "1", "203.0.113.1", "203.0.113.50"},
		{"f4-both", "both.flvx.test", "1", "203.0.113.1", "203.0.113.50"},
		{"f6-v6lit", "2001:db8:2::2", "2", "203.0.113.1", "203.0.113.50"},
		{"f6-v4lit", "198.51.100.2", "2", "203.0.113.1", "203.0.113.50"},
		{"f6-v4only", "v4only.flvx.test", "2", "203.0.113.1", "203.0.113.50"},
		{"f6-both", "both.flvx.test", "2", "203.0.113.1", "203.0.113.50"},
		{"sys-v6lit", "2001:db8:2::2", "4", "203.0.113.1", "203.0.113.50"},
		{"sys-v4lit", "198.51.100.2", "4", "203.0.113.1", "203.0.113.50"},
		{"cn-blocked", "198.51.100.2", "3", "1.0.1.1", "1.0.1.2"},
		{"cn-exempt", "198.51.100.2", "3", "1.0.1.1", "1.0.1.3"},
		{"cn-exempt-8081", "198.51.100.2:8081", "3", "1.0.1.1", "1.0.1.3"},
		{"noncn", "198.51.100.2", "3", "203.0.113.1", "203.0.113.50"},
		{"socks", "2001:db8:2::2", "7", "203.0.113.1", "203.0.113.50"},
	}
	var inbounds, outbounds, rules []any
	for _, r := range routes {
		host, port := r.target, 8080
		if h, p, err := net.SplitHostPort(r.target); err == nil {
			host = h
			port, _ = strconv.Atoi(p)
		}
		idx, _ := strconv.Atoi(r.node)
		node := nodes[idx-1]
		inbounds = append(inbounds, map[string]any{"tag": "in-" + r.name, "listen": "127.0.0.1", "port": 20001 + len(inbounds), "protocol": "dokodemo-door", "settings": map[string]any{"address": host, "port": port, "network": "tcp"}})
		outbounds = append(outbounds, map[string]any{"tag": "out-" + r.name, "protocol": "shadowsocks", "sendThrough": r.source, "settings": map[string]any{"servers": []any{map[string]any{"address": r.server, "port": node.Port, "method": node.SS2022.Method, "password": ss(node)}}}})
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{"in-" + r.name}, "outboundTag": "out-" + r.name})
	}
	// Connection caps: node 5 (SOCKS upstream) and node 6 (direct control).
	for i, node := range []flvx.Node{nodes[4], nodes[5]} {
		tag := "cap-" + node.ID
		inbounds = append(inbounds, map[string]any{"tag": "in-" + tag, "listen": "127.0.0.1", "port": 21001 + i, "protocol": "dokodemo-door", "settings": map[string]any{"address": "198.51.100.2", "port": 8080, "network": "tcp"}})
		outbounds = append(outbounds, map[string]any{"tag": "out-" + tag, "protocol": "shadowsocks", "sendThrough": "203.0.113.50", "settings": map[string]any{"servers": []any{map[string]any{"address": "203.0.113.1", "port": node.Port, "method": node.SS2022.Method, "password": ss(node)}}}})
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{"in-" + tag}, "outboundTag": "out-" + tag})
	}
	for i, id := range []string{"1", "2", "4"} {
		idx, _ := strconv.Atoi(id)
		node := nodes[idx-1]
		tag := "udp-" + id
		inbounds = append(inbounds, map[string]any{"tag": "in-" + tag, "listen": "127.0.0.1", "port": 1081 + i, "protocol": "socks", "settings": map[string]any{"udp": true, "auth": "noauth"}})
		outbounds = append(outbounds, map[string]any{"tag": "out-" + tag, "protocol": "shadowsocks", "sendThrough": "203.0.113.50", "settings": map[string]any{"servers": []any{map[string]any{"address": "203.0.113.1", "port": node.Port, "method": node.SS2022.Method, "password": ss(node)}}}})
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{"in-" + tag}, "outboundTag": "out-" + tag})
	}
	flvxNetnsCore(t, "mmwxeg-c", bin, filepath.Join(base, "client.json"), "", map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": inbounds, "outbounds": outbounds, "routing": map[string]any{"rules": rules}}, false)
	fetcher := `import json, socket, sys, time
out = {}
for item in sys.argv[1:]:
    name, port = item.split("=")
    for attempt in range(60):
        try:
            c = socket.create_connection(("127.0.0.1", int(port)), timeout=5)
            break
        except OSError:
            time.sleep(0.05)
    else:
        out[name] = "client-unreachable"; continue
    data = b""
    try:
        c.settimeout(5)
        while not data.endswith(b"\n"):
            chunk = c.recv(256)
            if not chunk:
                break
            data += chunk
    except Exception:
        pass
    c.close()
    out[name] = data.decode().strip()
print(json.dumps(out))
`
	os.WriteFile(filepath.Join(base, "fetch.py"), []byte(fetcher), 0644)
	args := []string{"netns", "exec", "mmwxeg-c", "python3", filepath.Join(base, "fetch.py")}
	for i, r := range routes {
		args = append(args, fmt.Sprintf("%s=%d", r.name, 20001+i))
	}
	out, err := exec.Command("ip", args...).CombinedOutput()
	fetched := map[string]string{}
	if err != nil || json.Unmarshal(out, &fetched) != nil {
		t.Fatalf("tcp client: %v %s", err, out)
	}
	expect := map[string]string{
		"f4-v4lit": "peer=198.51.100.1", "f4-v6lit": "", "f4-v6only": "", "f4-both": "peer=198.51.100.1",
		"f6-v6lit": "peer=2001:db8:2::1", "f6-v4lit": "", "f6-v4only": "", "f6-both": "peer=2001:db8:2::1",
		"sys-v6lit": "peer=2001:db8:2::1", "sys-v4lit": "peer=198.51.100.1",
		"cn-blocked": "", "cn-exempt": "peer=198.51.100.1", "cn-exempt-8081": "", "noncn": "peer=198.51.100.1",
		// Through the SOCKS upstream the target sees the proxy, not the landing.
		"socks": "peer=2001:db8:2::2",
	}
	for _, r := range routes {
		if fetched[r.name] != expect[r.name] {
			t.Errorf("%s: got %q want %q", r.name, fetched[r.name], expect[r.name])
		}
	}

	// UDP: one SOCKS5 association, an allowed datagram, then one of the
	// forbidden family inside the same session.
	udpClient := `import socket, struct, sys
port = int(sys.argv[1]); order = sys.argv[2:]
t = socket.create_connection(("127.0.0.1", port), timeout=3)
t.sendall(b"\x05\x01\x00"); t.recv(2)
t.sendall(b"\x05\x03\x00\x01\x00\x00\x00\x00\x00\x00"); r = t.recv(10)
relay = (socket.inet_ntoa(r[4:8]), struct.unpack(">H", r[8:10])[0])
u = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); u.settimeout(1.5)
out = []
for target in order:
    if ":" in target:
        hdr = b"\x00\x00\x00\x04" + socket.inet_pton(socket.AF_INET6, target)
    else:
        hdr = b"\x00\x00\x00\x01" + socket.inet_aton(target)
    u.sendto(hdr + struct.pack(">H", 9000) + ("p%d" % port).encode(), relay)
    try:
        u.recvfrom(2048); out.append("ok")
    except Exception:
        out.append("none")
print(",".join(out))
`
	os.WriteFile(filepath.Join(base, "udp.py"), []byte(udpClient), 0644)
	udp := func(port int, order ...string) string {
		out, err := exec.Command("ip", append([]string{"netns", "exec", "mmwxeg-c", "python3", filepath.Join(base, "udp.py"), strconv.Itoa(port)}, order...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("udp client: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	udpResults := map[string]string{
		"force_ipv4 v4,v6": udp(1081, "198.51.100.2", "2001:db8:2::2"),
		"force_ipv6 v6,v4": udp(1082, "2001:db8:2::2", "198.51.100.2"),
		"system v4,v6":     udp(1083, "198.51.100.2", "2001:db8:2::2"),
	}
	if udpResults["force_ipv4 v4,v6"] != "ok,none" || udpResults["force_ipv6 v6,v4"] != "ok,none" || udpResults["system v4,v6"] != "ok,ok" {
		t.Errorf("UDP sessions: %v", udpResults)
	}

	// Connection caps apply the same way through the SOCKS upstream as
	// through the direct outbound: hold one session, the next one is refused.
	capCheck := `import socket, sys, time
port = int(sys.argv[1])
first = socket.create_connection(("127.0.0.1", port), timeout=5)
first.settimeout(5)
a = first.recv(64)
time.sleep(0.5)
second = socket.create_connection(("127.0.0.1", port), timeout=5)
second.settimeout(3)
try:
    b = second.recv(64)
except Exception:
    b = b""
print("%s|%s" % (a.decode().strip(), b.decode().strip()))
`
	os.WriteFile(filepath.Join(base, "cap.py"), []byte(capCheck), 0644)
	capResults := map[string]string{}
	for i, id := range []string{"5", "6"} {
		out, err := exec.Command("ip", "netns", "exec", "mmwxeg-c", "python3", filepath.Join(base, "cap.py"), strconv.Itoa(21001+i)).CombinedOutput()
		if err != nil {
			t.Fatalf("cap client: %v %s", err, out)
		}
		capResults[id] = strings.TrimSpace(string(out))
	}
	if capResults["5"] != "peer=198.51.100.2|" || capResults["6"] != "peer=198.51.100.1|" {
		t.Errorf("node connection caps: socks=%q direct=%q", capResults["5"], capResults["6"])
	}
	// Share accounting still counts traffic that left through the upstream.
	if err := m.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	usage := map[string]int64{}
	for _, s := range m.state.Report.Nodes {
		usage[s.ID] = s.UpBytes + s.DownBytes
	}
	if usage["5"] == 0 || usage["7"] == 0 || m.state.Report.TrafficUsedBytes < usage["5"]+usage["7"] {
		t.Errorf("traffic through the SOCKS upstream not counted: %v total=%d", usage, m.state.Report.TrafficUsedBytes)
	}

	// The target log is the ground truth for the forced families.
	time.Sleep(300 * time.Millisecond)
	logRaw, _ := os.ReadFile(targetLog)
	v4From, v6From := 0, 0
	udpPeers := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(logRaw)), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		if f[0] == "tcp" && f[2] == "198.51.100.1" {
			v4From++
		}
		if f[0] == "tcp" && f[2] == "2001:db8:2::1" {
			v6From++
		}
		if f[0] == "udp" {
			udpPeers[f[1]+" "+f[2]]++
		}
	}
	// v4 from the landing: f4-v4lit, f4-both, sys-v4lit, cn-exempt, noncn and
	// the direct cap session; v6: f6-v6lit, f6-both, sys-v6lit.
	if v4From != 6 || v6From != 3 {
		t.Errorf("target saw %d IPv4 / %d IPv6 TCP connections from the landing; want 6 / 3\n%s", v4From, v6From, logRaw)
	}
	if udpPeers["2001:db8:2::1 p1081"] != 0 || udpPeers["198.51.100.1 p1082"] != 0 {
		t.Errorf("forbidden-family datagrams reached the target: %v", udpPeers)
	}
	report, _ := json.MarshalIndent(map[string]any{"tcp": fetched, "udp": udpResults, "udp_peers": udpPeers, "connection_caps": capResults, "node_bytes": usage, "target_v4_tcp_from_landing": v4From, "target_v6_tcp_from_landing": v6From, "warnings": m.state.Report.Egress.Warnings}, "", "  ")
	if path := os.Getenv("MMWXC_TEST_EVIDENCE"); path != "" {
		os.WriteFile(filepath.Join(path, "netns-dataplane.json"), report, 0644)
	}
	t.Logf("netns data plane: %s", report)
}
