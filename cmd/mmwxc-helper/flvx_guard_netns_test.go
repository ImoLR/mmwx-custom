package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

// Reality 防盗 on the shared FLVX Core with the real Core and the Helper's
// own reconcile, in three private namespaces (the host network is never
// touched):
//
//	client 203.0.113.50 ── 203.0.113.1 landing 198.51.100.1 ── 198.51.100.2 dest
//
// The dest serves TLS for www.flvx.test on 8443 (logging every SNI) plus
// banner / bulk / echo services. Landing nodes: SS2022 31001, Reality 31002
// (serverName www.flvx.test) and 31004 (alt.flvx.test), same dest, so the
// guard renders two groups. Phases: guard absent (old FLVX: probes relayed),
// guard on (wrong / no SNI and non-TLS closed, right SNI gets the dest
// certificate, authenticated users work, late ClientHello), guard off again.
// Resource cost: Core RSS / fds / threads, CPU per new connection and how
// long the guard's sockets and the Core's connection count stay up after a
// burst. MMWXC_GUARD_E2E_OUT receives the evidence JSON;
// MMWXC_GUARD_E2E_SKIP_PERF / _SKIP_LATE / _DECAY(_OFF) shorten diagnosis runs.
const (
	rgLanding = "203.0.113.1"
	rgDest    = "198.51.100.2"
)

func TestFLVXRealityGuardNetnsE2E(t *testing.T) {
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
	c, l, d := "rgc"+suffix, "rgl"+suffix, "rgd"+suffix
	ns := flvxNetns{t: t}
	flvxNetnsCleanup(c, l, d)
	t.Cleanup(func() { flvxNetnsCleanup(c, l, d) })
	for _, name := range []string{c, l, d} {
		ns.sh("ip", "netns", "add", name)
		ns.sh("ip", "-n", name, "link", "set", "lo", "up")
	}
	ns.sh("ip", "link", "add", "rgc0"+suffix, "type", "veth", "peer", "name", "rgl0"+suffix)
	ns.sh("ip", "link", "set", "rgc0"+suffix, "netns", c)
	ns.sh("ip", "link", "set", "rgl0"+suffix, "netns", l)
	ns.sh("ip", "link", "add", "rgl1"+suffix, "type", "veth", "peer", "name", "rgd0"+suffix)
	ns.sh("ip", "link", "set", "rgl1"+suffix, "netns", l)
	ns.sh("ip", "link", "set", "rgd0"+suffix, "netns", d)
	ns.sh("ip", "-n", c, "addr", "add", "203.0.113.50/24", "dev", "rgc0"+suffix)
	ns.sh("ip", "-n", l, "addr", "add", rgLanding+"/24", "dev", "rgl0"+suffix)
	ns.sh("ip", "-n", l, "addr", "add", "198.51.100.1/24", "dev", "rgl1"+suffix)
	ns.sh("ip", "-n", d, "addr", "add", rgDest+"/24", "dev", "rgd0"+suffix)
	for _, link := range [][2]string{{c, "rgc0"}, {l, "rgl0"}, {l, "rgl1"}, {d, "rgd0"}} {
		ns.sh("ip", "-n", link[0], "link", "set", link[1]+suffix, "up")
	}
	base := t.TempDir()
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	ns.sh("openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-days", "2",
		"-subj", "/CN=www.flvx.test", "-addext", "subjectAltName=DNS:www.flvx.test",
		"-keyout", filepath.Join(base, "dest.key"), "-out", filepath.Join(base, "dest.crt"))
	if err := os.WriteFile(filepath.Join(base, "dest.py"), []byte(rgDestServer), 0644); err != nil {
		t.Fatal(err)
	}
	dest := exec.Command("ip", "netns", "exec", d, "python3", filepath.Join(base, "dest.py"), filepath.Join(base, "dest.log"), base)
	destOut, _ := os.Create(filepath.Join(base, "dest.out"))
	dest.Stdout, dest.Stderr = destOut, destOut
	if err := dest.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dest.Process.Kill(); dest.Wait() })

	out := os.Getenv("MMWXC_GUARD_E2E_OUT")
	if out == "" {
		out = filepath.Join(base, "evidence.json")
	}
	inner := exec.Command("ip", "netns", "exec", l, os.Args[0], "-test.run", "^TestFLVXRealityGuardNetnsInner$", "-test.v", "-test.timeout", "20m")
	inner.Env = append(os.Environ(), "MMWXC_GUARD_INNER=1", "MMWXC_GUARD_BASE="+base, "MMWXC_GUARD_CLIENT_NS="+c, "MMWXC_GUARD_E2E_OUT="+out)
	output, err := inner.CombinedOutput()
	t.Logf("inner run:\n%s", output)
	if err != nil {
		t.Fatalf("inner E2E failed: %v", err)
	}
}

func TestFLVXRealityGuardNetnsInner(t *testing.T) {
	if os.Getenv("MMWXC_GUARD_INNER") != "1" {
		t.Skip("runs only inside the E2E landing namespace")
	}
	raiseNoFile()
	coreBin, base, clientNS := os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED"), os.Getenv("MMWXC_GUARD_BASE"), os.Getenv("MMWXC_GUARD_CLIENT_NS")
	ctx := context.Background()
	dir := t.TempDir()
	evidence := map[string]any{"started_at": time.Now().UTC().Format(time.RFC3339), "core_sha256": mustHash(t, coreBin)}

	// The FLVX Core exactly as the Helper configures it.
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
	corePID := coreCmd.Process.Pid
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
	reality := func(id string, port int, name string) flvx.Node {
		n := helperFLVXNode(id, port)
		n.MaxConnections, n.MaxOnlineIPs = 0, 0
		n.Protocol, n.SS2022 = "vless_reality_vision", nil
		n.VLESS = &flvx.VLESS{UUID: uuid, Flow: "xtls-rprx-vision", Reality: flvx.Reality{Dest: "www.flvx.test:8443", ServerNames: []string{name}, PrivateKey: private, ShortIDs: []string{"a1b2c3d4"}}}
		return n
	}
	ss := helperFLVXNode("1", 31001)
	ss.MaxConnections, ss.MaxOnlineIPs = 0, 0
	nodes := []flvx.Node{ss, reality("2", 31002, "www.flvx.test"), reality("4", 31004, "alt.flvx.test")}
	hash := mustHash(t, coreBin)
	m := &flvxManager{dir: filepath.Join(dir, "state"), binary: coreBin, core: client, logs: newPeriodicLogLimiter()}
	m.state = flvxLocalState{Payload: flvx.Payload{Version: 1, Status: "active", Config: flvx.Config{PortMin: 31000, PortMax: 31999, ResetDay: 1, MemoryMB: 256, CPUPercent: 100},
		Desired: flvx.Desired{Revision: 1, Nodes: nodes}, Artifact: flvx.Artifact{Version: "b01adef", SHA256: hash}}, APIPort: apiPort, Counters: map[string]*flvx.Counter{}, Applied: map[string]string{}, Report: flvx.Report{Generation: "guard-e2e"}}
	m.setup = func(context.Context) error { return nil }
	m.stop = func(context.Context) error { return nil }
	m.cleanup = func(context.Context) error { return nil }
	m.readOfficial = func() ([]int, error) { return []int{}, nil }
	// The decoy name resolves to the dest namespace (the Helper pins it).
	m.lookupHost = func(_ context.Context, host string) ([]string, error) {
		if host != "www.flvx.test" {
			return nil, fmt.Errorf("unknown host %s", host)
		}
		return []string{rgDest}, nil
	}
	m.apiCall = m.api
	apply := func(passes int) {
		t.Helper()
		for i := 0; i < passes; i++ {
			if err := m.reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	setGuard := func(v *bool) {
		m.state.Payload.Desired.RealityGuard = v
		m.state.Payload.Desired.Revision++
		m.state.Payload.Version++
	}
	listed := func() string {
		raw, err := m.api(ctx, "lsi")
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	apply(1)
	for _, n := range m.state.Report.Nodes {
		if n.State != "active" {
			t.Fatalf("node %s not active: %+v", n.ID, n)
		}
	}
	unguarded := map[string]string{}
	for tag, cfg := range m.state.Applied {
		unguarded[tag] = cfg
	}
	time.Sleep(3 * time.Second)
	idleOff := rgProcStats(t, corePID)

	// Client: the same Core binary, one dokodemo inbound per case.
	vless := func(host string, port int, name string) map[string]any {
		return map[string]any{"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": host, "port": port, "users": []any{map[string]any{"id": uuid, "flow": "xtls-rprx-vision", "encryption": "none"}}}}},
			"streamSettings": map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"serverName": name, "publicKey": public, "shortId": "a1b2c3d4", "fingerprint": "chrome"}}}
	}
	outs := map[string]map[string]any{
		"ss": {"protocol": "shadowsocks", "settings": map[string]any{"servers": []any{map[string]any{"address": rgLanding, "port": 31001, "method": ss.SS2022.Method, "password": ss.SS2022.ServerKey + ":" + ss.SS2022.UserKey}}}},
		"g":  vless(rgLanding, 31002, "www.flvx.test"),
		"h":  vless(rgLanding, 31004, "alt.flvx.test"),
		// Through the late-ClientHello relay in the client namespace.
		"gl": vless("127.0.0.1", 25001, "www.flvx.test"),
	}
	type route struct {
		name, out string
		port      int
	}
	routes := []route{{"ss-443", "ss", 443}, {"g-443", "g", 443}, {"h-443", "h", 443}, {"g-bulk", "g", 9000}, {"g-lat", "g", 9001}, {"h-lat", "h", 9001}, {"gl-lat", "gl", 9001}}
	var inbounds, outbounds, rules []any
	portOf := map[string]int{}
	for i, r := range routes {
		portOf[r.name] = 20001 + i
		inbounds = append(inbounds, map[string]any{"tag": "in-" + r.name, "listen": "127.0.0.1", "port": 20001 + i, "protocol": "dokodemo-door", "settings": map[string]any{"address": rgDest, "port": r.port, "network": "tcp"}})
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{"in-" + r.name}, "outboundTag": "out-" + r.out})
	}
	for name, o := range outs {
		o["tag"] = "out-" + name
		outbounds = append(outbounds, o)
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
	for name, code := range map[string]string{"fetch.py": rgFetch, "probe.py": rgProbe, "measure.py": rgMeasure, "relay.py": rgRelay} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(code), 0644); err != nil {
			t.Fatal(err)
		}
	}
	py := func(name string, args ...string) string {
		t.Helper()
		out, err := exec.Command("ip", append([]string{"netns", "exec", clientNS, "python3", filepath.Join(base, name)}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", name, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	jsonMap := func(s string) map[string]any {
		t.Helper()
		v := map[string]any{}
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatalf("%v: %s", err, s)
		}
		return v
	}
	certDER, err := exec.Command("openssl", "x509", "-in", filepath.Join(base, "dest.crt"), "-outform", "DER").Output()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(certDER)
	destCert := "cert:" + hex.EncodeToString(sum[:])[:16]
	sniLog := func() map[string]int {
		raw, _ := os.ReadFile(filepath.Join(base, "dest.log"))
		seen := map[string]int{}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "tls sni=") {
				seen[strings.TrimPrefix(line, "tls sni=")]++
			}
		}
		return seen
	}
	sniSince := func(before map[string]int) map[string]int {
		time.Sleep(300 * time.Millisecond)
		now, diff := sniLog(), map[string]int{}
		for k, v := range now {
			if v-before[k] != 0 {
				diff[k] = v - before[k]
			}
		}
		return diff
	}
	probeArgs := []string{"right=31002=www.flvx.test", "wrong=31002=www.example.com", "nosni=31002=", "plain=31002=-", "right-late=31002=www.flvx.test=0.5", "wrong-late=31002=www.example.com=0.5", "nosni-late=31002==0.5",
		"h-right=31004=alt.flvx.test", "h-other=31004=www.flvx.test", "h-wrong=31004=www.example.com"}
	run := func(mode, name string, n int) map[string]any {
		t.Helper()
		return jsonMap(py("measure.py", mode, strconv.Itoa(portOf[name]), strconv.Itoa(n)))
	}
	cpuPer := func(name string, n int) map[string]any {
		t.Helper()
		s0 := rgProcStats(t, corePID)
		lat := run("lat", name, n)
		s1 := rgProcStats(t, corePID)
		lat["core_cpu_ms_per_conn"] = (s1["cpu_s"] - s0["cpu_s"]) * 1000 / float64(n)
		return lat
	}
	// decay: fds and TCP sockets by state after a burst of new connections,
	// sampled until they settle (guard connections close with the Core's
	// policy timeouts, not at once).
	decay := func(name string, n int, at []float64) map[string]any {
		t.Helper()
		burst := cpuPer(name, n)
		samples := []map[string]any{}
		start := time.Now()
		for _, at := range at {
			time.Sleep(time.Until(start.Add(time.Duration(at * float64(time.Second)))))
			stats := rgProcStats(t, corePID)
			out, _ := exec.Command("ss", "-tanH").Output()
			states, loopback := map[string]int{}, map[string]int{}
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				f := strings.Fields(line)
				if len(f) < 5 {
					continue
				}
				states[f[0]]++
				if strings.HasPrefix(f[3], "127.0.0.1:") && strings.HasPrefix(f[4], "127.0.0.1:") {
					loopback[f[0]]++
				}
			}
			core, _ := client.snapshot(ctx)
			samples = append(samples, map[string]any{"t": at, "fds": stats["fds"], "rss_mib": stats["rss_mib"], "threads": stats["threads"], "tcp": states, "loopback": loopback, "fd_kinds": rgFDKinds(t, corePID),
				"core_current_total": core.Global.CurrentTotal, "core_current_inbound": core.Global.CurrentInbound})
		}
		return map[string]any{"burst": burst, "samples": samples}
	}
	skipPerf := os.Getenv("MMWXC_GUARD_E2E_SKIP_PERF") == "1"
	perf := func(label string) map[string]any {
		t.Helper()
		result := map[string]any{}
		if skipPerf {
			return result
		}
		for round := 1; round <= 2; round++ {
			result[fmt.Sprintf("lat_r%d", round)] = cpuPer("g-lat", 300)
		}
		s0 := rgProcStats(t, corePID)
		bulk := run("bulk", "g-bulk", 3)
		s1 := rgProcStats(t, corePID)
		bulk["core_cpu_s"] = s1["cpu_s"] - s0["cpu_s"]
		result["bulk"] = bulk
		result["core_after"] = s1
		for round := 1; round <= 2; round++ {
			if v := result[fmt.Sprintf("lat_r%d", round)].(map[string]any); v["fails"].(float64) != 0 {
				t.Errorf("%s: authenticated connections failed: %v", label, v)
			}
		}
		return result
	}

	// Phase 1: an old FLVX panel (no reality_guard): today's behaviour, the
	// wrong-SNI probe is relayed to the dest (the steal the guard prevents).
	before := sniLog()
	probesOff := jsonMap(py("probe.py", "right=31002=www.flvx.test", "wrong=31002=www.example.com"))
	if probesOff["right"] != destCert || probesOff["wrong"] != destCert {
		t.Fatalf("unguarded probes: %v (want %s)", probesOff, destCert)
	}
	evidence["phase1_absent"] = map[string]any{"probes": probesOff, "dest_sni": sniSince(before), "functional": jsonMap(py("fetch.py", "ss-443="+strconv.Itoa(portOf["ss-443"]), "g-443="+strconv.Itoa(portOf["g-443"]), "h-443="+strconv.Itoa(portOf["h-443"]))),
		"guard_inbounds": strings.Count(listed(), flvx.GuardTagPrefix), "report": m.state.Report.RealityGuard}
	evidence["perf_guard_off"] = perf("guard off")
	evidence["decay_guard_off"] = decay("g-lat", 300, rgDecayAt("MMWXC_GUARD_E2E_DECAY_OFF"))

	// Phase 2: guard on.
	on := true
	setGuard(&on)
	apply(2)
	if s := m.state.Report.RealityGuard; s == nil || s.State != "on" || s.Nodes != 2 {
		t.Fatalf("guard status %+v", s)
	}
	if got := strings.Count(listed(), `"`+flvx.GuardTagPrefix); got != 2 {
		t.Fatalf("guard inbounds in the Core: %d\n%s", got, listed())
	}
	for _, tag := range []string{"flvxl-2", "flvxl-4"} {
		var cfg map[string]any
		json.Unmarshal([]byte(m.state.Applied[tag]), &cfg)
		dest := cfg["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)["dest"].(string)
		if !strings.HasPrefix(dest, "127.0.0.1:") {
			t.Fatalf("%s dest %s", tag, dest)
		}
	}
	if m.state.Applied["flvxl-1"] != unguarded["flvxl-1"] {
		t.Fatal("SS2022 node changed")
	}
	rulesRaw, _ := m.api(ctx, "lsrules")
	time.Sleep(3 * time.Second)
	idleOn := rgProcStats(t, corePID)
	before = sniLog()
	probesOn := jsonMap(py("probe.py", probeArgs...))
	sniOn := sniSince(before)
	for _, k := range []string{"right", "right-late", "h-right"} {
		if probesOn[k] != destCert {
			t.Errorf("%s must get the dest certificate %s: %v", k, destCert, probesOn[k])
		}
	}
	for _, k := range []string{"wrong", "nosni", "plain", "wrong-late", "nosni-late", "h-other", "h-wrong"} {
		if v, _ := probesOn[k].(string); !strings.HasPrefix(v, "closed:") {
			t.Errorf("%s relayed: %v", k, probesOn[k])
		}
	}
	// The dest saw only the allowed names (REALITY may add its own record
	// length probe with an allowed name), never a wrong or missing one.
	for name := range sniOn {
		if name != "www.flvx.test" && name != "alt.flvx.test" {
			t.Errorf("dest saw SNI %q through the guard: %v", name, sniOn)
		}
	}
	if sniOn["www.flvx.test"] < 2 || sniOn["alt.flvx.test"] < 1 {
		t.Errorf("dest saw SNIs %v; want www.flvx.test ×2 and alt.flvx.test ×1 at least", sniOn)
	}
	functional := jsonMap(py("fetch.py", "ss-443="+strconv.Itoa(portOf["ss-443"]), "g-443="+strconv.Itoa(portOf["g-443"]), "h-443="+strconv.Itoa(portOf["h-443"])))
	for k, v := range functional {
		if v != "peer=198.51.100.1 port=443" {
			t.Errorf("authenticated %s through the guarded Core: %v", k, v)
		}
	}
	guardsOn, _ := json.Marshal(m.state.Guards)
	reportOn := *m.state.Report.RealityGuard
	evidence["phase2_on"] = map[string]any{"probes": probesOn, "dest_sni": sniOn, "functional": functional, "report": reportOn, "guards": json.RawMessage(guardsOn),
		"rules": json.RawMessage(rulesRaw), "idle_guard_off": idleOff, "idle_guard_on": idleOn}
	evidence["perf_guard_on"] = perf("guard on")
	evidence["decay_guard_on"] = decay("g-lat", 300, rgDecayAt("MMWXC_GUARD_E2E_DECAY"))
	evidence["h_lat_guard_on"] = cpuPer("h-lat", 100)
	if os.Getenv("MMWXC_GUARD_E2E_SKIP_LATE") == "1" {
		raw, _ = json.MarshalIndent(evidence, "", "  ")
		os.WriteFile(os.Getenv("MMWXC_GUARD_E2E_OUT"), raw, 0644)
		return
	}

	// Late ClientHello of authenticated users through a relay that holds the
	// first client→landing chunk (whole: lost first segment / slow client) or
	// the rest after 300 bytes (split: a lost later segment).
	late := map[string]any{}
	lateRun := func(label, delay, mode string, n int) map[string]any {
		t.Helper()
		relay := exec.Command("ip", "netns", "exec", clientNS, "python3", filepath.Join(base, "relay.py"), delay, mode, rgLanding, "31002")
		if err := relay.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { relay.Process.Kill(); relay.Wait() }()
		time.Sleep(500 * time.Millisecond)
		r := run("lat", "gl-lat", n)
		late[label] = r
		return r
	}
	for _, delay := range []string{"0.5", "1.5"} {
		if r := lateRun("whole_"+delay+"s", delay, "whole", 30); r["fails"].(float64) != 0 {
			t.Errorf("late ClientHello %ss failed through the guard: %v", delay, r)
		}
	}
	if r := lateRun("split_0.15s", "0.15", "split", 30); r["fails"].(float64) != 0 {
		t.Errorf("split ClientHello 0.15 s failed: %v", r)
	}
	lateRun("split_0.5s", "0.5", "split", 30) // documented trade-off, recorded only
	// Control: the same guarded node without TCP_DEFER_ACCEPT, written
	// straight into the Core, fails the late ClientHello.
	var guarded map[string]any
	json.Unmarshal([]byte(m.state.Applied["flvxl-2"]), &guarded)
	delete(guarded["streamSettings"].(map[string]any), "sockopt")
	noDefer, _ := json.Marshal(map[string]any{"inbounds": []any{guarded}})
	if err := os.WriteFile(filepath.Join(dir, "nodefer.json"), noDefer, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.api(ctx, "rmi", "flvxl-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.api(ctx, "adi", filepath.Join(dir, "nodefer.json")); err != nil {
		t.Fatal(err)
	}
	control := lateRun("control_without_defer_accept_whole_0.5s", "0.5", "whole", 30)
	if control["fails"].(float64) == 0 {
		t.Errorf("control: a late ClientHello passed the guard even without TCP_DEFER_ACCEPT: %v", control)
	}
	// The Helper restores the node on its next pass once its record is reset.
	delete(m.state.Applied, "flvxl-2")
	apply(1)
	if r := lateRun("whole_0.5s_restored", "0.5", "whole", 30); r["fails"].(float64) != 0 {
		t.Errorf("restored node: %v", r)
	}
	evidence["late_clienthello"] = late

	// Phase 3: the guard switched off: back to the real dest, guards gone,
	// probes relayed again.
	off := false
	setGuard(&off)
	apply(2)
	if s := m.state.Report.RealityGuard; s != nil || strings.Contains(listed(), flvx.GuardTagPrefix) || len(m.state.Guards) != 0 {
		t.Fatalf("guard left after off: %+v %s", s, listed())
	}
	for tag, cfg := range unguarded {
		if m.state.Applied[tag] != cfg {
			t.Fatalf("%s not byte-identical to the unguarded render after off:\n%s\n%s", tag, m.state.Applied[tag], cfg)
		}
	}
	rulesOff, _ := m.api(ctx, "lsrules")
	before = sniLog()
	probesBack := jsonMap(py("probe.py", "wrong=31002=www.example.com", "nosni=31002="))
	if probesBack["wrong"] != destCert {
		t.Errorf("guard off: wrong-SNI probe not relayed again: %v", probesBack)
	}
	evidence["phase3_off"] = map[string]any{"probes": probesBack, "dest_sni": sniSince(before), "rules": json.RawMessage(rulesOff), "core_after": rgProcStats(t, corePID)}
	raw, _ = json.MarshalIndent(evidence, "", "  ")
	if err := os.WriteFile(os.Getenv("MMWXC_GUARD_E2E_OUT"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("evidence: %s", raw)
	if t.Failed() {
		cl, _ := os.ReadFile(filepath.Join(base, "client.log"))
		ll, _ := os.ReadFile(filepath.Join(base, "landing-core.log"))
		t.Logf("client log:\n%s\nlanding log:\n%s", cl, ll)
	}
}

func rgDecayAt(env string) []float64 {
	if v := os.Getenv(env); v != "" {
		out := []float64{}
		for _, f := range strings.Split(v, ",") {
			n, _ := strconv.ParseFloat(f, 64)
			out = append(out, n)
		}
		return out
	}
	return []float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 10, 12, 15}
}

// rgFDKinds sorts a process's fds: sockets still in a TCP table, sockets in
// none (closed but never released), and everything else.
func rgFDKinds(t *testing.T, pid int) map[string]int {
	t.Helper()
	inTable := map[string]bool{}
	for _, file := range []string{"/proc/net/tcp", "/proc/net/tcp6", "/proc/net/udp", "/proc/net/udp6", "/proc/net/unix"} {
		raw, _ := os.ReadFile(file)
		for i, line := range strings.Split(string(raw), "\n") {
			f := strings.Fields(line)
			if i == 0 || len(f) < 10 {
				continue
			}
			inode := f[9]
			if strings.HasSuffix(file, "unix") {
				inode = f[6]
			}
			inTable[inode] = true
		}
	}
	kinds := map[string]int{}
	entries, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	for _, e := range entries {
		link, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, e.Name()))
		switch {
		case err != nil:
		case strings.HasPrefix(link, "socket:["):
			if inTable[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] {
				kinds["socket_in_table"]++
			} else {
				kinds["socket_not_in_table"]++
			}
		default:
			kinds[strings.SplitN(link, ":", 2)[0]]++
		}
	}
	return kinds
}

func mustHash(t *testing.T, path string) string {
	t.Helper()
	hash, err := flvxFileHash(path)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// rgProcStats: RSS (MiB), threads, open fds and CPU seconds of a process.
func rgProcStats(t *testing.T, pid int) map[string]float64 {
	t.Helper()
	result := map[string]float64{}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseFloat(fields[1], 64)
		switch fields[0] {
		case "VmRSS:":
			result["rss_mib"] = v / 1024
		case "Threads:":
			result["threads"] = v
		}
	}
	fds, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	result["fds"] = float64(len(fds))
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+2:]))
	utime, _ := strconv.ParseFloat(fields[11], 64)
	stime, _ := strconv.ParseFloat(fields[12], 64)
	result["cpu_s"] = (utime + stime) / 100
	return result
}

const rgDestServer = `import socket, ssl, sys, threading
log = open(sys.argv[1], "a", buffering=1)
base = sys.argv[2]
def serve(port, handler):
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("0.0.0.0", port)); s.listen(1024)
    while True:
        c, a = s.accept()
        threading.Thread(target=handler, args=(c, a, port), daemon=True).start()
def banner(c, a, port):
    log.write("tcp %d %s\n" % (port, a[0]))
    try:
        c.sendall(("peer=%s port=%d\n" % (a[0], port)).encode())
    except Exception:
        pass
    c.close()
def bulk(c, a, port):
    c.settimeout(30)
    try:
        n = int(c.recv(32).strip())
        chunk = b"\0" * 65536
        while n > 0:
            k = min(n, len(chunk)); c.sendall(chunk[:k]); n -= k
    except Exception:
        pass
    c.close()
def echo1(c, a, port):
    c.settimeout(10)
    try:
        if c.recv(1):
            c.sendall(b"x")
    except Exception:
        pass
    c.close()
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(base + "/dest.crt", base + "/dest.key")
def sni(sock, name, _):
    log.write("tls sni=%s\n" % name)
ctx.sni_callback = sni
def tls(c, a, port):
    log.write("tcp %d %s\n" % (port, a[0]))
    c.settimeout(10)
    try:
        t = ctx.wrap_socket(c, server_side=True)
        t.sendall(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
        t.close()
    except Exception:
        c.close()
threading.Thread(target=serve, args=(443, banner), daemon=True).start()
threading.Thread(target=serve, args=(9000, bulk), daemon=True).start()
threading.Thread(target=serve, args=(9001, echo1), daemon=True).start()
serve(8443, tls)
`

const rgFetch = `import json, socket, sys, time
out = {}
for item in sys.argv[1:]:
    name, port = item.split("=")
    for attempt in range(60):
        try:
            c = socket.create_connection(("127.0.0.1", int(port)), timeout=5); break
        except OSError:
            time.sleep(0.05)
    else:
        out[name] = "client-unreachable"; continue
    c.settimeout(5)
    try:
        data = b""
        while not data.endswith(b"\n"):
            chunk = c.recv(256)
            if not chunk:
                break
            data += chunk
        out[name] = data.decode(errors="replace").strip()
    except Exception:
        out[name] = ""
    c.close()
print(json.dumps(out))
`

// probe: unauthenticated TLS 1.3 ClientHello (or plain bytes for sni "-")
// straight at the landing port; "cert:<sha256 prefix>" or "closed:<error>".
const rgProbe = `import hashlib, json, socket, ssl, sys, time
out = {}
for item in sys.argv[1:]:
    parts = item.split("=")
    name, port, sni = parts[:3]
    delay = float(parts[3]) if len(parts) > 3 else 0
    try:
        c = socket.create_connection(("203.0.113.1", int(port)), timeout=5); c.settimeout(5)
        if delay:
            time.sleep(delay)
        if sni == "-":
            c.sendall(b"GET / HTTP/1.1\r\nHost: www.flvx.test\r\n\r\n")
            data = c.recv(64)
            out[name] = ("data:%d" % len(data)) if data else "closed:EOF"
            c.close(); continue
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
        ctx.minimum_version = ssl.TLSVersion.TLSv1_3
        t = ctx.wrap_socket(c, server_hostname=(sni or None))
        der = t.getpeercert(binary_form=True)
        out[name] = "cert:" + hashlib.sha256(der).hexdigest()[:16]
        t.close()
    except Exception as e:
        out[name] = "closed:" + type(e).__name__
print(json.dumps(out))
`

const rgMeasure = `import json, socket, statistics, sys, time
mode, port, n = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
def conn():
    for attempt in range(60):
        try:
            return socket.create_connection(("127.0.0.1", port), timeout=10)
        except OSError:
            time.sleep(0.05)
    raise SystemExit("unreachable")
if mode == "bulk":
    rates = []
    for i in range(n):
        c = conn(); c.settimeout(30)
        size = 128 << 20
        t0 = time.perf_counter(); c.sendall(b"%d\n" % size); got = 0
        while got < size:
            b = c.recv(1 << 20)
            if not b:
                break
            got += len(b)
        dt = time.perf_counter() - t0; c.close()
        rates.append(round(got / dt / (1 << 20), 1) if got == size else 0.0)
    print(json.dumps({"mib_s": rates}))
else:
    lat, fails = [], 0
    for i in range(n):
        try:
            t0 = time.perf_counter(); c = conn(); c.settimeout(10); c.sendall(b"GET / HTTP/1.1\r\nHost: lat.flvx.test\r\n\r\n")
            b = c.recv(1); dt = time.perf_counter() - t0; c.close()
            if b == b"x":
                lat.append(dt * 1000)
            else:
                fails += 1
        except Exception:
            fails += 1
    lat.sort()
    q = lambda p: round(lat[min(len(lat) - 1, int(len(lat) * p))], 2) if lat else None
    print(json.dumps({"n": n, "fails": fails, "p50_ms": q(0.5), "p90_ms": q(0.9), "p99_ms": q(0.99), "mean_ms": round(statistics.mean(lat), 2) if lat else None}))
`

// relay 127.0.0.1:25001 → landing, holding the first client→landing chunk
// ("whole") or everything after its first 300 bytes ("split") for <delay> s.
const rgRelay = `import socket, sys, threading, time
delay, mode, host, port = float(sys.argv[1]), sys.argv[2], sys.argv[3], int(sys.argv[4])
def pipe(a, b, first):
    try:
        while True:
            d = a.recv(65536)
            if not d:
                break
            if first and mode == "split":
                b.sendall(d[:300]); d = d[300:]
                time.sleep(delay); first = False
            elif first:
                time.sleep(delay); first = False
            b.sendall(d)
    except Exception:
        pass
    for x in (a, b):
        try: x.shutdown(socket.SHUT_RDWR)
        except Exception: pass
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("127.0.0.1", 25001)); s.listen(256)
while True:
    c, _ = s.accept()
    u = socket.create_connection((host, port))
    threading.Thread(target=pipe, args=(c, u, True), daemon=True).start()
    threading.Thread(target=pipe, args=(u, c, False), daemon=True).start()
`
