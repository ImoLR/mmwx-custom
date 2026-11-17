package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// FLVX 预热池 (FLVX plans 244 / 264) against the shared FLVX Core, with the
// real Core and the Helper's own reconcile, in three private namespaces
// (client ⇄ landing 10 ms each way, so one RTT is visible):
//
//	client 11.0.1.50 ── 11.0.1.1 landing 11.0.2.1 ── 11.0.2.2 dest
//
// (public-looking addresses: the Core's freedom outbound drops private and
// reserved targets for SS / VLESS inbounds.)
// Nodes: SS2022 31001, Reality 31002 (guard on), AnyTLS 31003, VLESS
// Encryption 31005. Per node a pool relay in the client namespace pre-dials
// connections to the landing like FLVX's entry: the "use" pool stays silent
// for MMWXC_WARM_IDLE_S (default 115 s, past the pool's 90–110 s recycling)
// and then carries real client handshakes (one pooled connection per client
// connection, never a new dial); the "hold" pool stays silent until the
// landing closes it, which gives the idle-close time. Cold connections
// (straight to the landing) are measured next to the warm ones.
// MMWXC_WARM_E2E_OUT receives the evidence JSON.
const (
	wiLanding = "11.0.1.1"
	wiDest    = "11.0.2.2"
)

func TestFLVXWarmIdleNetnsE2E(t *testing.T) {
	core := os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED")
	if core == "" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_TEST_FLVX_CORE_FIXED and run as root")
	}
	for _, tool := range []string{"python3", "openssl", "ip", "tc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " missing")
		}
	}
	suffix := strconv.Itoa(os.Getpid() % 100000)
	c, l, d := "wic"+suffix, "wil"+suffix, "wid"+suffix
	ns := flvxNetns{t: t}
	flvxNetnsCleanup(c, l, d)
	t.Cleanup(func() { flvxNetnsCleanup(c, l, d) })
	for _, name := range []string{c, l, d} {
		ns.sh("ip", "netns", "add", name)
		ns.sh("ip", "-n", name, "link", "set", "lo", "up")
	}
	ns.sh("ip", "link", "add", "wic0"+suffix, "type", "veth", "peer", "name", "wil0"+suffix)
	ns.sh("ip", "link", "set", "wic0"+suffix, "netns", c)
	ns.sh("ip", "link", "set", "wil0"+suffix, "netns", l)
	ns.sh("ip", "link", "add", "wil1"+suffix, "type", "veth", "peer", "name", "wid0"+suffix)
	ns.sh("ip", "link", "set", "wil1"+suffix, "netns", l)
	ns.sh("ip", "link", "set", "wid0"+suffix, "netns", d)
	ns.sh("ip", "-n", c, "addr", "add", "11.0.1.50/24", "dev", "wic0"+suffix)
	ns.sh("ip", "-n", l, "addr", "add", wiLanding+"/24", "dev", "wil0"+suffix)
	ns.sh("ip", "-n", l, "addr", "add", "11.0.2.1/24", "dev", "wil1"+suffix)
	ns.sh("ip", "-n", d, "addr", "add", wiDest+"/24", "dev", "wid0"+suffix)
	for _, link := range [][2]string{{c, "wic0"}, {l, "wil0"}, {l, "wil1"}, {d, "wid0"}} {
		ns.sh("ip", "-n", link[0], "link", "set", link[1]+suffix, "up")
	}
	ns.sh("tc", "-n", c, "qdisc", "add", "dev", "wic0"+suffix, "root", "netem", "delay", "10ms")
	ns.sh("tc", "-n", l, "qdisc", "add", "dev", "wil0"+suffix, "root", "netem", "delay", "10ms")
	base := t.TempDir()
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	ns.sh("openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-days", "2",
		"-subj", "/CN=www.flvx.test", "-addext", "subjectAltName=DNS:www.flvx.test",
		"-keyout", filepath.Join(base, "dest.key"), "-out", filepath.Join(base, "dest.crt"))
	if err := os.WriteFile(filepath.Join(base, "dest.py"), []byte(wiDestServer), 0644); err != nil {
		t.Fatal(err)
	}
	dest := exec.Command("ip", "netns", "exec", d, "python3", filepath.Join(base, "dest.py"), filepath.Join(base, "dest.log"), base)
	destOut, _ := os.Create(filepath.Join(base, "dest.out"))
	dest.Stdout, dest.Stderr = destOut, destOut
	if err := dest.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dest.Process.Kill(); dest.Wait() })

	out := os.Getenv("MMWXC_WARM_E2E_OUT")
	if out == "" {
		out = filepath.Join(base, "evidence.json")
	}
	inner := exec.Command("ip", "netns", "exec", l, os.Args[0], "-test.run", "^TestFLVXWarmIdleNetnsInner$", "-test.v", "-test.timeout", "20m")
	inner.Env = append(os.Environ(), "MMWXC_WARM_INNER=1", "MMWXC_WARM_BASE="+base, "MMWXC_WARM_CLIENT_NS="+c, "MMWXC_WARM_E2E_OUT="+out)
	output, err := inner.CombinedOutput()
	t.Logf("inner run:\n%s", output)
	if err != nil {
		t.Fatalf("inner E2E failed: %v", err)
	}
}

func TestFLVXWarmIdleNetnsInner(t *testing.T) {
	if os.Getenv("MMWXC_WARM_INNER") != "1" {
		t.Skip("runs only inside the E2E landing namespace")
	}
	raiseNoFile()
	coreBin, base, clientNS := os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED"), os.Getenv("MMWXC_WARM_BASE"), os.Getenv("MMWXC_WARM_CLIENT_NS")
	idle := 115.0
	if os.Getenv("MMWXC_WARM_DEBUG") == "1" {
		idle = 5
	}
	if v, err := strconv.ParseFloat(os.Getenv("MMWXC_WARM_IDLE_S"), 64); err == nil && v > 0 {
		idle = v
	}
	ctx := context.Background()
	dir := t.TempDir()
	evidence := map[string]any{"started_at": time.Now().UTC().Format(time.RFC3339), "core_sha256": mustHash(t, coreBin), "idle_s": idle}

	// The FLVX Core exactly as the Helper configures it.
	apiPort := flvxFreePort(t)
	socket := filepath.Join(dir, "control.sock")
	baseConfig := flvx.BaseConfig(apiPort)
	evidence["policy"] = baseConfig["policy"]
	if os.Getenv("MMWXC_WARM_DEBUG") == "1" {
		baseConfig["log"] = map[string]any{"loglevel": "debug", "access": filepath.Join(base, "landing-access.log")}
		t.Cleanup(func() {
			raw, _ := os.ReadFile(filepath.Join(dir, "core.json.log"))
			os.WriteFile(os.Getenv("MMWXC_WARM_E2E_OUT")+".landing.log", raw, 0644)
			raw, _ = os.ReadFile(filepath.Join(base, "landing-access.log"))
			os.WriteFile(os.Getenv("MMWXC_WARM_E2E_OUT")+".access.log", raw, 0644)
		})
	}
	flvxStartCore(t, coreBin, filepath.Join(dir, "core.json"), socket, baseConfig)
	client := newCoreClient(socket)
	flvxWait(t, func() bool { _, err := client.snapshot(ctx); return err == nil })
	pid := lpPID(t, filepath.Join(dir, "core.json"))

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
	ss := helperFLVXNode("1", 31001)
	ss.MaxConnections, ss.MaxOnlineIPs = 0, 0
	reality := helperFLVXNode("2", 31002)
	reality.MaxConnections, reality.MaxOnlineIPs = 0, 0
	reality.Protocol, reality.SS2022 = "vless_reality_vision", nil
	reality.VLESS = &flvx.VLESS{UUID: uuid, Flow: "xtls-rprx-vision", Reality: flvx.Reality{Dest: "www.flvx.test:8443", ServerNames: []string{"www.flvx.test"}, PrivateKey: private, ShortIDs: []string{"a1b2c3d4"}}}
	var anytls flvx.Node
	for _, n := range helperProtocolNodes(t) {
		if n.Protocol == flvx.ProtocolAnyTLS {
			anytls = n
		}
	}
	anytls.ID, anytls.Port, anytls.MaxConnections, anytls.MaxOnlineIPs = "3", 31003, 0, 0
	ve := veMakeNode(t, "5", 31005, "native", "xtls-rprx-vision")
	ve.node.MaxOnlineIPs = 0
	guard := true
	hash := mustHash(t, coreBin)
	m := &flvxManager{dir: filepath.Join(dir, "state"), binary: coreBin, core: client, logs: newPeriodicLogLimiter()}
	m.state = flvxLocalState{Payload: flvx.Payload{Version: 1, Status: "active", Config: flvx.Config{PortMin: 31000, PortMax: 31999, ResetDay: 1, MemoryMB: 256, CPUPercent: 100},
		Desired: flvx.Desired{Revision: 1, Nodes: []flvx.Node{ss, reality, anytls, ve.node}, RealityGuard: &guard}, Artifact: flvx.Artifact{Version: "f6f5844", SHA256: hash}},
		APIPort: apiPort, Counters: map[string]*flvx.Counter{}, Applied: map[string]string{}, Report: flvx.Report{Generation: "warm-e2e"}}
	m.setup = func(context.Context) error { return nil }
	m.stop = func(context.Context) error { return nil }
	m.cleanup = func(context.Context) error { return nil }
	m.readOfficial = func() ([]int, error) { return []int{}, nil }
	m.lookupHost = func(_ context.Context, host string) ([]string, error) {
		if host != "www.flvx.test" {
			return nil, fmt.Errorf("unknown host %s", host)
		}
		return []string{wiDest}, nil
	}
	m.apiCall = m.api
	for i := 0; i < 2; i++ {
		if err := m.reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range m.state.Report.Nodes {
		if n.State != "active" {
			t.Fatalf("node %s not active: %+v", n.ID, n)
		}
	}
	if s := m.state.Report.RealityGuard; s == nil || s.State != "on" || s.Nodes != 1 {
		t.Fatalf("guard status %+v", s)
	}
	if !strings.Contains(m.state.Applied["flvxl-2"], `"dest":"127.0.0.1:`) || !strings.Contains(m.state.Applied["flvxl-2"], `"opt":"9","system":"linux","type":"int","value":"130"`) {
		t.Fatalf("Reality node not guarded with TCP_DEFER_ACCEPT 130: %s", m.state.Applied["flvxl-2"])
	}

	// Client: the same Core binary; per node a cold outbound (the landing)
	// and a warm one (the pool relay on 127.0.0.1).
	block, _ := pem.Decode([]byte(anytls.AnyTLS.TLS.CertificatePEM))
	pin := sha256.Sum256(block.Bytes)
	outbound := func(kind, host string, port int) map[string]any {
		switch kind {
		case "ss":
			return map[string]any{"protocol": "shadowsocks", "settings": map[string]any{"servers": []any{map[string]any{"address": host, "port": port, "method": ss.SS2022.Method, "password": ss.SS2022.ServerKey + ":" + ss.SS2022.UserKey}}}}
		case "reality":
			return map[string]any{"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": host, "port": port, "users": []any{map[string]any{"id": uuid, "flow": "xtls-rprx-vision", "encryption": "none"}}}}},
				"streamSettings": map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"serverName": "www.flvx.test", "publicKey": public, "shortId": "a1b2c3d4", "fingerprint": "chrome"}}}
		case "anytls":
			return map[string]any{"protocol": "anytls", "settings": map[string]any{"address": host, "port": port, "password": anytls.AnyTLS.Password},
				"streamSettings": map[string]any{"network": "tcp", "security": "tls", "tlsSettings": map[string]any{"serverName": anytls.AnyTLS.TLS.ServerName, "pinnedPeerCertSha256": hex.EncodeToString(pin[:]), "fingerprint": "chrome"}}}
		default:
			return map[string]any{"protocol": "vless", "settings": map[string]any{"address": host, "port": port, "id": ve.node.VLESSEnc.UUID, "encryption": ve.encryption, "flow": ve.flow},
				"streamSettings": map[string]any{"network": "tcp", "security": "none"}}
		}
	}
	kinds := []struct {
		name string
		port int
	}{{"ss", 31001}, {"reality", 31002}, {"anytls", 31003}, {"vless_enc", 31005}}
	var inbounds, outbounds, rules []any
	coldPort, warmPort, relayPort := map[string]int{}, map[string]int{}, map[string]int{}
	for i, k := range kinds {
		coldPort[k.name], warmPort[k.name], relayPort[k.name] = 20001+i, 20101+i, 26001+i
		for _, mode := range []string{"cold", "warm"} {
			tag, host, port, listen := mode+"-"+k.name, wiLanding, k.port, coldPort[k.name]
			if mode == "warm" {
				host, port, listen = "127.0.0.1", relayPort[k.name], warmPort[k.name]
			}
			o := outbound(k.name, host, port)
			o["tag"] = "out-" + tag
			outbounds = append(outbounds, o)
			inbounds = append(inbounds, map[string]any{"tag": "in-" + tag, "listen": "127.0.0.1", "port": listen, "protocol": "dokodemo-door", "settings": map[string]any{"address": wiDest, "port": 9002, "network": "tcp"}})
			rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{"in-" + tag}, "outboundTag": "out-" + tag})
		}
	}
	clientCfg, _ := json.Marshal(map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": inbounds, "outbounds": outbounds, "routing": map[string]any{"rules": rules}})
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
	for name, code := range map[string]string{"measure.py": rgMeasure, "pool.py": wiPool} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(code), 0644); err != nil {
			t.Fatal(err)
		}
	}
	measure := func(port, n int) map[string]any {
		t.Helper()
		raw, err := exec.Command("ip", "netns", "exec", clientNS, "python3", filepath.Join(base, "measure.py"), "lat", strconv.Itoa(port), strconv.Itoa(n)).CombinedOutput()
		if err != nil {
			t.Fatalf("measure: %v %s", err, raw)
		}
		v := map[string]any{}
		if err = json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("%v: %s", err, raw)
		}
		return v
	}
	time.Sleep(time.Second)
	cold := map[string]any{}
	for _, k := range kinds {
		r := measure(coldPort[k.name], 10)
		if r["fails"].(float64) != 0 {
			t.Errorf("cold %s: %v", k.name, r)
		}
		cold[k.name] = r
	}
	evidence["cold_before"] = cold

	// Pools: 10 to use after the idle, 3 held until the landing closes them.
	const useN, holdN = 10, 3
	status := func(name string) map[string]any {
		raw, _ := os.ReadFile(filepath.Join(base, "pool-"+name+".json"))
		v := map[string]any{}
		json.Unmarshal(raw, &v)
		return v
	}
	for _, k := range kinds {
		pool := exec.Command("ip", "netns", "exec", clientNS, "python3", filepath.Join(base, "pool.py"), strconv.Itoa(relayPort[k.name]), wiLanding, strconv.Itoa(k.port),
			strconv.Itoa(useN), strconv.Itoa(holdN), fmt.Sprint(idle), filepath.Join(base, "pool-"+k.name+".json"))
		poolLog, _ := os.Create(filepath.Join(base, "pool-"+k.name+".log"))
		pool.Stdout, pool.Stderr = poolLog, poolLog
		if err := pool.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pool.Process.Kill(); pool.Wait() })
	}
	started := time.Now()
	// While silent: what the landing and its Core see of the pooled sockets.
	time.Sleep(60 * time.Second)
	snapshot, _ := client.snapshot(ctx)
	states := map[string]map[string]int{}
	ssOut, _ := exec.Command("ss", "-tanH").Output()
	for _, line := range strings.Split(strings.TrimSpace(string(ssOut)), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		local := strings.NewReplacer("[::ffff:", "", "]", "").Replace(f[3])
		if !strings.HasPrefix(local, wiLanding+":") {
			continue
		}
		port := strings.TrimPrefix(local, wiLanding+":")
		if states[port] == nil {
			states[port] = map[string]int{}
		}
		states[port][f[0]]++
	}
	evidence["silent_at_60s"] = map[string]any{"landing_sockets": states, "core_current_inbound": snapshot.Global.CurrentInbound, "core_current_total": snapshot.Global.CurrentTotal, "core": rgProcStats(t, pid)}
	for _, k := range kinds {
		port := strconv.Itoa(k.port)
		// At least the pooled ones: the AnyTLS client keeps the idle sessions of
		// the cold phase open on the same port.
		if got := states[port]["ESTAB"] + states[port]["SYN-RECV"]; got < useN+holdN {
			t.Errorf("%s: %d pooled sockets at 60 s, want %d: %v", k.name, got, useN+holdN, states[port])
		}
	}
	if snapshot.Global.CurrentInbound != 0 {
		t.Errorf("silent pooled connections counted by the Core: %+v", snapshot.Global)
	}
	time.Sleep(time.Until(started.Add(time.Duration((idle + 2) * float64(time.Second)))))
	warm := map[string]any{}
	for _, k := range kinds {
		s := status(k.name)
		if s["use_alive_at_idle"] != float64(useN) {
			t.Errorf("%s: only %v of %d pooled connections alive after %.0f s: %v", k.name, s["use_alive_at_idle"], useN, idle, s)
		}
		r := measure(warmPort[k.name], useN)
		if r["fails"].(float64) != 0 {
			t.Errorf("warm %s after %.0f s silent: %v", k.name, idle, r)
		}
		warm[k.name] = map[string]any{"lat": r, "pool": status(k.name)}
	}
	evidence["warm_after_idle"] = warm
	cold = map[string]any{}
	for _, k := range kinds {
		r := measure(coldPort[k.name], 10)
		if r["fails"].(float64) != 0 {
			t.Errorf("cold %s after: %v", k.name, r)
		}
		cold[k.name] = r
	}
	evidence["cold_after"] = cold
	// The hold pools: wait until the landing closed the silent connections
	// (SS2022 / AnyTLS: handshake 120 s; guarded Reality: the deferred accept
	// ~251 s, then the guard). VLESS Encryption is recorded only: the Core
	// runs its ML-KEM handshake before it sets the handshake read deadline,
	// so a silent connection there has no timeout at all.
	timed := map[string]bool{"ss": true, "reality": true, "anytls": true}
	deadline := started.Add(300 * time.Second)
	for time.Now().Before(deadline) {
		done := true
		for _, k := range kinds {
			if s := status(k.name); timed[k.name] && s["hold_open"] != float64(0) {
				done = false
			}
		}
		if done {
			break
		}
		time.Sleep(2 * time.Second)
	}
	closes := map[string]any{"checked_at_s": time.Since(started).Seconds()}
	for _, k := range kinds {
		s := status(k.name)
		closes[k.name] = map[string]any{"closed_s": s["hold_closed_s"], "still_open": s["hold_open"]}
		list, _ := s["hold_closed_s"].([]any)
		if !timed[k.name] {
			continue
		}
		if len(list) != holdN {
			t.Errorf("%s: hold pool not closed by the landing within 300 s: %v", k.name, s)
			continue
		}
		for _, v := range list {
			if v.(float64) < 118 {
				t.Errorf("%s: silent connection closed after %.1f s (< 118 s)", k.name, v)
			}
		}
	}
	evidence["idle_close_s"] = closes
	evidence["core_after"] = rgProcStats(t, pid)
	raw, _ := json.MarshalIndent(evidence, "", "  ")
	if err := os.WriteFile(os.Getenv("MMWXC_WARM_E2E_OUT"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("evidence: %s", raw)
	if t.Failed() {
		cl, _ := os.ReadFile(filepath.Join(base, "client.log"))
		t.Logf("client log:\n%s", cl)
	}
}

// wiDestServer adds 9002 to the guard lab's dest: one byte in, "x" out, then
// held until the client closes (9001 closes at once, and SS2022 / AnyTLS can
// lose the reply in that half-close race, cold or warm alike).
var wiDestServer = strings.Replace(rgDestServer, "serve(8443, tls)", `def echo_hold(c, a, port):
    c.settimeout(10)
    try:
        if c.recv(1):
            c.sendall(b"x")
            while c.recv(4096):
                pass
    except Exception:
        pass
    c.close()
threading.Thread(target=serve, args=(9002, echo_hold), daemon=True).start()
serve(8443, tls)`, 1)

// pool <listen> <host> <port> <use_n> <hold_n> <idle_s> <status>: pre-dials
// use_n + hold_n connections and keeps them silent. After idle_s each local
// connection on 127.0.0.1:<listen> is piped through one live "use"
// connection (never a new dial; none left = closed), TCP_NODELAY like FLVX's
// Go entry. Close times (seconds
// since the dial) are polled every 0.5 s and written to <status>.
const wiPool = `import json, select, socket, sys, threading, time
listen, host, port, use_n, hold_n, idle, path = int(sys.argv[1]), sys.argv[2], int(sys.argv[3]), int(sys.argv[4]), int(sys.argv[5]), float(sys.argv[6]), sys.argv[7]
t0 = time.monotonic()
def dial():
    s = socket.create_connection((host, port), timeout=5)
    s.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)  # as FLVX's Go entry
    return s
use = [dial() for _ in range(use_n)]
hold = [dial() for _ in range(hold_n)]
lock = threading.Lock()
st = {"use_n": use_n, "hold_n": hold_n, "use_closed_s": [], "hold_closed_s": [], "hold_open": hold_n, "served_warm": 0, "no_pool": 0}
def write():
    with open(path + ".tmp", "w") as f: json.dump(st, f)
    import os; os.replace(path + ".tmp", path)
def closed(s):
    r, _, _ = select.select([s], [], [], 0)
    if not r: return False
    try: return s.recv(1, socket.MSG_PEEK) == b""
    except OSError: return True
idle_pool = list(use)
def watch():
    while True:
        now = round(time.monotonic() - t0, 1)
        with lock:
            for s in list(idle_pool):
                if closed(s):
                    idle_pool.remove(s); st["use_closed_s"].append(now)
            for s in list(hold):
                if closed(s):
                    hold.remove(s); st["hold_closed_s"].append(now)
            st["hold_open"] = len(hold)
            write()
        time.sleep(0.5)
threading.Thread(target=watch, daemon=True).start()
time.sleep(max(0, idle - (time.monotonic() - t0)))
with lock:
    for s in list(idle_pool):
        if closed(s):
            idle_pool.remove(s); st["use_closed_s"].append(round(time.monotonic() - t0, 1))
    st["use_alive_at_idle"] = len(idle_pool); write()
def pipe(a, b):
    try:
        while True:
            d = a.recv(65536)
            if not d: break
            b.sendall(d)
    except Exception: pass
    for x in (a, b):
        try: x.shutdown(socket.SHUT_RDWR)
        except Exception: pass
srv = socket.socket(); srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); srv.bind(("127.0.0.1", listen)); srv.listen(64)
while True:
    c, _ = srv.accept()
    with lock:
        u = idle_pool.pop(0) if idle_pool else None
        if u is None: st["no_pool"] += 1
        else: st["served_warm"] += 1
        write()
    if u is None:
        c.close(); continue
    c.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
    u.settimeout(None)
    threading.Thread(target=pipe, args=(c, u), daemon=True).start()
    threading.Thread(target=pipe, args=(u, c), daemon=True).start()
`
