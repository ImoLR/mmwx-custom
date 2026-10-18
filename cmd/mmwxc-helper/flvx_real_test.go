package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

func flvxFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func flvxStartCore(t *testing.T, bin, path, socket string, config any) func() {
	t.Helper()
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bin, "run", "-config", path)
	command.Env = append(os.Environ(), "MMWXC_CORE_CONTROL_SOCKET="+socket)
	logFile, err := os.Create(path + ".log")
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout, command.Stderr = logFile, logFile
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			command.Process.Kill()
			command.Wait()
			logFile.Close()
		}
	}
	t.Cleanup(stop)
	return stop
}

func flvxWait(t *testing.T, fn func() bool) {
	t.Helper()
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("local Core readiness timeout")
}

func flvxSOCKS(t *testing.T, port int, target string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(3 * time.Second))
	c.Write([]byte{5, 1, 0})
	reply := make([]byte, 2)
	if _, err = io.ReadFull(c, reply); err != nil || reply[1] != 0 {
		c.Close()
		t.Fatal("SOCKS negotiation", err)
	}
	ip := net.ParseIP(target).To4()
	c.Write(append([]byte{5, 1, 0, 1}, append(ip, 0, 80)...))
	header := make([]byte, 4)
	if _, err = io.ReadFull(c, header); err != nil || header[1] != 0 {
		c.Close()
		t.Fatal("SOCKS CONNECT", err, header)
	}
	size := 6
	if header[3] == 4 {
		size = 18
	}
	if _, err = io.ReadFull(c, make([]byte, size)); err != nil {
		c.Close()
		t.Fatal(err)
	}
	return c
}

func TestFLVXRealCore(t *testing.T) {
	bin := os.Getenv("MMWXC_TEST_FLVX_CORE")
	if bin == "" {
		t.Skip("MMWXC_TEST_FLVX_CORE not configured")
	}
	if strings.Contains(strings.ToLower(filepath.Base(bin)), "xray") {
		t.Fatal("FLVX binary name must not contain xray")
	}
	if hash, err := flvxFileHash(bin); err != nil || hash != "ae4b83f0cf77c4aa73857cb1de773e7ec89b1bc5ded272a677b46d417cf40212" {
		t.Fatal("real test requires v1.4.59 amd64 release")
	}
	dir := t.TempDir()
	ctx := context.Background()
	// Every listener in this fixture binds loopback. Only this test's direct
	// outbound redirects a public test target to the local echo server, avoiding
	// external network dependencies while exercising the real first routing rule.
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	// Model an existing official External Core with an actual xray-named binary
	// and a loopback listener. FLVX must leave its process count and owner intact.
	referenceBin := filepath.Join(dir, "xray")
	if err := copyFile(bin, referenceBin, 0755); err != nil {
		t.Fatal(err)
	}
	officialPort := flvxFreePort(t)
	flvxStartCore(t, referenceBin, filepath.Join(dir, "official.json"), "", map[string]any{"log": map[string]any{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": officialPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}}, "outbounds": []any{map[string]any{"protocol": "blackhole"}}})
	flvxWait(t, func() bool {
		c, e := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(officialPort)), time.Second)
		if e == nil {
			c.Close()
		}
		return e == nil
	})
	beforeCount, _ := countXrayProcesses()
	beforeOwners, _ := listeningPIDsForPorts([]int{officialPort})
	apiPort, nodePort, secondPort, socksPort := flvxFreePort(t), flvxFreePort(t), flvxFreePort(t), flvxFreePort(t)
	base := flvx.BaseConfig(apiPort)
	base["outbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["redirect"] = echo.Addr().String()
	socket := filepath.Join(dir, "control.sock")
	stopCore := flvxStartCore(t, bin, filepath.Join(dir, "core.json"), socket, base)
	core := newCoreClient(socket)
	flvxWait(t, func() bool { _, err := core.snapshot(ctx); return err == nil })
	node := helperFLVXNode("real", nodePort)
	node.MaxConnections = 0
	node.MaxOnlineIPs = 0
	m := &flvxManager{dir: filepath.Join(dir, "state"), binary: bin, core: core, logs: newPeriodicLogLimiter()}
	m.state = flvxLocalState{Payload: flvx.Payload{Version: 1, Status: "active", Config: flvx.Config{PortMin: 1024, PortMax: 65535, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}, Desired: flvx.Desired{Revision: 1, Nodes: []flvx.Node{node}}, Artifact: flvx.Artifact{Version: "8f5b614"}}, APIPort: apiPort, Counters: map[string]*flvx.Counter{}, Applied: map[string]string{}, Report: flvx.Report{Generation: "real-helper"}}
	m.setup = func(context.Context) error { return nil }
	m.stop = func(context.Context) error { stopCore(); return nil }
	m.cleanup = func(context.Context) error { return os.RemoveAll(m.dir) }
	m.readOfficial = func() ([]int, error) { return []int{officialPort}, nil }
	m.apiCall = func(ctx context.Context, command string, args ...string) ([]byte, error) {
		if command == "adi" {
			raw, _ := os.ReadFile(args[0])
			var cfg map[string]any
			json.Unmarshal(raw, &cfg)
			for _, v := range cfg["inbounds"].([]any) {
				v.(map[string]any)["listen"] = "127.0.0.1"
			}
			raw, _ = json.Marshal(cfg)
			os.WriteFile(args[0], raw, 0600)
		}
		return m.api(ctx, command, args...)
	}
	apply := func() {
		t.Helper()
		if err := m.reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	clientConfig := map[string]any{"log": map[string]any{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": socksPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}}, "outbounds": []any{map[string]any{"protocol": "shadowsocks", "settings": map[string]any{"servers": []any{map[string]any{"address": "127.0.0.1", "port": nodePort, "method": node.SS2022.Method, "password": node.SS2022.ServerKey + ":" + node.SS2022.UserKey}}}}}}
	flvxStartCore(t, bin, filepath.Join(dir, "client.json"), "", clientConfig)
	flvxWait(t, func() bool {
		c, e := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort)), time.Second)
		if e == nil {
			c.Close()
		}
		return e == nil
	})
	connection := flvxSOCKS(t, socksPort, "203.0.113.10")
	defer connection.Close()
	exchange := func() {
		t.Helper()
		payload := bytes.Repeat([]byte("flvx-loopback-fixture"), 1024)
		connection.SetDeadline(time.Now().Add(3 * time.Second))
		if _, e := connection.Write(payload); e != nil {
			t.Fatal(e)
		}
		got := make([]byte, len(payload))
		if _, e := io.ReadFull(connection, got); e != nil || !bytes.Equal(got, payload) {
			t.Fatal("existing stream interrupted", e)
		}
	}
	exchange()
	before, _ := core.snapshot(ctx)
	second := helperFLVXNode("second", secondPort)
	m.state.Payload.Desired.Nodes = append(m.state.Payload.Desired.Nodes, second)
	m.state.Payload.Desired.Revision++
	apply()
	exchange()
	m.state.Payload.Desired.Nodes = m.state.Payload.Desired.Nodes[:1]
	m.state.Payload.Desired.Revision++
	apply()
	exchange()
	after, _ := core.snapshot(ctx)
	if !before.StartedAt.Equal(after.StartedAt) {
		t.Fatal("hot add/remove restarted Core")
	}
	apply()
	if m.state.Report.Nodes[0].UpBytes < 60000 || m.state.Report.Nodes[0].DownBytes < 60000 {
		t.Fatal("inbound cumulative stats missing", m.state.Report.Nodes)
	}
	used := m.state.Report.TrafficUsedBytes
	// The private-address check must survive a forged public HTTP Host header.
	for _, target := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "224.0.0.1"} {
		c := flvxSOCKS(t, socksPort, target)
		c.Write([]byte("GET / HTTP/1.1\r\nHost: public.example\r\n\r\n"))
		one := make([]byte, 1)
		n, _ := c.Read(one)
		c.Close()
		if n != 0 {
			t.Fatalf("private target %s bypassed routing", target)
		}
	}
	// Raw pre-authentication bytes belong to inbound totals as well.
	apply()
	used = m.state.Report.TrafficUsedBytes
	rawClient, e := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(nodePort)))
	if e != nil {
		t.Fatal(e)
	}
	rawClient.Write(bytes.Repeat([]byte{0x42}, 64))
	rawClient.Close()
	time.Sleep(100 * time.Millisecond)
	apply()
	if m.state.Report.TrafficUsedBytes <= used {
		t.Fatal("raw inbound traffic not counted")
	}
	afterCount, _ := countXrayProcesses()
	afterOwners, _ := listeningPIDsForPorts([]int{officialPort})
	if beforeCount != afterCount || !reflect.DeepEqual(beforeOwners, afterOwners) {
		t.Fatalf("FLVX changed SingleCore inputs or official port ownership: %d -> %d", beforeCount, afterCount)
	}
	// Same instance, connection caps are enforced through its own socket.
	m.state.Payload.Config.MaxConnections = 2
	apply()
	blocked := flvxSOCKS(t, socksPort, "203.0.113.10")
	blocked.Write([]byte("second-session"))
	one := make([]byte, 1)
	n, _ := blocked.Read(one)
	blocked.Close()
	if n != 0 {
		t.Fatal("share total connection cap not enforced")
	}
	m.state.Payload.Config.MaxConnections = 0
	m.state.Payload.Status = "paused"
	apply()
	connection.SetDeadline(time.Now().Add(time.Second))
	connection.Write([]byte("paused"))
	n, _ = connection.Read(one)
	if n != 0 {
		t.Fatal("pause kept existing session usable")
	}
	m.state.Payload.Status = "active"
	apply()
	newConnection := flvxSOCKS(t, socksPort, "203.0.113.10")
	newConnection.Write([]byte("restored"))
	got := make([]byte, 8)
	if _, e = io.ReadFull(newConnection, got); e != nil {
		t.Fatal("resume", e)
	}
	newConnection.Close()
	oldTotal := m.state.Report.TrafficUsedBytes
	stopCore()
	stopCore = flvxStartCore(t, bin, filepath.Join(dir, "core.json"), socket, base)
	flvxWait(t, func() bool { s, e := core.snapshot(ctx); return e == nil && !s.StartedAt.Equal(before.StartedAt) })
	apply()
	if m.state.Report.TrafficUsedBytes < oldTotal {
		t.Fatal("Core restart lost lifetime counters")
	}
	m.state.Payload.Config.TrafficLimitBytes = 1
	apply()
	if len(m.state.Applied) != 0 {
		t.Fatal("traffic cap retained handlers")
	}
	t.Logf("real Core v1.4.59: hot add/remove kept stream; stats=%d bytes; private targets blocked; SingleCore count=%d unchanged; official port ownership unchanged; limits/pause/resume/restart passed", m.state.Report.TrafficUsedBytes, afterCount)
}
