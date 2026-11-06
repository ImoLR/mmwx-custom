package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// reality-pick-v1 §3 with the real FLVX Core and the Helper's own reconcile,
// in three private namespaces (the host network is never touched):
//
//	client 203.0.113.50 ── 203.0.113.1 landing 198.51.100.1 ── 198.51.100.2 / .3 dest
//
// The dest name resolves to both dest addresses; the connect times the
// Helper measures are injected (.2 fast, then .3 fast). One guarded Reality
// node: it is pinned to the fast address, an authenticated client streams
// through it while the pin moves to the other address, and the stream must
// not break: only the loopback guard inbound is re-added, the node inbound,
// the Core process and the client connection stay. The dest logs which of
// its addresses each REALITY handshake reached. MMWXC_PIN_E2E_OUT receives
// the evidence JSON.
func TestFLVXRealityPinNetnsE2E(t *testing.T) {
	core := os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED")
	if core == "" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_TEST_FLVX_CORE_FIXED and run as root")
	}
	for _, tool := range []string{"python3", "openssl", "ip"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " missing")
		}
	}
	suffix := strconv.Itoa(os.Getpid() % 100000)
	c, l, d := "rpc"+suffix, "rpl"+suffix, "rpd"+suffix
	ns := flvxNetns{t: t}
	flvxNetnsCleanup(c, l, d)
	t.Cleanup(func() { flvxNetnsCleanup(c, l, d) })
	for _, name := range []string{c, l, d} {
		ns.sh("ip", "netns", "add", name)
		ns.sh("ip", "-n", name, "link", "set", "lo", "up")
	}
	ns.sh("ip", "link", "add", "rpc0"+suffix, "type", "veth", "peer", "name", "rpl0"+suffix)
	ns.sh("ip", "link", "set", "rpc0"+suffix, "netns", c)
	ns.sh("ip", "link", "set", "rpl0"+suffix, "netns", l)
	ns.sh("ip", "link", "add", "rpl1"+suffix, "type", "veth", "peer", "name", "rpd0"+suffix)
	ns.sh("ip", "link", "set", "rpl1"+suffix, "netns", l)
	ns.sh("ip", "link", "set", "rpd0"+suffix, "netns", d)
	ns.sh("ip", "-n", c, "addr", "add", "203.0.113.50/24", "dev", "rpc0"+suffix)
	ns.sh("ip", "-n", l, "addr", "add", rgLanding+"/24", "dev", "rpl0"+suffix)
	ns.sh("ip", "-n", l, "addr", "add", "198.51.100.1/24", "dev", "rpl1"+suffix)
	ns.sh("ip", "-n", d, "addr", "add", "198.51.100.2/24", "dev", "rpd0"+suffix)
	ns.sh("ip", "-n", d, "addr", "add", "198.51.100.3/24", "dev", "rpd0"+suffix)
	for _, link := range [][2]string{{c, "rpc0"}, {l, "rpl0"}, {l, "rpl1"}, {d, "rpd0"}} {
		ns.sh("ip", "-n", link[0], "link", "set", link[1]+suffix, "up")
	}
	base := t.TempDir()
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	ns.sh("openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-days", "2",
		"-subj", "/CN=www.flvx.test", "-addext", "subjectAltName=DNS:www.flvx.test",
		"-keyout", filepath.Join(base, "dest.key"), "-out", filepath.Join(base, "dest.crt"))
	if err := os.WriteFile(filepath.Join(base, "dest.py"), []byte(rpDestServer), 0644); err != nil {
		t.Fatal(err)
	}
	dest := exec.Command("ip", "netns", "exec", d, "python3", filepath.Join(base, "dest.py"), filepath.Join(base, "dest.log"), base)
	destOut, _ := os.Create(filepath.Join(base, "dest.out"))
	dest.Stdout, dest.Stderr = destOut, destOut
	if err := dest.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dest.Process.Kill(); dest.Wait() })
	out := os.Getenv("MMWXC_PIN_E2E_OUT")
	if out == "" {
		out = filepath.Join(base, "evidence.json")
	}
	inner := exec.Command("ip", "netns", "exec", l, os.Args[0], "-test.run", "^TestFLVXRealityPinNetnsInner$", "-test.v", "-test.timeout", "5m")
	inner.Env = append(os.Environ(), "MMWXC_PIN_INNER=1", "MMWXC_PIN_BASE="+base, "MMWXC_PIN_CLIENT_NS="+c, "MMWXC_PIN_E2E_OUT="+out)
	output, err := inner.CombinedOutput()
	t.Logf("inner run:\n%s", output)
	if err != nil {
		t.Fatalf("inner E2E failed: %v", err)
	}
}

func TestFLVXRealityPinNetnsInner(t *testing.T) {
	if os.Getenv("MMWXC_PIN_INNER") != "1" {
		t.Skip("runs only inside the E2E landing namespace")
	}
	coreBin, base, clientNS := os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED"), os.Getenv("MMWXC_PIN_BASE"), os.Getenv("MMWXC_PIN_CLIENT_NS")
	ctx := context.Background()
	dir := t.TempDir()
	evidence := map[string]any{"started_at": time.Now().UTC().Format(time.RFC3339), "core_sha256": mustHash(t, coreBin)}

	apiPort := flvxFreePort(t)
	socket := filepath.Join(dir, "control.sock")
	raw, _ := json.Marshal(flvx.BaseConfig(apiPort))
	if err := os.WriteFile(filepath.Join(dir, "core.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	coreCmd := exec.Command(coreBin, "run", "-config", filepath.Join(dir, "core.json"))
	coreCmd.Env = append(os.Environ(), "MMWXC_CORE_CONTROL_SOCKET="+socket)
	coreLog, _ := os.Create(filepath.Join(base, "landing-core.log"))
	coreCmd.Stdout, coreCmd.Stderr = coreLog, coreLog
	if err := coreCmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { coreCmd.Process.Kill(); coreCmd.Wait() })
	client := newCoreClient(socket)
	flvxWait(t, func() bool { _, err := client.snapshot(ctx); return err == nil })

	var key [32]byte
	rand.Read(key[:])
	private := base64.RawURLEncoding.EncodeToString(key[:])
	pubOut, err := exec.Command(coreBin, "x25519", "-i", private).Output()
	if err != nil {
		t.Fatal(err)
	}
	public := ""
	for _, line := range strings.Split(string(pubOut), "\n") {
		if strings.HasPrefix(line, "Password (PublicKey): ") || strings.HasPrefix(line, "Public key: ") {
			public = strings.TrimSpace(line[strings.Index(line, ":")+1:])
		}
	}
	if public == "" {
		t.Fatalf("public key: %s", pubOut)
	}
	const uuid = "8a8e3c4d-1f2b-4c3d-9e8f-0a1b2c3d4e5f"
	node := helperFLVXNode("2", 31002)
	node.MaxConnections, node.MaxOnlineIPs = 0, 0
	node.Protocol, node.SS2022 = "vless_reality_vision", nil
	node.VLESS = &flvx.VLESS{UUID: uuid, Flow: "xtls-rprx-vision", Reality: flvx.Reality{Dest: "www.flvx.test:8443", ServerNames: []string{"www.flvx.test"}, PrivateKey: private, ShortIDs: []string{"a1b2c3d4"}}}
	on := true
	m := &flvxManager{dir: filepath.Join(dir, "state"), binary: coreBin, core: client, logs: newPeriodicLogLimiter()}
	m.state = flvxLocalState{Payload: flvx.Payload{Version: 1, Status: "active", Config: flvx.Config{PortMin: 31000, PortMax: 31999, ResetDay: 1},
		Desired: flvx.Desired{Revision: 1, Nodes: []flvx.Node{node}, RealityGuard: &on}, Artifact: flvx.Artifact{Version: "test", SHA256: mustHash(t, coreBin)}}, APIPort: apiPort, Counters: map[string]*flvx.Counter{}, Applied: map[string]string{}, Report: flvx.Report{Generation: "pin-e2e"}}
	m.setup = func(context.Context) error { return nil }
	m.stop = func(context.Context) error { return nil }
	m.cleanup = func(context.Context) error { return nil }
	m.readOfficial = func() ([]int, error) { return []int{}, nil }
	m.apiCall = m.api
	m.lookupHost = func(_ context.Context, host string) ([]string, error) {
		if host != "www.flvx.test" {
			return nil, fmt.Errorf("unknown host %s", host)
		}
		return []string{"198.51.100.2", "198.51.100.3"}, nil
	}
	var mu sync.Mutex
	measured := map[string]time.Duration{"198.51.100.2": 10 * time.Millisecond, "198.51.100.3": 60 * time.Millisecond}
	m.connectTime = func(ctx context.Context, address string) (time.Duration, error) {
		// The real connect proves the address answers; the time is injected.
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return 0, err
		}
		conn.Close()
		host, _, _ := net.SplitHostPort(address)
		mu.Lock()
		defer mu.Unlock()
		return measured[host], nil
	}
	now := time.Now()
	m.clock = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	apply := func(passes int) {
		t.Helper()
		for i := 0; i < passes; i++ {
			if err := m.reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	guardAddress := func() string {
		for tag, cfg := range m.state.Applied {
			if strings.HasPrefix(tag, flvx.GuardTagPrefix) {
				var in struct {
					Settings struct {
						Address string `json:"address"`
					} `json:"settings"`
				}
				json.Unmarshal([]byte(cfg), &in)
				return tag + "=" + in.Settings.Address
			}
		}
		return ""
	}
	apply(3)
	if s := m.state.Report.Nodes; len(s) != 1 || s[0].State != "active" {
		t.Fatalf("node not active: %+v", s)
	}
	nodeConfig := m.state.Applied[node.Tag()]
	if flvx.GuardPortOf(nodeConfig) == 0 || !strings.HasSuffix(guardAddress(), "=198.51.100.2") {
		t.Fatalf("not guarded on the fast address: %s %s", guardAddress(), nodeConfig)
	}
	evidence["pin_before"], evidence["guard_before"], evidence["status_before"] = m.state.RealityPins[node.Tag()].Dest, guardAddress(), m.state.Report.RealityPins

	// Client: dokodemo 127.0.0.1:20001 -> VLESS Reality -> dest 198.51.100.2:9100 (a stream).
	clientCfg, _ := json.Marshal(map[string]any{"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{"tag": "in", "listen": "127.0.0.1", "port": 20001, "protocol": "dokodemo-door", "settings": map[string]any{"address": "198.51.100.2", "port": 9100, "network": "tcp"}}},
		"outbounds": []any{map[string]any{"tag": "out", "protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": rgLanding, "port": 31002, "users": []any{map[string]any{"id": uuid, "flow": "xtls-rprx-vision", "encryption": "none"}}}}},
			"streamSettings": map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"serverName": "www.flvx.test", "publicKey": public, "shortId": "a1b2c3d4", "fingerprint": "chrome"}}}}})
	if err := os.WriteFile(filepath.Join(base, "client.json"), clientCfg, 0644); err != nil {
		t.Fatal(err)
	}
	clientCmd := exec.Command("ip", "netns", "exec", clientNS, coreBin, "run", "-config", filepath.Join(base, "client.json"))
	clientLog, _ := os.Create(filepath.Join(base, "client.log"))
	clientCmd.Stdout, clientCmd.Stderr = clientLog, clientLog
	if err := clientCmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clientCmd.Process.Kill(); clientCmd.Wait() })
	if err := os.WriteFile(filepath.Join(base, "stream.py"), []byte(rpStream), 0644); err != nil {
		t.Fatal(err)
	}
	destLog := func() []string {
		raw, _ := os.ReadFile(filepath.Join(base, "dest.log"))
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
	handshakes := func(from int) map[string]int {
		seen := map[string]int{}
		for _, line := range destLog()[from:] {
			if strings.HasPrefix(line, "tls local=") {
				seen[strings.Fields(strings.TrimPrefix(line, "tls local="))[0]]++
			}
		}
		return seen
	}
	runStream := func(seconds string) string {
		out, err := exec.Command("ip", "netns", "exec", clientNS, "python3", filepath.Join(base, "stream.py"), "20001", seconds).CombinedOutput()
		if err != nil {
			out = append(out, []byte(" error="+err.Error())...)
		}
		return strings.TrimSpace(string(out))
	}
	// Proxied traffic needs the Core's outbound to get replies in these
	// namespaces, which fails on some dev boxes (also for SS2022 in
	// TestFLVXParentPermsNetnsE2E). Without it only the stream part is
	// skipped: the dest still logs every REALITY handshake.
	sanity := runStream("0.5")
	streaming := strings.Contains(sanity, `"bytes": 5,`)
	evidence["stream_sanity"] = sanity
	if !streaming {
		t.Logf("proxied traffic gets no reply in this environment (%s); live-stream check skipped", sanity)
	}
	// The stream runs 8 s (one byte per 100 ms) through the node.
	streamOut := make(chan string, 1)
	mark := len(destLog())
	go func() { streamOut <- runStream("8") }()
	time.Sleep(2 * time.Second)
	firstHandshake := handshakes(mark)
	pid := coreCmd.Process.Pid
	snapshot, _ := client.snapshot(ctx)
	epoch := flvxEpoch(snapshot)
	// The fast address turns slow (>= 20 ms behind): the 6 h re-check moves
	// the pin while the stream is running.
	mu.Lock()
	measured = map[string]time.Duration{"198.51.100.2": 70 * time.Millisecond, "198.51.100.3": 5 * time.Millisecond}
	now = now.Add(flvxPinRecheck)
	mu.Unlock()
	calls := []string{}
	m.apiCall = func(ctx context.Context, command string, args ...string) ([]byte, error) {
		if command != "statsquery" {
			calls = append(calls, strings.TrimSpace(command+" "+strings.Join(args, " ")))
		}
		return m.api(ctx, command, args...)
	}
	start := time.Now()
	apply(1)
	evidence["retarget_ms"] = time.Since(start).Milliseconds()
	apply(1)
	m.apiCall = m.api
	stream := <-streamOut
	evidence["stream"] = stream
	if !strings.HasSuffix(guardAddress(), "=198.51.100.3") || m.state.Applied[node.Tag()] != nodeConfig {
		t.Fatalf("pin did not move the guard only: %s node changed=%v", guardAddress(), m.state.Applied[node.Tag()] != nodeConfig)
	}
	for _, call := range calls {
		if strings.Contains(call, node.Tag()) || strings.HasPrefix(call, "adrules") {
			t.Fatalf("node inbound or rules touched: %v", calls)
		}
	}
	snapshot, _ = client.snapshot(ctx)
	if coreCmd.Process.Pid != pid || flvxEpoch(snapshot) != epoch {
		t.Fatal("Core restarted")
	}
	var got struct {
		Bytes  int     `json:"bytes"`
		MaxGap float64 `json:"max_gap_ms"`
		EOF    bool    `json:"eof"`
	}
	if err := json.Unmarshal([]byte(stream), &got); streaming && (err != nil || got.Bytes < 75 || got.MaxGap > 1000 || got.EOF) {
		t.Fatalf("live stream broke across the re-pin: %s", stream)
	}
	// A new connection after the move handshakes with .3.
	mark = len(destLog())
	evidence["stream_after"] = runStream("0.5")
	time.Sleep(300 * time.Millisecond)
	after := handshakes(mark)
	if firstHandshake["198.51.100.2"] == 0 || after["198.51.100.3"] == 0 || after["198.51.100.2"] != 0 {
		t.Fatalf("handshake addresses before %v after %v", firstHandshake, after)
	}
	evidence["calls"], evidence["pin_after"], evidence["guard_after"], evidence["status_after"] = calls, m.state.RealityPins[node.Tag()].Dest, guardAddress(), m.state.Report.RealityPins
	evidence["handshakes_before"], evidence["handshakes_after"], evidence["core_pid_unchanged"], evidence["stream_checked"] = firstHandshake, after, true, streaming
	raw, _ = json.MarshalIndent(evidence, "", "  ")
	if err := os.WriteFile(os.Getenv("MMWXC_PIN_E2E_OUT"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("evidence: %s", raw)
}

// rpDestServer: TLS on 8443 for www.flvx.test logging the local address
// each handshake reached, and a stream on 9100 (one byte per 100 ms).
const rpDestServer = `import socket, ssl, sys, threading, time
log = open(sys.argv[1], "a", buffering=1)
base = sys.argv[2]
def serve(port, handler):
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("0.0.0.0", port)); s.listen(1024)
    while True:
        c, a = s.accept()
        threading.Thread(target=handler, args=(c, a), daemon=True).start()
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(base + "/dest.crt", base + "/dest.key")
ctx.set_alpn_protocols(["h2", "http/1.1"])
def tls(c, a):
    local = c.getsockname()[0]
    names = []
    ctx2 = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx2.load_cert_chain(base + "/dest.crt", base + "/dest.key")
    ctx2.set_alpn_protocols(["h2", "http/1.1"])
    ctx2.sni_callback = lambda sock, name, _: names.append(name)
    c.settimeout(10)
    try:
        t = ctx2.wrap_socket(c, server_side=True)
        log.write("tls local=%s sni=%s\n" % (local, names[0] if names else ""))
        time.sleep(0.2); t.close()
    except Exception as e:
        log.write("tls local=%s failed %s %s\n" % (local, type(e).__name__, str(e)[:120]))
        c.close()
def stream(c, a):
    c.settimeout(30)
    try:
        n = int(c.recv(16).strip())
        for i in range(n):
            c.sendall(b"x"); time.sleep(0.1)
    except Exception:
        pass
    c.close()
threading.Thread(target=serve, args=(9100, stream), daemon=True).start()
serve(8443, tls)
`

// rpStream reads PORT for SECONDS through the client's dokodemo inbound and
// reports the bytes, the largest gap and whether the stream ended early.
const rpStream = `import json, socket, sys, time
port, seconds = int(sys.argv[1]), float(sys.argv[2])
want = int(seconds * 10)
for attempt in range(50):
    try:
        c = socket.create_connection(("127.0.0.1", port), timeout=5); break
    except OSError:
        time.sleep(0.2)
c.settimeout(5); c.sendall(b"%d\n" % want)
got, gap, last, eof = 0, 0.0, time.time(), False
while got < want:
    try:
        b = c.recv(64)
    except socket.timeout:
        break
    now = time.time()
    if not b:
        eof = True; break
    if got:
        gap = max(gap, (now - last) * 1000)
    got += len(b); last = now
print(json.dumps({"bytes": got, "max_gap_ms": round(gap, 1), "eof": eof and got < want}))
`
