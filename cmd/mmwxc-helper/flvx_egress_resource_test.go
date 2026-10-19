package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// TestFLVXEgressResourceProfile measures the FLVX Core driven by the Helper
// on a throwaway network namespace: RSS/heap/threads/fds/goroutines with 1
// and 50 nodes, per-feature deltas of the egress plan, 1000 concurrent
// connections through a forced-IPv4 outbound and back to baseline, idle CPU,
// the Helper's reconcile cost per tick and disk/log growth.
// Opt-in: root, MMWXC_TEST_FLVX_RESOURCE=1, MMWXC_TEST_FLVX_CORE,
// MMWXC_TEST_GEO_DIR; MMWXC_TEST_EVIDENCE receives resource-profile.json.
func TestFLVXEgressResourceProfile(t *testing.T) {
	if os.Getenv("MMWXC_TEST_FLVX_RESOURCE") != "1" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_TEST_FLVX_RESOURCE=1 and run as root")
	}
	bin := realEgressCore(t)
	base := t.TempDir()
	if !realGeo(t, filepath.Join(base, "state")) {
		t.Skip("MMWXC_TEST_GEO_DIR not supplied")
	}
	const ns = "mmwxres"
	n := flvxNetns{t, base}
	flvxNetnsCleanup(ns)
	t.Cleanup(func() { flvxNetnsCleanup(ns) })
	n.sh("ip", "netns", "add", ns)
	n.sh("ip", "-n", ns, "link", "set", "lo", "up")
	n.sh("ip", "-n", ns, "link", "add", "d0", "type", "dummy")
	for _, a := range []string{"203.0.113.1/32", "198.51.100.2/32"} {
		n.sh("ip", "-n", ns, "addr", "add", a, "dev", "d0")
	}
	n.sh("ip", "-n", ns, "link", "set", "d0", "up")
	echo := `import asyncio
async def handle(r, w):
    try:
        while True:
            d = await r.read(4096)
            if not d:
                break
            w.write(d)
            await w.drain()
    except Exception:
        pass
    finally:
        w.close()
async def main():
    s = await asyncio.start_server(handle, "198.51.100.2", 8080, backlog=4096)
    await s.serve_forever()
asyncio.run(main())
`
	os.WriteFile(filepath.Join(base, "echo.py"), []byte(echo), 0644)
	target := exec.Command("ip", "netns", "exec", ns, "sh", "-c", "ulimit -n 65535; exec python3 "+filepath.Join(base, "echo.py"))
	if err := target.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Process.Kill(); target.Wait() })

	nodes := make([]flvx.Node, 0, 50)
	all := []string{}
	for i := 1; i <= 50; i++ {
		node := netnsNode(strconv.Itoa(i), 32000+i, 0)
		nodes = append(nodes, node)
		all = append(all, node.Tag())
	}
	// The Core starts from the production base config plus a metrics
	// listener (pprof/expvar) for goroutine and heap numbers.
	const metricsPort = 39772
	m, coreCmd := func() (*flvxManager, *exec.Cmd) {
		wrapper := filepath.Join(base, "core-in-netns")
		os.WriteFile(wrapper, []byte("#!/bin/sh\nexec ip netns exec "+ns+" "+bin+" \"$@\"\n"), 0755)
		cfg := flvx.BaseConfig(39871)
		cfg["metrics"] = map[string]any{"tag": "metrics", "listen": "127.0.0.1:" + strconv.Itoa(metricsPort)}
		socket := filepath.Join(base, "control.sock")
		cmd := flvxNetnsCore(t, ns, bin, filepath.Join(base, "core.json"), socket, cfg, true)
		client := newCoreClient(socket)
		flvxWait(t, func() bool { _, err := client.snapshot(context.Background()); return err == nil })
		m := &flvxManager{dir: filepath.Join(base, "state"), binary: wrapper, core: client, logs: newPeriodicLogLimiter()}
		m.state = flvxLocalState{Payload: flvx.Payload{Version: 1, Status: "active", Config: flvx.Config{PortMin: 1024, PortMax: 65535, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}, Desired: flvx.Desired{Revision: 1, Nodes: nodes[:1]}}, APIPort: 39871, Counters: map[string]*flvx.Counter{}, Applied: map[string]string{}, Report: flvx.Report{Generation: "resource"}}
		m.apiCall = m.api
		m.setup = func(context.Context) error { return nil }
		m.stop = func(context.Context) error { return nil }
		m.readOfficial = func() ([]int, error) { return nil, nil }
		m.restartCore = func(context.Context) error { t.Fatal("unexpected Core restart"); return nil }
		return m, cmd
	}()
	helperLog := 0
	m.logs.print = func(string, ...any) { helperLog++ }
	m.detectLocal = func() flvx.LocalAddrs { return flvx.LocalAddrs{V4: netip.MustParseAddr("203.0.113.1")} }
	ctx := context.Background()
	apply := func(list []flvx.Node, plan *flvx.Egress) {
		t.Helper()
		m.state.Payload.Desired.Revision++
		m.state.Payload.Desired.Nodes, m.state.Payload.Desired.Egress = list, plan
		if err := m.reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if plan.Active() && m.state.Report.Egress.State != "applied" {
			t.Fatalf("plan not applied: %+v", m.state.Report.Egress)
		}
	}
	corePID := func() int {
		out, _ := exec.Command("ip", "netns", "pids", ns).Output()
		for _, pid := range strings.Fields(string(out)) {
			if cmdline, _ := os.ReadFile("/proc/" + pid + "/cmdline"); strings.Contains(string(cmdline), "core.json") {
				id, _ := strconv.Atoi(pid)
				return id
			}
		}
		t.Fatal("Core process not found")
		return 0
	}()
	_ = coreCmd
	nsGet := func(path string) string {
		out, err := exec.Command("ip", "netns", "exec", ns, "python3", "-c", fmt.Sprintf("import urllib.request;print(urllib.request.urlopen('http://127.0.0.1:%d%s',timeout=5).read().decode())", metricsPort, path)).Output()
		if err != nil {
			return ""
		}
		return string(out)
	}
	type sample struct {
		Label      string  `json:"label"`
		RSSMiB     float64 `json:"rss_mib"`
		HeapMiB    float64 `json:"heap_inuse_mib"`
		Threads    int     `json:"threads"`
		FDs        int     `json:"fds"`
		Goroutines int     `json:"goroutines"`
		CPUSeconds float64 `json:"cpu_seconds_total"`
	}
	take := func(label string) sample {
		t.Helper()
		time.Sleep(2 * time.Second)
		s := sample{Label: label}
		status, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", corePID))
		for _, line := range strings.Split(string(status), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && f[0] == "VmRSS:" {
				kb, _ := strconv.Atoi(f[1])
				s.RSSMiB = float64(kb) / 1024
			}
			if len(f) >= 2 && f[0] == "Threads:" {
				s.Threads, _ = strconv.Atoi(f[1])
			}
		}
		fds, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", corePID))
		s.FDs = len(fds)
		if g := nsGet("/debug/pprof/goroutine?debug=1"); strings.HasPrefix(g, "goroutine profile: total ") {
			s.Goroutines, _ = strconv.Atoi(strings.Fields(strings.TrimPrefix(g, "goroutine profile: total "))[0])
		}
		var vars struct {
			Memstats struct {
				HeapInuse uint64 `json:"HeapInuse"`
			} `json:"memstats"`
		}
		if json.Unmarshal([]byte(nsGet("/debug/vars")), &vars) == nil {
			s.HeapMiB = float64(vars.Memstats.HeapInuse) / (1 << 20)
		}
		stat, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", corePID))
		if i := strings.LastIndexByte(string(stat), ')'); i > 0 {
			if f := strings.Fields(string(stat)[i+2:]); len(f) > 13 {
				u, _ := strconv.ParseFloat(f[11], 64)
				k, _ := strconv.ParseFloat(f[12], 64)
				s.CPUSeconds = (u + k) / 100
			}
		}
		t.Logf("%-46s rss=%6.1f MiB heap=%6.1f MiB threads=%3d fds=%4d goroutines=%4d cpu=%.2fs", label, s.RSSMiB, s.HeapMiB, s.Threads, s.FDs, s.Goroutines, s.CPUSeconds)
		return s
	}
	report := map[string]any{}
	var samples []sample
	var childBefore syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_CHILDREN, &childBefore)

	apply(nodes[:1], nil)
	samples = append(samples, take("1 node, default plan"))
	apply(nodes, nil)
	samples = append(samples, take("50 nodes, default plan"))
	forced := flvx.EgressOutbound{Tag: "flvxo-f4", Type: "direct", IPVersion: "force_ipv4"}
	planForced := &flvx.Egress{Version: 1, Outbounds: []flvx.EgressOutbound{forced}, Rules: []flvx.EgressRule{
		{Name: "BT", InboundTags: all, Protocol: []string{"bittorrent"}, Outbound: "block"},
		{Name: "default", InboundTags: all, Outbound: "flvxo-f4"},
	}}
	apply(nodes, planForced)
	samples = append(samples, take("50 nodes + forced IPv4 + BT block"))
	warp := goldenEgressPlan(all).Outbounds[3]
	warp.IPVersion = "system"
	planWarp := &flvx.Egress{Version: 1, Outbounds: []flvx.EgressOutbound{forced, warp}, Rules: append([]flvx.EgressRule{
		{Name: "AI", InboundTags: all, Domain: []string{"geosite:openai", "geosite:anthropic", "geosite:google-gemini"}, Outbound: "flvxo-warp"},
	}, planForced.Rules...)}
	apply(nodes, planWarp)
	samples = append(samples, take("+ idle WARP outbound + AI geosite"))
	exempt := []string{}
	for i := 1; i <= 30; i++ {
		exempt = append(exempt, fmt.Sprintf("1.0.%d.7", i))
	}
	planCN := &flvx.Egress{Version: 1, ExemptSources: exempt, Outbounds: planWarp.Outbounds, Rules: append(cnRules(all)[:3], planWarp.Rules...)}
	started := time.Now()
	apply(nodes, planCN)
	report["apply_block_cn_seconds"] = time.Since(started).Seconds()
	report["block_cn_geo_payload_bytes"] = m.state.EgressGeoBytes
	if list, ok := m.egress.render.rules[3].(map[string]any)["sourceIP"].([]string); ok {
		report["cn_source_cidrs_after_exemption"] = len(list)
	}
	var childAfter syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_CHILDREN, &childAfter)
	report["cli_child_peak_rss_mib"] = float64(childAfter.Maxrss) / 1024
	samples = append(samples, take("+ 屏蔽中国大陆 (3 rules, source minus 30 exempt)"))

	// Steady state: the Helper reconcile tick (5 s in production).
	var self0, child0, self1, child1 syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &self0)
	syscall.Getrusage(syscall.RUSAGE_CHILDREN, &child0)
	goroutines0 := runtime.NumGoroutine()
	ticks := 10
	tickStart := time.Now()
	for i := 0; i < ticks; i++ {
		if err := m.reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	wall := time.Since(tickStart)
	syscall.Getrusage(syscall.RUSAGE_SELF, &self1)
	syscall.Getrusage(syscall.RUSAGE_CHILDREN, &child1)
	cpu := func(a, b syscall.Rusage) float64 {
		d := func(x, y syscall.Timeval) float64 { return float64(y.Sec-x.Sec) + float64(y.Usec-x.Usec)/1e6 }
		return d(a.Utime, b.Utime) + d(a.Stime, b.Stime)
	}
	report["helper_tick"] = map[string]any{"ticks": ticks, "wall_ms_per_tick": float64(wall.Milliseconds()) / float64(ticks),
		"helper_cpu_ms_per_tick": cpu(self0, self1) * 1000 / float64(ticks), "cli_children_cpu_ms_per_tick": cpu(child0, child1) * 1000 / float64(ticks),
		"helper_goroutines_before": goroutines0, "helper_goroutines_after": runtime.NumGoroutine(),
		"note": "children = one Core API CLI statsquery per tick (pre-existing); the egress plan adds no call in steady state"}
	idle0 := take("idle start")
	time.Sleep(30 * time.Second)
	idle1 := take("idle +30 s")
	report["core_idle_cpu_percent"] = (idle1.CPUSeconds - idle0.CPUSeconds) / 30 * 100

	// 1000 concurrent connections through node 1 (forced IPv4), then closed.
	clientCfg := map[string]any{"log": map[string]any{"loglevel": "warning"},
		"inbounds":  []any{map[string]any{"tag": "in", "listen": "127.0.0.1", "port": 20001, "protocol": "dokodemo-door", "settings": map[string]any{"address": "198.51.100.2", "port": 8080, "network": "tcp"}}},
		"outbounds": []any{map[string]any{"tag": "out", "protocol": "shadowsocks", "settings": map[string]any{"servers": []any{map[string]any{"address": "203.0.113.1", "port": nodes[0].Port, "method": nodes[0].SS2022.Method, "password": nodes[0].SS2022.ServerKey + ":" + nodes[0].SS2022.UserKey}}}}}}
	flvxNetnsCore(t, ns, bin, filepath.Join(base, "client.json"), "", clientCfg, true)
	time.Sleep(time.Second)
	load := `import socket, sys
n, port = int(sys.argv[1]), int(sys.argv[2])
conns = [socket.create_connection(("127.0.0.1", port), timeout=20) for _ in range(n)]
for c in conns:
    c.sendall(b"ping\n")
ok = 0
for c in conns:
    c.settimeout(20)
    try:
        if c.recv(16).startswith(b"ping"):
            ok += 1
    except Exception:
        pass
print("OPEN %d" % ok, flush=True)
sys.stdin.readline()
for c in conns:
    c.close()
print("CLOSED", flush=True)
sys.stdin.readline()
`
	os.WriteFile(filepath.Join(base, "load.py"), []byte(load), 0644)
	before := take("before 1000 connections")
	loader := exec.Command("ip", "netns", "exec", ns, "sh", "-c", "ulimit -n 65535; exec python3 "+filepath.Join(base, "load.py")+" 1000 20001")
	stdin, _ := loader.StdinPipe()
	stdout, _ := loader.StdoutPipe()
	if err := loader.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdout)
	if !lines.Scan() || !strings.HasPrefix(lines.Text(), "OPEN ") {
		t.Fatalf("load client: %q", lines.Text())
	}
	opened, _ := strconv.Atoi(strings.TrimPrefix(lines.Text(), "OPEN "))
	peak := take("1000 connections open")
	io.WriteString(stdin, "\n")
	if !lines.Scan() || lines.Text() != "CLOSED" {
		t.Fatalf("load client close: %q", lines.Text())
	}
	time.Sleep(15 * time.Second)
	after := take("15 s after closing them")
	io.WriteString(stdin, "\n")
	loader.Wait()
	time.Sleep(105 * time.Second)
	later := take("2 min after closing them")
	report["connections"] = map[string]any{"opened_and_echoed": opened, "before": before, "peak": peak, "after_15s": after, "after_2min": later}
	if opened != 1000 {
		t.Errorf("only %d/1000 connections echoed through the forced-IPv4 outbound", opened)
	}
	if after.FDs > before.FDs+5 || after.Goroutines > before.Goroutines+10 {
		t.Errorf("connections leaked: fds %d->%d, goroutines %d->%d", before.FDs, after.FDs, before.Goroutines, after.Goroutines)
	}
	var stateBytes, geoBytes int64
	filepath.Walk(m.dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			if filepath.Dir(path) == m.geoDir() {
				geoBytes += info.Size()
			} else {
				stateBytes += info.Size()
			}
		}
		return nil
	})
	coreLog, _ := os.Stat(filepath.Join(base, "core.json.log"))
	report["disk"] = map[string]any{"helper_state_bytes": stateBytes, "geo_bytes": geoBytes, "core_log_bytes_whole_test": coreLog.Size(), "helper_log_lines": helperLog}
	report["samples"] = samples
	out, _ := json.MarshalIndent(report, "", "  ")
	t.Logf("%s", out)
	if path := os.Getenv("MMWXC_TEST_EVIDENCE"); path != "" {
		os.WriteFile(filepath.Join(path, "resource-profile.json"), out, 0644)
	}
}
