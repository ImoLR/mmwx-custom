package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Real-nft end-to-end check of machine protection inside private network
// namespaces only (never the host's tables or sysctls):
//
//	client mpc 10.231.1.2 ── 10.231.1.1 server mps 10.231.2.1 ── 10.231.2.2 backend mpb
//
// The server runs the real manager (real /proc/net, nft and per-netns
// tcp_max_tw_buckets) with a local business listener (:10443, the "mmwx Core"
// port), a non-business listener (:10022) and an nft DNAT forward
// :20080 → backend:9000 announced like the FLVX agent does
// (/run/flvx/business-ports.json format).
const (
	netnsServerIP  = "10.231.1.1"
	netnsBackendIP = "10.231.2.2"
	netnsCorePort  = 10443
	netnsOpenPort  = 10022
	netnsFwdPort   = 20080
)

type netnsServerReport struct {
	Status        machineProtectionStatus `json:"status"`
	Loops         int64                   `json:"loops"`
	LastLoopNanos int64                   `json:"last_loop_nanos"`
	ReadNanos     int64                   `json:"read_nanos"`
	SelfCPUNanos  int64                   `json:"self_cpu_nanos"`
	ChildCPUNanos int64                   `json:"child_cpu_nanos"`
	RSSKB         int64                   `json:"rss_kb"`
	NftCalls      int64                   `json:"nft_calls"`
	Algorithm     string                  `json:"algorithm"`
}

func TestMachineProtectionNetnsE2E(t *testing.T) {
	if os.Getenv("MMWXC_TEST_MACHINE_NETNS") != "1" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_TEST_MACHINE_NETNS=1 and run as root")
	}
	raiseNoFile()
	suffix := strconv.Itoa(os.Getpid() % 100000)
	client, server, backend := "mpc"+suffix, "mps"+suffix, "mpb"+suffix
	ns := flvxNetns{t: t}
	flvxNetnsCleanup(client, server, backend)
	t.Cleanup(func() { flvxNetnsCleanup(client, server, backend) })
	for _, name := range []string{client, server, backend} {
		ns.sh("ip", "netns", "add", name)
		ns.sh("ip", "-n", name, "link", "set", "lo", "up")
	}
	ns.sh("ip", "link", "add", "mpc0"+suffix, "netns", client, "type", "veth", "peer", "name", "mps0"+suffix, "netns", server)
	ns.sh("ip", "link", "add", "mps1"+suffix, "netns", server, "type", "veth", "peer", "name", "mpb0"+suffix, "netns", backend)
	for _, item := range [][3]string{{client, "mpc0" + suffix, "10.231.1.2/24"}, {server, "mps0" + suffix, "10.231.1.1/24"}, {server, "mps1" + suffix, "10.231.2.1/24"}, {backend, "mpb0" + suffix, "10.231.2.2/24"}} {
		ns.sh("ip", "-n", item[0], "addr", "add", item[2], "dev", item[1])
		ns.sh("ip", "-n", item[0], "link", "set", item[1], "up")
	}
	ns.sh("ip", "-n", client, "route", "add", "default", "via", netnsServerIP)
	ns.sh("ip", "-n", backend, "route", "add", "default", "via", "10.231.2.1")
	ns.sh("ip", "netns", "exec", server, "sysctl", "-qw", "net.ipv4.ip_forward=1")
	// The FLVX-style forward: DNAT in prerouting at -100, after our -150 hook.
	dnat := fmt.Sprintf("table ip flvxtest {\n chain pre {\n  type nat hook prerouting priority -100; policy accept;\n  tcp dport %d dnat to %s:9000\n  udp dport %d dnat to %s:9000\n }\n}\n", netnsFwdPort, netnsBackendIP, netnsFwdPort, netnsBackendIP)
	nft := exec.Command("ip", "netns", "exec", server, "nft", "-f", "-")
	nft.Stdin = strings.NewReader(dnat)
	if out, err := nft.CombinedOutput(); err != nil {
		t.Fatalf("dnat: %v %s", err, out)
	}

	dir := t.TempDir()
	paths := map[string]string{
		"settings": filepath.Join(dir, "settings.json"), "report": filepath.Join(dir, "report.json"),
		"status": filepath.Join(dir, "mmwxc", "machine-protection.json"), "flvx": filepath.Join(dir, "business-ports.json"),
		"twstate": filepath.Join(dir, "tw-state.json"),
	}
	writeJSONFile(t, paths["settings"], machineProtectionSettings{Enabled: false})
	writeJSONFile(t, paths["flvx"], map[string]any{"version": 1, "updated_at": time.Now().UTC().Format(time.RFC3339), "tcp": []int{netnsFwdPort}, "udp": []int{netnsFwdPort}})

	backendProc := startNetnsRole(t, backend, map[string]string{"MP_ROLE": "echo", "MP_PORTS": "9000"}, filepath.Join(dir, "backend.log"))
	defer stopRole(backendProc)
	// Listeners live in their own process so restarting the protection loop
	// never closes the sockets it is counting.
	listenerProc := startNetnsRole(t, server, map[string]string{"MP_ROLE": "echo", "MP_PORTS": fmt.Sprintf("%d,%d", netnsCorePort, netnsOpenPort)}, filepath.Join(dir, "listeners.log"))
	defer stopRole(listenerProc)
	serverEnv := map[string]string{"MP_ROLE": "server", "MP_CORE_PORTS": strconv.Itoa(netnsCorePort),
		"MP_SETTINGS": paths["settings"], "MP_REPORT": paths["report"], "MP_STATUS": paths["status"], "MP_FLVX": paths["flvx"], "MP_TWSTATE": paths["twstate"], "MP_INTERVAL": "500ms", "MP_ALGO": "new"}
	serverProc := startNetnsRole(t, server, serverEnv, filepath.Join(dir, "server.log"))
	defer func() { stopRole(serverProc) }()

	hostTW := readSysctlIn(t, "", "net/ipv4/tcp_max_tw_buckets")
	originalTW := readSysctlIn(t, server, "net/ipv4/tcp_max_tw_buckets")
	evidence := map[string]any{"host_tw_buckets": hostTW, "netns_tw_buckets_before": originalTW}
	report := waitReport(t, paths["report"], "server up", func(r netnsServerReport) bool { return r.Loops > 2 })
	evidence["baseline_disabled"] = resourceEvidence(report)

	// 1. Enable with a high cap: idle table (no hook), TIME_WAIT applied in the netns only.
	writeJSONFile(t, paths["settings"], machineProtectionSettings{Enabled: true, MaxTotal: int64Value(100000), TWBuckets: int64Value(1234), CloseWaitSeconds: int64Value(10)})
	report = waitReport(t, paths["report"], "enabled idle", func(r netnsServerReport) bool {
		return r.Status.Effective && r.Status.Configured.Enabled && r.Status.TWBuckets != nil
	})
	if got := readSysctlIn(t, server, "net/ipv4/tcp_max_tw_buckets"); got != 1234 {
		t.Fatalf("tw_buckets in netns = %d, want 1234", got)
	}
	if got := readSysctlIn(t, "", "net/ipv4/tcp_max_tw_buckets"); got != hostTW {
		t.Fatalf("host tw_buckets changed: %d -> %d", hostTW, got)
	}
	var saved machineTWSaved
	readJSONFile(t, paths["twstate"], &saved)
	if saved.Original != originalTW || saved.Applied != 1234 {
		t.Fatalf("persisted TIME_WAIT state = %#v, want original %d", saved, originalTW)
	}
	idleRules := nftListTable(t, server)
	if strings.Contains(idleRules, "hook prerouting") || !strings.Contains(idleRules, "20080") || !strings.Contains(idleRules, "10443") {
		t.Fatalf("idle table must hold both port sources and no hook:\n%s", idleRules)
	}
	evidence["idle_table"] = idleRules
	if !containsPort(report.Status.TCPPorts, netnsFwdPort) || !containsPort(report.Status.UDPPorts, netnsFwdPort) || report.Status.Sources.FLVXFile != "fresh" {
		t.Fatalf("FLVX ports not merged: %#v", report.Status)
	}

	// 2. Existing connections, opened before any blocking.
	coreAddr, fwdAddr, openAddr := fmt.Sprintf("%s:%d", netnsServerIP, netnsCorePort), fmt.Sprintf("%s:%d", netnsServerIP, netnsFwdPort), fmt.Sprintf("%s:%d", netnsServerIP, netnsOpenPort)
	existingCore := mustDialTCP(t, client, coreAddr)
	existingFwd := mustDialTCP(t, client, fwdAddr)
	existingUDPCore := mustDialUDP(t, client, coreAddr)
	existingUDPFwd := mustDialUDP(t, client, fwdAddr)
	defer existingCore.Close()
	defer existingFwd.Close()
	for name, conn := range map[string]net.Conn{"tcp core": existingCore, "tcp forward": existingFwd, "udp core": existingUDPCore, "udp forward": existingUDPFwd} {
		if err := echo(conn); err != nil {
			t.Fatalf("existing %s before blocking: %v", name, err)
		}
	}
	time.Sleep(time.Second)
	report = waitReport(t, paths["report"], "existing counted", func(r netnsServerReport) bool { return r.Status.Connections != nil })
	base := report.Status.Total
	limit := base + 30
	writeJSONFile(t, paths["settings"], machineProtectionSettings{Enabled: true, MaxTotal: int64Value(limit), TWBuckets: int64Value(1234), CloseWaitSeconds: int64Value(10)})
	evidence["cap"], evidence["base_total"] = limit, base

	// 3. Push the whole-machine total over the cap through a NON-business port.
	var fillers []net.Conn
	for len(fillers) < 34 {
		conn := mustDialTCP(t, client, openAddr)
		fillers = append(fillers, conn)
	}
	report = waitReport(t, paths["report"], "blocking", func(r netnsServerReport) bool { return r.Status.Blocking })
	evidence["blocking_status"] = report.Status
	blockingRules := nftListTable(t, server)
	if !strings.Contains(blockingRules, "hook prerouting priority mangle") && !strings.Contains(blockingRules, "hook prerouting priority -150") {
		t.Fatalf("blocking chain must hook prerouting at -150:\n%s", blockingRules)
	}
	evidence["blocking_table"] = blockingRules

	// 4. New business connections are dropped (TCP + UDP, local and forwarded);
	// the non-business port, loopback and every existing connection still work.
	for name, addr := range map[string]string{"tcp core": coreAddr, "tcp forward": fwdAddr} {
		if conn, err := dialTCPIn(client, addr, 1500*time.Millisecond); err == nil {
			conn.Close()
			t.Fatalf("new %s connection was accepted while blocking", name)
		}
	}
	for name, addr := range map[string]string{"udp core": coreAddr, "udp forward": fwdAddr} {
		conn := mustDialUDP(t, client, addr)
		if err := echo(conn); err == nil {
			t.Fatalf("new %s flow was answered while blocking", name)
		}
		conn.Close()
	}
	openTCP := mustDialTCP(t, client, openAddr)
	if err := echo(openTCP); err != nil {
		t.Fatalf("non-business TCP while blocking: %v", err)
	}
	openTCP.Close()
	openUDP := mustDialUDP(t, client, openAddr)
	if err := echo(openUDP); err != nil {
		t.Fatalf("non-business UDP while blocking: %v", err)
	}
	openUDP.Close()
	loop, err := dialTCPIn(server, fmt.Sprintf("127.0.0.1:%d", netnsCorePort), 2*time.Second)
	if err != nil || echo(loop) != nil {
		t.Fatalf("loopback business connection while blocking: %v", err)
	}
	loop.Close()
	for name, conn := range map[string]net.Conn{"tcp core": existingCore, "tcp forward": existingFwd, "udp core": existingUDPCore, "udp forward": existingUDPFwd} {
		if err := echo(conn); err != nil {
			t.Fatalf("existing %s broke while blocking: %v", name, err)
		}
	}
	report = waitReport(t, paths["report"], "drops counted", func(r netnsServerReport) bool { return r.Status.DroppedTotal >= 4 })
	evidence["dropped_total"] = report.Status.DroppedTotal
	var statusFile map[string]any
	readJSONFile(t, paths["status"], &statusFile)
	if statusFile["blocking"] != true || statusFile["owner"] != "mmwx" || statusFile["max_total"] != float64(limit) || statusFile["dropped_total"].(float64) < 4 {
		t.Fatalf("status file while blocking: %v", statusFile)
	}
	evidence["status_file_blocking"] = statusFile

	// 5. Hysteresis: just below the cap keeps blocking; below 95 % releases.
	release := *report.Status.ReleaseBelow
	for report.Status.Total >= limit && len(fillers) > 0 {
		fillers[len(fillers)-1].Close()
		fillers = fillers[:len(fillers)-1]
		time.Sleep(300 * time.Millisecond)
		report = readReport(t, paths["report"])
	}
	time.Sleep(1500 * time.Millisecond)
	report = readReport(t, paths["report"])
	if report.Status.Total >= release && !report.Status.Blocking {
		t.Fatalf("released inside the hysteresis band: total %d, release below %d", report.Status.Total, release)
	}
	evidence["hysteresis"] = map[string]any{"total": report.Status.Total, "release_below": release, "blocking": report.Status.Blocking}
	for _, conn := range fillers {
		conn.Close()
	}
	fillers = nil
	report = waitReport(t, paths["report"], "released", func(r netnsServerReport) bool { return !r.Status.Blocking && r.Status.Total < release })
	for name, addr := range map[string]string{"tcp core": coreAddr, "tcp forward": fwdAddr} {
		conn := mustDialTCP(t, client, addr)
		if err := echo(conn); err != nil {
			t.Fatalf("new %s after release: %v", name, err)
		}
		conn.Close()
	}
	for name, addr := range map[string]string{"udp core": coreAddr, "udp forward": fwdAddr} {
		conn := mustDialUDP(t, client, addr)
		if err := echo(conn); err != nil {
			t.Fatalf("new %s after release: %v", name, err)
		}
		conn.Close()
	}
	if rules := nftListTable(t, server); strings.Contains(rules, "hook prerouting") {
		t.Fatalf("released table still hooks prerouting:\n%s", rules)
	}

	// 6. Resource use with 1000+ sockets: loop time, CPU, RSS, nft calls.
	writeJSONFile(t, paths["settings"], machineProtectionSettings{Enabled: true, MaxTotal: int64Value(100000), TWBuckets: int64Value(1234), CloseWaitSeconds: int64Value(10)})
	for len(fillers) < 1200 {
		fillers = append(fillers, mustDialTCP(t, client, openAddr))
	}
	report = waitReport(t, paths["report"], "1200 sockets", func(r netnsServerReport) bool { return r.Status.Total >= 1200 })
	evidence["sockets_1200_total"] = report.Status.Total
	stopRole(serverProc)
	resources := map[string]any{}
	for _, algo := range []string{"old", "new"} {
		env := map[string]string{}
		for key, value := range serverEnv {
			env[key] = value
		}
		env["MP_ALGO"], env["MP_INTERVAL"], env["MP_REPORT"] = algo, "5s", filepath.Join(dir, "report-"+algo+".json")
		proc := startNetnsRole(t, server, env, filepath.Join(dir, "server-"+algo+".log"))
		first := waitReport(t, env["MP_REPORT"], algo+" warm", func(r netnsServerReport) bool { return r.Loops >= 2 })
		time.Sleep(60 * time.Second)
		last := readReport(t, env["MP_REPORT"])
		stopRole(proc)
		loops := last.Loops - first.Loops
		resources[algo] = map[string]any{
			"loops": loops, "total_sockets": last.Status.Total,
			"cpu_ms_per_loop_self":     float64(last.SelfCPUNanos-first.SelfCPUNanos) / float64(loops) / 1e6,
			"cpu_ms_per_loop_children": float64(last.ChildCPUNanos-first.ChildCPUNanos) / float64(loops) / 1e6,
			"nft_calls_per_minute":     float64(last.NftCalls-first.NftCalls) * 12 / float64(loops),
			"last_loop_ms":             float64(last.LastLoopNanos) / 1e6, "socket_read_ms": float64(last.ReadNanos) / 1e6, "rss_kb": last.RSSKB,
		}
	}
	evidence["resources_1200_sockets_5s_loop"] = resources
	for _, conn := range fillers {
		conn.Close()
	}

	// 7. Disable: table removed, TIME_WAIT restored inside the netns.
	serverProc = startNetnsRole(t, server, serverEnv, filepath.Join(dir, "server-final.log"))
	waitReport(t, paths["report"], "restart", func(r netnsServerReport) bool { return r.Status.Effective && r.Status.Configured.Enabled })
	writeJSONFile(t, paths["settings"], machineProtectionSettings{Enabled: false, MaxTotal: int64Value(100000)})
	waitReport(t, paths["report"], "disabled", func(r netnsServerReport) bool { return !r.Status.Configured.Enabled && r.Status.TWBuckets == nil })
	if got := readSysctlIn(t, server, "net/ipv4/tcp_max_tw_buckets"); got != originalTW {
		t.Fatalf("tw_buckets not restored in netns: %d, want %d", got, originalTW)
	}
	if out, err := exec.Command("ip", "netns", "exec", server, "nft", "list", "table", "inet", machineProtectionTableName).CombinedOutput(); err == nil {
		t.Fatalf("table still present after disable:\n%s", out)
	}
	evidence["netns_tw_buckets_after"] = readSysctlIn(t, server, "net/ipv4/tcp_max_tw_buckets")
	evidence["host_tw_buckets_after"] = readSysctlIn(t, "", "net/ipv4/tcp_max_tw_buckets")
	if path := os.Getenv("MMWXC_TEST_EVIDENCE"); path != "" {
		raw, _ := json.MarshalIndent(evidence, "", "  ")
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("evidence: %s", mustMarshal(evidence))
}

// TestMachineProtectionNetnsHelperProcess is the in-namespace side of the E2E
// (echo servers and the real protection loop); it only runs when spawned.
func TestMachineProtectionNetnsHelperProcess(t *testing.T) {
	role := os.Getenv("MP_ROLE")
	if role == "" {
		t.Skip("spawned by TestMachineProtectionNetnsE2E")
	}
	raiseNoFile()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	for _, raw := range strings.Split(os.Getenv("MP_PORTS"), ",") {
		if port, err := strconv.Atoi(raw); err == nil {
			go echoServers(ctx, port)
		}
	}
	if role == "agent" {
		runNetnsOfficialAgent(ctx)
	}
	if role == "server" {
		runNetnsProtectionLoop(ctx)
	}
	<-ctx.Done()
}

func runNetnsProtectionLoop(ctx context.Context) {
	interval, _ := time.ParseDuration(os.Getenv("MP_INTERVAL"))
	var corePorts []uint32
	for _, raw := range strings.Split(os.Getenv("MP_CORE_PORTS"), ",") {
		if port, err := strconv.Atoi(raw); err == nil {
			corePorts = append(corePorts, uint32(port))
		}
	}
	var nftCalls atomic.Int64
	manager := newMachineProtectionManager()
	manager.statusPath = os.Getenv("MP_STATUS")
	manager.flvxFile = flvxBusinessPortsReader{path: os.Getenv("MP_FLVX")}
	ops := manager.nft
	manager.nft = machineNftOps{
		apply:   func(ctx context.Context, script []byte) error { nftCalls.Add(1); return ops.apply(ctx, script) },
		remove:  func(ctx context.Context) error { nftCalls.Add(1); return ops.remove(ctx) },
		counter: func(ctx context.Context) (int64, error) { nftCalls.Add(1); return ops.counter(ctx) },
	}
	twPath := os.Getenv("MP_TWSTATE")
	if raw, err := os.ReadFile(twPath); err == nil {
		var saved machineTWSaved
		if json.Unmarshal(raw, &saved) == nil && saved.Applied > 0 {
			manager.tw = &saved
		}
	}
	manager.saveTW = func(saved *machineTWSaved) error {
		if saved == nil {
			return os.WriteFile(twPath, []byte("{}"), 0600)
		}
		raw, _ := json.Marshal(saved)
		return os.WriteFile(twPath, raw, 0600)
	}
	legacy := os.Getenv("MP_ALGO") == "old"
	var loops int64
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		started := time.Now()
		var settings machineProtectionSettings
		if raw, err := os.ReadFile(os.Getenv("MP_SETTINGS")); err == nil {
			_ = json.Unmarshal(raw, &settings)
		}
		report := netnsServerReport{Algorithm: os.Getenv("MP_ALGO")}
		if legacy {
			// Helper v0.7.8: two reconciles per loop, each nft -c + delete + apply,
			// two separate socket-table reads.
			readStart := time.Now()
			entries, _ := readTCPSockets()
			_, _ = readConnections()
			report.ReadNanos = time.Since(readStart).Nanoseconds()
			report.Status.Total = summarizeTCP(entries).Total
			for range 2 {
				for _, args := range [][]string{{"-c", "-f", "-"}, {"delete", "table", "inet", machineProtectionTableName}, {"-f", "-"}} {
					command := exec.Command("nft", args...)
					if args[len(args)-1] == "-" {
						command.Stdin = strings.NewReader(legacyMachineProtectionNft(corePorts, false))
					}
					_ = command.Run()
					nftCalls.Add(1)
				}
			}
		} else {
			readStart := time.Now()
			tables := readSocketTables(procSocketTables)
			report.ReadNanos = time.Since(readStart).Nanoseconds()
			machine := tables.Machine
			input := machineProtectionInput{System: summarizeTCP(tables.TCP), Machine: &machine, CorePorts: corePorts}
			if config := os.Getenv("MP_OFFICIAL_CONFIG"); config != "" {
				input.Official = officialAgentInput(config, false, tables.TCP)
			}
			report.Status = manager.reconcile(ctx, &settings, input)
			report.Status = manager.reconcile(ctx, &settings, input)
		}
		loops++
		report.Loops, report.LastLoopNanos, report.NftCalls = loops, time.Since(started).Nanoseconds(), nftCalls.Load()
		var self, children syscall.Rusage
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &self)
		_ = syscall.Getrusage(syscall.RUSAGE_CHILDREN, &children)
		report.SelfCPUNanos = self.Utime.Nano() + self.Stime.Nano()
		report.ChildCPUNanos = children.Utime.Nano() + children.Stime.Nano()
		report.RSSKB = currentRSSKB()
		raw, _ := json.Marshal(report)
		_ = os.WriteFile(os.Getenv("MP_REPORT")+".tmp", raw, 0644)
		_ = os.Rename(os.Getenv("MP_REPORT")+".tmp", os.Getenv("MP_REPORT"))
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func legacyMachineProtectionNft(ports []uint32, blocking bool) string {
	var output strings.Builder
	fmt.Fprintf(&output, "table inet %s {\n", machineProtectionTableName)
	output.WriteString("  chain input {\n    type filter hook input priority -4; policy accept;\n")
	output.WriteString("    iifname \"lo\" return\n    ct state established,related return\n")
	if blocking && len(ports) > 0 {
		values := make([]string, len(ports))
		for index, port := range ports {
			values[index] = strconv.Itoa(int(port))
		}
		fmt.Fprintf(&output, "    tcp dport { %s } ct state new counter drop\n", strings.Join(values, ", "))
	}
	output.WriteString("  }\n}\n")
	return output.String()
}

func currentRSSKB() int64 {
	raw, _ := os.ReadFile("/proc/self/status")
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				value, _ := strconv.ParseInt(fields[1], 10, 64)
				return value
			}
		}
	}
	return 0
}

func echoServers(ctx context.Context, port int) {
	listener, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err == nil {
		go func() {
			<-ctx.Done()
			listener.Close()
		}()
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go func() {
					defer conn.Close()
					_, _ = io.Copy(conn, conn)
				}()
			}
		}()
	}
	packet, err := net.ListenPacket("udp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return
	}
	go func() {
		<-ctx.Done()
		packet.Close()
	}()
	buffer := make([]byte, 2048)
	for {
		n, addr, err := packet.ReadFrom(buffer)
		if err != nil {
			return
		}
		_, _ = packet.WriteTo(buffer[:n], addr)
	}
}

func raiseNoFile() {
	limit := syscall.Rlimit{Cur: 65535, Max: 65535}
	_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit)
}

func startNetnsRole(t *testing.T, namespace string, env map[string]string, logPath string) *exec.Cmd {
	t.Helper()
	return startNetnsRoleAs(t, namespace, os.Args[0], env, logPath)
}

// startNetnsRoleAs runs the role from binary; a symlink named mmw-agent gives
// the process the official Agent's comm.
func startNetnsRoleAs(t *testing.T, namespace, binary string, env map[string]string, logPath string) *exec.Cmd {
	t.Helper()
	command := exec.Command("ip", "netns", "exec", namespace, binary, "-test.run", "^TestMachineProtectionNetnsHelperProcess$", "-test.v")
	command.Env = os.Environ()
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	return command
}

var stopped sync.Map

func stopRole(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	if _, done := stopped.LoadOrStore(command, true); done {
		return
	}
	// ip netns exec replaces itself with the test binary, so this is the role.
	_ = command.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		<-done
	}
}

func readReport(t *testing.T, path string) netnsServerReport {
	t.Helper()
	var report netnsServerReport
	raw, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(raw, &report)
	}
	return report
}

func waitReport(t *testing.T, path, what string, ready func(netnsServerReport) bool) netnsServerReport {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		report := readReport(t, path)
		if ready(report) {
			return report
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: %s", what, mustMarshal(report))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func resourceEvidence(report netnsServerReport) map[string]any {
	return map[string]any{"rss_kb": report.RSSKB, "self_cpu_ms": report.SelfCPUNanos / 1e6, "loops": report.Loops}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	raw, _ := json.Marshal(value)
	if err := os.WriteFile(path+".tmp", raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
}

func readJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, value); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func mustMarshal(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func readSysctlIn(t *testing.T, namespace, key string) int64 {
	t.Helper()
	args := []string{"cat", "/proc/sys/" + key}
	if namespace != "" {
		args = append([]string{"ip", "netns", "exec", namespace}, args...)
	}
	out, err := exec.Command(args[0], args[1:]...).Output()
	if err != nil {
		t.Fatalf("read %s in %q: %v", key, namespace, err)
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func nftListTable(t *testing.T, namespace string) string {
	t.Helper()
	out, err := exec.Command("ip", "netns", "exec", namespace, "nft", "list", "table", "inet", machineProtectionTableName).CombinedOutput()
	if err != nil {
		t.Fatalf("list table: %v %s", err, out)
	}
	return string(out)
}

// inNetns runs fn on a thread switched into the named namespace; sockets made
// there stay in that namespace. Go spawns new threads from a clean template
// thread while this one is locked.
func inNetns(namespace string, fn func()) error {
	runtime.LockOSThread()
	original, err := os.Open("/proc/thread-self/ns/net")
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer original.Close()
	target, err := os.Open("/var/run/netns/" + namespace)
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer target.Close()
	if _, _, errno := syscall.RawSyscall(sysSetns, target.Fd(), syscall.CLONE_NEWNET, 0); errno != 0 {
		runtime.UnlockOSThread()
		return errno
	}
	fn()
	if _, _, errno := syscall.RawSyscall(sysSetns, original.Fd(), syscall.CLONE_NEWNET, 0); errno != 0 {
		// Leave the thread locked: it exits with this goroutine.
		return errno
	}
	runtime.UnlockOSThread()
	return nil
}

func dialTCPIn(namespace, addr string, timeout time.Duration) (net.Conn, error) {
	var conn net.Conn
	var dialErr error
	done := make(chan error, 1)
	go func() {
		done <- inNetns(namespace, func() { conn, dialErr = net.DialTimeout("tcp4", addr, timeout) })
	}()
	if err := <-done; err != nil {
		return nil, err
	}
	return conn, dialErr
}

func mustDialTCP(t *testing.T, namespace, addr string) net.Conn {
	t.Helper()
	conn, err := dialTCPIn(namespace, addr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial %s in %s: %v", addr, namespace, err)
	}
	return conn
}

func mustDialUDP(t *testing.T, namespace, addr string) net.Conn {
	t.Helper()
	var conn net.Conn
	var dialErr error
	done := make(chan error, 1)
	go func() {
		done <- inNetns(namespace, func() { conn, dialErr = net.Dial("udp4", addr) })
	}()
	if err := <-done; err != nil || dialErr != nil {
		t.Fatalf("udp %s in %s: %v %v", addr, namespace, err, dialErr)
	}
	return conn
}

func echo(conn net.Conn) error {
	payload := []byte("mmwxc-" + strconv.FormatInt(time.Now().UnixNano(), 36))
	_ = conn.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	defer conn.SetDeadline(time.Time{})
	if _, err := conn.Write(payload); err != nil {
		return err
	}
	buffer := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		return err
	}
	if !bytes.Equal(buffer, payload) {
		return fmt.Errorf("echo mismatch")
	}
	return nil
}

// Without any NAT table (a plain mmwx server) conntrack is only switched on
// by our own idle table. Connections that existed before it, an idle TCP one
// and an active UDP flow, must survive once blocking starts; new ones drop.
func TestMachineProtectionNetnsConntrackWarmup(t *testing.T) {
	if os.Getenv("MMWXC_TEST_MACHINE_NETNS") != "1" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_TEST_MACHINE_NETNS=1 and run as root")
	}
	suffix := strconv.Itoa(os.Getpid()%100000 + 1)
	client, server := "mwc"+suffix, "mws"+suffix
	ns := flvxNetns{t: t}
	flvxNetnsCleanup(client, server)
	t.Cleanup(func() { flvxNetnsCleanup(client, server) })
	for _, name := range []string{client, server} {
		ns.sh("ip", "netns", "add", name)
		ns.sh("ip", "-n", name, "link", "set", "lo", "up")
	}
	ns.sh("ip", "link", "add", "mwc0"+suffix, "netns", client, "type", "veth", "peer", "name", "mws0"+suffix, "netns", server)
	ns.sh("ip", "-n", client, "addr", "add", "10.232.1.2/24", "dev", "mwc0"+suffix)
	ns.sh("ip", "-n", server, "addr", "add", "10.232.1.1/24", "dev", "mws0"+suffix)
	ns.sh("ip", "-n", client, "link", "set", "mwc0"+suffix, "up")
	ns.sh("ip", "-n", server, "link", "set", "mws0"+suffix, "up")
	if out, _ := exec.Command("ip", "netns", "exec", server, "nft", "list", "ruleset").Output(); len(bytes.TrimSpace(out)) != 0 {
		t.Fatalf("server namespace must start without any nft table:\n%s", out)
	}
	dir := t.TempDir()
	settings, report := filepath.Join(dir, "settings.json"), filepath.Join(dir, "report.json")
	writeJSONFile(t, settings, machineProtectionSettings{Enabled: false})
	listeners := startNetnsRole(t, server, map[string]string{"MP_ROLE": "echo", "MP_PORTS": "10443"}, filepath.Join(dir, "listeners.log"))
	defer stopRole(listeners)
	loop := startNetnsRole(t, server, map[string]string{"MP_ROLE": "server", "MP_CORE_PORTS": "10443", "MP_SETTINGS": settings, "MP_REPORT": report,
		"MP_STATUS": filepath.Join(dir, "status.json"), "MP_FLVX": filepath.Join(dir, "absent.json"), "MP_TWSTATE": filepath.Join(dir, "tw.json"), "MP_INTERVAL": "500ms", "MP_ALGO": "new"}, filepath.Join(dir, "loop.log"))
	defer stopRole(loop)
	waitReport(t, report, "loop up", func(r netnsServerReport) bool { return r.Loops > 1 })

	addr := "10.232.1.1:10443"
	idleTCP := mustDialTCP(t, client, addr)
	defer idleTCP.Close()
	activeUDP := mustDialUDP(t, client, addr)
	defer activeUDP.Close()
	if echo(idleTCP) != nil || echo(activeUDP) != nil {
		t.Fatal("echo before protection failed")
	}
	// Over the cap at once; the guard must warm conntrack up before dropping.
	writeJSONFile(t, settings, machineProtectionSettings{Enabled: true, MaxTotal: int64Value(1), TWBuckets: int64Value(0)})
	stop := make(chan struct{})
	udpErrors := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				close(udpErrors)
				return
			case <-time.After(300 * time.Millisecond):
				if err := echo(activeUDP); err != nil {
					udpErrors <- err
					close(udpErrors)
					return
				}
			}
		}
	}()
	status := waitReport(t, report, "blocking", func(r netnsServerReport) bool { return r.Status.Blocking }).Status
	time.Sleep(time.Second)
	close(stop)
	if err := <-udpErrors; err != nil {
		t.Fatalf("active UDP flow broke while protection started: %v", err)
	}
	if err := echo(idleTCP); err != nil {
		t.Fatalf("idle TCP connection from before the table broke: %v", err)
	}
	if conn, err := dialTCPIn(client, addr, 1500*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("new TCP accepted while blocking")
	}
	fresh := mustDialUDP(t, client, addr)
	defer fresh.Close()
	if echo(fresh) == nil {
		t.Fatal("new UDP flow answered while blocking")
	}
	t.Logf("warm-up evidence: blocking=%v dropped=%d total=%d", status.Blocking, readReport(t, report).Status.DroppedTotal, status.Total)
}
