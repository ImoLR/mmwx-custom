package main

// Local DNS cache (task helper-dns-cache, 2026-11-08). FLVX plan 239 put a local DNS cache on every machine
// by hand and plan 240 turned the recipe into flvx-dns-cache.sh. The Helper ships that script byte-identical
// (flvx-dns-cache.sh here, sha256 dnsCacheScriptSHA256, FLVX commit 1455ba60 = 3.0.27-fork.127) so every
// server Custom manages keeps the cache, new ones included.
//
//   - `ensure` runs once per Helper version: from install-helper.sh (`mmwxc-helper dns-cache ensure-once`,
//     the installer prints the result) and from the new Helper process a minute after it first reaches
//     Custom (the managed 「升级 Helper」 runs the old binary, so the new one has to do it itself). It is a
//     no-op on hosts that are already right (plan 239). A failure only warns: no retry loop, nothing blocks.
//     Host-local extras (PQS TW: flvx-dnscache.service.d drop-in, /etc/flvx-dnscache/local.d, flvx-dnstcp)
//     are never touched by the script.
//   - The status Custom shows is a local check (resolv.conf, the script's state file, /proc) at most every
//     10 minutes: no exec, no network. Same rules as do_check in the script and FLVX's dnscache/check.go.
//   - 「修复 DNS 缓存」 (task custom-dns-cache-repair, 2026-11-09; FLVX's button of the same name): the
//     admin's click becomes the dns.cache.ensure command. It runs `ensure` once in the same transient unit,
//     in the background (a run may take minutes; the report loop never waits for it), and is never retried.
//     The record (dns_cache.repair) and a fresh local check go out with the next report.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed flvx-dns-cache.sh
var dnsCacheScript []byte

const (
	// dnsCacheScriptSHA256 pins the vendored copy to FLVX's (go-gost/x/dnscache/flvx-dns-cache.sh @ 1455ba60).
	dnsCacheScriptSHA256  = "7350f279b4d27975ab0ad3feb83e16c9e1722d9e8eb8ab7602508a06c98f4919"
	dnsCacheScriptPath    = "/var/lib/mmwxc/flvx-dns-cache.sh"
	dnsCacheOutputPath    = "/var/lib/mmwxc/dns-cache-ensure.log"
	dnsCacheMarkerPath    = "/var/lib/mmwxc-helper/dns-cache.json"
	dnsCacheLockPath      = "/run/mmwxc-dns-cache.lock"
	dnsCacheUnit          = "mmwxc-dns-cache-ensure"
	dnsCacheCheckInterval = 10 * time.Minute
	dnsCacheEnsureDelay   = time.Minute
	// dnsCacheEnsureTimeout bounds one run (apt on a slow mirror included), as FLVX's EnsureTimeout.
	dnsCacheEnsureTimeout = 200 * time.Second
	// dnsCacheMaxAttempts: a run cut off by a Helper restart is resumed or retried once, never looped.
	dnsCacheMaxAttempts = 2
	// dnsCacheRepairCapability is the 「修复 DNS 缓存」 command (and capability) Custom sends.
	dnsCacheRepairCapability = "dns.cache.ensure"
	dnsCacheRepairPath       = "/var/lib/mmwxc-helper/dns-cache-repair.json"
	// dnsCacheRepairCooldown: at most one repair a minute per server (Custom checks it too).
	dnsCacheRepairCooldown = time.Minute
)

// dnsCacheStatus is reported as dns_cache in the Helper status.
type dnsCacheStatus struct {
	Mode      string          `json:"mode"` // dnsmasq | resolved | minimal | none
	OK        bool            `json:"ok"`
	Reason    string          `json:"reason,omitempty"`
	CheckedAt time.Time       `json:"checked_at"`
	Ensure    *dnsCacheEnsure `json:"ensure,omitempty"`
	// Repair is the last 「修复 DNS 缓存」 run; it is still running while finished_at is empty.
	Repair *dnsCacheEnsure `json:"repair,omitempty"`
}

// dnsCacheEnsure is the once-per-Helper-version record (the marker file and the last run in the status),
// and the same record for a repair run (CommandID set).
type dnsCacheEnsure struct {
	CommandID     string    `json:"command_id,omitempty"`
	HelperVersion string    `json:"helper_version"`
	ScriptSHA256  string    `json:"script_sha256"`
	Attempts      int       `json:"attempts"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	ExitCode      int       `json:"exit_code"`
	Changed       bool      `json:"changed"`
	Summary       string    `json:"summary,omitempty"`
}

// ---------------------------------------------------------------- local check

type dnsCacheChecker struct {
	resolvConf string
	state      string
	conf       string
	proc       string
}

var defaultDNSCacheChecker = dnsCacheChecker{
	resolvConf: "/etc/resolv.conf",
	state:      "/etc/flvx-dnscache/state",
	conf:       "/etc/flvx-dnscache/dnsmasq.conf",
	proc:       "/proc",
}

// check: resolv.conf points at a local cache and that cache runs. Keep in sync with do_check in
// flvx-dns-cache.sh (and FLVX go-gost/x/dnscache/check.go).
func (c dnsCacheChecker) check() dnsCacheStatus {
	raw, err := os.ReadFile(c.resolvConf)
	if err != nil {
		return dnsCacheStatus{Mode: "none", Reason: "没有 /etc/resolv.conf"}
	}
	ns, opts := parseDNSCacheResolv(raw)
	stateMode, stateReason := readDNSCacheState(c.state)
	if len(ns) == 0 {
		return dnsCacheStatus{Mode: "none", Reason: "resolv.conf 里没有 nameserver"}
	}
	switch ns[0] {
	case "127.0.0.1", "::1":
		if c.running("dnsmasq", c.conf) {
			return dnsCacheStatus{Mode: "dnsmasq", OK: true}
		}
		if stateMode == "minimal" {
			return dnsCacheStatus{Mode: "minimal", OK: true, Reason: stateReason}
		}
		return dnsCacheStatus{Mode: "dnsmasq", Reason: "本机缓存 flvx-dnscache 未运行"}
	case "127.0.0.53":
		if c.running("systemd-resolve", "") || c.running("systemd-resolved", "") {
			return dnsCacheStatus{Mode: "resolved", OK: true}
		}
		return dnsCacheStatus{Mode: "resolved", Reason: "systemd-resolved 未运行"}
	}
	if len(ns) >= 2 && dnsCacheShortTimeout(opts) {
		if stateReason == "" {
			stateReason = "未装本机缓存（只有备用解析器 + 1 秒超时）"
		}
		return dnsCacheStatus{Mode: "minimal", OK: true, Reason: stateReason}
	}
	if len(ns) < 2 {
		return dnsCacheStatus{Mode: "none", Reason: "未启用本机 DNS 缓存（只有 1 个解析器）"}
	}
	return dnsCacheStatus{Mode: "none", Reason: "未启用本机 DNS 缓存（解析超时仍是 5 秒）"}
}

func parseDNSCacheResolv(raw []byte) (ns, opts []string) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") || strings.HasPrefix(fields[0], ";") {
			continue
		}
		switch fields[0] {
		case "nameserver":
			ns = append(ns, fields[1])
		case "options":
			opts = append(opts, fields[1:]...)
		}
	}
	return ns, opts
}

func dnsCacheShortTimeout(opts []string) bool {
	for _, option := range opts {
		if option == "timeout:1" || option == "timeout:2" {
			return true
		}
	}
	return false
}

func readDNSCacheState(path string) (mode, reason string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "mode":
			mode = strings.TrimSpace(value)
		case "reason":
			reason = strings.TrimSpace(value)
		}
	}
	return mode, truncateDNSCacheText(reason, 300)
}

// running reports whether a process with this comm runs (and, if argContains is set, has it on its
// command line). One pass over /proc/*/comm; cmdline is read only for comm matches.
func (c dnsCacheChecker) running(comm, argContains string) bool {
	entries, err := os.ReadDir(c.proc)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(c.proc, name, "comm"))
		if err != nil || strings.TrimSpace(string(raw)) != comm {
			continue
		}
		if argContains == "" {
			return true
		}
		cmdline, err := os.ReadFile(filepath.Join(c.proc, name, "cmdline"))
		if err == nil && bytes.Contains(cmdline, []byte(argContains)) {
			return true
		}
	}
	return false
}

func truncateDNSCacheText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit], "")
}

// ---------------------------------------------------------------- monitor (status + the daemon's ensure)

type dnsCacheMonitor struct {
	mu        sync.Mutex
	status    dnsCacheStatus
	ensure    *dnsCacheEnsure
	check     func() dnsCacheStatus
	now       func() time.Time
	ensurer   *dnsCacheEnsurer
	scheduled bool
	enabled   bool
	zones     []string
	delay     time.Duration
	print     func(string, ...any)
	// 「修复 DNS 缓存」: the repairer is the ensurer with its own record file; repairing while a run is active.
	repairer  *dnsCacheEnsurer
	repair    *dnsCacheEnsure
	repairing bool
}

func newDNSCacheMonitor(cfg config) *dnsCacheMonitor {
	monitor := &dnsCacheMonitor{
		check: defaultDNSCacheChecker.check, now: time.Now, ensurer: newDNSCacheEnsurer(),
		enabled: cfg.DNSCacheEnsure, zones: dnsCacheZones(cfg.CustomAPIURL), delay: dnsCacheEnsureDelay, print: log.Printf,
	}
	if marker, err := readDNSCacheMarker(monitor.ensurer.markerPath); err == nil {
		monitor.ensure = &marker
	}
	repairer := *monitor.ensurer
	repairer.markerPath = dnsCacheRepairPath
	monitor.repairer = &repairer
	if record, err := readDNSCacheMarker(repairer.markerPath); err == nil && !record.StartedAt.IsZero() {
		monitor.repair = &record
		if record.FinishedAt.IsZero() {
			// The previous process was restarted during a repair: record how it ended (never relaunched).
			monitor.repairing = true
			go monitor.resumeRepair()
		}
	}
	return monitor
}

// snapshot returns the last local check, re-checking when it is older than 10 minutes.
func (m *dnsCacheMonitor) snapshot() *dnsCacheStatus {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.CheckedAt.IsZero() || m.now().Sub(m.status.CheckedAt) >= dnsCacheCheckInterval {
		m.refreshLocked()
	}
	status := m.status
	if m.ensure != nil {
		ensure := *m.ensure
		status.Ensure = &ensure
	}
	if m.repair != nil {
		repair := *m.repair
		status.Repair = &repair
	}
	return &status
}

func (m *dnsCacheMonitor) refreshLocked() {
	status := m.check()
	status.CheckedAt = m.now().UTC()
	m.status = status
}

// scheduleEnsure starts the once-per-version ensure a minute after the Helper first reached Custom
// (install / upgrade health checks are done by then). Once per process; the marker makes it once per version.
func (m *dnsCacheMonitor) scheduleEnsure() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scheduled || !m.enabled {
		return
	}
	m.scheduled = true
	time.AfterFunc(m.delay, m.runEnsure)
}

func (m *dnsCacheMonitor) runEnsure() {
	ctx, cancel := context.WithTimeout(context.Background(), dnsCacheEnsureTimeout+time.Minute)
	defer cancel()
	marker, ran, err := m.ensurer.run(ctx, m.zones)
	m.mu.Lock()
	if marker.HelperVersion != "" {
		m.ensure = &marker
	}
	m.refreshLocked()
	m.mu.Unlock()
	switch {
	case err != nil:
		m.print("[mmwxc-helper] DNS cache ensure (%s): %v", helperVersion, err)
	case ran && marker.ExitCode != 0:
		m.print("[mmwxc-helper] DNS cache ensure (%s) did not complete (exit %d, the Helper is not affected): %s", helperVersion, marker.ExitCode, marker.Summary)
	case ran:
		m.print("[mmwxc-helper] DNS cache ensure (%s): %s", helperVersion, marker.Summary)
	}
}

var errDNSCacheRepairBusy = errors.New("这台服务器正在执行 DNS 缓存检查，请稍候")

// startRepair is the dns.cache.ensure command (「修复 DNS 缓存」): it claims the run and returns at once; the
// run goes on in the background and its record + a fresh check go out with the following reports. Busy,
// inside the cooldown or switched off → an error and nothing runs.
func (m *dnsCacheMonitor) startRepair(ctx context.Context, commandID string) (*dnsCacheEnsure, error) {
	if m == nil || m.repairer == nil {
		return nil, errors.New("DNS 缓存修复不可用")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled {
		return nil, errors.New("这台服务器在 /etc/mmwxc-helper.env 里关掉了 DNS 缓存（MMWXC_HELPER_DNS_CACHE=0）")
	}
	if m.repairing {
		return nil, errDNSCacheRepairBusy
	}
	now := m.now()
	if m.repair != nil {
		if wait := dnsCacheRepairCooldown - now.Sub(m.repair.StartedAt); wait > 0 {
			return nil, fmt.Errorf("刚刚执行过，请 %d 秒后再试", int(wait.Seconds())+1)
		}
	}
	unlock, err := m.repairer.beginRepair(ctx)
	if err != nil {
		return nil, err
	}
	record := dnsCacheEnsure{CommandID: commandID, HelperVersion: m.repairer.version, ScriptSHA256: m.repairer.sha256, Attempts: 1, StartedAt: now.UTC()}
	m.repair, m.repairing = &record, true
	go func() {
		defer unlock()
		m.runRepair(record)
	}()
	started := record
	return &started, nil
}

func (m *dnsCacheMonitor) runRepair(record dnsCacheEnsure) {
	ctx, cancel := context.WithTimeout(context.Background(), dnsCacheEnsureTimeout+time.Minute)
	defer cancel()
	record, err := m.repairer.repairLocked(ctx, record, m.zones)
	m.endRepair(record, err, "repair")
}

// resumeRepair: the previous process was restarted during a repair. Wait for the unit if it still runs,
// then record its outcome; nothing is launched again.
func (m *dnsCacheMonitor) resumeRepair() {
	ctx, cancel := context.WithTimeout(context.Background(), dnsCacheEnsureTimeout+time.Minute)
	defer cancel()
	record, err := m.repairer.resumeRepair(ctx)
	m.endRepair(record, err, "repair (resumed)")
}

// endRepair stores the outcome, re-checks the cache at once and logs one line.
func (m *dnsCacheMonitor) endRepair(record dnsCacheEnsure, err error, what string) {
	if record.FinishedAt.IsZero() {
		// Timed out waiting for the unit, or the record could not be written: this run is over for us.
		record.FinishedAt, record.ExitCode = m.now().UTC(), -1
		if errors.Is(err, errDNSCacheBusy) {
			record.Summary = fmt.Sprintf("超过 %d 秒没有结束，没有自动重试", int((dnsCacheEnsureTimeout + time.Minute).Seconds()))
		} else if err != nil {
			record.Summary = truncateDNSCacheText(err.Error(), 300)
		}
		_ = writeDNSCacheMarker(m.repairer.markerPath, record)
	}
	m.mu.Lock()
	m.repair, m.repairing = &record, false
	m.refreshLocked()
	m.mu.Unlock()
	if record.ExitCode != 0 {
		m.print("[mmwxc-helper] DNS cache %s did not complete (exit %d, the Helper is not affected): %s", what, record.ExitCode, record.Summary)
		return
	}
	m.print("[mmwxc-helper] DNS cache %s: %s", what, record.Summary)
}

var dnsCacheZoneRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// dnsCacheZones: the Custom API host, so its zone (imgamer.top for mmwxc.imgamer.top) is sent to
// Cloudflare. The script keeps the zones already configured (union), so this never removes one.
func dnsCacheZones(apiURL string) []string {
	parsed, err := url.Parse(apiURL)
	if err != nil {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if len(host) > 253 || !dnsCacheZoneRe.MatchString(host) || strings.Trim(host, "0123456789.") == "" {
		return nil
	}
	return []string{host}
}

// ---------------------------------------------------------------- ensure once per Helper version

var errDNSCacheBusy = errors.New("another flvx-dns-cache ensure run is still active")

type dnsCacheEnsurer struct {
	markerPath string
	scriptPath string
	outputPath string
	lockPath   string
	version    string
	sha256     string
	// launch runs the installed script with args, its output going to outputPath; returns the exit code.
	launch func(ctx context.Context, args []string) (int, error)
	// active reports whether a previous launch (transient unit) still runs.
	active        func(ctx context.Context) bool
	now           func() time.Time
	poll          time.Duration
	launchTimeout time.Duration
}

func newDNSCacheEnsurer() *dnsCacheEnsurer {
	ensurer := &dnsCacheEnsurer{
		markerPath: dnsCacheMarkerPath, scriptPath: dnsCacheScriptPath, outputPath: dnsCacheOutputPath, lockPath: dnsCacheLockPath,
		version: helperVersion, sha256: dnsCacheScriptSHA256, now: time.Now, poll: 2 * time.Second, launchTimeout: dnsCacheEnsureTimeout,
	}
	ensurer.launch = ensurer.launchTransient
	ensurer.active = func(ctx context.Context) bool {
		return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", dnsCacheUnit+".service").Run() == nil
	}
	return ensurer
}

// run executes `ensure` unless this Helper version already did. ran is false when nothing was started.
func (e *dnsCacheEnsurer) run(ctx context.Context, zones []string) (marker dnsCacheEnsure, ran bool, err error) {
	unlock, err := lockDNSCacheFile(e.lockPath)
	if err != nil {
		return dnsCacheEnsure{}, false, err
	}
	defer unlock()
	marker, readErr := readDNSCacheMarker(e.markerPath)
	same := readErr == nil && marker.HelperVersion == e.version && marker.ScriptSHA256 == e.sha256
	if same && !marker.FinishedAt.IsZero() {
		return marker, false, nil
	}
	if same && e.active(ctx) {
		// The process that started it was restarted (takeover / upgrade); the unit kept running.
		return e.finish(ctx, marker, -1, true)
	}
	if same && marker.Attempts >= dnsCacheMaxAttempts {
		marker.FinishedAt, marker.ExitCode = e.now().UTC(), -1
		if marker.Summary == "" {
			marker.Summary = "上次执行被中断，本版本不再自动重试"
		}
		return marker, false, writeDNSCacheMarker(e.markerPath, marker)
	}
	if !same {
		marker = dnsCacheEnsure{HelperVersion: e.version, ScriptSHA256: e.sha256}
	}
	marker.Attempts++
	marker.StartedAt, marker.FinishedAt, marker.ExitCode, marker.Changed, marker.Summary = e.now().UTC(), time.Time{}, 0, false, ""
	return e.launchRecorded(ctx, marker, zones)
}

// beginRepair takes the run lock for a 「修复 DNS 缓存」 without waiting: another run (the once-per-version
// ensure, or a unit still going) → errDNSCacheRepairBusy.
func (e *dnsCacheEnsurer) beginRepair(ctx context.Context) (func(), error) {
	unlock, err := tryLockDNSCacheFile(e.lockPath)
	if err != nil {
		return nil, err
	}
	if e.active(ctx) {
		unlock()
		return nil, errDNSCacheRepairBusy
	}
	return unlock, nil
}

// repairLocked runs `ensure` now, whatever the once-per-version marker says (lock held by beginRepair).
func (e *dnsCacheEnsurer) repairLocked(ctx context.Context, record dnsCacheEnsure, zones []string) (dnsCacheEnsure, error) {
	record, _, err := e.launchRecorded(ctx, record, zones)
	return record, err
}

// resumeRepair records a repair the previous process left unfinished: waits for the unit if it still
// runs, else takes the output it left (or marks the run as cut off). Never launches anything.
func (e *dnsCacheEnsurer) resumeRepair(ctx context.Context) (dnsCacheEnsure, error) {
	unlock, err := lockDNSCacheFile(e.lockPath)
	if err != nil {
		return dnsCacheEnsure{}, err
	}
	defer unlock()
	record, err := readDNSCacheMarker(e.markerPath)
	if err != nil || !record.FinishedAt.IsZero() {
		return record, err
	}
	if e.active(ctx) {
		record, _, err = e.finish(ctx, record, -1, true)
		return record, err
	}
	if info, statErr := os.Stat(e.outputPath); statErr == nil && !info.ModTime().Before(record.StartedAt) {
		record, _, err = e.finish(ctx, record, -1, false)
		return record, err
	}
	record.FinishedAt, record.ExitCode, record.Summary = e.now().UTC(), -1, "执行被 Helper 重启打断，没有自动重试"
	return record, writeDNSCacheMarker(e.markerPath, record)
}

// launchRecorded claims the run in the record file, launches the script and records the outcome (lock held).
func (e *dnsCacheEnsurer) launchRecorded(ctx context.Context, marker dnsCacheEnsure, zones []string) (dnsCacheEnsure, bool, error) {
	if err := writeDNSCacheMarker(e.markerPath, marker); err != nil {
		return marker, false, err
	}
	if err := installDNSCacheScript(e.scriptPath); err != nil {
		return marker, false, err
	}
	_ = os.Remove(e.outputPath) // systemd's file: output overwrites without truncating
	args := []string{"ensure"}
	if len(zones) > 0 {
		args = append(args, "--zones", strings.Join(zones, " "))
	}
	launchCtx, cancel := context.WithTimeout(ctx, e.launchTimeout)
	code, launchErr := e.launch(launchCtx, args)
	timedOut := launchCtx.Err() != nil
	cancel()
	if errors.Is(launchErr, errDNSCacheBusy) || timedOut {
		// Busy, or our systemd-run client was cut off while the unit keeps going: wait for its result.
		return e.finish(ctx, marker, -1, true)
	}
	if launchErr != nil {
		marker.FinishedAt, marker.ExitCode = e.now().UTC(), -1
		marker.Summary = truncateDNSCacheText("无法执行 flvx-dns-cache："+launchErr.Error(), 300)
		return marker, true, writeDNSCacheMarker(e.markerPath, marker)
	}
	return e.finish(ctx, marker, code, false)
}

// finish waits for a still-running unit when asked, then records the outcome from the script output.
func (e *dnsCacheEnsurer) finish(ctx context.Context, marker dnsCacheEnsure, code int, wait bool) (dnsCacheEnsure, bool, error) {
	if wait {
		for e.active(ctx) {
			select {
			case <-ctx.Done():
				return marker, true, errDNSCacheBusy
			case <-time.After(e.poll):
			}
		}
	}
	output, _ := os.ReadFile(e.outputPath)
	summary, changed, ok := parseDNSCacheOutput(string(output))
	if code < 0 && ok {
		code = 0 // the unit's own exit code is gone; the RESULT line says it ended in a working mode
	}
	marker.FinishedAt, marker.ExitCode, marker.Changed, marker.Summary = e.now().UTC(), code, changed, summary
	return marker, true, writeDNSCacheMarker(e.markerPath, marker)
}

// parseDNSCacheOutput: the script's 「DNS 缓存：…」 line (else the last line) and its RESULT line.
func parseDNSCacheOutput(output string) (summary string, changed, ok bool) {
	last := ""
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "DNS 缓存："):
			summary = line
		case strings.HasPrefix(line, "RESULT "):
			changed = strings.Contains(line, " changed=1")
			ok = strings.Contains(line, " ok=1")
			continue
		}
		last = line
	}
	if summary == "" {
		summary = last
	}
	return truncateDNSCacheText(summary, 300), changed, ok
}

// launchTransient runs the script as a transient unit (outside the Helper's cgroup, so a Helper restart
// cannot cut an apt install in half). Without systemd-run it runs as a child process.
func (e *dnsCacheEnsurer) launchTransient(ctx context.Context, args []string) (int, error) {
	if path, err := exec.LookPath("systemd-run"); err == nil {
		command := append([]string{"--quiet", "--wait", "--collect", "--unit=" + dnsCacheUnit,
			"-p", "StandardOutput=file:" + e.outputPath, "-p", "StandardError=inherit", "/bin/bash", e.scriptPath}, args...)
		output, runErr := exec.CommandContext(ctx, path, command...).CombinedOutput()
		if bytes.Contains(output, []byte("already exists")) || bytes.Contains(output, []byte("already loaded")) {
			return 0, errDNSCacheBusy
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		if runErr != nil {
			return 0, fmt.Errorf("systemd-run: %v: %s", runErr, strings.TrimSpace(string(output)))
		}
		return 0, nil
	}
	file, err := os.OpenFile(e.outputPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	command := exec.CommandContext(ctx, "/bin/bash", append([]string{e.scriptPath}, args...)...)
	command.Stdout, command.Stderr = file, file
	runErr := command.Run()
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 0, runErr
}

func installDNSCacheScript(path string) error {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, dnsCacheScript) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return durableReplaceFile(path, dnsCacheScript, 0700)
}

func readDNSCacheMarker(path string) (dnsCacheEnsure, error) {
	var marker dnsCacheEnsure
	raw, err := os.ReadFile(path)
	if err != nil {
		return marker, err
	}
	return marker, json.Unmarshal(raw, &marker)
}

func writeDNSCacheMarker(path string, marker dnsCacheEnsure) error {
	encoded, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return durableReplaceFile(path, append(encoded, '\n'), 0600)
}

func lockDNSCacheFile(path string) (func(), error) {
	return flockDNSCacheFile(path, syscall.LOCK_EX)
}

// tryLockDNSCacheFile is lockDNSCacheFile without waiting: held elsewhere → errDNSCacheRepairBusy.
func tryLockDNSCacheFile(path string) (func(), error) {
	unlock, err := flockDNSCacheFile(path, syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil, errDNSCacheRepairBusy
	}
	return unlock, err
}

func flockDNSCacheFile(path string, how int) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), how); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

// ---------------------------------------------------------------- CLI: mmwxc-helper dns-cache ...

// dnsCacheCLI: `ensure-once` is the installer's step (prints the 「DNS 缓存：…」 line, exit 1 when it did
// not end in a working mode); `version`; anything else (ensure / status / check / rollback, --dry-run …)
// goes to the script itself.
func dnsCacheCLI(args []string, configPath string) int {
	if len(args) > 0 && args[0] == "version" {
		sum := sha256.Sum256(dnsCacheScript)
		fmt.Printf("flvx-dns-cache %s sha256=%s\n", dnsCacheScriptVersion(), hex.EncodeToString(sum[:]))
		return 0
	}
	if len(args) > 0 && args[0] == "ensure-once" {
		var zones []string
		if cfg, err := loadConfig(configPath); err == nil {
			if !cfg.DNSCacheEnsure {
				fmt.Println("DNS 缓存：已按 MMWXC_HELPER_DNS_CACHE 跳过")
				return 0
			}
			zones = dnsCacheZones(cfg.CustomAPIURL)
		}
		ctx, cancel := context.WithTimeout(context.Background(), dnsCacheEnsureTimeout+time.Minute)
		defer cancel()
		marker, _, err := newDNSCacheEnsurer().run(ctx, zones)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flvx-dns-cache: %v\n", err)
			return 1
		}
		fmt.Println(marker.Summary)
		if marker.ExitCode != 0 {
			return 1
		}
		return 0
	}
	if err := installDNSCacheScript(dnsCacheScriptPath); err != nil {
		fmt.Fprintf(os.Stderr, "flvx-dns-cache: cannot install %s: %v\n", dnsCacheScriptPath, err)
		return 1
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		fmt.Fprintln(os.Stderr, "flvx-dns-cache: bash not found")
		return 1
	}
	if err := syscall.Exec(bash, append([]string{"bash", dnsCacheScriptPath}, args...), os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "flvx-dns-cache: %v\n", err)
	}
	return 1
}

func dnsCacheScriptVersion() string {
	for _, line := range strings.Split(string(dnsCacheScript), "\n") {
		if value, ok := strings.CutPrefix(line, "SCRIPT_VERSION="); ok {
			return strings.TrimSpace(value)
		}
	}
	return "?"
}
