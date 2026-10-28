package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

func (proc *officialProc) listeningScanner(core string) listeningPortsScanner {
	return listeningPortsScanner{procRoot: proc.root, portRange: filepath.Join(proc.root, "ip_local_port_range"), configPath: filepath.Join(proc.root, "config.json"), coreBinaries: []string{core}}
}

// tickTCP returns the fixture's parsed TCP rows, as one Helper tick reads them.
func (proc *officialProc) tickTCP() []socketEntry {
	proc.t.Helper()
	net := filepath.Join(proc.root, "net")
	tables := readSocketTables(socketTablePaths{TCP: filepath.Join(net, "tcp"), TCP6: filepath.Join(net, "tcp6"), UDP: filepath.Join(net, "udp"), UDP6: filepath.Join(net, "udp6")})
	if tables.TCPErr != nil {
		proc.t.Fatal(tables.TCPErr)
	}
	return tables.TCP
}

func (proc *officialProc) exe(pid int, target string) {
	proc.t.Helper()
	if err := os.Symlink(target, filepath.Join(proc.root, fmt.Sprint(pid), "exe")); err != nil {
		proc.t.Fatal(err)
	}
}

// boilHKT99uFixture mirrors 「Boil HKT 99u」(2) on 2026-10-25: the External
// Core serves 10015/10016/10018 plus its loopback API/metrics, LeiKwan's
// backup lkbk-core holds 10017 TCP+UDP, sshd and the old Flux panel agent.
func boilHKT99uFixture(t *testing.T) (*officialProc, string) {
	proc := newOfficialProc(t)
	core := filepath.Join(proc.root, "core-xray")
	if err := os.WriteFile(core, []byte("core"), 0755); err != nil {
		t.Fatal(err)
	}
	config := `{"inbounds":[{"tag":"api","port":46736},{"tag":"wings ss","port":10015},{"tag":"自己用 ss-10016","port":"10016"},{"tag":"king ss-10017","port":10018}]}`
	if err := os.WriteFile(filepath.Join(proc.root, "config.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	proc.process(231078, "xray", "socket:[11]", "socket:[12]", "socket:[13]", "socket:[14]", "socket:[15]", "socket:[16]", "socket:[17]", "socket:[18]", "socket:[19]", "socket:[20]")
	proc.exe(231078, core)
	proc.process(212883, "lkbk-core", "socket:[31]", "socket:[32]")
	proc.process(521, "sshd", "socket:[41]", "socket:[42]")
	proc.process(502, "flux_agent", "socket:[51]")
	proc.process(475, "mmw-agent", "socket:[61]")
	proc.socket("tcp6", hex6Any(10015), anyRemote6, "0A", 11)
	proc.socket("tcp6", hex6Any(10016), anyRemote6, "0A", 12)
	proc.socket("tcp6", hex6Any(10018), anyRemote6, "0A", 13)
	proc.socket("tcp", hex4(127, 0, 0, 1, 38889), anyRemote4, "0A", 14)
	proc.socket("tcp", hex4(127, 0, 0, 1, 46736), anyRemote4, "0A", 15)
	proc.socket("udp6", hex6Any(10015), anyRemote6, "07", 16)
	proc.socket("udp6", hex6Any(10016), anyRemote6, "07", 17)
	proc.socket("udp6", hex6Any(10018), anyRemote6, "07", 18)
	proc.socket("udp", hex4(0, 0, 0, 0, 41234), anyRemote4, "07", 19)               // outbound relay socket
	proc.socket("tcp", hex4(10, 0, 0, 2, 10016), hex4(1, 2, 3, 4, 50000), "01", 20) // a client connection
	proc.socket("tcp6", hex6Any(10017), anyRemote6, "0A", 31)
	proc.socket("udp6", hex6Any(10017), anyRemote6, "07", 32)
	proc.socket("tcp", hex4(0, 0, 0, 0, 22), anyRemote4, "0A", 41)
	proc.socket("tcp6", hex6Any(22), anyRemote6, "0A", 42)
	proc.socket("tcp6", hex6Any(51378), anyRemote6, "0A", 51)
	proc.socket("tcp", hex4(127, 0, 0, 1, 23889), anyRemote4, "0A", 61)
	proc.socket("udp", hex4(0, 0, 0, 0, 51820), anyRemote4, "07", 0) // kernel WireGuard
	return proc, core
}

func TestListeningPortsNameEveryOwner(t *testing.T) {
	proc, core := boilHKT99uFixture(t)
	scanner := proc.listeningScanner(core)
	report := scanner.current(proc.tickTCP(), time.Now())
	if report == nil || report.Error != "" || report.Truncated {
		t.Fatalf("report = %#v", report)
	}
	got := map[string]listeningPortOwner{}
	for _, entry := range report.Ports {
		got[fmt.Sprint(entry.Protocol, entry.Port)] = entry
	}
	want := map[string]listeningPortOwner{
		"tcp10017": {Port: 10017, Protocol: "tcp", Address: "*", PID: 212883, Process: "lkbk-core"},
		"udp10017": {Port: 10017, Protocol: "udp", Address: "*", PID: 212883, Process: "lkbk-core"},
		"tcp10016": {Port: 10016, Protocol: "tcp", Address: "*", PID: 231078, Process: "xray", Owner: "core", InboundTag: "自己用 ss-10016"},
		"udp10018": {Port: 10018, Protocol: "udp", Address: "*", PID: 231078, Process: "xray", Owner: "core", InboundTag: "king ss-10017"},
		"tcp46736": {Port: 46736, Protocol: "tcp", Address: "127.0.0.1", PID: 231078, Process: "xray", Owner: "core", InboundTag: "api"},
		"tcp38889": {Port: 38889, Protocol: "tcp", Address: "127.0.0.1", PID: 231078, Process: "xray", Owner: "core"},
		"tcp22":    {Port: 22, Protocol: "tcp", Address: "*", PID: 521, Process: "sshd"},
		"tcp51378": {Port: 51378, Protocol: "tcp", Address: "*", PID: 502, Process: "flux_agent"},
		"tcp23889": {Port: 23889, Protocol: "tcp", Address: "127.0.0.1", PID: 475, Process: "mmw-agent", Owner: "agent"},
		"udp51820": {Port: 51820, Protocol: "udp", Address: "*"},
		"tcp10015": {Port: 10015, Protocol: "tcp", Address: "*", PID: 231078, Process: "xray", Owner: "core", InboundTag: "wings ss"},
		"udp10015": {Port: 10015, Protocol: "udp", Address: "*", PID: 231078, Process: "xray", Owner: "core", InboundTag: "wings ss"},
		"udp10016": {Port: 10016, Protocol: "udp", Address: "*", PID: 231078, Process: "xray", Owner: "core", InboundTag: "自己用 ss-10016"},
		"tcp10018": {Port: 10018, Protocol: "tcp", Address: "*", PID: 231078, Process: "xray", Owner: "core", InboundTag: "king ss-10017"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listening ports:\n got %#v\nwant %#v", got, want)
	}
	// tcp4 + tcp6 on :22 by one sshd is one entry; ports stay sorted.
	if len(report.Ports) != len(want) {
		t.Fatalf("duplicate entries: %#v", report.Ports)
	}
	for index := 1; index < len(report.Ports); index++ {
		if report.Ports[index-1].Port > report.Ports[index].Port {
			t.Fatalf("ports not sorted: %#v", report.Ports)
		}
	}
	// The ephemeral outbound UDP socket and the client connection are not listeners.
	if _, ok := got["udp41234"]; ok {
		t.Fatalf("ephemeral outbound UDP socket reported: %#v", report.Ports)
	}
	encoded, _ := json.Marshal(report.Ports[0])
	if !strings.Contains(string(encoded), `"protocol":"tcp","address":"*"`) {
		t.Fatalf("wire form = %s", encoded)
	}
}

func TestListeningPortsRescanOnlyWhenListenersChange(t *testing.T) {
	proc, core := boilHKT99uFixture(t)
	scanner := proc.listeningScanner(core)
	start := time.Now()
	first := scanner.current(proc.tickTCP(), start)
	// Owners are cached while the TCP listener set is unchanged.
	if err := os.WriteFile(filepath.Join(proc.root, "212883", "comm"), []byte("renamed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if again := scanner.current(proc.tickTCP(), start.Add(30*time.Second)); again != first {
		t.Fatal("unchanged listeners must reuse the cached owner scan")
	}
	// A new TCP listener past the minimum gap rescans at once.
	proc.process(9001, "nginx", "socket:[71]")
	proc.socket("tcp", hex4(0, 0, 0, 0, 443), anyRemote4, "0A", 71)
	changed := scanner.current(proc.tickTCP(), start.Add(32*time.Second))
	found := false
	for _, entry := range changed.Ports {
		found = found || entry.Port == 443 && entry.Process == "nginx"
	}
	if changed == first || !found {
		t.Fatalf("a changed listener set past the minimum gap must rescan: %#v", changed.Ports)
	}
	// A change within the minimum gap waits for it.
	scanned := scanner.scannedAt
	proc.socket("tcp", hex4(127, 0, 0, 1, 8443), anyRemote4, "0A", 72)
	if within := scanner.current(proc.tickTCP(), scanned.Add(2*time.Second)); !within.ScannedAt.Equal(scanned.UTC()) {
		t.Fatal("a change within the minimum gap must not rescan yet")
	}
	if after := scanner.current(proc.tickTCP(), scanned.Add(listeningMinGap)); after.ScannedAt.Equal(scanned.UTC()) {
		t.Fatal("a loopback listener change must rescan once the minimum gap passed")
	}
	// UDP churn and client connections never trigger a rescan before the refresh.
	scanned = scanner.scannedAt
	proc.socket("udp", hex4(0, 0, 0, 0, 45000), anyRemote4, "07", 73)
	proc.socket("udp", hex4(0, 0, 0, 0, 20000), anyRemote4, "07", 74)
	proc.socket("tcp", hex4(10, 0, 0, 2, 10016), hex4(5, 6, 7, 8, 40000), "01", 75)
	if same := scanner.current(proc.tickTCP(), scanned.Add(10*time.Second)); !same.ScannedAt.Equal(scanned.UTC()) {
		t.Fatal("UDP sockets and client connections must not rescan owners")
	}
	// Without usable TCP rows only the refresh rescans; then the UDP listener shows.
	if blind := scanner.current(nil, scanned.Add(20*time.Second)); !blind.ScannedAt.Equal(scanned.UTC()) {
		t.Fatal("missing TCP rows must not count as a listener change")
	}
	refreshed := scanner.current(nil, scanned.Add(listeningRefresh))
	udp := false
	for _, entry := range refreshed.Ports {
		udp = udp || entry.Port == 20000 && entry.Protocol == "udp"
	}
	if refreshed.ScannedAt.Equal(scanned.UTC()) || !udp {
		t.Fatalf("the refresh must rescan and list the new UDP service: %#v", refreshed.Ports)
	}
}

func TestListeningPortsKnowTheCoreAfterItsBinaryWasReplaced(t *testing.T) {
	proc := newOfficialProc(t)
	core := filepath.Join(proc.root, "core-xray")
	proc.process(4242, "xray", "socket:[81]")
	proc.exe(4242, core+" (deleted)")
	proc.process(4343, "xray", "socket:[82]")
	proc.exe(4343, filepath.Join(proc.root, "other-xray"))
	proc.socket("tcp6", hex6Any(10016), anyRemote6, "0A", 81)
	proc.socket("tcp6", hex6Any(20000), anyRemote6, "0A", 82)
	scanner := proc.listeningScanner(core)
	report := scanner.current(proc.tickTCP(), time.Now())
	if len(report.Ports) != 2 || report.Ports[0].Owner != listeningOwnerCore || report.Ports[1].Owner != "" {
		t.Fatalf("a replaced Core binary must still count as the Core, another xray must not: %#v", report.Ports)
	}
}

func TestListenerSocketsKeepFLVXSemantics(t *testing.T) {
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	row := func(local, remote, state string, inode int) string {
		return fmt.Sprintf("   0: %s %s %s 00000000:00000000 00:00000000 00000000     0        0 %d 1 0000000000000000 100 0 0 10 0\n", local, remote, state, inode)
	}
	udp := header + row(hex4(0, 0, 0, 0, 30000), anyRemote4, "07", 1) + row(hex4(10, 0, 0, 2, 41000), hex4(8, 8, 8, 8, 53), "07", 2) + row(hex4(10, 0, 0, 2, 42000), hex4(8, 8, 8, 8, 53), "01", 3)
	sockets, err := parseListenerSockets(strings.NewReader(udp), false, true)
	if err != nil || len(sockets) != 1 || sockets[0].Port != 30000 || sockets[0].Inode != 1 || sockets[0].Protocol != "udp" {
		t.Fatalf("udp listeners = %#v, %v", sockets, err)
	}
	tcp := header + row(hex4(127, 0, 0, 1, 46736), anyRemote4, "0A", 4) + row(hex4(10, 0, 0, 2, 10016), hex4(1, 2, 3, 4, 50000), "01", 5)
	sockets, err = parseListenerSockets(strings.NewReader(tcp), false, false)
	if err != nil || len(sockets) != 1 || sockets[0].Port != 46736 || sockets[0].Address.String() != "127.0.0.1" {
		t.Fatalf("tcp listeners = %#v, %v", sockets, err)
	}
	if _, err := parseListenerSockets(strings.NewReader(header+row("zz:0050", anyRemote4, "0A", 6)), false, false); err == nil {
		t.Fatal("a malformed listener row must fail the read, as the FLVX busy-port scan expects")
	}
}

func TestListeningPortsBoundedAndReportsUnreadableTables(t *testing.T) {
	proc := newOfficialProc(t)
	for port := 1; port <= flvx.MaxBusyPorts+10; port++ {
		proc.rows["tcp"] = append(proc.rows["tcp"], fmt.Sprintf("%4d: %s %s 0A 00000000:00000000 00:00000000 00000000     0        0 %d 1 0000000000000000 100 0 0 10 0", port, hex4(0, 0, 0, 0, uint16(port)), anyRemote4, 100000+port))
	}
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	if err := os.WriteFile(filepath.Join(proc.root, "net", "tcp"), []byte(header+strings.Join(proc.rows["tcp"], "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	scanner := proc.listeningScanner(filepath.Join(proc.root, "missing-core"))
	report := scanner.current(nil, time.Now())
	if len(report.Ports) != flvx.MaxBusyPorts || !report.Truncated {
		t.Fatalf("report has %d ports, truncated=%t", len(report.Ports), report.Truncated)
	}
	empty := listeningPortsScanner{procRoot: t.TempDir(), portRange: "/nonexistent"}
	if failed := empty.current(nil, time.Now()); failed == nil || failed.Error == "" || failed.Ports == nil {
		t.Fatalf("unreadable tables must report an error with an empty list: %#v", failed)
	}
}

func TestForeignPortOwnersNameTheCulprit(t *testing.T) {
	owners := map[int]map[int]struct{}{10016: {231078: {}}, 10017: {212883: {}, 231078: {}}, 10018: {231078: {}}}
	comm := map[int]string{212883: "lkbk-core", 231078: "xray"}
	conflicts := foreignPortOwners(owners, 231078, func(pid int) string { return comm[pid] })
	if want := []ownershipPortConflict{{Port: 10017, PID: 212883, Process: "lkbk-core"}}; !reflect.DeepEqual(conflicts, want) {
		t.Fatalf("conflicts = %#v, want %#v", conflicts, want)
	}
	if got := portConflictPrefix(conflicts); got != "端口 10017 被 lkbk-core (PID 212883) 占用；" {
		t.Fatalf("prefix = %q", got)
	}
	if foreignPortOwners(map[int]map[int]struct{}{10016: {231078: {}}}, 231078, func(int) string { return "" }) != nil || portConflictPrefix(nil) != "" {
		t.Fatal("a port served only by the Core is not a conflict")
	}
	many := []ownershipPortConflict{{Port: 1, PID: 9}, {Port: 2, PID: 9, Process: "a"}, {Port: 3, PID: 9, Process: "b"}, {Port: 4, PID: 9, Process: "c"}, {Port: 5, PID: 9, Process: "d"}}
	if got := portConflictPrefix(many); got != "端口 1 被 未知进程 (PID 9) 占用；端口 2 被 a (PID 9) 占用；端口 3 被 b (PID 9) 占用；另有 2 处端口被占用；" {
		t.Fatalf("bounded prefix = %q", got)
	}
}

func TestHandoffTimeoutMessageKeepsTheCulpritWithinTheSignedLimit(t *testing.T) {
	status := externalOwnershipStatus{PortConflicts: []ownershipPortConflict{{Port: 10017, PID: 212883, Process: "lkbk-core"}}}
	message := portConflictPrefix(status.PortConflicts) + "external health timeout: " + strings.Repeat("x", 600) + "端口"
	sanitized := sanitizeManagementMessage(message)
	if !strings.HasPrefix(sanitized, "端口 10017 被 lkbk-core (PID 212883) 占用；external health timeout") || len(sanitized) > 512 || !utf8.ValidString(sanitized) {
		t.Fatalf("sanitized = %q (%d bytes)", sanitized, len(sanitized))
	}
	// A cut inside a multi-byte rune drops the partial rune instead of
	// producing invalid UTF-8 (which JSON would rewrite, breaking the HMAC).
	cut := sanitizeManagementMessage(strings.Repeat("a", 511) + "端")
	if !utf8.ValidString(cut) || cut != strings.Repeat("a", 511) {
		t.Fatalf("cut = %q", cut)
	}
}
