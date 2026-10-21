package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// Real FLVX Core + real mihomo clients, all inside one private network
// namespace (the host's network is never touched). The Helper's own
// reconcile renders AnyTLS, Snell v4, Snell v5 (TLS obfs) and Mieru into the
// Core over the real Xray CLI; every protocol must pass TCP byte-exact, UDP
// to two targets, traffic accounting both ways, the online-IP cap, the
// connection cap and disable / re-enable, all without a Core restart.
//
//	203.0.113.10:80        TCP echo
//	203.0.113.11/.12:53    UDP echo answering "A:" / "B:" + payload
//	203.0.113.20–.22       client source addresses (one mihomo listener each)
const (
	lpEchoTCP = "203.0.113.10"
	lpEchoA   = "203.0.113.11"
	lpEchoB   = "203.0.113.12"
	lpBulk    = 2 << 20
)

var lpSources = []string{"203.0.113.20", "203.0.113.21", "203.0.113.22"}

func TestFLVXLandingProtocolsNetnsE2E(t *testing.T) {
	lpRunInNetns(t, "TestFLVXLandingProtocolsNetnsInner", "MMWXC_TEST_LANDING_EVIDENCE")
}

// lpRunInNetns re-runs inner inside a throwaway namespace holding the
// fixture addresses; evidenceEnv names the file receiving its evidence.
func lpRunInNetns(t *testing.T, inner, evidenceEnv string) {
	core, mihomo := os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED"), os.Getenv("MMWXC_TEST_MIHOMO")
	if core == "" || mihomo == "" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_TEST_FLVX_CORE_FIXED, MMWXC_TEST_MIHOMO and run as root")
	}
	name := "lpe" + strconv.Itoa(os.Getpid()%100000)
	ns := flvxNetns{t: t}
	flvxNetnsCleanup(name)
	t.Cleanup(func() { flvxNetnsCleanup(name) })
	ns.sh("ip", "netns", "add", name)
	ns.sh("ip", "-n", name, "link", "set", "lo", "up")
	ns.sh("ip", "-n", name, "link", "add", "d0", "type", "dummy")
	ns.sh("ip", "-n", name, "link", "set", "d0", "up")
	for _, address := range append([]string{lpEchoTCP, lpEchoA, lpEchoB}, lpSources...) {
		ns.sh("ip", "-n", name, "addr", "add", address+"/32", "dev", "d0")
	}
	evidence := os.Getenv(evidenceEnv)
	if evidence == "" {
		evidence = filepath.Join(t.TempDir(), "evidence.json")
	}
	command := exec.Command("ip", "netns", "exec", name, os.Args[0], "-test.run", "^"+inner+"$", "-test.v", "-test.timeout", "15m")
	command.Env = append(os.Environ(), "MMWXC_LANDING_INNER=1", "MMWXC_LANDING_EVIDENCE="+evidence)
	out, err := command.CombinedOutput()
	t.Logf("inner run:\n%s", out)
	if err != nil {
		t.Fatalf("inner E2E failed: %v", err)
	}
}

type lpKind struct {
	name, proxy string
	node        string // node ID
	listeners   []int  // one per source address
}

type lpResult map[string]any

func TestFLVXLandingProtocolsNetnsInner(t *testing.T) {
	if os.Getenv("MMWXC_LANDING_INNER") != "1" {
		t.Skip("runs only inside the E2E namespace")
	}
	raiseNoFile()
	coreBin, mihomo := os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED"), os.Getenv("MMWXC_TEST_MIHOMO")
	ctx := context.Background()
	dir := t.TempDir()
	evidence := map[string]any{"started_at": time.Now().UTC().Format(time.RFC3339)}
	lpStartFixtures(t)

	// The FLVX Core exactly as the Helper configures it (BaseConfig).
	apiPort := flvxFreePort(t)
	socket := filepath.Join(dir, "control.sock")
	stopCore := flvxStartCore(t, coreBin, filepath.Join(dir, "core.json"), socket, flvx.BaseConfig(apiPort))
	core := newCoreClient(socket)
	flvxWait(t, func() bool { _, err := core.snapshot(ctx); return err == nil })
	started, _ := core.snapshot(ctx)
	hash, err := flvxFileHash(coreBin)
	if err != nil {
		t.Fatal(err)
	}

	nodes := helperProtocolNodes(t)
	for i := range nodes {
		nodes[i].Port = 30101 + i
		nodes[i].MaxConnections, nodes[i].MaxOnlineIPs = 0, 2
	}
	byID := map[string]int{}
	for i, n := range nodes {
		byID[n.ID] = i
	}
	m := &flvxManager{dir: filepath.Join(dir, "state"), binary: coreBin, core: core, logs: newPeriodicLogLimiter()}
	m.state = flvxLocalState{Payload: flvx.Payload{Version: 1, Status: "active", Config: flvx.Config{PortMin: 30000, PortMax: 30999, ResetDay: 1, MemoryMB: 128, CPUPercent: 50},
		Desired: flvx.Desired{Revision: 1, Nodes: nodes}, Artifact: flvx.Artifact{Version: "fixed", SHA256: hash}}, APIPort: apiPort, Counters: map[string]*flvx.Counter{}, Applied: map[string]string{}, Report: flvx.Report{Generation: "netns-e2e"}}
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
	apply()
	for _, n := range nodes {
		if s := report(n.ID); s.State != "active" {
			t.Fatalf("node %s (%s) not active: %+v", n.ID, n.Protocol, s)
		}
	}
	if caps := m.state.Report.Protocols; caps["anytls"].UDP != true || caps["snell"].UDP != true || caps["mieru"].UDP != true {
		t.Fatalf("fixed Core must advertise all three with UDP: %+v", caps)
	}
	evidence["advertised"] = m.state.Report.Protocols
	evidence["listening_report"] = m.state.Report.Listening

	// mihomo: one mixed listener per (proxy, source address).
	kinds := []*lpKind{{name: "AnyTLS", proxy: "anytls", node: "41"}, {name: "Snell v4", proxy: "snell4", node: "42"}, {name: "Snell v5 + TLS obfs", proxy: "snell5", node: "44"}, {name: "Mieru TCP", proxy: "mieru-tcp", node: "43"}, {name: "Mieru UDP", proxy: "mieru-udp", node: "43"}}
	restartMihomo := lpStartMihomo(t, mihomo, dir, kinds, nodes, byID)

	results := map[string]lpResult{}
	for _, k := range kinds {
		r := lpResult{}
		results[k.name] = r
		before := report(k.node)
		// 1. TCP: 2 MiB up and back, byte-exact.
		start := time.Now()
		if err := lpBulkEcho(k.listeners[0], lpBulk); err != nil {
			t.Fatalf("%s TCP: %v", k.name, err)
		}
		r["tcp_2mib_seconds"] = time.Since(start).Seconds()
		// 2. UDP: one session, two targets, every reply from its own target.
		if err := lpUDPTwoTargets(k.listeners[0], k.proxy); err != nil {
			t.Fatalf("%s UDP: %v", k.name, err)
		}
		r["udp_two_targets"] = "ok"
		// 3. Accounting: the node's inbound counters cover both directions.
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

	// 4. Online-IP cap 2: two sources hold streams, the third is refused.
	for _, k := range kinds[:4] {
		held := []net.Conn{}
		for _, listener := range k.listeners[:2] {
			c, err := lpDial(listener)
			if err == nil {
				err = lpEcho(c, 512)
			}
			if err != nil {
				t.Fatalf("%s online IP within cap: %v", k.name, err)
			}
			held = append(held, c)
		}
		c, err := lpDial(k.listeners[2])
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

	// 5. Connection cap 6: twelve concurrent streams, the Core holds the cap.
	for i := range m.state.Payload.Desired.Nodes {
		m.state.Payload.Desired.Nodes[i].MaxOnlineIPs, m.state.Payload.Desired.Nodes[i].MaxConnections = 0, 6
	}
	m.state.Payload.Desired.Revision++
	apply()
	// Pooled client sessions from the steps above count toward the cap; start
	// from fresh clients and a drained Core.
	restartMihomo()
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
	for _, k := range kinds[:4] {
		until := time.Now().Add(20 * time.Second)
		for tagTotal("flvxl-"+k.node) > 0 && time.Now().Before(until) {
			time.Sleep(200 * time.Millisecond)
		}
	}
	for _, k := range kinds[:4] {
		held, ok := []net.Conn{}, 0
		for i := 0; i < 12; i++ {
			c, err := lpDial(k.listeners[0])
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
			t.Fatalf("%s connection cap 6: %d of 12 streams ok, Core total %d", k.name, ok, total)
		}
		t.Logf("%s: connection cap 6 held (%d of 12 streams, Core total %d)", k.name, ok, total)
		time.Sleep(time.Second)
	}
	for i := range m.state.Payload.Desired.Nodes {
		m.state.Payload.Desired.Nodes[i].MaxConnections = 0
	}
	m.state.Payload.Desired.Revision++
	apply()

	// 6. Disable cuts a live stream and refuses new ones; re-enable restores.
	for _, k := range kinds {
		live, err := lpDial(k.listeners[1])
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
		if c, err := lpDial(k.listeners[1]); err == nil {
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
			if c, err = lpDial(k.listeners[1]); err == nil {
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

	final, _ := core.snapshot(ctx)
	if !final.StartedAt.Equal(started.StartedAt) {
		t.Fatal("hot apply restarted the Core")
	}
	ports := m.appliedPorts()
	evidence["machine_protection_ports"] = ports
	evidence["results"] = results
	evidence["core_restarted"] = false
	evidence["finished_at"] = time.Now().UTC().Format(time.RFC3339)
	raw, _ := json.MarshalIndent(evidence, "", "  ")
	if path := os.Getenv("MMWXC_LANDING_EVIDENCE"); path != "" {
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("evidence:\n%s", raw)
}

func lpStartFixtures(t *testing.T) {
	t.Helper()
	l, err := net.Listen("tcp4", lpEchoTCP+":80")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	for address, prefix := range map[string]string{lpEchoA: "A:", lpEchoB: "B:"} {
		u, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(address), Port: 53})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { u.Close() })
		go func(u *net.UDPConn, prefix string) {
			buf := make([]byte, 65535)
			for {
				n, from, err := u.ReadFromUDP(buf)
				if err != nil {
					return
				}
				u.WriteToUDP(append([]byte(prefix), buf[:n]...), from)
			}
		}(u, prefix)
	}
}

// lpStartMihomo starts mihomo and returns a restart that drops every pooled
// client session.
func lpStartMihomo(t *testing.T, bin, dir string, kinds []*lpKind, nodes []flvx.Node, byID map[string]int) func() {
	t.Helper()
	anytls, snell4, mieru, snell5 := nodes[byID["41"]], nodes[byID["42"]], nodes[byID["43"]], nodes[byID["44"]]
	block, _ := pem.Decode([]byte(anytls.AnyTLS.TLS.CertificatePEM))
	pin := sha256.Sum256(block.Bytes)
	proxies, listeners := []any{}, []any{}
	port := 41000
	for _, k := range kinds {
		for i, source := range lpSources {
			name := fmt.Sprintf("%s-%d", k.proxy, i)
			var p map[string]any
			switch k.proxy {
			case "anytls":
				p = map[string]any{"type": "anytls", "port": anytls.Port, "password": anytls.AnyTLS.Password, "sni": anytls.AnyTLS.TLS.ServerName, "fingerprint": hex.EncodeToString(pin[:]), "client-fingerprint": "chrome"}
			case "snell4":
				p = map[string]any{"type": "snell", "port": snell4.Port, "psk": snell4.Snell.PSK, "version": 4}
			case "snell5":
				p = map[string]any{"type": "snell", "port": snell5.Port, "psk": snell5.Snell.PSK, "version": 5, "obfs-opts": map[string]any{"mode": snell5.Snell.Obfs, "host": snell5.Snell.ObfsHost}}
			case "ss2022":
				n := nodes[byID["ss"]]
				p = map[string]any{"type": "ss", "port": n.Port, "cipher": n.SS2022.Method, "password": n.SS2022.ServerKey + ":" + n.SS2022.UserKey}
			case "vless":
				n := nodes[byID["vl"]]
				raw, _ := base64.RawURLEncoding.DecodeString(n.VLESS.Reality.PrivateKey)
				key, _ := ecdh.X25519().NewPrivateKey(raw)
				p = map[string]any{"type": "vless", "port": n.Port, "uuid": n.VLESS.UUID, "network": "tcp", "tls": true, "flow": "xtls-rprx-vision", "servername": n.VLESS.Reality.ServerNames[0], "client-fingerprint": "chrome",
					"reality-opts": map[string]any{"public-key": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "short-id": n.VLESS.Reality.ShortIDs[0]}}
			case "mieru-tcp", "mieru-udp":
				p = map[string]any{"type": "mieru", "port": mieru.Port, "transport": strings.ToUpper(strings.TrimPrefix(k.proxy, "mieru-")), "username": mieru.Mieru.Username, "password": mieru.Mieru.Password, "multiplexing": "MULTIPLEXING_LOW"}
			}
			p["name"], p["server"], p["udp"] = name, source, true
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
	var command *exec.Cmd
	start := func() {
		command = exec.Command(bin, "-f", path, "-d", dir)
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

// lpDial opens a SOCKS5 CONNECT to the TCP echo through a mihomo listener.
func lpDial(listener int) (net.Conn, error) { return lpDialTo(listener, lpEchoTCP, 80) }

func lpDialTo(listener int, ip string, port int) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(listener), 2*time.Second)
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	reply := make([]byte, 2)
	if _, err = c.Write([]byte{5, 1, 0}); err == nil {
		_, err = io.ReadFull(c, reply)
	}
	if err == nil && reply[1] != 0 {
		err = errors.New("SOCKS method refused")
	}
	header := make([]byte, 10)
	if err == nil {
		_, err = c.Write(append([]byte{5, 1, 0, 1}, append(net.ParseIP(ip).To4(), byte(port>>8), byte(port))...))
	}
	if err == nil {
		_, err = io.ReadFull(c, header)
	}
	if err == nil && header[1] != 0 {
		err = fmt.Errorf("SOCKS CONNECT reply %d", header[1])
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

func lpEcho(c net.Conn, size int) error {
	payload := make([]byte, size)
	rand.Read(payload)
	c.SetDeadline(time.Now().Add(4 * time.Second))
	defer c.SetDeadline(time.Time{})
	if _, err := c.Write(payload); err != nil {
		return err
	}
	got := make([]byte, size)
	if _, err := io.ReadFull(c, got); err != nil {
		return err
	}
	if !bytes.Equal(got, payload) {
		return errors.New("echo mismatch")
	}
	return nil
}

func lpBulkEcho(listener, size int) error {
	c, err := lpDial(listener)
	if err != nil {
		return err
	}
	defer c.Close()
	payload := make([]byte, size)
	rand.Read(payload)
	c.SetDeadline(time.Now().Add(60 * time.Second))
	written := make(chan error, 1)
	go func() { _, err := c.Write(payload); written <- err }()
	got := make([]byte, size)
	if _, err = io.ReadFull(c, got); err != nil {
		return err
	}
	if err = <-written; err != nil {
		return err
	}
	if !bytes.Equal(got, payload) {
		return errors.New("2 MiB echo mismatch")
	}
	return nil
}

// lpUDPTwoTargets sends through one SOCKS5 UDP association to both UDP
// echoes, interleaved; each reply must come from the datagram's own target.
func lpUDPTwoTargets(listener int, tag string) error {
	ctrl, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(listener), 2*time.Second)
	if err != nil {
		return err
	}
	defer ctrl.Close()
	ctrl.SetDeadline(time.Now().Add(10 * time.Second))
	reply := make([]byte, 2)
	ctrl.Write([]byte{5, 1, 0})
	if _, err = io.ReadFull(ctrl, reply); err != nil {
		return err
	}
	ctrl.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0})
	header := make([]byte, 10)
	if _, err = io.ReadFull(ctrl, header); err != nil || header[1] != 0 || header[3] != 1 {
		return fmt.Errorf("UDP ASSOCIATE %v %v", header, err)
	}
	relay := &net.UDPAddr{IP: net.IP(header[4:8]), Port: int(binary.BigEndian.Uint16(header[8:10]))}
	if relay.IP.IsUnspecified() {
		relay.IP = net.IPv4(127, 0, 0, 1)
	}
	u, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return err
	}
	defer u.Close()
	want := map[string]string{}
	for i, target := range []string{lpEchoA, lpEchoB, lpEchoA, lpEchoB, lpEchoB, lpEchoA} {
		payload := fmt.Sprintf("%s-%d", tag, i)
		prefix := "A:"
		if target == lpEchoB {
			prefix = "B:"
		}
		want[prefix+payload] = target
		packet := append([]byte{0, 0, 0, 1}, append(net.ParseIP(target).To4(), 0, 53)...)
		if _, err = u.WriteToUDP(append(packet, payload...), relay); err != nil {
			return err
		}
	}
	u.SetReadDeadline(time.Now().Add(6 * time.Second))
	buf := make([]byte, 2048)
	for len(want) > 0 {
		n, _, err := u.ReadFromUDP(buf)
		if err != nil {
			return fmt.Errorf("%d replies missing (%v): %v", len(want), want, err)
		}
		if n < 10 || buf[3] != 1 {
			return errors.New("malformed SOCKS5 UDP reply")
		}
		from, body := net.IP(buf[4:8]).String(), string(buf[10:n])
		target, ok := want[body]
		if !ok || target != from {
			return fmt.Errorf("reply %q came from %s (misdelivered)", body, from)
		}
		delete(want, body)
	}
	return nil
}
