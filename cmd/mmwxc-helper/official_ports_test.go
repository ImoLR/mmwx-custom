package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// officialProc is a fake /proc: processes with comm + socket fds, and the four
// socket tables with inodes.
type officialProc struct {
	t    *testing.T
	root string
	rows map[string][]string
}

func newOfficialProc(t *testing.T) *officialProc {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ip_local_port_range"), []byte("32768\t60999\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return &officialProc{t: t, root: root, rows: map[string][]string{}}
}

func (proc *officialProc) scanner() officialPortsScanner {
	return officialPortsScanner{procRoot: proc.root, portRange: filepath.Join(proc.root, "ip_local_port_range"), comm: officialAgentComm}
}

func (proc *officialProc) process(pid int, comm string, links ...string) {
	proc.t.Helper()
	dir := filepath.Join(proc.root, fmt.Sprint(pid))
	if err := os.MkdirAll(filepath.Join(dir, "fd"), 0755); err != nil {
		proc.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0644); err != nil {
		proc.t.Fatal(err)
	}
	for index, link := range links {
		if err := os.Symlink(link, filepath.Join(dir, "fd", fmt.Sprint(index+3))); err != nil {
			proc.t.Fatal(err)
		}
	}
}

// socket adds a row: table tcp/tcp6/udp/udp6, local "ip:port" in the kernel's
// hex form, state, inode.
func (proc *officialProc) socket(table, local, remote, state string, inode int) {
	proc.rows[table] = append(proc.rows[table], fmt.Sprintf("%4d: %s %s %s 00000000:00000000 00:00000000 00000000     0        0 %d 1 0000000000000000 100 0 0 10 0", len(proc.rows[table]), local, remote, state, inode))
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	if err := os.WriteFile(filepath.Join(proc.root, "net", table), []byte(header+strings.Join(proc.rows[table], "\n")+"\n"), 0644); err != nil {
		proc.t.Fatal(err)
	}
}

func hex4(a, b, c, d byte, port uint16) string {
	return fmt.Sprintf("%02X%02X%02X%02X:%04X", d, c, b, a, port)
}

func hex6Any(port uint16) string { return strings.Repeat("0", 32) + fmt.Sprintf(":%04X", port) }

const (
	anyRemote4 = "00000000:0000"
	anyRemote6 = "00000000000000000000000000000000:0000"
)

// embeddedFixture mirrors 「Boil HKT」(1) / 「Dmit T1」(10) / 「Nobrand KFC 1cm」(9):
// the Agent's inbounds, its loopback API and management port, an outbound
// relay socket, and listeners that are not ours (x-ui, mita, kernel WireGuard).
func embeddedFixture(t *testing.T) *officialProc {
	proc := newOfficialProc(t)
	proc.process(355, "mmw-agent",
		"socket:[1001]", "socket:[1002]", "socket:[1003]", "socket:[1004]", "socket:[1005]", "socket:[1006]",
		"socket:[1007]", "socket:[1008]", "pipe:[77]", "socket:[1009]", "socket:[1010]", "anon_inode:[eventpoll]")
	proc.process(1062523, "x-ui", "socket:[2001]", "socket:[2002]")
	proc.process(488, "mita", "socket:[3001]")
	proc.process(369, "sshd", "socket:[4001]")
	proc.socket("tcp6", hex6Any(10015), anyRemote6, "0A", 1001)                       // vless *:10015
	proc.socket("tcp", hex4(127, 0, 0, 1, 46736), anyRemote4, "0A", 1002)             // Agent API on loopback
	proc.socket("tcp6", hex6Any(23889), anyRemote6, "0A", 1003)                       // Agent management port
	proc.socket("udp6", hex6Any(10015), anyRemote6, "07", 1004)                       // SS UDP on the same port
	proc.socket("udp", hex4(0, 0, 0, 0, 41000), anyRemote4, "07", 1005)               // outbound relay socket
	proc.socket("udp", hex4(0, 0, 0, 0, 5000), anyRemote4, "07", 1006)                // UDP-only inbound below the ephemeral range
	proc.socket("tcp", hex4(10, 0, 0, 1, 10015), hex4(1, 2, 3, 4, 50000), "01", 1007) // a live client connection
	proc.socket("udp", hex4(10, 0, 0, 1, 42000), hex4(8, 8, 8, 8, 53), "01", 1008)    // connected UDP
	proc.socket("tcp", hex4(0, 0, 0, 0, 50409), anyRemote4, "0A", 1009)               // SS inside the ephemeral range
	proc.socket("udp", hex4(0, 0, 0, 0, 50409), anyRemote4, "07", 1010)               // its UDP twin
	proc.socket("tcp", hex4(0, 0, 0, 0, 2096), anyRemote4, "0A", 2001)                // x-ui
	proc.socket("udp6", hex6Any(51501), anyRemote6, "07", 2002)                       // x-ui's xray
	proc.socket("tcp6", hex6Any(22511), anyRemote6, "0A", 3001)                       // mita
	proc.socket("tcp", hex4(0, 0, 0, 0, 22), anyRemote4, "0A", 4001)                  // sshd
	proc.socket("udp", hex4(0, 0, 0, 0, 22501), anyRemote4, "07", 0)                  // kernel WireGuard
	return proc
}

func TestOfficialPortsScanOnlyAgentInbounds(t *testing.T) {
	proc := embeddedFixture(t)
	scanner := proc.scanner()
	ports, errText := scanner.current(officialAgentView{Embedded: true, ManagementPort: 23889}, time.Now())
	want := machinePorts{TCP: []uint32{10015, 50409}, UDP: []uint32{5000, 10015, 50409}}
	if errText != "" || !reflect.DeepEqual(ports, want) {
		t.Fatalf("official ports = %#v (%q), want %#v", ports, errText, want)
	}
	for _, foreign := range []uint32{22, 2096, 22511, 22501, 23889, 46736, 41000, 42000, 51501} {
		if containsPort(ports.TCP, foreign) || containsPort(ports.UDP, foreign) {
			t.Fatalf("port %d is not an official inbound: %#v", foreign, ports)
		}
	}
	// Without a readable ephemeral range only UDP twins of TCP listeners count.
	scanner = proc.scanner()
	scanner.portRange = filepath.Join(proc.root, "missing")
	if ports, _ := scanner.current(officialAgentView{Embedded: true, ManagementPort: 23889}, time.Now()); !reflect.DeepEqual(ports.UDP, []uint32{10015, 50409}) {
		t.Fatalf("unknown ephemeral range: %#v", ports)
	}
}

func TestOfficialPortsOnlyInEmbeddedMode(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")
	sockets := []socketEntry{{LocalPort: 10015, State: tcpListen}, {LocalPort: 22, State: tcpListen}, {LocalPort: 443, State: tcpEstablished}}
	write := func(body string) {
		if err := os.WriteFile(config, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("mode: agent\ntoken: secret\nxray_mode: embedded\nlisten_port: \"23889\"\n")
	if view := officialAgentInput(config, false, sockets); !view.Embedded || view.ManagementPort != 23889 || view.TCPListen != "22,10015" {
		t.Fatalf("embedded view = %#v", view)
	}
	if view := officialAgentInput(config, true, sockets); view != (officialAgentView{}) {
		t.Fatalf("External ownership must switch the source off: %#v", view)
	}
	write("xray_mode: embedded\nlisten_port: 24000\n")
	if view := officialAgentInput(config, false, nil); view.ManagementPort != 24000 || view.TCPListen != "" {
		t.Fatalf("unquoted listen_port: %#v", view)
	}
	write("xray_mode: embedded\n")
	if view := officialAgentInput(config, false, nil); view.ManagementPort != defaultOfficialAgentPort {
		t.Fatalf("default management port: %#v", view)
	}
	write("xray_mode: external\nlisten_port: \"23889\"\n")
	if view := officialAgentInput(config, false, sockets); view.Embedded {
		t.Fatalf("External mode: %#v", view)
	}
	if view := officialAgentInput(filepath.Join(dir, "absent.yaml"), false, sockets); view.Embedded {
		t.Fatalf("no official Agent config: %#v", view)
	}
}

func TestOfficialPortsRescanCadence(t *testing.T) {
	proc := embeddedFixture(t)
	scanner := proc.scanner()
	start := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	view := officialAgentView{Embedded: true, ManagementPort: 23889, TCPListen: "a"}
	scanner.current(view, start)
	// A node is added: its listener appears.
	if err := os.Symlink("socket:[1011]", filepath.Join(proc.root, "355", "fd", "99")); err != nil {
		t.Fatal(err)
	}
	proc.socket("tcp6", hex6Any(10016), anyRemote6, "0A", 1011)
	if ports, _ := scanner.current(view, start.Add(10*time.Second)); containsPort(ports.TCP, 10016) {
		t.Fatal("rescanned inside 30 s without a listener change")
	}
	view.TCPListen = "b"
	if ports, _ := scanner.current(view, start.Add(12*time.Second)); !containsPort(ports.TCP, 10016) {
		t.Fatalf("a TCP listener change must rescan at once: %#v", ports)
	}
	if err := os.Symlink("socket:[1012]", filepath.Join(proc.root, "355", "fd", "100")); err != nil {
		t.Fatal(err)
	}
	proc.socket("tcp6", hex6Any(10017), anyRemote6, "0A", 1012)
	view.TCPListen = "c"
	if ports, _ := scanner.current(view, start.Add(14*time.Second)); containsPort(ports.TCP, 10017) {
		t.Fatal("listener changes rescan at most every 5 s")
	}
	if ports, _ := scanner.current(view, start.Add(18*time.Second)); !containsPort(ports.TCP, 10017) {
		t.Fatalf("pending listener change after the gap: %#v", ports)
	}
	// UDP-only changes are picked up by the 30 s refresh.
	if err := os.Symlink("socket:[1013]", filepath.Join(proc.root, "355", "fd", "101")); err != nil {
		t.Fatal(err)
	}
	proc.socket("udp", hex4(0, 0, 0, 0, 6000), anyRemote4, "07", 1013)
	if ports, _ := scanner.current(view, start.Add(47*time.Second)); containsPort(ports.UDP, 6000) {
		t.Fatal("rescanned before 30 s")
	}
	if ports, _ := scanner.current(view, start.Add(48*time.Second)); !containsPort(ports.UDP, 6000) {
		t.Fatalf("30 s refresh: %#v", ports)
	}
	// A different management port rescans at once and is excluded.
	view.ManagementPort = 10015
	if ports, _ := scanner.current(view, start.Add(49*time.Second)); containsPort(ports.TCP, 10015) || containsPort(ports.UDP, 10015) {
		t.Fatalf("management port must be excluded: %#v", ports)
	}
	// Leaving Embedded drops the source and the cache; coming back scans at once.
	if ports, errText := scanner.current(officialAgentView{}, start.Add(50*time.Second)); !ports.empty() || errText != "" || scanner.active {
		t.Fatalf("External: %#v %q", ports, errText)
	}
	view.ManagementPort = 23889
	if ports, _ := scanner.current(view, start.Add(51*time.Second)); !containsPort(ports.TCP, 10015) {
		t.Fatalf("back to Embedded: %#v", ports)
	}
}

func TestOfficialPortsAgentRestartAndAbsence(t *testing.T) {
	proc := embeddedFixture(t)
	scanner := proc.scanner()
	view := officialAgentView{Embedded: true, ManagementPort: 23889}
	start := time.Now()
	scanner.current(view, start)
	if !reflect.DeepEqual(scanner.pids, []int{355}) {
		t.Fatalf("cached Agent pids = %v", scanner.pids)
	}
	// The Agent restarts under a new PID with the same inbounds.
	if err := os.RemoveAll(filepath.Join(proc.root, "355")); err != nil {
		t.Fatal(err)
	}
	proc.process(9001, "mmw-agent", "socket:[1001]")
	ports, errText := scanner.current(view, start.Add(31*time.Second))
	if errText != "" || !reflect.DeepEqual(ports.TCP, []uint32{10015}) || !reflect.DeepEqual(scanner.pids, []int{9001}) {
		t.Fatalf("after restart: %#v %q pids=%v", ports, errText, scanner.pids)
	}
	// No Agent at all: nothing to guard, and the status says why.
	if err := os.RemoveAll(filepath.Join(proc.root, "9001")); err != nil {
		t.Fatal(err)
	}
	ports, errText = scanner.current(view, start.Add(62*time.Second))
	if !ports.empty() || errText != "official Agent process not found" {
		t.Fatalf("no Agent: %#v %q", ports, errText)
	}
}

func TestMachinePortsMergeOfficialEmbedded(t *testing.T) {
	proc := embeddedFixture(t)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	embedded := &machineProtectionManager{official: proc.scanner()}
	input := machineProtectionInput{FLVXShared: machinePorts{TCP: []uint32{30000}, UDP: []uint32{30000}}, Official: officialAgentView{Embedded: true, ManagementPort: 23889}}
	ports, sources := embedded.ports(input, now)
	want := machinePorts{TCP: []uint32{10015, 30000, 50409}, UDP: []uint32{5000, 10015, 30000, 50409}}
	if !reflect.DeepEqual(ports, want) || sources.OfficialEmbedded != 3 || sources.OfficialEmbeddedError != "" || sources.Core != 0 || sources.FLVXShared != 1 {
		t.Fatalf("embedded merge: ports=%#v sources=%#v", ports, sources)
	}

	// External servers: same ports and the same nft script as v0.7.9, with the
	// official source never scanned (its /proc root does not even exist).
	external := machineProtectionInput{CorePorts: []uint32{10016, 443}, FLVXShared: machinePorts{TCP: []uint32{30000}, UDP: []uint32{30000}}}
	before := &machineProtectionManager{}
	after := &machineProtectionManager{official: officialPortsScanner{procRoot: filepath.Join(t.TempDir(), "absent"), comm: officialAgentComm}}
	portsBefore, _ := before.ports(external, now)
	portsAfter, sourcesAfter := after.ports(external, now)
	for _, blocking := range []bool{false, true} {
		if renderMachineProtectionNft(portsBefore, blocking, false) != renderMachineProtectionNft(portsAfter, blocking, false) {
			t.Fatalf("External ruleset changed (blocking=%v)", blocking)
		}
	}
	if sourcesAfter.OfficialEmbedded != 0 || sourcesAfter.OfficialEmbeddedError != "" || after.official.active {
		t.Fatalf("External sources: %#v", sourcesAfter)
	}
}

func TestMachineProtectionGuardsOfficialPortsOverCap(t *testing.T) {
	proc := embeddedFixture(t)
	fake, clock := &fakeMachineNft{}, &fakeClock{now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	manager := newTestMachineManager(fake, clock)
	manager.official = proc.scanner()
	settings := &machineProtectionSettings{Enabled: true, MaxTotal: int64Value(100)}
	input := func(total int64) machineProtectionInput {
		return machineProtectionInput{Machine: &machineConnectionCounts{Total: total}, Official: officialAgentView{Embedded: true, ManagementPort: 23889}}
	}
	status := manager.reconcile(context.Background(), settings, input(20))
	if status.IdleReason != "" || status.Blocking || !reflect.DeepEqual(status.TCPPorts, []uint32{10015, 50409}) || status.Sources.OfficialEmbedded != 3 {
		t.Fatalf("idle with official ports: %#v", status)
	}
	if script := fake.applies[len(fake.applies)-1]; !strings.Contains(script, "tcp_ports { 10015, 50409 }") || !strings.Contains(script, "udp_ports { 5000, 10015, 50409 }") || strings.Contains(script, "hook prerouting priority -150; policy accept; }\nadd rule") {
		t.Fatalf("idle table: %s", script)
	}
	manager.reconcile(context.Background(), settings, input(150))
	clock.now = clock.now.Add(machineConntrackWarmup)
	status = manager.reconcile(context.Background(), settings, input(150))
	if !status.Blocking || !strings.Contains(fake.applies[len(fake.applies)-1], "tcp dport @tcp_ports") {
		t.Fatalf("over the cap the official ports must be guarded: %#v", status)
	}
	// Same machine without the official source: the old no_business_ports state.
	without := newTestMachineManager(&fakeMachineNft{}, clock)
	if status := without.reconcile(context.Background(), settings, machineProtectionInput{Machine: &machineConnectionCounts{Total: 150}}); status.IdleReason != "no_business_ports" || status.Blocking {
		t.Fatalf("without official ports: %#v", status)
	}
}

// The rescan on the real /proc for an Agent holding 2000 sockets (1000
// client connections, both ends in this process) stays a few milliseconds;
// it runs at most every 30 s (or 5 s after a listener change).
func TestOfficialPortsScanCostLive(t *testing.T) {
	raiseNoFile()
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Skip("no TCP listener: ", err)
	}
	defer listener.Close()
	port := uint32(listener.Addr().(*net.TCPAddr).Port)
	accepted := make(chan net.Conn, 1000)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()
	var conns []net.Conn
	defer func() {
		for _, conn := range conns {
			conn.Close()
		}
	}()
	for len(conns) < 2000 {
		conn, err := net.Dial("tcp4", listener.Addr().String())
		if err != nil {
			t.Skip("cannot open 1000 connections: ", err)
		}
		conns = append(conns, conn, <-accepted)
	}
	scanner := officialPortsScanner{procRoot: "/proc", portRange: ipLocalPortRangePath, comm: processComm("/proc", os.Getpid())}
	ports, err := scanner.scan(0)
	if err != nil || !containsPort(ports.TCP, port) {
		t.Fatalf("live scan: %#v %v (want TCP %d)", ports, err, port)
	}
	const runs = 20
	started := time.Now()
	for range runs {
		if _, err := scanner.scan(0); err != nil {
			t.Fatal(err)
		}
	}
	perScan := time.Since(started) / runs
	t.Logf("official ports scan with %d sockets: %v per scan", len(conns), perScan)
	if perScan > 250*time.Millisecond {
		t.Fatalf("scan too slow: %v", perScan)
	}
}
