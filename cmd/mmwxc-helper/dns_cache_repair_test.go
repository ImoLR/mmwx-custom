package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// 「修复 DNS 缓存」 (dns.cache.ensure): runs ensure now in the background, one at a time, at most once a
// minute, never retried; the record and a fresh check go out with the next report.

type repairTestMonitor struct {
	*dnsCacheMonitor
	mu       sync.Mutex
	launches [][]string
	logs     []string
	checks   int
	now      time.Time
}

func newRepairTestMonitor(t *testing.T, launch func(e *dnsCacheEnsurer) (int, error)) *repairTestMonitor {
	t.Helper()
	test := &repairTestMonitor{now: time.Date(2026, 11, 9, 4, 0, 0, 0, time.UTC)}
	ensurer, dir := newTestDNSCacheEnsurer(t, "v0.7.26", nil)
	repairer := *ensurer
	repairer.markerPath = filepath.Join(dir, "state", "dns-cache-repair.json")
	repairer.launch = func(ctx context.Context, args []string) (int, error) {
		test.mu.Lock()
		test.launches = append(test.launches, args)
		test.mu.Unlock()
		return launch(&repairer)
	}
	repairer.now = func() time.Time { return test.clock() }
	test.dnsCacheMonitor = &dnsCacheMonitor{
		now: test.clock, ensurer: ensurer, repairer: &repairer, enabled: true, zones: []string{"mmwxc.imgamer.top"},
		check: func() dnsCacheStatus {
			test.mu.Lock()
			defer test.mu.Unlock()
			test.checks++
			return dnsCacheStatus{Mode: "dnsmasq", OK: true}
		},
		print: func(format string, args ...any) {
			test.mu.Lock()
			defer test.mu.Unlock()
			test.logs = append(test.logs, fmt.Sprintf(format, args...))
		},
	}
	return test
}

func (m *repairTestMonitor) clock() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

func (m *repairTestMonitor) advance(d time.Duration) {
	m.mu.Lock()
	m.now = m.now.Add(d)
	m.mu.Unlock()
}

// wait until the background run is over and has logged its one line.
func (m *repairTestMonitor) wait(t *testing.T) dnsCacheStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.dnsCacheMonitor.mu.Lock()
		repairing := m.repairing
		m.dnsCacheMonitor.mu.Unlock()
		m.mu.Lock()
		logged := len(m.logs) >= len(m.launches)
		m.mu.Unlock()
		if !repairing && logged {
			return *m.snapshot()
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("repair did not finish")
	return dnsCacheStatus{}
}

func writeRepairOutput(e *dnsCacheEnsurer, output string) error {
	if err := os.MkdirAll(filepath.Dir(e.outputPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(e.outputPath, []byte(output), 0o600)
}

func TestDNSCacheRepairRunsNowAndReportsAtOnce(t *testing.T) {
	m := newRepairTestMonitor(t, func(e *dnsCacheEnsurer) (int, error) { return 0, writeRepairOutput(e, dnsCacheNoopOutput) })
	// This Helper version already did its once-per-version ensure: a repair runs anyway.
	done := dnsCacheEnsure{HelperVersion: "v0.7.26", ScriptSHA256: dnsCacheScriptSHA256, Attempts: 1, StartedAt: m.clock(), FinishedAt: m.clock()}
	if err := writeDNSCacheMarker(m.ensurer.markerPath, done); err != nil {
		t.Fatal(err)
	}
	m.snapshot()
	checksBefore := m.checks
	started, err := m.startRepair(context.Background(), "cmd-1")
	if err != nil || started == nil || started.CommandID != "cmd-1" || started.StartedAt.IsZero() || !started.FinishedAt.IsZero() {
		t.Fatalf("start: %+v %v", started, err)
	}
	status := m.wait(t)
	repair := status.Repair
	if repair == nil || repair.CommandID != "cmd-1" || repair.HelperVersion != "v0.7.26" || repair.FinishedAt.IsZero() || repair.ExitCode != 0 || repair.Changed ||
		repair.Summary != "DNS 缓存：本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）无需改动" {
		t.Fatalf("repair record = %+v", repair)
	}
	if !reflect.DeepEqual(m.launches, [][]string{{"ensure", "--zones", "mmwxc.imgamer.top"}}) {
		t.Fatalf("launches = %v", m.launches)
	}
	// The new state is checked at once, not 10 minutes later.
	if m.checks != checksBefore+1 {
		t.Fatalf("checks = %d, want a fresh check after the repair (%d)", m.checks, checksBefore+1)
	}
	if len(m.logs) != 1 || !strings.Contains(m.logs[0], "DNS cache repair: DNS 缓存：本机 dnsmasq") {
		t.Fatalf("logs = %q, want exactly one line", m.logs)
	}
	stored, err := readDNSCacheMarker(m.repairer.markerPath)
	if err != nil || !reflect.DeepEqual(stored, *repair) {
		t.Fatalf("stored repair record = %+v (%v)", stored, err)
	}
	// The once-per-version marker is untouched (the dialog keeps showing the install / upgrade run).
	if marker, _ := readDNSCacheMarker(m.ensurer.markerPath); !reflect.DeepEqual(marker, done) || status.Ensure != nil && status.Ensure.CommandID != "" {
		t.Fatalf("once-per-version marker changed: %+v", marker)
	}
}

func TestDNSCacheRepairGuards(t *testing.T) {
	release := make(chan struct{})
	m := newRepairTestMonitor(t, func(e *dnsCacheEnsurer) (int, error) {
		<-release
		return 0, writeRepairOutput(e, dnsCacheNoopOutput)
	})
	if _, err := m.startRepair(context.Background(), "cmd-1"); err != nil {
		t.Fatal(err)
	}
	// One run at a time.
	if _, err := m.startRepair(context.Background(), "cmd-2"); !errors.Is(err, errDNSCacheRepairBusy) {
		t.Fatalf("second click while running: %v", err)
	}
	if status := m.snapshot(); status.Repair == nil || status.Repair.CommandID != "cmd-1" || !status.Repair.FinishedAt.IsZero() {
		t.Fatalf("running repair not reported: %+v", status.Repair)
	}
	close(release)
	m.wait(t)
	// At most one a minute.
	m.advance(30 * time.Second)
	if _, err := m.startRepair(context.Background(), "cmd-3"); err == nil || !strings.Contains(err.Error(), "刚刚执行过，请 31 秒后再试") {
		t.Fatalf("click inside the cooldown: %v", err)
	}
	// The once-per-version ensure (or a unit still going) holds the lock: busy, nothing waits.
	m.advance(31 * time.Second)
	unlock, err := lockDNSCacheFile(m.repairer.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.startRepair(context.Background(), "cmd-4"); !errors.Is(err, errDNSCacheRepairBusy) {
		t.Fatalf("click while the lock is held: %v", err)
	}
	unlock()
	m.repairer.active = func(context.Context) bool { return true }
	if _, err := m.startRepair(context.Background(), "cmd-5"); !errors.Is(err, errDNSCacheRepairBusy) {
		t.Fatalf("click while the unit runs: %v", err)
	}
	m.repairer.active = func(context.Context) bool { return false }
	m.enabled = false
	if _, err := m.startRepair(context.Background(), "cmd-6"); err == nil || !strings.Contains(err.Error(), "MMWXC_HELPER_DNS_CACHE=0") {
		t.Fatalf("switched-off host: %v", err)
	}
	m.enabled = true
	if _, err := m.startRepair(context.Background(), "cmd-7"); err != nil {
		t.Fatalf("after the cooldown: %v", err)
	}
	m.wait(t)
	if len(m.launches) != 2 {
		t.Fatalf("launches = %d, want 2 (cmd-1 and cmd-7)", len(m.launches))
	}
	var nilMonitor *dnsCacheMonitor
	if _, err := nilMonitor.startRepair(context.Background(), "x"); err == nil {
		t.Fatal("nil monitor accepted a repair")
	}
}

func TestDNSCacheRepairFailureIsNeverRetried(t *testing.T) {
	m := newRepairTestMonitor(t, func(e *dnsCacheEnsurer) (int, error) {
		return 1, writeRepairOutput(e, "[flvx-dns-cache] cache not possible: apt 安装 dnsmasq-base 失败 -> minimal\nDNS 缓存：精简模式（apt 安装 dnsmasq-base 失败），解析器 10.0.0.2，超时 1 秒\nRESULT mode=minimal changed=1 mode=none ok=0 reason=x\n")
	})
	if _, err := m.startRepair(context.Background(), "cmd-1"); err != nil {
		t.Fatal(err)
	}
	status := m.wait(t)
	if status.Repair == nil || status.Repair.ExitCode != 1 || !strings.HasPrefix(status.Repair.Summary, "DNS 缓存：精简模式") {
		t.Fatalf("failed repair record = %+v", status.Repair)
	}
	m.advance(10 * time.Minute)
	m.snapshot()
	time.Sleep(20 * time.Millisecond)
	if len(m.launches) != 1 || len(m.logs) != 1 || !strings.Contains(m.logs[0], "did not complete (exit 1") {
		t.Fatalf("launches=%d logs=%q: a failed repair must not run again by itself", len(m.launches), m.logs)
	}
	// A launch error is a finished, failed run too.
	broken := newRepairTestMonitor(t, func(*dnsCacheEnsurer) (int, error) { return 0, os.ErrPermission })
	if _, err := broken.startRepair(context.Background(), "cmd-1"); err != nil {
		t.Fatal(err)
	}
	if status := broken.wait(t); status.Repair.ExitCode != -1 || !strings.Contains(status.Repair.Summary, "无法执行 flvx-dns-cache") || status.Repair.FinishedAt.IsZero() {
		t.Fatalf("launch error record = %+v", status.Repair)
	}
}

func TestDNSCacheRepairTimeoutIsRecorded(t *testing.T) {
	m := newRepairTestMonitor(t, func(e *dnsCacheEnsurer) (int, error) { return 0, errDNSCacheBusy })
	m.repairer.active = func(context.Context) bool { return false }
	unlock, err := m.repairer.beginRepair(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.repairer.active = func(context.Context) bool { return true } // the unit never ends within our wait
	record, err := m.repairer.repairLocked(ctx, dnsCacheEnsure{CommandID: "cmd-1", StartedAt: m.clock()}, nil)
	unlock()
	m.endRepair(record, err, "repair")
	status := m.snapshot()
	if status.Repair == nil || status.Repair.FinishedAt.IsZero() || status.Repair.ExitCode != -1 || !strings.Contains(status.Repair.Summary, "没有自动重试") {
		t.Fatalf("timed-out repair record = %+v (err %v)", status.Repair, err)
	}
	if stored, _ := readDNSCacheMarker(m.repairer.markerPath); stored.FinishedAt.IsZero() {
		t.Fatalf("timed-out repair not stored as finished: %+v", stored)
	}
}

func TestDNSCacheRepairResumedAfterHelperRestart(t *testing.T) {
	m := newRepairTestMonitor(t, func(*dnsCacheEnsurer) (int, error) {
		t.Fatal("a resumed repair must never launch again")
		return 0, nil
	})
	claim := dnsCacheEnsure{CommandID: "cmd-1", HelperVersion: "v0.7.26", ScriptSHA256: dnsCacheScriptSHA256, Attempts: 1, StartedAt: time.Now().UTC().Add(-time.Second)}
	if err := writeDNSCacheMarker(m.repairer.markerPath, claim); err != nil {
		t.Fatal(err)
	}
	// The unit still runs after the restart: wait for it, take its output.
	polls := 0
	m.repairer.active = func(context.Context) bool {
		polls++
		if polls == 2 {
			_ = writeRepairOutput(m.repairer, dnsCacheNoopOutput)
		}
		return polls < 3
	}
	m.repairing = true
	m.resumeRepair()
	status := m.snapshot()
	if m.repairing || status.Repair == nil || status.Repair.CommandID != "cmd-1" || status.Repair.ExitCode != 0 || !strings.Contains(status.Repair.Summary, "无需改动") || len(m.launches) != 0 {
		t.Fatalf("resumed repair = %+v launches=%d", status.Repair, len(m.launches))
	}
	// Cut off before the unit started (no fresh output): recorded as interrupted, not relaunched.
	_ = os.Remove(m.repairer.outputPath)
	claim.StartedAt = time.Now().UTC()
	_ = writeDNSCacheMarker(m.repairer.markerPath, claim)
	m.repairer.active = func(context.Context) bool { return false }
	m.resumeRepair()
	if status := m.snapshot(); status.Repair.ExitCode != -1 || !strings.Contains(status.Repair.Summary, "Helper 重启打断") || len(m.launches) != 0 {
		t.Fatalf("interrupted repair = %+v", status.Repair)
	}
	// A finished record is left alone.
	before, _ := readDNSCacheMarker(m.repairer.markerPath)
	if record, err := m.repairer.resumeRepair(context.Background()); err != nil || !reflect.DeepEqual(record, before) {
		t.Fatalf("finished record changed: %+v %v", record, err)
	}
}

func TestDNSCacheRepairCommand(t *testing.T) {
	m := newRepairTestMonitor(t, func(e *dnsCacheEnsurer) (int, error) { return 0, writeRepairOutput(e, dnsCacheNoopOutput) })
	executor := &commandExecutor{state: &localState{}, dnsCache: m.dnsCacheMonitor}
	command := signedTestCommand(t, "token", dnsCacheRepairCapability, nil)
	result := executor.execute(context.Background(), command, "token")
	if !result.Success || result.Action != "dns.cache.ensure" {
		t.Fatalf("result = %+v", result)
	}
	var data struct {
		Repair dnsCacheEnsure `json:"repair"`
	}
	if err := json.Unmarshal(result.Data, &data); err != nil || data.Repair.CommandID != command.ID || data.Repair.StartedAt.IsZero() {
		t.Fatalf("result data %s: %v", result.Data, err)
	}
	m.wait(t)
	// A second click inside the minute fails at once with the reason; nothing runs.
	again := signedTestCommand(t, "token", dnsCacheRepairCapability, nil)
	again.ID = "command-2"
	again.Signature, _ = managementMAC(helperTokenHash("token"), commandSigningBytes(again))
	if result := executor.execute(context.Background(), again, "token"); result.Success || !strings.Contains(result.Message, "刚刚执行过") {
		t.Fatalf("second click = %+v", result)
	}
	if len(m.launches) != 1 {
		t.Fatalf("launches = %d", len(m.launches))
	}
	// Advertised so Custom can enable the button; older Helpers do not have it.
	found := false
	for _, capability := range helperCapabilities {
		found = found || capability == "dns.cache.ensure"
	}
	if !found {
		t.Fatal("dns.cache.ensure capability not advertised")
	}
	// No monitor (very old state): a clear failure, not a panic.
	if result := (&commandExecutor{state: &localState{}}).execute(context.Background(), signedTestCommand(t, "token", dnsCacheRepairCapability, nil), "token"); result.Success {
		t.Fatalf("repair without a monitor succeeded: %+v", result)
	}
}

func TestDNSCacheRepairInStatusJSON(t *testing.T) {
	status := agentStatus{DNSCache: &dnsCacheStatus{Mode: "dnsmasq", OK: true, CheckedAt: time.Date(2026, 11, 9, 4, 0, 1, 0, time.UTC),
		Repair: &dnsCacheEnsure{CommandID: "cmd-1", HelperVersion: "v0.7.26", StartedAt: time.Date(2026, 11, 9, 4, 0, 0, 0, time.UTC)}}}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"repair":{"command_id":"cmd-1","helper_version":"v0.7.26","script_sha256":"","attempts":0,"started_at":"2026-11-09T04:00:00Z","finished_at":"0001-01-01T00:00:00Z"`; !strings.Contains(string(encoded), want) {
		t.Fatalf("status JSON %s\nmissing %s", encoded, want)
	}
}
