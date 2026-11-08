package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDNSCacheScriptIsTheFLVXCopy(t *testing.T) {
	sum := sha256.Sum256(dnsCacheScript)
	if got := hex.EncodeToString(sum[:]); got != dnsCacheScriptSHA256 {
		t.Fatalf("vendored flvx-dns-cache.sh sha256 = %s, want FLVX 1455ba60 %s (copy it byte-identical, then update the pin)", got, dnsCacheScriptSHA256)
	}
	if dnsCacheScriptVersion() != "1" || !strings.HasPrefix(string(dnsCacheScript), "#!/bin/bash\n# flvx-dns-cache") {
		t.Fatalf("unexpected script header / version %q", dnsCacheScriptVersion())
	}
}

type dnsCacheFakeHost struct {
	t       *testing.T
	checker dnsCacheChecker
}

func newDNSCacheFakeHost(t *testing.T, resolv string) *dnsCacheFakeHost {
	t.Helper()
	dir := t.TempDir()
	host := &dnsCacheFakeHost{t: t, checker: dnsCacheChecker{
		resolvConf: filepath.Join(dir, "resolv.conf"), state: filepath.Join(dir, "state"),
		conf: "/etc/flvx-dnscache/dnsmasq.conf", proc: filepath.Join(dir, "proc"),
	}}
	if resolv != "" {
		host.write(host.checker.resolvConf, resolv)
	}
	host.proc("1", "systemd", "/sbin/init")
	return host
}

func (h *dnsCacheFakeHost) write(path, contents string) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *dnsCacheFakeHost) proc(pid, comm, cmdline string) {
	h.write(filepath.Join(h.checker.proc, pid, "comm"), comm+"\n")
	h.write(filepath.Join(h.checker.proc, pid, "cmdline"), strings.ReplaceAll(cmdline, " ", "\x00")+"\x00")
}

// The same cases as FLVX's TestCheckRules, plus the mmwx fleet layouts seen on 2026-11-09.
func TestDNSCacheCheckRules(t *testing.T) {
	const ours = "/usr/sbin/dnsmasq --keep-in-foreground --conf-file=/etc/flvx-dnscache/dnsmasq.conf"
	cases := []struct {
		name   string
		resolv string
		state  string
		procs  [][3]string
		want   dnsCacheStatus
	}{
		{"plan239 dnsmasq host (Boil HKT 99u)", "# FLVX\nnameserver 127.0.0.1\nnameserver 1.1.1.1\nnameserver 1.0.0.1\noptions timeout:1 attempts:2\n", "",
			[][3]string{{"812", "dnsmasq", ours}}, dnsCacheStatus{Mode: "dnsmasq", OK: true}},
		{"PQS TW: drop-in adds --conf-dir, still ours", "nameserver 127.0.0.1\nnameserver 1.1.1.1\nnameserver 168.95.1.1\noptions timeout:1 attempts:2\n", "",
			[][3]string{{"640", "dnsmasq", ours + " --conf-dir=/etc/flvx-dnscache/local.d,*.conf"}, {"641", "python3", "/usr/bin/python3 /usr/local/sbin/flvx-dns-tcp.py"}},
			dnsCacheStatus{Mode: "dnsmasq", OK: true}},
		{"cache down, fallback still there", "nameserver 127.0.0.1\nnameserver 1.1.1.1\noptions timeout:1 attempts:2\n", "",
			nil, dnsCacheStatus{Mode: "dnsmasq", Reason: "本机缓存 flvx-dnscache 未运行"}},
		{"someone else's dnsmasq does not count", "nameserver 127.0.0.1\nnameserver 1.1.1.1\n", "",
			[][3]string{{"900", "dnsmasq", "/usr/sbin/dnsmasq -x /run/dnsmasq/dnsmasq.pid"}}, dnsCacheStatus{Mode: "dnsmasq", Reason: "本机缓存 flvx-dnscache 未运行"}},
		{"other local resolver, ensure chose minimal", "nameserver 127.0.0.1\nnameserver 8.8.8.8\noptions timeout:1\n", "mode=minimal\nreason=53 端口已被 unbound 占用\n",
			[][3]string{{"77", "unbound", "/usr/sbin/unbound -d"}}, dnsCacheStatus{Mode: "minimal", OK: true, Reason: "53 端口已被 unbound 占用"}},
		{"resolved stub (Nobrand KFC 1cm)", "nameserver 127.0.0.53\noptions edns0 trust-ad\nsearch comcorn.net\n", "",
			[][3]string{{"300", "systemd-resolve", "/lib/systemd/systemd-resolved"}}, dnsCacheStatus{Mode: "resolved", OK: true}},
		{"resolved via static file (Fachost seednet 黑五)", "nameserver 127.0.0.53\nnameserver 1.1.1.1\nnameserver 8.8.8.8\noptions timeout:1 attempts:2 edns0 trust-ad\nsearch lan\n", "",
			[][3]string{{"301", "systemd-resolved", "/usr/lib/systemd/systemd-resolved"}}, dnsCacheStatus{Mode: "resolved", OK: true}},
		{"resolved stub, resolved dead", "nameserver 127.0.0.53\nnameserver 1.1.1.1\noptions timeout:1 attempts:2 edns0 trust-ad\n", "",
			nil, dnsCacheStatus{Mode: "resolved", Reason: "systemd-resolved 未运行"}},
		{"phase-A only", "nameserver 8.8.8.8\nnameserver 1.1.1.1\noptions timeout:1 attempts:2\n", "",
			nil, dnsCacheStatus{Mode: "minimal", OK: true, Reason: "未装本机缓存（只有备用解析器 + 1 秒超时）"}},
		{"minimal with reason from ensure", "nameserver 8.8.8.8\nnameserver 1.1.1.1\noptions timeout:1 attempts:2 rotate\n", "version=1\nmode=minimal\nreason=可用内存 40 MB（低于 60 MB）\n",
			nil, dnsCacheStatus{Mode: "minimal", OK: true, Reason: "可用内存 40 MB（低于 60 MB）"}},
		{"fresh host, single resolver", "nameserver 10.0.0.2\n", "", nil, dnsCacheStatus{Mode: "none", Reason: "未启用本机 DNS 缓存（只有 1 个解析器）"}},
		{"fresh host, no timeout", "nameserver 1.1.1.1\nnameserver 8.8.8.8\n", "", nil, dnsCacheStatus{Mode: "none", Reason: "未启用本机 DNS 缓存（解析超时仍是 5 秒）"}},
		{"commented nameservers ignored", "#nameserver 127.0.0.1\n; nameserver 1.1.1.1\n", "", nil, dnsCacheStatus{Mode: "none", Reason: "resolv.conf 里没有 nameserver"}},
		{"no resolv.conf", "", "", nil, dnsCacheStatus{Mode: "none", Reason: "没有 /etc/resolv.conf"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := newDNSCacheFakeHost(t, tc.resolv)
			if tc.state != "" {
				host.write(host.checker.state, tc.state)
			}
			for _, process := range tc.procs {
				host.proc(process[0], process[1], process[2])
			}
			if got := host.checker.check(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("check() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDNSCacheMonitorChecksAtMostEvery10Minutes(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	checks := 0
	monitor := &dnsCacheMonitor{now: func() time.Time { return now }, check: func() dnsCacheStatus {
		checks++
		return dnsCacheStatus{Mode: "dnsmasq", OK: true}
	}}
	for i := 0; i < 120; i++ { // 10 minutes of 5-second reports
		if status := monitor.snapshot(); status == nil || !status.OK || !status.CheckedAt.Equal(time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("report %d: %+v", i, status)
		}
		now = now.Add(5 * time.Second)
	}
	if checks != 1 {
		t.Fatalf("checks in the first 10 min = %d, want 1", checks)
	}
	monitor.snapshot()
	if checks != 2 {
		t.Fatalf("checks after 10 min = %d, want 2", checks)
	}
	var nilMonitor *dnsCacheMonitor
	if nilMonitor.snapshot() != nil {
		t.Fatal("nil monitor reported a status")
	}
	nilMonitor.scheduleEnsure()
}

func TestDNSCacheMonitorScheduleIsOncePerProcessAndOptional(t *testing.T) {
	ran := make(chan struct{}, 4)
	ensurer, _ := newTestDNSCacheEnsurer(t, "v0.7.25", func(ctx context.Context, args []string) (int, error) {
		ran <- struct{}{}
		return 0, nil
	})
	monitor := &dnsCacheMonitor{now: time.Now, check: func() dnsCacheStatus { return dnsCacheStatus{Mode: "dnsmasq", OK: true} },
		ensurer: ensurer, enabled: true, delay: time.Millisecond, print: func(string, ...any) {}}
	monitor.scheduleEnsure()
	monitor.scheduleEnsure()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("ensure did not run")
	}
	time.Sleep(50 * time.Millisecond)
	if len(ran) != 0 {
		t.Fatal("ensure ran twice in one process")
	}
	off := &dnsCacheMonitor{ensurer: ensurer, enabled: false, delay: time.Millisecond}
	off.scheduleEnsure()
	time.Sleep(50 * time.Millisecond)
	if len(ran) != 0 || off.scheduled {
		t.Fatal("MMWXC_HELPER_DNS_CACHE=0 still scheduled an ensure")
	}
}

func newTestDNSCacheEnsurer(t *testing.T, version string, launch func(context.Context, []string) (int, error)) (*dnsCacheEnsurer, string) {
	t.Helper()
	dir := t.TempDir()
	ensurer := &dnsCacheEnsurer{
		markerPath: filepath.Join(dir, "state", "dns-cache.json"), scriptPath: filepath.Join(dir, "lib", "flvx-dns-cache.sh"),
		outputPath: filepath.Join(dir, "lib", "dns-cache-ensure.log"), lockPath: filepath.Join(dir, "run", "dns-cache.lock"),
		version: version, sha256: dnsCacheScriptSHA256, now: time.Now, poll: time.Millisecond, launchTimeout: 5 * time.Second,
		active: func(context.Context) bool { return false },
	}
	ensurer.launch = launch
	return ensurer, dir
}

const dnsCacheNoopOutput = "[flvx-dns-cache] host boil3130 owner=static ns=[127.0.0.1 1.1.1.1 1.0.0.1] mem_avail=376MB\n" +
	"DNS 缓存：本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）无需改动\n" +
	"RESULT mode=dnsmasq changed=0 mode=dnsmasq ok=1 reason=\n"

func TestDNSCacheEnsureRunsOncePerHelperVersion(t *testing.T) {
	var calls [][]string
	var ensurer *dnsCacheEnsurer
	ensurer, _ = newTestDNSCacheEnsurer(t, "v0.7.25", func(ctx context.Context, args []string) (int, error) {
		calls = append(calls, args)
		script, err := os.ReadFile(ensurer.scriptPath)
		if err != nil || string(script) != string(dnsCacheScript) {
			t.Fatalf("script not installed before launch: %v", err)
		}
		return 0, os.WriteFile(ensurer.outputPath, []byte(dnsCacheNoopOutput), 0o600)
	})
	marker, ran, err := ensurer.run(context.Background(), []string{"mmwxc.imgamer.top"})
	if err != nil || !ran {
		t.Fatalf("first run: ran=%v err=%v", ran, err)
	}
	if !reflect.DeepEqual(calls, [][]string{{"ensure", "--zones", "mmwxc.imgamer.top"}}) {
		t.Fatalf("launch args = %v", calls)
	}
	if marker.HelperVersion != "v0.7.25" || marker.ScriptSHA256 != dnsCacheScriptSHA256 || marker.Attempts != 1 || marker.FinishedAt.IsZero() ||
		marker.ExitCode != 0 || marker.Changed || marker.Summary != "DNS 缓存：本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）无需改动" {
		t.Fatalf("marker = %+v", marker)
	}
	stored, err := readDNSCacheMarker(ensurer.markerPath)
	if err != nil || !reflect.DeepEqual(stored, marker) {
		t.Fatalf("stored marker = %+v (%v)", stored, err)
	}
	// Helper restarts (reboot, takeover, systemd restart) of the same version: nothing runs.
	for i := 0; i < 3; i++ {
		again, ran, err := ensurer.run(context.Background(), []string{"mmwxc.imgamer.top"})
		if err != nil || ran || !reflect.DeepEqual(again, marker) {
			t.Fatalf("restart %d: ran=%v err=%v marker=%+v", i, ran, err, again)
		}
	}
	// The next Helper version runs it once more.
	ensurer.version = "v0.7.26"
	next, ran, err := ensurer.run(context.Background(), nil)
	if err != nil || !ran || next.HelperVersion != "v0.7.26" || next.Attempts != 1 || len(calls) != 2 || !reflect.DeepEqual(calls[1], []string{"ensure"}) {
		t.Fatalf("upgrade: ran=%v err=%v marker=%+v calls=%v", ran, err, next, calls)
	}
}

func TestDNSCacheEnsureFailureOnlyWarns(t *testing.T) {
	var ensurer *dnsCacheEnsurer
	ensurer, _ = newTestDNSCacheEnsurer(t, "v0.7.25", func(ctx context.Context, args []string) (int, error) {
		out := "[flvx-dns-cache] cache not possible: apt 安装 dnsmasq-base 失败 -> minimal\nDNS 缓存：精简模式（apt 安装 dnsmasq-base 失败），解析器 10.0.0.2，超时 1 秒\nRESULT mode=minimal changed=1 mode=none ok=0 reason=未启用本机 DNS 缓存（只有 1 个解析器）\n"
		return 1, os.WriteFile(ensurer.outputPath, []byte(out), 0o600)
	})
	marker, ran, err := ensurer.run(context.Background(), nil)
	if err != nil || !ran || marker.ExitCode != 1 || !marker.Changed || !strings.HasPrefix(marker.Summary, "DNS 缓存：精简模式") || marker.FinishedAt.IsZero() {
		t.Fatalf("ran=%v err=%v marker=%+v", ran, err, marker)
	}
	// No retry loop: a failed run is finished for this version.
	if _, ran, _ := ensurer.run(context.Background(), nil); ran {
		t.Fatal("failed ensure was retried")
	}
	// A launch error (no bash, systemd-run broken) is recorded, not returned as a fatal error.
	broken, _ := newTestDNSCacheEnsurer(t, "v0.7.25", func(context.Context, []string) (int, error) { return 0, os.ErrPermission })
	marker, ran, err = broken.run(context.Background(), nil)
	if err != nil || !ran || marker.ExitCode != -1 || !strings.Contains(marker.Summary, "无法执行 flvx-dns-cache") {
		t.Fatalf("launch error: ran=%v err=%v marker=%+v", ran, err, marker)
	}
}

func TestDNSCacheEnsureResumesAfterHelperRestart(t *testing.T) {
	launched := 0
	ensurer, _ := newTestDNSCacheEnsurer(t, "v0.7.25", func(context.Context, []string) (int, error) {
		launched++
		return 0, nil
	})
	// The previous process claimed the run and was restarted (takeover); the transient unit still runs.
	claim := dnsCacheEnsure{HelperVersion: "v0.7.25", ScriptSHA256: dnsCacheScriptSHA256, Attempts: 1, StartedAt: time.Now().UTC()}
	if err := writeDNSCacheMarker(ensurer.markerPath, claim); err != nil {
		t.Fatal(err)
	}
	polls := 0
	ensurer.active = func(context.Context) bool {
		polls++
		if polls == 3 {
			_ = os.MkdirAll(filepath.Dir(ensurer.outputPath), 0o700)
			_ = os.WriteFile(ensurer.outputPath, []byte(dnsCacheNoopOutput), 0o600)
		}
		return polls < 4
	}
	marker, ran, err := ensurer.run(context.Background(), nil)
	if err != nil || !ran || launched != 0 || marker.Attempts != 1 || marker.ExitCode != 0 || marker.FinishedAt.IsZero() || !strings.Contains(marker.Summary, "无需改动") {
		t.Fatalf("resume: ran=%v err=%v launched=%d marker=%+v", ran, err, launched, marker)
	}
	// Claimed but neither running nor finished (cut before systemd-run started): retried once, then given up.
	claim.Attempts = 1
	_ = writeDNSCacheMarker(ensurer.markerPath, claim)
	ensurer.active = func(context.Context) bool { return false }
	ensurer.launch = func(context.Context, []string) (int, error) {
		launched++
		return 0, os.WriteFile(ensurer.outputPath, []byte(dnsCacheNoopOutput), 0o600)
	}
	if marker, ran, err = ensurer.run(context.Background(), nil); err != nil || !ran || launched != 1 || marker.Attempts != 2 {
		t.Fatalf("retry: ran=%v err=%v launched=%d marker=%+v", ran, err, launched, marker)
	}
	claim.Attempts = dnsCacheMaxAttempts
	_ = writeDNSCacheMarker(ensurer.markerPath, claim)
	marker, ran, err = ensurer.run(context.Background(), nil)
	if err != nil || ran || launched != 1 || marker.ExitCode != -1 || marker.FinishedAt.IsZero() || marker.Summary == "" {
		t.Fatalf("give up: ran=%v err=%v launched=%d marker=%+v", ran, err, launched, marker)
	}
}

func TestDNSCacheEnsureWaitsForABusyUnit(t *testing.T) {
	var ensurer *dnsCacheEnsurer
	polls := 0
	ensurer, _ = newTestDNSCacheEnsurer(t, "v0.7.25", func(context.Context, []string) (int, error) { return 0, errDNSCacheBusy })
	ensurer.active = func(context.Context) bool {
		polls++
		if polls == 2 {
			_ = os.WriteFile(ensurer.outputPath, []byte(dnsCacheNoopOutput), 0o600)
		}
		return polls < 3
	}
	marker, ran, err := ensurer.run(context.Background(), nil)
	if err != nil || !ran || marker.ExitCode != 0 || !strings.Contains(marker.Summary, "无需改动") {
		t.Fatalf("busy: ran=%v err=%v marker=%+v", ran, err, marker)
	}
}

func TestDNSCacheZones(t *testing.T) {
	for raw, want := range map[string][]string{
		"https://mmwxc.imgamer.top":       {"mmwxc.imgamer.top"},
		"https://MMWXC.imgamer.top./api/": {"mmwxc.imgamer.top"},
		"http://mmwxc.imgamer.top:8443":   {"mmwxc.imgamer.top"},
		"http://203.0.113.9:12890":        nil,
		"https://[2001:db8::1]:443":       nil,
		"http://localhost:12890":          nil,
		"https://bad_host.example.com":    nil,
		"https://x.example.com;rm -rf /":  nil,
		"::not a url":                     nil,
	} {
		if got := dnsCacheZones(raw); !reflect.DeepEqual(got, want) {
			t.Errorf("dnsCacheZones(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestParseDNSCacheOutput(t *testing.T) {
	summary, changed, ok := parseDNSCacheOutput(dnsCacheNoopOutput)
	if summary != "DNS 缓存：本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）无需改动" || changed || !ok {
		t.Fatalf("noop: %q %v %v", summary, changed, ok)
	}
	summary, changed, ok = parseDNSCacheOutput("need root\n")
	if summary != "need root" || changed || ok {
		t.Fatalf("error: %q %v %v", summary, changed, ok)
	}
	long := strings.Repeat("缓", 200)
	if summary, _, _ = parseDNSCacheOutput("DNS 缓存：" + long); len(summary) > 300 || !strings.HasPrefix(summary, "DNS 缓存：") {
		t.Fatalf("long summary not capped on a rune: %d bytes", len(summary))
	}
}

func TestDNSCacheInHelperStatusJSON(t *testing.T) {
	status := agentStatus{DNSCache: &dnsCacheStatus{Mode: "minimal", OK: true, Reason: "可用内存 40 MB（低于 60 MB）", CheckedAt: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC),
		Ensure: &dnsCacheEnsure{HelperVersion: "v0.7.25", ExitCode: 0, Summary: "DNS 缓存：精简模式"}}}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"dns_cache":{"mode":"minimal","ok":true,"reason":"可用内存 40 MB（低于 60 MB）","checked_at":"2026-11-09T00:00:00Z","ensure":{"helper_version":"v0.7.25"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("status JSON %s\nmissing %s", encoded, want)
		}
	}
	if encoded, _ = json.Marshal(agentStatus{}); strings.Contains(string(encoded), "dns_cache") {
		t.Fatal("dns_cache must be omitted when unknown")
	}
}

func TestDNSCacheConfigSwitch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "helper.env")
	base := "MMWXC_HELPER_API_URL=https://mmwxc.imgamer.top\nMMWXC_HELPER_SERVER_ID=2\nMMWXC_HELPER_TOKEN=token\n"
	for extra, want := range map[string]bool{"": true, "MMWXC_HELPER_DNS_CACHE=0\n": false, "MMWXC_HELPER_DNS_CACHE=false\n": false,
		"MMWXC_HELPER_DNS_CACHE=off\n": false, "MMWXC_HELPER_DNS_CACHE=1\n": true, "MMWXC_HELPER_DNS_CACHE=typo\n": true} {
		if err := os.WriteFile(path, []byte(base+extra), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadConfig(path)
		if err != nil || cfg.DNSCacheEnsure != want {
			t.Fatalf("%q: DNSCacheEnsure=%v err=%v, want %v", extra, cfg.DNSCacheEnsure, err, want)
		}
	}
}

func TestDNSCacheEnsureOutlivesItsLaunchTimeout(t *testing.T) {
	var ensurer *dnsCacheEnsurer
	ensurer, _ = newTestDNSCacheEnsurer(t, "v0.7.25", func(ctx context.Context, args []string) (int, error) {
		<-ctx.Done() // a slow apt: our systemd-run client is cut off, the unit keeps running
		return -1, nil
	})
	ensurer.launchTimeout = 20 * time.Millisecond
	polls := 0
	ensurer.active = func(context.Context) bool {
		polls++
		if polls == 2 {
			_ = os.WriteFile(ensurer.outputPath, []byte(dnsCacheNoopOutput), 0o600)
		}
		return polls < 3
	}
	marker, ran, err := ensurer.run(context.Background(), nil)
	if err != nil || !ran || polls < 3 || marker.ExitCode != 0 || marker.FinishedAt.IsZero() || !strings.Contains(marker.Summary, "无需改动") {
		t.Fatalf("ran=%v err=%v polls=%d marker=%+v", ran, err, polls, marker)
	}
}
