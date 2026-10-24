package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func int64Value(value int64) *int64 { return &value }

func TestMachineActiveTCPDefinition(t *testing.T) {
	counts := tcpStateCounts{Established: 10, SynSent: 1, SynRecv: 2, FinWait1: 3, FinWait2: 4, TimeWait: 100, CloseWait: 5, LastAck: 6, Closing: 7, Close: 8, Listen: 11, Unknown: 9}
	if got := machineActiveTCP(counts); got != 38 {
		t.Fatalf("machine active=%d, want 38", got)
	}
}

const procTCPFixture = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:01BB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1F90 0200007F:C350 01 00000000:00000000 00:00000000 00000000     0        0 2 1 0000000000000000 20 4 30 10 -1
   2: 0100007F:1F91 0200007F:C351 06 00000000:00000000 00:00000000 00000000     0        0 0 1 0000000000000000 20 4 30 10 -1
   3: 0100007F:1F92 0200007F:C352 06 00000000:00000000 00:00000000 00000000     0        0 0 1 0000000000000000 20 4 30 10 -1
   4: 0100007F:1F93 0200007F:C353 08 00000000:00000000 00:00000000 00000000     0        0 3 1 0000000000000000 20 4 30 10 -1
   5: 0100007F:1F94 0200007F:C354 04 00000000:00000000 00:00000000 00000000     0        0 0 1 0000000000000000 20 4 30 10 -1
`

const procTCP6Fixture = `  sl  local_address                         remote_address                        st tx_queue rx_queue
   0: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A 00000000:00000000
   1: 0000000000000000FFFF00000100007F:2710 0000000000000000FFFF00000200007F:C350 01 00000000:00000000
`

const procUDPFixture = `   sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  100: 00000000:2710 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 9 2 0000000000000000 0
  101: 0100007F:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 9 2 0000000000000000 0
  102: 0A00000A:9C40 0A00000B:2710 01 00000000:00000000 00:00000000 00000000     0        0 9 2 0000000000000000 0
`

const procUDP6Fixture = `  sl  local_address                         remote_address                        st tx_queue rx_queue
  200: 00000000000000000000000000000000:2710 00000000000000000000000000000000:0000 07 00000000:00000000
`

func writeSocketFixtures(t *testing.T, tables map[string]string) socketTablePaths {
	t.Helper()
	dir := t.TempDir()
	paths := socketTablePaths{TCP: filepath.Join(dir, "tcp"), TCP6: filepath.Join(dir, "tcp6"), UDP: filepath.Join(dir, "udp"), UDP6: filepath.Join(dir, "udp6")}
	for name, body := range tables {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

// referenceConnections is readConnections() on arbitrary paths: the card's
// number, every row of the four tables.
func referenceConnections(paths socketTablePaths) (int64, int64) {
	tcp, _ := countProcNetRows(paths.TCP)
	tcp6, _ := countProcNetRows(paths.TCP6)
	udp, _ := countProcNetRows(paths.UDP)
	udp6, _ := countProcNetRows(paths.UDP6)
	return tcp + tcp6, udp + udp6
}

func TestMachineTotalIsTCPPlusUDPLikeTheServerCard(t *testing.T) {
	paths := writeSocketFixtures(t, map[string]string{"tcp": procTCPFixture, "tcp6": procTCP6Fixture, "udp": procUDPFixture, "udp6": procUDP6Fixture})
	tables := readSocketTables(paths)
	if tables.MachineErr != nil || tables.TCPErr != nil {
		t.Fatalf("unexpected errors: %v / %v", tables.MachineErr, tables.TCPErr)
	}
	tcp, udp := referenceConnections(paths)
	want := machineConnectionCounts{Total: tcp + udp, TCP: tcp, UDP: udp, Established: 2, TimeWait: 2, CloseWait: 1, OtherTCP: 3}
	if tables.Machine != want || want.Total != 12 {
		t.Fatalf("machine counts = %#v, want %#v", tables.Machine, want)
	}
	// The TCP-only system total stays what it was (Level 1 「机器 TCP」).
	if system := summarizeTCP(tables.TCP); system.Total != tcp || system.Total == want.Total {
		t.Fatalf("system TCP total = %d, machine total %d", system.Total, want.Total)
	}
}

func TestMachineTotalMatchesLiveProcNet(t *testing.T) {
	// Sockets move between the two reads, so allow a few attempts.
	for attempt := 0; attempt < 20; attempt++ {
		legacy, err := readConnections()
		tables := readSocketTables(procSocketTables)
		if err != nil || tables.MachineErr != nil {
			t.Skip("no /proc/net socket tables")
		}
		if legacy.ConnectionCount == tables.Machine.Total && legacy.UDPCount == tables.Machine.UDP {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("whole-machine total never matched readConnections()")
}

func TestMachineTotalKeepsCountsForMalformedAndMissingTables(t *testing.T) {
	malformed := procTCPFixture + "   6: ZZ:1F95 0200007F:C355 01 00000000:00000000\n"
	paths := writeSocketFixtures(t, map[string]string{"tcp": malformed, "udp": procUDPFixture})
	tables := readSocketTables(paths)
	tcp, udp := referenceConnections(paths)
	if tables.MachineErr != nil || tables.Machine.Total != tcp+udp || tables.Machine.TCP != 7 || tables.Machine.Established != 2 {
		t.Fatalf("malformed row changed the machine counts: %#v err=%v", tables.Machine, tables.MachineErr)
	}
	if tables.TCPErr == nil || tables.TCP != nil {
		t.Fatalf("malformed TCP row must invalidate the parsed entries: %v", tables.TCPErr)
	}
	none := readSocketTables(socketTablePaths{TCP: "/nonexistent/tcp", TCP6: "/nonexistent/tcp6", UDP: "/nonexistent/udp", UDP6: "/nonexistent/udp6"})
	if none.MachineErr == nil || none.Machine != (machineConnectionCounts{}) {
		t.Fatalf("all tables unreadable must be an error: %#v", none)
	}
}

func writeFLVXPorts(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestMachinePortsMergeCoreSharedAndFLVXFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "business-ports.json")
	now := time.Date(2026, 10, 20, 16, 0, 0, 0, time.UTC)
	manager := &machineProtectionManager{flvxFile: flvxBusinessPortsReader{path: path}}
	input := machineProtectionInput{CorePorts: []uint32{10016, 443, 10016}, FLVXShared: machinePorts{TCP: []uint32{30000, 30001}, UDP: []uint32{30000}}}

	ports, sources := manager.ports(input, now)
	if !reflect.DeepEqual(ports, machinePorts{TCP: []uint32{443, 10016, 30000, 30001}, UDP: []uint32{443, 10016, 30000}}) || sources.FLVXFile != "absent" || sources.Core != 2 || sources.FLVXShared != 2 {
		t.Fatalf("absent file: ports=%#v sources=%#v", ports, sources)
	}

	writeFLVXPorts(t, path, `{"version":1,"updated_at":"2026-10-20T15:58:00Z","tcp":[20001,20003,443],"udp":[20001,20004]}`)
	ports, sources = manager.ports(input, now)
	want := machinePorts{TCP: []uint32{443, 10016, 20001, 20003, 30000, 30001}, UDP: []uint32{443, 10016, 20001, 20004, 30000}}
	if !reflect.DeepEqual(ports, want) || sources.FLVXFile != "fresh" || sources.FLVXFileTCP != 3 || sources.FLVXFileUDP != 2 || sources.FLVXUpdatedAt != "2026-10-20T15:58:00Z" {
		t.Fatalf("fresh file: ports=%#v sources=%#v", ports, sources)
	}

	// Older than 10 minutes: ignored (the FLVX agent rewrites every 4 min).
	ports, sources = manager.ports(input, now.Add(11*time.Minute))
	if sources.FLVXFile != "stale" || containsPort(ports.TCP, 20001) || !containsPort(ports.TCP, 30000) {
		t.Fatalf("stale file: ports=%#v sources=%#v", ports, sources)
	}

	for name, body := range map[string]string{
		"not json":      `{"version":1,`,
		"version 2":     `{"version":2,"updated_at":"2026-10-20T15:58:00Z","tcp":[1],"udp":[]}`,
		"port zero":     `{"version":1,"updated_at":"2026-10-20T15:58:00Z","tcp":[0],"udp":[]}`,
		"port too big":  `{"version":1,"updated_at":"2026-10-20T15:58:00Z","tcp":[70000],"udp":[]}`,
		"null udp":      `{"version":1,"updated_at":"2026-10-20T15:58:00Z","tcp":[1],"udp":null}`,
		"string port":   `{"version":1,"updated_at":"2026-10-20T15:58:00Z","tcp":["1"],"udp":[]}`,
		"bad timestamp": `{"version":1,"updated_at":"yesterday","tcp":[1],"udp":[]}`,
		"too large":     `{"version":1,"updated_at":"2026-10-20T15:58:00Z","tcp":[1],"udp":[],"pad":"` + strings.Repeat("x", flvxBusinessPortsMaxBytes) + `"}`,
	} {
		writeFLVXPorts(t, path, body)
		// Same size and second can keep the mtime; force a re-read.
		manager.flvxFile.cached = nil
		ports, sources = manager.ports(input, now)
		if sources.FLVXFile != "malformed" || containsPort(ports.TCP, 1) || !containsPort(ports.TCP, 30001) {
			t.Fatalf("%s: ports=%#v sources=%#v", name, ports, sources)
		}
	}

	writeFLVXPorts(t, path, `{"version":1,"updated_at":"2026-10-20T15:59:59.123456789Z","tcp":[],"udp":[5353]}`)
	manager.flvxFile.cached = nil
	if ports, sources = manager.ports(machineProtectionInput{}, now); !reflect.DeepEqual(ports, machinePorts{TCP: []uint32{}, UDP: []uint32{5353}}) || sources.FLVXFile != "fresh" {
		t.Fatalf("RFC3339Nano file: ports=%#v sources=%#v", ports, sources)
	}
}

func containsPort(ports []uint32, wanted uint32) bool {
	for _, port := range ports {
		if port == wanted {
			return true
		}
	}
	return false
}

func TestRenderMachineProtectionNftPreroutingTCPAndUDP(t *testing.T) {
	ports := machinePorts{TCP: []uint32{443, 20001}, UDP: []uint32{20001}}
	blocking := renderMachineProtectionNft(ports, true, false)
	for _, expected := range []string{
		"add table inet " + machineProtectionTableName,
		"add counter inet " + machineProtectionTableName + " dropped",
		"add element inet " + machineProtectionTableName + " tcp_ports { 443, 20001 }",
		"add element inet " + machineProtectionTableName + " udp_ports { 20001 }",
		"type filter hook prerouting priority -150; policy accept;",
		"prerouting iifname \"lo\" return",
		"ct state new tcp flags & (fin | syn | rst | ack) == syn tcp dport @tcp_ports fib daddr type local counter name \"dropped\" drop",
		"ct state new udp dport @udp_ports fib daddr type local counter name \"dropped\" drop",
		"conntrack ct state new",
	} {
		if !strings.Contains(blocking, expected) {
			t.Fatalf("blocking script missing %q:\n%s", expected, blocking)
		}
	}
	if strings.Index(blocking, "iifname \"lo\" return") > strings.Index(blocking, "tcp dport @tcp_ports") {
		t.Fatalf("loopback exemption must come first:\n%s", blocking)
	}
	idle := renderMachineProtectionNft(ports, false, false)
	if strings.Contains(idle, "hook") || strings.Contains(idle, " drop ") {
		t.Fatalf("idle table must not hook or drop anything:\n%s", idle)
	}
	cleanup := renderMachineProtectionNft(ports, false, true)
	for _, expected := range []string{"delete chain inet " + machineProtectionTableName + " input", "delete chain inet " + machineProtectionTableName + " prerouting"} {
		if !strings.Contains(cleanup, expected) {
			t.Fatalf("cleanup script missing %q:\n%s", expected, cleanup)
		}
	}
	for _, script := range []string{blocking, idle, cleanup} {
		for _, forbidden := range []string{"flush ruleset", "dport 22", "delete table", "ip saddr", "ip daddr"} {
			if strings.Contains(script, forbidden) {
				t.Fatalf("script contains unsafe %q:\n%s", forbidden, script)
			}
		}
	}
	if empty := renderMachineProtectionNft(machinePorts{}, true, false); strings.Contains(empty, "add element") {
		t.Fatalf("empty sets must not add elements:\n%s", empty)
	}
}

func TestRenderMachineProtectionNftSyntax(t *testing.T) {
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft is not installed")
	}
	ports := machinePorts{TCP: []uint32{10016, 10017}, UDP: []uint32{10016}}
	for _, variant := range []struct{ blocking, cleanup bool }{{true, false}, {true, true}, {false, false}, {false, true}} {
		script := strings.ReplaceAll(renderMachineProtectionNft(ports, variant.blocking, variant.cleanup), machineProtectionTableName, machineProtectionTableName+"_test")
		command := exec.CommandContext(context.Background(), "nft", "-c", "-f", "-")
		command.Stdin = strings.NewReader(script)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("nft rejected %+v rules: %v: %s\n%s", variant, err, output, script)
		}
	}
}

func TestMachineProtectionHysteresis(t *testing.T) {
	settings := machineProtectionSettings{Enabled: true, MaxTotal: int64Value(1000)}
	for _, step := range []struct {
		total       int64
		wasBlocking bool
		want        bool
		reason      string
	}{
		{999, false, false, ""}, {1000, false, true, "total"}, {1200, true, true, "total"},
		{951, true, true, "total_hysteresis"}, {950, true, true, "total_hysteresis"},
		{949, true, false, ""}, {960, false, false, ""},
	} {
		blocking, reason, release := machineProtectionThreshold(settings, 0, step.total, step.wasBlocking)
		if blocking != step.want || reason != step.reason || release == nil || *release != 950 {
			t.Fatalf("total=%d wasBlocking=%v: blocking=%v reason=%q release=%v", step.total, step.wasBlocking, blocking, reason, release)
		}
	}
	compat := machineProtectionSettings{Enabled: true, MaxActive: int64Value(100)}
	if blocking, reason, _ := machineProtectionThreshold(compat, 100, 5, false); !blocking || reason != "active_compat" {
		t.Fatalf("max_active compatibility fallback: %v %q", blocking, reason)
	}
	both := machineProtectionSettings{Enabled: true, MaxActive: int64Value(10), MaxTotal: int64Value(200)}
	if blocking, _, _ := machineProtectionThreshold(both, 50, 150, false); blocking {
		t.Fatal("max_total must win over the old max_active")
	}
	if release := machineReleaseBelow(1); release != 0 {
		t.Fatalf("tiny cap release=%d", release)
	}
}

type fakeMachineNft struct {
	applies  []string
	removes  int
	counters int
	present  bool
	dropped  int64
	applyErr func(string) error
}

func (fake *fakeMachineNft) ops() machineNftOps {
	return machineNftOps{
		apply: func(_ context.Context, script []byte) error {
			fake.applies = append(fake.applies, string(script))
			if fake.applyErr != nil {
				if err := fake.applyErr(string(script)); err != nil {
					return err
				}
			}
			if !fake.present {
				fake.dropped = 0
			}
			fake.present = true
			return nil
		},
		remove: func(context.Context) error {
			fake.removes++
			fake.present = false
			return nil
		},
		counter: func(context.Context) (int64, error) {
			fake.counters++
			if !fake.present {
				return 0, errors.New("missing")
			}
			return fake.dropped, nil
		},
	}
}

type fakeClock struct{ now time.Time }

func (clock *fakeClock) Now() time.Time { return clock.now }

func newTestMachineManager(fake *fakeMachineNft, clock *fakeClock) *machineProtectionManager {
	return &machineProtectionManager{supported: true, nft: fake.ops(), now: clock.Now, needCleanup: true}
}

func machineInputWithTotal(total int64, ports ...uint32) machineProtectionInput {
	return machineProtectionInput{Machine: &machineConnectionCounts{Total: total, TCP: total - 10, UDP: 10}, CorePorts: ports}
}

func TestMachineProtectionWarmupBlockingAndRelease(t *testing.T) {
	fake, clock := &fakeMachineNft{}, &fakeClock{now: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}
	manager := newTestMachineManager(fake, clock)
	settings := &machineProtectionSettings{Enabled: true, MaxTotal: int64Value(100)}

	status := manager.reconcile(context.Background(), settings, machineInputWithTotal(150, 10016))
	if status.Blocking || !status.WarmingUp || !status.Effective || len(fake.applies) != 1 || strings.Contains(fake.applies[0], "hook prerouting priority -150; policy accept; }\nadd rule") {
		t.Fatalf("a fresh table must warm conntrack up before dropping: %#v applies=%q", status, fake.applies)
	}
	if !strings.Contains(fake.applies[0], "delete chain inet "+machineProtectionTableName+" input") {
		t.Fatalf("first apply must clean the old input-chain layout:\n%s", fake.applies[0])
	}
	clock.now = clock.now.Add(machineConntrackWarmup)
	fake.dropped = 7
	status = manager.reconcile(context.Background(), settings, machineInputWithTotal(150, 10016))
	if !status.Blocking || status.ThresholdReason != "total" || status.DroppedTotal != 7 || status.ReleaseBelow == nil || *status.ReleaseBelow != 95 || len(fake.applies) != 2 || !strings.Contains(fake.applies[1], "prerouting ct state new tcp flags") {
		t.Fatalf("blocking status=%#v applies=%d", status, len(fake.applies))
	}
	// Inside the hysteresis band the guard keeps blocking without new rules.
	clock.now = clock.now.Add(time.Second)
	status = manager.reconcile(context.Background(), settings, machineInputWithTotal(96, 10016))
	if !status.Blocking || status.ThresholdReason != "total_hysteresis" || len(fake.applies) != 2 {
		t.Fatalf("hysteresis status=%#v applies=%d", status, len(fake.applies))
	}
	clock.now = clock.now.Add(time.Second)
	fake.dropped = 9
	status = manager.reconcile(context.Background(), settings, machineInputWithTotal(94, 10016))
	if status.Blocking || len(fake.applies) != 3 || !strings.Contains(fake.applies[2], "delete chain inet "+machineProtectionTableName+" prerouting") || status.DroppedTotal != 9 {
		t.Fatalf("release status=%#v applies=%q", status, fake.applies)
	}
}

func TestMachineProtectionIdleCostsNoNftCalls(t *testing.T) {
	fake, clock := &fakeMachineNft{}, &fakeClock{now: time.Unix(1792500000, 0)}
	manager := newTestMachineManager(fake, clock)
	settings := &machineProtectionSettings{Enabled: true, MaxTotal: int64Value(1000)}
	manager.reconcile(context.Background(), settings, machineInputWithTotal(300, 443))
	calls := len(fake.applies) + fake.counters + fake.removes
	// 11 more loops (two reconciles each) inside one minute: zero nft calls.
	for loop := 0; loop < 11; loop++ {
		clock.now = clock.now.Add(5 * time.Second)
		manager.reconcile(context.Background(), settings, machineInputWithTotal(300, 443))
		manager.reconcile(context.Background(), settings, machineInputWithTotal(300, 443))
	}
	if got := len(fake.applies) + fake.counters + fake.removes; got != calls {
		t.Fatalf("idle guard made %d nft calls within a minute", got-calls)
	}
	clock.now = clock.now.Add(6 * time.Second)
	manager.reconcile(context.Background(), settings, machineInputWithTotal(300, 443))
	if fake.counters != 1 || len(fake.applies) != 1 {
		t.Fatalf("idle recheck must be a single counter read per minute: counters=%d applies=%d", fake.counters, len(fake.applies))
	}
	// A port change re-applies once.
	manager.reconcile(context.Background(), settings, machineInputWithTotal(300, 443, 8443))
	if len(fake.applies) != 2 || !strings.Contains(fake.applies[1], "{ 443, 8443 }") {
		t.Fatalf("port change: %q", fake.applies)
	}
}

func TestMachineProtectionRebuildsAVanishedTable(t *testing.T) {
	fake, clock := &fakeMachineNft{}, &fakeClock{now: time.Unix(1792500000, 0)}
	manager := newTestMachineManager(fake, clock)
	settings := &machineProtectionSettings{Enabled: true, MaxTotal: int64Value(100)}
	manager.reconcile(context.Background(), settings, machineInputWithTotal(200, 443))
	clock.now = clock.now.Add(machineConntrackWarmup)
	fake.dropped = 5
	if status := manager.reconcile(context.Background(), settings, machineInputWithTotal(200, 443)); !status.Blocking || status.DroppedTotal != 5 {
		t.Fatalf("not blocking: %#v", status)
	}
	fake.present = false // nft flush ruleset
	clock.now = clock.now.Add(machineBlockingRecheck)
	status := manager.reconcile(context.Background(), settings, machineInputWithTotal(200, 443))
	if status.Blocking || !status.WarmingUp || !fake.present || status.DroppedTotal != 5 {
		t.Fatalf("vanished table must be rebuilt idle and warm up again: %#v", status)
	}
	clock.now = clock.now.Add(machineConntrackWarmup)
	fake.dropped = 2
	if status = manager.reconcile(context.Background(), settings, machineInputWithTotal(200, 443)); !status.Blocking || status.DroppedTotal != 7 {
		t.Fatalf("dropped_total must stay monotonic across a rebuilt counter: %#v", status)
	}
}

func TestMachineProtectionApplyFailureFailsOpen(t *testing.T) {
	fake, clock := &fakeMachineNft{applyErr: func(string) error { return errors.New("nft: syntax error") }}, &fakeClock{now: time.Unix(1792500000, 0)}
	manager := newTestMachineManager(fake, clock)
	status := manager.reconcile(context.Background(), &machineProtectionSettings{Enabled: true, MaxTotal: int64Value(10)}, machineInputWithTotal(200, 443))
	if status.Blocking || status.Effective || status.LastError == "" || fake.removes != 2 || fake.present {
		t.Fatalf("apply failure must remove our table and report: %#v removes=%d", status, fake.removes)
	}
	failures := 0
	fake.applyErr = func(string) error {
		failures++
		if failures == 1 {
			return errors.New("unexpected old layout")
		}
		return nil
	}
	status = manager.reconcile(context.Background(), &machineProtectionSettings{Enabled: true, MaxTotal: int64Value(10)}, machineInputWithTotal(200, 443))
	if !status.Effective || status.LastError != "" || !fake.present {
		t.Fatalf("a replaced table must recover: %#v", status)
	}
}

func TestMachineProtectionDisabledRemovesOnlyOwnedTableOnce(t *testing.T) {
	fake, clock := &fakeMachineNft{}, &fakeClock{now: time.Unix(1792500000, 0)}
	manager := newTestMachineManager(fake, clock)
	for loop := 0; loop < 5; loop++ {
		status := manager.reconcile(context.Background(), nil, machineInputWithTotal(25, 10017))
		if !status.Effective || status.Blocking || status.Configured.Enabled {
			t.Fatalf("unexpected disabled status: %#v", status)
		}
	}
	if fake.removes != 1 || len(fake.applies) != 0 {
		t.Fatalf("disabled protection: removes=%d applies=%d", fake.removes, len(fake.applies))
	}
	malformed := &machineProtectionSettings{Enabled: true, MaxActive: int64Value(0)}
	if status := manager.reconcile(context.Background(), malformed, machineInputWithTotal(25, 10017)); status.Effective || status.Blocking || status.LastError == "" {
		t.Fatalf("malformed config did not fail open: %#v", status)
	}
}

func TestMachineProtectionNoPortsAndUnknownTotalNeverBlock(t *testing.T) {
	fake, clock := &fakeMachineNft{}, &fakeClock{now: time.Unix(1792500000, 0)}
	manager := newTestMachineManager(fake, clock)
	settings := &machineProtectionSettings{Enabled: true, MaxTotal: int64Value(10)}
	manager.reconcile(context.Background(), settings, machineInputWithTotal(500))
	clock.now = clock.now.Add(time.Minute)
	status := manager.reconcile(context.Background(), settings, machineInputWithTotal(500))
	if status.Blocking || status.IdleReason != "no_business_ports" || status.LastError != "" || !status.Effective {
		t.Fatalf("no ports: %#v", status)
	}
	status = manager.reconcile(context.Background(), settings, machineProtectionInput{CorePorts: []uint32{443}})
	if status.Blocking || status.LastError == "" {
		t.Fatalf("unknown machine total must fail open: %#v", status)
	}
}

type fakeSysctl struct {
	value  int64
	writes []int64
}

func (fake *fakeSysctl) ops() machineSysctl {
	return machineSysctl{
		read:  func() (int64, error) { return fake.value, nil },
		write: func(value int64) error { fake.value = value; fake.writes = append(fake.writes, value); return nil },
	}
}

func TestMachineProtectionTWBucketsSaveApplyRestore(t *testing.T) {
	sysctl, clock := &fakeSysctl{value: 262144}, &fakeClock{now: time.Unix(1792500000, 0)}
	manager := newTestMachineManager(&fakeMachineNft{}, clock)
	manager.sysctl = sysctl.ops()
	var persisted []*machineTWSaved
	manager.saveTW = func(saved *machineTWSaved) error {
		if saved != nil && manager.tw == nil && sysctl.value != saved.Original {
			t.Fatalf("original must be persisted before the first change: kernel=%d saved=%#v", sysctl.value, saved)
		}
		persisted = append(persisted, saved)
		return nil
	}
	enabled := &machineProtectionSettings{Enabled: true, MaxTotal: int64Value(1000)}
	status := manager.reconcile(context.Background(), enabled, machineInputWithTotal(10, 443))
	if sysctl.value != 200 || len(persisted) != 1 || persisted[0].Original != 262144 || persisted[0].Applied != 200 || status.TWBuckets == nil || *status.TWBuckets.Current != 200 || *status.TWBuckets.Original != 262144 {
		t.Fatalf("default TIME_WAIT 200: kernel=%d persisted=%#v status=%#v", sysctl.value, persisted, status.TWBuckets)
	}
	for loop := 0; loop < 3; loop++ {
		manager.reconcile(context.Background(), enabled, machineInputWithTotal(10, 443))
	}
	if len(sysctl.writes) != 1 || len(persisted) != 1 {
		t.Fatalf("steady state rewrote the sysctl: writes=%v persisted=%d", sysctl.writes, len(persisted))
	}
	custom := &machineProtectionSettings{Enabled: true, MaxTotal: int64Value(1000), TWBuckets: int64Value(2000)}
	manager.reconcile(context.Background(), custom, machineInputWithTotal(10, 443))
	if sysctl.value != 2000 || persisted[len(persisted)-1].Original != 262144 {
		t.Fatalf("changed target must keep the first original: kernel=%d saved=%#v", sysctl.value, persisted[len(persisted)-1])
	}
	manager.reconcile(context.Background(), &machineProtectionSettings{Enabled: false}, machineInputWithTotal(10, 443))
	if sysctl.value != 262144 || manager.tw != nil || persisted[len(persisted)-1] != nil {
		t.Fatalf("disable must restore and forget the original: kernel=%d tw=%#v", sysctl.value, manager.tw)
	}
	// Someone else changed the value while we owned it: never clobber it.
	manager.reconcile(context.Background(), enabled, machineInputWithTotal(10, 443))
	sysctl.value = 4096
	manager.reconcile(context.Background(), &machineProtectionSettings{Enabled: true, MaxTotal: int64Value(1000), TWBuckets: int64Value(0)}, machineInputWithTotal(10, 443))
	if sysctl.value != 4096 || manager.tw != nil {
		t.Fatalf("0 = off must not overwrite a foreign value: kernel=%d", sysctl.value)
	}
	// A failed save must not touch the kernel.
	manager.saveTW = func(*machineTWSaved) error { return errors.New("disk full") }
	writes := len(sysctl.writes)
	status = manager.reconcile(context.Background(), enabled, machineInputWithTotal(10, 443))
	if len(sysctl.writes) != writes || status.TWBuckets.Error == "" {
		t.Fatalf("unsaved original must block the write: %#v", status.TWBuckets)
	}
}

func TestMachineProtectionStatusFileContract(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mmwxc")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "machine-protection.json")
	clock := &fakeClock{now: time.Date(2026, 10, 20, 16, 41, 47, 0, time.UTC)}
	manager := newTestMachineManager(&fakeMachineNft{}, clock)
	manager.statusPath = path
	manager.sysctl = (&fakeSysctl{value: 4096}).ops()
	input := machineProtectionInput{Machine: &machineConnectionCounts{Total: 823, TCP: 700, UDP: 123}, CorePorts: []uint32{443}}
	manager.reconcile(context.Background(), &machineProtectionSettings{Enabled: true, MaxTotal: int64Value(1000)}, input)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"version": 1.0, "owner": "mmwx", "updated_at": "2026-10-20T16:41:47Z", "enabled": true, "max_total": 1000.0, "tw_buckets": 200.0, "close_wait_seconds": 10.0, "total": 823.0, "tcp": 700.0, "udp": 123.0, "blocking": false, "dropped_total": 0.0}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("status file = %s\nwant %#v", raw, want)
	}
	for target, mode := range map[string]os.FileMode{dir: 0755, path: 0644} {
		info, err := os.Stat(target)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s mode = %v, want %v (%v)", target, info.Mode().Perm(), mode, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("atomic write left temporary files: %v", entries)
	}

	clock.now = clock.now.Add(5 * time.Second)
	manager.reconcile(context.Background(), &machineProtectionSettings{Enabled: false, MaxTotal: int64Value(1000)}, input)
	raw, _ = os.ReadFile(path)
	decoded = map[string]any{}
	_ = json.Unmarshal(raw, &decoded)
	for _, key := range []string{"max_total", "tw_buckets", "close_wait_seconds"} {
		if value, ok := decoded[key]; !ok || value != nil {
			t.Fatalf("disabled status must carry %s = null: %s", key, raw)
		}
	}
	if decoded["enabled"] != false || decoded["updated_at"] != "2026-10-20T16:41:52Z" || decoded["total"] != 823.0 {
		t.Fatalf("disabled status file: %s", raw)
	}
	manager.release(context.Background())
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("release must remove the status file: %v", err)
	}
}

func TestMachineCloseWaitDefaultReachesBothCores(t *testing.T) {
	enabled := &machineProtectionSettings{Enabled: true, MaxTotal: int64Value(1000)}
	for _, item := range []struct {
		name     string
		settings connectionSettings
		want     *int64
	}{
		{"protection default", connectionSettings{MachineProtection: enabled}, int64Value(10)},
		{"explicit server default wins", connectionSettings{MachineProtection: enabled, DefaultCloseWaitTimeoutSeconds: int64Value(30)}, int64Value(30)},
		{"explicit zero falls back", connectionSettings{MachineProtection: enabled, DefaultCloseWaitTimeoutSeconds: int64Value(0)}, int64Value(10)},
		{"custom value", connectionSettings{MachineProtection: &machineProtectionSettings{Enabled: true, MaxTotal: int64Value(1000), CloseWaitSeconds: int64Value(20)}}, int64Value(20)},
		{"zero = off", connectionSettings{MachineProtection: &machineProtectionSettings{Enabled: true, MaxTotal: int64Value(1000), CloseWaitSeconds: int64Value(0)}}, nil},
		{"disabled", connectionSettings{MachineProtection: &machineProtectionSettings{Enabled: false, CloseWaitSeconds: int64Value(20)}}, nil},
		{"no protection", connectionSettings{}, nil},
	} {
		got := item.settings.coreConfig().DefaultCloseWaitTimeoutSeconds
		if (got == nil) != (item.want == nil) || got != nil && *got != *item.want {
			t.Fatalf("%s: default close wait = %v, want %v", item.name, got, item.want)
		}
		if legacy := item.settings.legacyCoreConfig().DefaultCloseWaitTimeoutSeconds; !reflect.DeepEqual(legacy, got) {
			t.Fatalf("%s: legacy config differs: %v", item.name, legacy)
		}
	}
	shared := &flvxManager{}
	shared.setMachineCloseWait(enabled)
	if value := shared.machineCloseWaitSeconds(); value == nil || *value != 10 {
		t.Fatalf("shared Core close wait = %v", value)
	}
	shared.setMachineCloseWait(nil)
	if value := shared.machineCloseWaitSeconds(); value != nil {
		t.Fatalf("shared Core close wait without protection = %v", *value)
	}
	var missing *flvxManager
	missing.setMachineCloseWait(enabled)
	if ports := missing.businessPorts(); !ports.empty() {
		t.Fatal("nil FLVX manager has ports")
	}
}
