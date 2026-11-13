package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// Real FLVX Core + real Xray (the same build, as client) and mihomo clients
// in a private network namespace: the Helper's own reconcile detects VLESS
// Encryption with its config test, renders vless_enc nodes (random + Vision,
// native + Vision, random without flow) and they must pass TCP byte-exact,
// UDP to two targets, traffic accounting both ways, the online-IP cap, the
// connection cap and disable / re-enable, all without a Core restart.
func TestFLVXVLESSEncNetnsE2E(t *testing.T) {
	lpRunInNetns(t, "TestFLVXVLESSEncNetnsInner", "MMWXC_TEST_VLESS_ENC_EVIDENCE")
}

// The Core's default freedom rule refuses private and reserved targets
// (203.0.113.0/24 included) for VLESS / SS / VMess / Trojan inbounds, as
// for every SS2022 / VLESS Reality node in production; the echoes of this
// lab therefore sit on public-looking addresses of the private namespace.
const (
	veEchoTCP = "11.0.0.10"
	veEchoA   = "11.0.0.11"
	veEchoB   = "11.0.0.12"
	// Not 53 / 443: Xray clients without Vision send UDP to those ports as
	// one plain VLESS UDP flow (single target), not XUDP.
	veEchoUDP = 5353
)

func veDial(listener int) (net.Conn, error) { return lpDialTo(listener, veEchoTCP, 80) }

type veNode struct {
	node                   flvx.Node
	mode, flow, encryption string
}

func veMakeNode(t *testing.T, id string, port int, mode, flow string) veNode {
	t.Helper()
	seed := make([]byte, 32)
	rand.Read(seed)
	// As xray x25519 / vlessenc generate it.
	seed[0] &= 248
	seed[31] = seed[31]&127 | 64
	key, err := ecdh.X25519().NewPrivateKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 16)
	rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	enabled := true
	n := flvx.Node{ID: id, Owner: flvx.Owner{ID: "7", Name: "Alice"}, Name: "VE " + id, Protocol: flvx.ProtocolVLESSEnc, Port: port, Enabled: &enabled, MaxOnlineIPs: 2,
		VLESSEnc: &flvx.VLESSEnc{UUID: fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), Flow: flow,
			Decryption: "mlkem768x25519plus." + mode + ".600s." + base64.RawURLEncoding.EncodeToString(seed)}}
	return veNode{node: n, mode: mode, flow: flow, encryption: "mlkem768x25519plus." + mode + ".0rtt." + base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())}
}

func TestFLVXVLESSEncNetnsInner(t *testing.T) {
	if os.Getenv("MMWXC_LANDING_INNER") != "1" {
		t.Skip("runs only inside the E2E namespace")
	}
	raiseNoFile()
	coreBin, mihomo := os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED"), os.Getenv("MMWXC_TEST_MIHOMO")
	ctx := context.Background()
	dir := t.TempDir()
	if keep := os.Getenv("MMWXC_VE_E2E_DIR"); keep != "" { // keeps the logs for diagnosis
		dir = keep
	}
	evidence := map[string]any{"started_at": time.Now().UTC().Format(time.RFC3339)}
	for _, address := range []string{veEchoTCP, veEchoA, veEchoB} {
		if out, err := exec.Command("ip", "addr", "add", address+"/32", "dev", "d0").CombinedOutput(); err != nil {
			t.Fatalf("ip addr add %s: %v %s", address, err, out)
		}
	}
	lpStartFixturesAt(t, veEchoTCP, veEchoA, veEchoB, veEchoUDP)
	// The private-target refusal holds for vless_enc too (checked below).
	lpStartFixtures(t)

	apiPort := flvxFreePort(t)
	socket := filepath.Join(dir, "control.sock")
	corePath := filepath.Join(dir, "core.json")
	base := flvx.BaseConfig(apiPort)
	if os.Getenv("MMWXC_VE_E2E_DIR") != "" {
		base["log"] = map[string]any{"loglevel": "debug"}
	}
	stopCore := flvxStartCore(t, coreBin, corePath, socket, base)
	core := newCoreClient(socket)
	flvxWait(t, func() bool { _, err := core.snapshot(ctx); return err == nil })
	started, _ := core.snapshot(ctx)
	hash, err := flvxFileHash(coreBin)
	if err != nil {
		t.Fatal(err)
	}
	ves := []veNode{veMakeNode(t, "51", 30201, "random", "xtls-rprx-vision"), veMakeNode(t, "52", 30202, "native", "xtls-rprx-vision"), veMakeNode(t, "53", 30203, "random", "")}
	nodes := []flvx.Node{}
	byID := map[string]int{}
	for i, v := range ves {
		nodes = append(nodes, v.node)
		byID[v.node.ID] = i
	}
	m := &flvxManager{dir: filepath.Join(dir, "state"), binary: coreBin, core: core, logs: newPeriodicLogLimiter()}
	m.state = flvxLocalState{Payload: flvx.Payload{Version: 1, Status: "active", Config: flvx.Config{PortMin: 30000, PortMax: 30999, ResetDay: 1, MemoryMB: 128, CPUPercent: 50},
		Desired: flvx.Desired{Revision: 1, Nodes: nodes}, Artifact: flvx.Artifact{Version: "f6f5844", SHA256: hash}}, APIPort: apiPort, Counters: map[string]*flvx.Counter{}, Applied: map[string]string{}, Report: flvx.Report{Generation: "netns-e2e"}}
	m.setup = func(context.Context) error { return nil }
	m.stop = func(context.Context) error { stopCore(); return nil }
	m.cleanup = func(context.Context) error { return os.RemoveAll(m.dir) }
	m.readOfficial = func() ([]int, error) { return []int{}, nil }
	m.apiCall = m.api
	apply := func() {
		t.Helper()
		if err := m.reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	report := func(id string) flvx.NodeStatus {
		for _, n := range m.state.Report.Nodes {
			if n.ID == id {
				return n
			}
		}
		t.Fatalf("node %s missing from report", id)
		return flvx.NodeStatus{}
	}
	start := time.Now()
	apply()
	evidence["first_reconcile_with_config_test_ms"] = time.Since(start).Milliseconds()
	for _, n := range nodes {
		if s := report(n.ID); s.State != "active" {
			t.Fatalf("node %s not active: %+v", n.ID, s)
		}
		want, _ := json.Marshal(flvx.Inbound(n))
		if m.state.Applied[n.Tag()] != string(want) {
			t.Fatalf("node %s applied %s", n.ID, m.state.Applied[n.Tag()])
		}
	}
	if got := m.state.Report.Protocols["vless_enc"]; !reflect.DeepEqual(got, flvx.ProtocolCap{UDP: true, Versions: []int{1}}) {
		t.Fatalf("real Core must advertise vless_enc: %+v", m.state.Report.Protocols)
	}
	if f := m.state.CoreFeatures; f == nil || f.VLESSEnc == nil || !*f.VLESSEnc || f.SHA256 != hash {
		t.Fatalf("config test result %+v", f)
	}
	evidence["advertised"] = m.state.Report.Protocols
	ports := m.appliedPorts()
	if !reflect.DeepEqual(ports.TCP, []uint32{30201, 30202, 30203}) || len(ports.UDP) != 0 {
		t.Fatalf("machine protection ports %+v (vless_enc is TCP only)", ports)
	}
	evidence["machine_protection_ports"] = ports
	corePID := veProcess(t, corePath)
	evidence["core_rss_kb_idle"] = veRSS(corePID)

	kinds := []*lpKind{
		{name: "Xray random + Vision", proxy: "xray-51", node: "51"}, {name: "Xray native + Vision", proxy: "xray-52", node: "52"}, {name: "Xray random, no flow", proxy: "xray-53", node: "53"},
		{name: "mihomo random + Vision", proxy: "mihomo-51", node: "51"},
	}
	restartXray := veStartXray(t, coreBin, dir, kinds[:3], ves, byID)
	stopMihomo := veStartMihomo(t, mihomo, dir, kinds[3:], ves, byID)

	results := map[string]lpResult{}
	for _, k := range kinds {
		r := lpResult{}
		results[k.name] = r
		before := report(k.node)
		start := time.Now()
		if err := lpBulkEchoTo(k.listeners[0], lpBulk, veEchoTCP); err != nil {
			t.Fatalf("%s TCP: %v", k.name, err)
		}
		r["tcp_2mib_echo_seconds"] = time.Since(start).Seconds()
		if err := lpUDPTwoTargetsVia(k.listeners[0], k.proxy, veEchoA, veEchoB, veEchoUDP); err != nil {
			t.Fatalf("%s UDP: %v", k.name, err)
		}
		r["udp_two_targets"] = "ok"
		time.Sleep(300 * time.Millisecond)
		apply()
		after := report(k.node)
		up, down := after.UpBytes-before.UpBytes, after.DownBytes-before.DownBytes
		r["up_bytes"], r["down_bytes"] = up, down
		if up < lpBulk || down < lpBulk {
			t.Fatalf("%s accounting up %d / down %d < %d", k.name, up, down, lpBulk)
		}
		t.Logf("%s: TCP 2 MiB echo ok, UDP two targets ok, billed up %d / down %d", k.name, up, down)
	}
	// mihomo's pooled sessions would count toward the caps below.
	stopMihomo()
	// random vs native, same client and path (32 MiB echo, best of 3).
	for _, k := range kinds[:3] {
		best := time.Duration(0)
		for i := 0; i < 3; i++ {
			start := time.Now()
			if err := lpBulkEchoTo(k.listeners[0], 32<<20, veEchoTCP); err != nil {
				t.Fatalf("%s 32 MiB: %v", k.name, err)
			}
			if d := time.Since(start); best == 0 || d < best {
				best = d
			}
		}
		results[k.name]["echo_32mib_best_seconds"] = best.Seconds()
		results[k.name]["echo_32mib_mbit_per_s"] = float64(64<<20*8) / best.Seconds() / 1e6
	}

	// Online-IP cap 2: two sources hold streams, the third is refused.
	for _, k := range kinds[:3] {
		held := []net.Conn{}
		for _, listener := range k.listeners[:2] {
			c, err := veDial(listener)
			if err == nil {
				err = lpEcho(c, 512)
			}
			if err != nil {
				t.Fatalf("%s online IP within cap: %v", k.name, err)
			}
			held = append(held, c)
		}
		if k.node == "51" {
			evidence["core_rss_kb_with_2_streams"] = veRSS(corePID)
		}
		c, err := veDial(k.listeners[2])
		if err == nil {
			err = lpEcho(c, 512)
			c.Close()
		}
		if err == nil {
			t.Fatalf("%s: third source IP admitted over the online-IP cap", k.name)
		}
		for _, c := range held {
			c.Close()
		}
		results[k.name]["online_ip_cap_2"] = "third source refused"
	}

	// Connection cap 6: twelve concurrent streams, the Core holds the cap.
	for i := range m.state.Payload.Desired.Nodes {
		m.state.Payload.Desired.Nodes[i].MaxOnlineIPs, m.state.Payload.Desired.Nodes[i].MaxConnections = 0, 6
	}
	m.state.Payload.Desired.Revision++
	apply()
	restartXray()
	tagTotal := func(tag string) int64 {
		snapshot, err := core.snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		total := int64(0)
		for _, u := range snapshot.Users {
			if u.Identity.InboundTag == tag {
				total += u.CurrentTotal
			}
		}
		return total
	}
	// 入站+出站 counts TIME_WAIT sockets too (60 s on Linux).
	for _, k := range kinds[:3] {
		until := time.Now().Add(75 * time.Second)
		for tagTotal("flvxl-"+k.node) > 0 && time.Now().Before(until) {
			time.Sleep(200 * time.Millisecond)
		}
	}
	for _, k := range kinds[:3] {
		held, ok := []net.Conn{}, 0
		for i := 0; i < 12; i++ {
			c, err := veDial(k.listeners[0])
			if err == nil && lpEcho(c, 256) == nil {
				ok++
				held = append(held, c)
			} else if c != nil {
				c.Close()
			}
		}
		total := tagTotal("flvxl-" + k.node)
		for _, c := range held {
			c.Close()
		}
		results[k.name]["connection_cap_6"] = map[string]any{"streams_ok": ok, "streams_tried": 12, "core_total": total}
		if ok == 0 || ok == 12 || total > 6 {
			snapshot, _ := core.snapshot(ctx)
			raw, _ := json.Marshal(snapshot)
			sockets, _ := exec.Command("ss", "-tunap").CombinedOutput()
			t.Fatalf("%s connection cap 6: %d of 12 streams ok, Core total %d\nsnapshot %s\nsockets\n%s", k.name, ok, total, raw, sockets)
		}
		t.Logf("%s: connection cap 6 held (%d of 12 streams, Core total %d)", k.name, ok, total)
		time.Sleep(time.Second)
	}
	for i := range m.state.Payload.Desired.Nodes {
		m.state.Payload.Desired.Nodes[i].MaxConnections = 0
	}
	m.state.Payload.Desired.Revision++
	apply()

	// 40 idle streams on node 51: Core memory per connection.
	{
		held := []net.Conn{}
		for i := 0; i < 40; i++ {
			c, err := veDial(kinds[0].listeners[i%2])
			if err == nil && lpEcho(c, 256) == nil {
				held = append(held, c)
			} else if c != nil {
				c.Close()
			}
		}
		time.Sleep(time.Second)
		evidence["core_rss_kb_with_40_streams"] = veRSS(corePID)
		evidence["streams_held"] = len(held)
		for _, c := range held {
			c.Close()
		}
	}

	// Disable cuts a live stream and refuses new ones; re-enable restores.
	for _, k := range kinds[:3] {
		live, err := veDial(k.listeners[1])
		if err == nil {
			err = lpEcho(live, 256)
		}
		if err != nil {
			t.Fatalf("%s live stream before disable: %v", k.name, err)
		}
		no, yes := false, true
		m.state.Payload.Desired.Nodes[byID[k.node]].Enabled = &no
		m.state.Payload.Desired.Revision++
		apply()
		if lpEcho(live, 256) == nil {
			t.Fatalf("%s: live stream survived disable", k.name)
		}
		live.Close()
		if c, err := veDial(k.listeners[1]); err == nil {
			if lpEcho(c, 256) == nil {
				t.Fatalf("%s: new stream admitted while disabled", k.name)
			}
			c.Close()
		}
		m.state.Payload.Desired.Nodes[byID[k.node]].Enabled = &yes
		m.state.Payload.Desired.Revision++
		apply()
		var c net.Conn
		for try := 0; try < 20; try++ {
			if c, err = veDial(k.listeners[1]); err == nil {
				if err = lpEcho(c, 256); err == nil {
					break
				}
				c.Close()
			}
			time.Sleep(250 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("%s after re-enable: %v", k.name, err)
		}
		c.Close()
		results[k.name]["disable_reenable"] = "live stream cut, new refused, restored"
	}

	// Like every VLESS / SS2022 node: a reserved target is refused.
	if c, err := lpDialTo(kinds[0].listeners[0], lpEchoTCP, 80); err == nil {
		if lpEcho(c, 64) == nil {
			t.Fatal("vless_enc node reached a reserved target")
		}
		c.Close()
	}
	evidence["reserved_target_refused"] = true

	final, _ := core.snapshot(ctx)
	if !final.StartedAt.Equal(started.StartedAt) {
		t.Fatal("hot apply restarted the Core")
	}
	evidence["core_rss_kb_end"] = veRSS(corePID)
	evidence["results"] = results
	evidence["core_restarted"] = false
	evidence["finished_at"] = time.Now().UTC().Format(time.RFC3339)
	// The secret never reaches the Helper's log limiter or the report.
	reportRaw, _ := json.Marshal(m.state.Report)
	for _, v := range ves {
		if strings.Contains(string(reportRaw), v.node.VLESSEnc.Decryption) {
			t.Fatal("decryption string in the report")
		}
	}
	raw, _ := json.MarshalIndent(evidence, "", "  ")
	if path := os.Getenv("MMWXC_LANDING_EVIDENCE"); path != "" {
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("evidence:\n%s", raw)
}

// veStartXray runs the Core build as an Xray client: one SOCKS listener per
// (kind, source address), each routed to a VLESS Encryption outbound dialing
// the node on that address (the kernel then uses it as the source).
func veStartXray(t *testing.T, bin, dir string, kinds []*lpKind, ves []veNode, byID map[string]int) func() {
	t.Helper()
	inbounds, outbounds, rules := []any{}, []any{}, []any{}
	port := 42000
	for _, k := range kinds {
		v := ves[byID[k.node]]
		for i, source := range lpSources {
			name := fmt.Sprintf("%s-%d", k.proxy, i)
			port++
			inbounds = append(inbounds, map[string]any{"tag": "in-" + name, "listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"udp": true, "ip": "127.0.0.1"}})
			out := map[string]any{"address": source, "port": v.node.Port, "id": v.node.VLESSEnc.UUID, "encryption": v.encryption}
			if v.flow != "" {
				out["flow"] = v.flow
			}
			outbounds = append(outbounds, map[string]any{"tag": "out-" + name, "protocol": "vless", "settings": out, "streamSettings": map[string]any{"network": "tcp", "security": "none"}})
			rules = append(rules, map[string]any{"inboundTag": []string{"in-" + name}, "outboundTag": "out-" + name})
			k.listeners = append(k.listeners, port)
		}
	}
	level := "warning"
	if os.Getenv("MMWXC_VE_E2E_DIR") != "" {
		level = "debug"
	}
	config := map[string]any{"log": map[string]any{"loglevel": level}, "inbounds": inbounds, "outbounds": outbounds, "routing": map[string]any{"rules": rules}}
	raw, _ := json.Marshal(config)
	path := filepath.Join(dir, "xray-client.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	logFile, _ := os.Create(filepath.Join(dir, "xray-client.log"))
	t.Cleanup(func() { logFile.Close() })
	var command *exec.Cmd
	start := func() {
		command = exec.Command(bin, "run", "-config", path)
		command.Stdout, command.Stderr = logFile, logFile
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		flvxWait(t, func() bool {
			c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
			if err == nil {
				c.Close()
			}
			return err == nil
		})
	}
	stop := func() { command.Process.Kill(); command.Wait() }
	start()
	t.Cleanup(stop)
	return func() { stop(); start() }
}

// veStartMihomo: mihomo's vless "encryption" client (Clash Meta subscription).
func veStartMihomo(t *testing.T, bin, dir string, kinds []*lpKind, ves []veNode, byID map[string]int) func() {
	t.Helper()
	proxies, listeners := []any{}, []any{}
	port := 43000
	for _, k := range kinds {
		v := ves[byID[k.node]]
		for i, source := range lpSources {
			name := fmt.Sprintf("%s-%d", k.proxy, i)
			p := map[string]any{"name": name, "type": "vless", "server": source, "port": v.node.Port, "uuid": v.node.VLESSEnc.UUID, "network": "tcp", "tls": false, "udp": true, "encryption": v.encryption}
			if v.flow != "" {
				p["flow"] = v.flow
			}
			proxies = append(proxies, p)
			port++
			listeners = append(listeners, map[string]any{"name": "in-" + name, "type": "mixed", "listen": "127.0.0.1", "port": port, "proxy": name, "udp": true})
			k.listeners = append(k.listeners, port)
		}
	}
	config := map[string]any{"log-level": "warning", "ipv6": false, "allow-lan": false, "mode": "rule", "dns": map[string]any{"enable": false}, "proxies": proxies, "listeners": listeners, "rules": []string{"MATCH,DIRECT"}}
	raw, _ := json.Marshal(config)
	path := filepath.Join(dir, "mihomo.yaml")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	logFile, _ := os.Create(filepath.Join(dir, "mihomo.log"))
	t.Cleanup(func() { logFile.Close() })
	command := exec.Command(bin, "-f", path, "-d", dir)
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			command.Process.Kill()
			command.Wait()
		}
	}
	t.Cleanup(stop)
	flvxWait(t, func() bool {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
		if err == nil {
			c.Close()
		}
		return err == nil
	})
	return stop
}

// veProcess finds the Core started with config path.
func veProcess(t *testing.T, path string) int {
	t.Helper()
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if strings.Contains(string(raw), path) && strings.Contains(string(raw), "run") {
			return pid
		}
	}
	t.Fatal("Core process not found")
	return 0
}

func veRSS(pid int) int64 {
	raw, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				v, _ := strconv.ParseInt(fields[1], 10, 64)
				return v
			}
		}
	}
	return 0
}
