package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// Resource profile of the landing-protocols-v1 inbounds on ONE FLVX Core, as
// the Helper renders it, next to SS2022 and VLESS Reality: per protocol in
// turn 100 idle + 10 active streams (128 KiB/s down, 16 KiB/s up each) for
// 20 s through real mihomo clients; Core CPU and RSS from /proc, and the
// Helper's steady reconcile round with and without the extra nodes.
// Opt-in like the E2E plus MMWXC_TEST_LANDING_RESOURCE=1.
func TestFLVXLandingProtocolsResourceNetns(t *testing.T) {
	if os.Getenv("MMWXC_TEST_LANDING_RESOURCE") != "1" {
		t.Skip("set MMWXC_TEST_LANDING_RESOURCE=1")
	}
	lpRunInNetns(t, "TestFLVXLandingProtocolsResourceNetnsInner", "MMWXC_TEST_LANDING_RESOURCE_EVIDENCE")
}

func TestFLVXLandingProtocolsResourceNetnsInner(t *testing.T) {
	if os.Getenv("MMWXC_LANDING_INNER") != "1" {
		t.Skip("runs only inside the E2E namespace")
	}
	raiseNoFile()
	coreBin, mihomo := os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED"), os.Getenv("MMWXC_TEST_MIHOMO")
	ctx := context.Background()
	dir := t.TempDir()
	lpStartFixtures(t)
	lpStartPacedSource(t, lpEchoTCP+":81")
	lpStartTLSDest(t, lpEchoTCP+":443")

	apiPort := flvxFreePort(t)
	socket := filepath.Join(dir, "control.sock")
	configPath := filepath.Join(dir, "core.json")
	stopCore := flvxStartCore(t, coreBin, configPath, socket, flvx.BaseConfig(apiPort))
	core := newCoreClient(socket)
	flvxWait(t, func() bool { _, err := core.snapshot(ctx); return err == nil })
	pid := lpPID(t, configPath)
	hash, _ := flvxFileHash(coreBin)

	extra := helperProtocolNodes(t)
	ss := helperFLVXNode("ss", 30100)
	ss.MaxConnections, ss.MaxOnlineIPs = 0, 0
	vl := helperFLVXNode("vl", 30109)
	vl.MaxConnections, vl.MaxOnlineIPs = 0, 0
	vl.Protocol, vl.SS2022 = "vless_reality_vision", nil
	vl.VLESS = &flvx.VLESS{UUID: "bea206e3-2c4e-4eb0-8490-931be016a269", Flow: "xtls-rprx-vision", Reality: flvx.Reality{Dest: lpEchoTCP + ":443", ServerNames: []string{"reality.lab.test"}, PrivateKey: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x23}, 32)), ShortIDs: []string{"662d0350"}}}
	for i := range extra {
		extra[i].Port = 30101 + i
		extra[i].MaxConnections, extra[i].MaxOnlineIPs = 0, 0
	}
	v1Only := []flvx.Node{ss, vl}
	all := append(append([]flvx.Node{}, v1Only...), extra...)
	byID := map[string]int{}
	for i, n := range all {
		byID[n.ID] = i
	}
	m := &flvxManager{dir: filepath.Join(dir, "state"), binary: coreBin, core: core, logs: newPeriodicLogLimiter()}
	m.state = flvxLocalState{Payload: flvx.Payload{Version: 1, Status: "active", Config: flvx.Config{PortMin: 30000, PortMax: 30999, ResetDay: 1, MemoryMB: 256, CPUPercent: 50},
		Desired: flvx.Desired{Revision: 1, Nodes: v1Only}, Artifact: flvx.Artifact{Version: "fixed", SHA256: hash}}, APIPort: apiPort, Counters: map[string]*flvx.Counter{}, Applied: map[string]string{}, Report: flvx.Report{Generation: "netns-resource"}}
	m.setup = func(context.Context) error { return nil }
	m.stop = func(context.Context) error { stopCore(); return nil }
	m.cleanup = func(context.Context) error { return os.RemoveAll(m.dir) }
	m.readOfficial = func() ([]int, error) { return []int{}, nil }
	m.apiCall = m.api
	apply := func() time.Duration {
		t.Helper()
		start := time.Now()
		if err := m.reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	// Helper round: steady reconcile with SS2022 + VLESS only, then with all.
	apply()
	rounds := func() float64 {
		total := time.Duration(0)
		for i := 0; i < 10; i++ {
			total += apply()
		}
		return float64(total.Milliseconds()) / 10
	}
	evidence := map[string]any{"helper_round_ms_v1_only": rounds()}
	m.state.Payload.Desired.Nodes = all
	m.state.Payload.Desired.Revision++
	apply()
	for _, n := range all {
		if m.state.Applied[n.Tag()] == "" {
			t.Fatalf("node %s (%s) not applied: %+v", n.ID, n.Protocol, m.state.Report.Nodes)
		}
	}
	evidence["helper_round_ms_all"] = rounds()
	scanStart := time.Now()
	for i := 0; i < 20; i++ {
		if _, _, err := readListeningSockets(); err != nil {
			t.Fatal(err)
		}
	}
	evidence["listening_scan_ms"] = float64(time.Since(scanStart).Microseconds()) / 20 / 1000

	kinds := []*lpKind{{name: "VLESS Reality（对照）", proxy: "vless", node: "vl"}, {name: "SS2022", proxy: "ss2022", node: "ss"}, {name: "AnyTLS", proxy: "anytls", node: "41"}, {name: "Snell v4", proxy: "snell4", node: "42"}, {name: "Snell v5 + TLS obfs", proxy: "snell5", node: "44"}, {name: "Mieru TCP", proxy: "mieru-tcp", node: "43"}, {name: "Mieru UDP", proxy: "mieru-udp", node: "43"}}
	restart := lpStartMihomo(t, mihomo, dir, kinds, all, byID)
	rows := []map[string]any{}
	for _, k := range kinds {
		restart()
		time.Sleep(3 * time.Second)
		rssStart, cpuStart := lpProcRSS(pid), lpProcCPU(pid)
		var held []net.Conn
		for i := 0; i < 100; i++ {
			if c, err := lpDial(k.listeners[0]); err == nil {
				held = append(held, c)
			}
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		received := int64(0)
		deadline := time.Now().Add(20 * time.Second)
		phaseCPU, phaseStart := lpProcCPU(pid), time.Now()
		active := 0
		for i := 0; i < 10; i++ {
			c, err := lpDialTo(k.listeners[0], lpEchoTCP, 81)
			if err != nil {
				continue
			}
			active++
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				c.SetDeadline(deadline)
				go func() {
					chunk := make([]byte, 2048)
					for time.Now().Before(deadline) {
						if _, err := c.Write(chunk); err != nil {
							return
						}
						time.Sleep(125 * time.Millisecond)
					}
				}()
				n, _ := io.Copy(io.Discard, c)
				mu.Lock()
				received += n
				mu.Unlock()
			}(c)
		}
		peak := rssStart
		for time.Now().Before(deadline) {
			if r := lpProcRSS(pid); r > peak {
				peak = r
			}
			time.Sleep(200 * time.Millisecond)
		}
		wg.Wait()
		elapsed := time.Since(phaseStart).Seconds()
		cpu := (lpProcCPU(pid) - phaseCPU) / elapsed * 100
		inbound := 0
		snapshot, _ := core.snapshot(ctx)
		for _, u := range snapshot.Users {
			if u.Identity.InboundTag == "flvxl-"+k.node {
				inbound += int(u.InboundTCP.Total)
			}
		}
		for _, c := range held {
			c.Close()
		}
		row := map[string]any{"protocol": k.name, "idle_open": len(held), "active_open": active, "core_cpu_percent": round1(cpu), "rss_start_mib": round1(rssStart / 1024), "rss_peak_mib": round1(peak / 1024), "inbound_tcp_sockets": inbound, "received_mib": round1(float64(received) / (1 << 20)), "cpu_seconds_total": round1(lpProcCPU(pid) - cpuStart)}
		rows = append(rows, row)
		t.Logf("%s: idle %d active %d, Core CPU %.1f%%, RSS %.1f → %.1f MiB, inbound TCP %d, down %.1f MiB", k.name, len(held), active, cpu, rssStart/1024, peak/1024, inbound, float64(received)/(1<<20))
	}
	evidence["rows"] = rows
	raw, _ := json.MarshalIndent(evidence, "", "  ")
	if path := os.Getenv("MMWXC_LANDING_EVIDENCE"); path != "" {
		os.WriteFile(path, raw, 0644)
	}
	t.Logf("resource evidence:\n%s", raw)
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// lpPID finds the Core started with configPath.
func lpPID(t *testing.T, configPath string) int {
	t.Helper()
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if strings.Contains(string(cmdline), configPath) {
			return pid
		}
	}
	t.Fatal("Core process not found")
	return 0
}

func lpProcRSS(pid int) float64 {
	raw, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "VmRSS:" {
			v, _ := strconv.ParseFloat(f[1], 64)
			return v
		}
	}
	return 0
}

// lpProcCPU is the process's user+system CPU time in seconds.
func lpProcCPU(pid int) float64 {
	raw, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	s := string(raw)
	if i := strings.LastIndexByte(s, ')'); i > 0 {
		f := strings.Fields(s[i+2:])
		if len(f) > 13 {
			u, _ := strconv.ParseFloat(f[11], 64)
			k, _ := strconv.ParseFloat(f[12], 64)
			return (u + k) / 100
		}
	}
	return 0
}

// lpStartPacedSource sends 16 KiB every 125 ms (128 KiB/s) to every client
// and discards what it receives.
func lpStartPacedSource(t *testing.T, address string) {
	t.Helper()
	l, err := net.Listen("tcp4", address)
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
			go func() {
				defer c.Close()
				go io.Copy(io.Discard, c)
				chunk := make([]byte, 16<<10)
				for {
					if _, err := c.Write(chunk); err != nil {
						return
					}
					time.Sleep(125 * time.Millisecond)
				}
			}()
		}
	}()
}

// lpStartTLSDest is the Reality camouflage destination: any TLS 1.3 server.
func lpStartTLSDest(t *testing.T, address string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "reality.lab.test"}, DNSNames: []string{"reality.lab.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	l, err := tls.Listen("tcp4", address, &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"h2", "http/1.1"}, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
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
			go func() { defer c.Close(); io.Copy(io.Discard, c) }()
		}
	}()
}
