package main

import (
	"context"
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
)

// Embedded-style end-to-end with real nft, inside private namespaces only:
//
//	client oec 10.233.1.2 ── 10.233.1.1 server oes
//
// The server runs a process named mmw-agent (the official Agent) that serves
// two inbounds itself, its management port, a loopback API and an outbound
// relay UDP socket; a foreign listener (x-ui style) and the real protection
// loop with no mmwx Core port at all.
const (
	netnsEmbeddedIP      = "10.233.1.1"
	netnsAgentPort       = 11021 // Mieru-style inbound, TCP + UDP
	netnsAgentRangePort  = 50409 // SS inbound inside the ephemeral range
	netnsAgentManagement = 23889
	netnsAgentLoopback   = 46736
	netnsAgentAdded      = 11022 // a node added while protection runs
	netnsForeignPort     = 10022
)

func TestMachineProtectionNetnsOfficialEmbeddedE2E(t *testing.T) {
	if os.Getenv("MMWXC_TEST_MACHINE_NETNS") != "1" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_TEST_MACHINE_NETNS=1 and run as root")
	}
	raiseNoFile()
	suffix := strconv.Itoa(os.Getpid()%100000 + 2)
	client, server := "oec"+suffix, "oes"+suffix
	ns := flvxNetns{t: t}
	flvxNetnsCleanup(client, server)
	t.Cleanup(func() { flvxNetnsCleanup(client, server) })
	for _, name := range []string{client, server} {
		ns.sh("ip", "netns", "add", name)
		ns.sh("ip", "-n", name, "link", "set", "lo", "up")
	}
	ns.sh("ip", "link", "add", "oec0"+suffix, "netns", client, "type", "veth", "peer", "name", "oes0"+suffix, "netns", server)
	ns.sh("ip", "-n", client, "addr", "add", "10.233.1.2/24", "dev", "oec0"+suffix)
	ns.sh("ip", "-n", server, "addr", "add", netnsEmbeddedIP+"/24", "dev", "oes0"+suffix)
	ns.sh("ip", "-n", client, "link", "set", "oec0"+suffix, "up")
	ns.sh("ip", "-n", server, "link", "set", "oes0"+suffix, "up")

	dir := t.TempDir()
	paths := map[string]string{
		"settings": filepath.Join(dir, "settings.json"), "report": filepath.Join(dir, "report.json"), "status": filepath.Join(dir, "mmwxc", "machine-protection.json"),
		"config": filepath.Join(dir, "config.yaml"), "outbound": filepath.Join(dir, "outbound-port"), "add": filepath.Join(dir, "add-port"),
	}
	writeJSONFile(t, paths["settings"], machineProtectionSettings{Enabled: false})
	if err := os.WriteFile(paths["config"], []byte("mode: agent\ntoken: test\nxray_mode: embedded\nlisten_port: \""+strconv.Itoa(netnsAgentManagement)+"\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	agentBinary := filepath.Join(dir, officialAgentComm)
	if err := os.Symlink(self, agentBinary); err != nil {
		t.Fatal(err)
	}
	agent := startNetnsRoleAs(t, server, agentBinary, map[string]string{"MP_ROLE": "agent",
		"MP_PORTS":          fmt.Sprintf("%d,%d,%d", netnsAgentPort, netnsAgentRangePort, netnsAgentManagement),
		"MP_LOOPBACK_PORTS": strconv.Itoa(netnsAgentLoopback), "MP_OUTBOUND_UDP_INFO": paths["outbound"], "MP_ADD_PORT_FILE": paths["add"]}, filepath.Join(dir, "agent.log"))
	defer stopRole(agent)
	foreign := startNetnsRole(t, server, map[string]string{"MP_ROLE": "echo", "MP_PORTS": strconv.Itoa(netnsForeignPort)}, filepath.Join(dir, "foreign.log"))
	defer stopRole(foreign)
	evidence := map[string]any{}
	deadline := time.Now().Add(10 * time.Second)
	for processComm("/proc", agent.Process.Pid) != officialAgentComm {
		if time.Now().After(deadline) {
			t.Fatalf("agent role comm = %q, want %s", processComm("/proc", agent.Process.Pid), officialAgentComm)
		}
		time.Sleep(100 * time.Millisecond)
	}
	outbound := waitPortFile(t, paths["outbound"])
	evidence["agent_comm"], evidence["agent_outbound_udp_port"] = officialAgentComm, outbound

	loop := startNetnsRole(t, server, map[string]string{"MP_ROLE": "server", "MP_CORE_PORTS": "", "MP_SETTINGS": paths["settings"], "MP_REPORT": paths["report"],
		"MP_STATUS": paths["status"], "MP_FLVX": filepath.Join(dir, "absent.json"), "MP_TWSTATE": filepath.Join(dir, "tw.json"), "MP_INTERVAL": "500ms", "MP_ALGO": "new",
		"MP_OFFICIAL_CONFIG": paths["config"]}, filepath.Join(dir, "loop.log"))
	defer stopRole(loop)
	waitReport(t, paths["report"], "loop up", func(r netnsServerReport) bool { return r.Loops > 2 })

	// 1. Enabled with a high cap: idle table holding exactly the Agent's inbounds.
	writeJSONFile(t, paths["settings"], machineProtectionSettings{Enabled: true, MaxTotal: int64Value(100000), TWBuckets: int64Value(0), CloseWaitSeconds: int64Value(0)})
	report := waitReport(t, paths["report"], "official ports", func(r netnsServerReport) bool {
		return r.Status.Effective && r.Status.Configured.Enabled && r.Status.Sources != nil && r.Status.Sources.OfficialEmbedded == 2
	})
	wantPorts := []uint32{netnsAgentPort, netnsAgentRangePort}
	if !reflect.DeepEqual(report.Status.TCPPorts, wantPorts) || !reflect.DeepEqual(report.Status.UDPPorts, wantPorts) || report.Status.Sources.Core != 0 || report.Status.IdleReason != "" {
		t.Fatalf("official ports = TCP %v UDP %v sources %#v idle %q", report.Status.TCPPorts, report.Status.UDPPorts, report.Status.Sources, report.Status.IdleReason)
	}
	if containsPort(report.Status.UDPPorts, uint32(outbound)) {
		t.Fatalf("the Agent's outbound relay UDP port %d must not be guarded", outbound)
	}
	idleRules := nftListTable(t, server)
	for _, foreignPort := range []int{netnsAgentManagement, netnsAgentLoopback, netnsForeignPort} {
		if strings.Contains(idleRules, strconv.Itoa(foreignPort)) {
			t.Fatalf("port %d must not be guarded:\n%s", foreignPort, idleRules)
		}
	}
	if strings.Contains(idleRules, "hook prerouting") || !strings.Contains(idleRules, strconv.Itoa(netnsAgentPort)) {
		t.Fatalf("idle table must hold the Agent inbounds and no hook:\n%s", idleRules)
	}
	evidence["idle_status_sources"], evidence["idle_table"] = report.Status.Sources, idleRules

	// 2. Connections that exist before the cap is reached.
	agentAddr, rangeAddr := fmt.Sprintf("%s:%d", netnsEmbeddedIP, netnsAgentPort), fmt.Sprintf("%s:%d", netnsEmbeddedIP, netnsAgentRangePort)
	managementAddr, foreignAddr := fmt.Sprintf("%s:%d", netnsEmbeddedIP, netnsAgentManagement), fmt.Sprintf("%s:%d", netnsEmbeddedIP, netnsForeignPort)
	existingTCP, existingRange, existingUDP := mustDialTCP(t, client, agentAddr), mustDialTCP(t, client, rangeAddr), mustDialUDP(t, client, agentAddr)
	defer existingTCP.Close()
	defer existingRange.Close()
	defer existingUDP.Close()
	existing := map[string]net.Conn{"tcp agent": existingTCP, "tcp agent (ephemeral range)": existingRange, "udp agent": existingUDP}
	for name, conn := range existing {
		if err := echo(conn); err != nil {
			t.Fatalf("existing %s before blocking: %v", name, err)
		}
	}
	time.Sleep(time.Second)
	report = waitReport(t, paths["report"], "existing counted", func(r netnsServerReport) bool { return r.Status.Connections != nil })
	limit := report.Status.Total + 30
	writeJSONFile(t, paths["settings"], machineProtectionSettings{Enabled: true, MaxTotal: int64Value(limit), TWBuckets: int64Value(0), CloseWaitSeconds: int64Value(0)})
	evidence["cap"], evidence["base_total"] = limit, report.Status.Total

	// 3. Over the cap through the foreign (non-business) port.
	var fillers []net.Conn
	for len(fillers) < 34 {
		fillers = append(fillers, mustDialTCP(t, client, foreignAddr))
	}
	defer func() {
		for _, conn := range fillers {
			conn.Close()
		}
	}()
	report = waitReport(t, paths["report"], "blocking", func(r netnsServerReport) bool { return r.Status.Blocking })
	blockingRules := nftListTable(t, server)
	evidence["blocking_table"] = blockingRules

	// 4. New connections to the Agent's inbounds are dropped; its management
	// port, the foreign listener, loopback and existing connections still work.
	for name, addr := range map[string]string{"tcp agent": agentAddr, "tcp agent (ephemeral range)": rangeAddr} {
		if conn, err := dialTCPIn(client, addr, 1500*time.Millisecond); err == nil {
			conn.Close()
			t.Fatalf("new %s connection was accepted while blocking", name)
		}
	}
	freshUDP := mustDialUDP(t, client, agentAddr)
	if echo(freshUDP) == nil {
		t.Fatal("new UDP flow to the Agent inbound was answered while blocking")
	}
	freshUDP.Close()
	for name, addr := range map[string]string{"management": managementAddr, "foreign": foreignAddr} {
		conn := mustDialTCP(t, client, addr)
		if err := echo(conn); err != nil {
			t.Fatalf("%s port while blocking: %v", name, err)
		}
		conn.Close()
	}
	loopback, err := dialTCPIn(server, fmt.Sprintf("127.0.0.1:%d", netnsAgentPort), 2*time.Second)
	if err != nil || echo(loopback) != nil {
		t.Fatalf("loopback connection to the Agent inbound while blocking: %v", err)
	}
	loopback.Close()
	for name, conn := range existing {
		if err := echo(conn); err != nil {
			t.Fatalf("existing %s broke while blocking: %v", name, err)
		}
	}
	report = waitReport(t, paths["report"], "drops counted", func(r netnsServerReport) bool { return r.Status.DroppedTotal >= 3 })
	evidence["dropped_total"], evidence["blocking_status_sources"] = report.Status.DroppedTotal, report.Status.Sources

	// 5. A node is added while blocking: its listener is guarded within seconds.
	added := time.Now()
	if err := os.WriteFile(paths["add"], []byte(strconv.Itoa(netnsAgentAdded)), 0644); err != nil {
		t.Fatal(err)
	}
	report = waitReport(t, paths["report"], "added node guarded", func(r netnsServerReport) bool { return containsPort(r.Status.TCPPorts, netnsAgentAdded) })
	evidence["added_node_guarded_after_seconds"] = time.Since(added).Seconds()
	if conn, err := dialTCPIn(client, fmt.Sprintf("%s:%d", netnsEmbeddedIP, netnsAgentAdded), 1500*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("new connection to the added node was accepted while blocking")
	}

	// 6. Below 95 %: released, new connections work again.
	for _, conn := range fillers {
		conn.Close()
	}
	fillers = nil
	report = waitReport(t, paths["report"], "released", func(r netnsServerReport) bool { return !r.Status.Blocking })
	for _, addr := range []string{agentAddr, rangeAddr, fmt.Sprintf("%s:%d", netnsEmbeddedIP, netnsAgentAdded)} {
		conn := mustDialTCP(t, client, addr)
		if err := echo(conn); err != nil {
			t.Fatalf("new connection to %s after release: %v", addr, err)
		}
		conn.Close()
	}
	afterUDP := mustDialUDP(t, client, agentAddr)
	if err := echo(afterUDP); err != nil {
		t.Fatalf("new UDP flow after release: %v", err)
	}
	afterUDP.Close()
	evidence["loops"], evidence["self_cpu_ms_per_loop"], evidence["rss_kb"] = report.Loops, float64(report.SelfCPUNanos)/float64(report.Loops)/1e6, report.RSSKB

	// 7. Disable: the table goes away.
	writeJSONFile(t, paths["settings"], machineProtectionSettings{Enabled: false, MaxTotal: int64Value(limit)})
	waitReport(t, paths["report"], "disabled", func(r netnsServerReport) bool { return !r.Status.Configured.Enabled })
	if out, err := exec.Command("ip", "netns", "exec", server, "nft", "list", "table", "inet", machineProtectionTableName).CombinedOutput(); err == nil {
		t.Fatalf("table still present after disable:\n%s", out)
	}
	if path := os.Getenv("MMWXC_TEST_EVIDENCE_EMBEDDED"); path != "" {
		raw, _ := json.MarshalIndent(evidence, "", "  ")
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("evidence: %s", mustMarshal(evidence))
}

// runNetnsOfficialAgent adds the official Agent's other sockets to the echo
// inbounds: a loopback API, an outbound relay UDP socket and, on request, a
// newly created inbound.
func runNetnsOfficialAgent(ctx context.Context) {
	for _, raw := range strings.Split(os.Getenv("MP_LOOPBACK_PORTS"), ",") {
		if port, err := strconv.Atoi(raw); err == nil {
			if listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
				defer listener.Close()
			}
		}
	}
	if info := os.Getenv("MP_OUTBOUND_UDP_INFO"); info != "" {
		if packet, err := net.ListenPacket("udp4", "0.0.0.0:0"); err == nil {
			defer packet.Close()
			_ = os.WriteFile(info, []byte(strconv.Itoa(packet.LocalAddr().(*net.UDPAddr).Port)), 0644)
		}
	}
	addFile := os.Getenv("MP_ADD_PORT_FILE")
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for added := false; ; {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if raw, err := os.ReadFile(addFile); err == nil && !added && addFile != "" {
			if port, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				added = true
				go echoServers(ctx, port)
			}
		}
	}
}

func waitPortFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if raw, err := os.ReadFile(path); err == nil {
			if port, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && port > 0 {
				return port
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no port in %s", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
