package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// TestFLVXParentPermsNetnsE2E reproduces the 10-22 「Dmit T1」 failure with
// real systemd and the real FLVX Core: /opt/mmwxc and /etc/mmwxc left at 0700
// by an earlier Custom Core install. The v0.7.11 start ends in 203/EXEC; the
// fixed setup starts the shared Core on the first try and serves SS2022. The
// unit joins an isolated network namespace, so nothing listens on the host.
func TestFLVXParentPermsNetnsE2E(t *testing.T) {
	core, mihomo := os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED"), os.Getenv("MMWXC_TEST_MIHOMO")
	if core == "" || mihomo == "" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_TEST_FLVX_CORE_FIXED, MMWXC_TEST_MIHOMO and run as root")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Skip("needs systemd as PID 1")
	}
	name := "ppe" + strconv.Itoa(os.Getpid()%100000)
	ns := flvxNetns{t: t}
	flvxNetnsCleanup(name)
	t.Cleanup(func() { flvxNetnsCleanup(name) })
	ns.sh("ip", "netns", "add", name)
	ns.sh("ip", "-n", name, "link", "set", "lo", "up")
	ns.sh("ip", "-n", name, "link", "add", "d0", "type", "dummy")
	ns.sh("ip", "-n", name, "link", "set", "d0", "up")
	for _, address := range []string{lpEchoTCP, lpEchoA, lpEchoB} {
		ns.sh("ip", "-n", name, "addr", "add", address+"/32", "dev", "d0")
	}
	evidence := os.Getenv("MMWXC_TEST_PERMS_EVIDENCE")
	if evidence == "" {
		evidence = filepath.Join(t.TempDir(), "evidence.json")
	}
	command := exec.Command("ip", "netns", "exec", name, os.Args[0], "-test.run", "^TestFLVXParentPermsNetnsInner$", "-test.v", "-test.timeout", "10m")
	command.Env = append(os.Environ(), "MMWXC_PERMS_INNER=1", "MMWXC_PERMS_NETNS="+name, "MMWXC_PERMS_EVIDENCE="+evidence)
	out, err := command.CombinedOutput()
	t.Logf("inner run:\n%s", out)
	if err != nil {
		t.Fatalf("inner E2E failed: %v", err)
	}
}

type permsE2E struct {
	t                     *testing.T
	root, service, unit   string
	m                     *flvxManager
	changes               []string
	evidence              map[string]any
	coreBin, hash, mihomo string
	account               flvxAccount
	createdUser           bool
}

func TestFLVXParentPermsNetnsInner(t *testing.T) {
	if os.Getenv("MMWXC_PERMS_INNER") != "1" {
		t.Skip("runs only inside the E2E namespace")
	}
	ctx := context.Background()
	id := strconv.Itoa(os.Getpid())
	e := &permsE2E{t: t, coreBin: os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED"), mihomo: os.Getenv("MMWXC_TEST_MIHOMO"), evidence: map[string]any{"started_at": time.Now().UTC().Format(time.RFC3339)},
		// Outside /tmp and /root: the unit runs with PrivateTmp= and ProtectHome=.
		root: "/opt/mmwxc-e2e-" + id, service: "mmwxc-flvx-core-e2e-" + id + ".service"}
	e.unit = "/etc/systemd/system/" + e.service
	var err error
	if e.hash, err = flvxFileHash(e.coreBin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.cleanup)
	lpStartFixtures(t)

	// 「Dmit T1」 as of 10-22 04:07: the old Custom Core directories are 0700.
	for _, dir := range []string{"opt/mmwxc/core", "etc/mmwxc/core"} {
		e.mkdir(dir, 0700)
	}
	e.mkdir("opt/mmwxc", 0700)
	e.mkdir("etc/mmwxc", 0700)
	if err = os.WriteFile(filepath.Join(e.root, "opt/mmwxc/core/xray"), []byte("old Custom Core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(e.root, 0755); err != nil {
		t.Fatal(err)
	}
	e.ensureAccount()
	e.newManager()
	e.evidence["before"] = e.modes()

	// 1. v0.7.11: binary, config and unit exactly as it left them, then start.
	if _, err = e.m.installBinary(ctx, e.coreBin, e.hash); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(flvx.BaseConfig(e.m.state.APIPort))
	if err = writeFLVXFile(e.m.configPath, raw, 0640); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Dir(e.m.configPath), e.m.configPath} {
		if err = os.Chown(path, 0, int(e.account.gid)); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Chmod(filepath.Dir(e.m.configPath), 0750); err != nil {
		t.Fatal(err)
	}
	e.writeUnit()
	e.systemctl("start", e.service)
	var old map[string]string
	flvxWaitFor(t, 15*time.Second, func() bool {
		old = e.show()
		return old["ExecMainStatus"] == "203"
	})
	e.evidence["v0711_start"] = map[string]any{"show": old, "journal": e.journal(), "modes": e.modes()}
	t.Logf("v0.7.11 start: ExecMainStatus=%s (%s), parents untouched: %v", old["ExecMainStatus"], e.journal(), e.modes())
	e.systemctl("stop", e.service)
	exec.Command("systemctl", "reset-failed", e.service).Run() // gone once unloaded

	// 2. The fixed setup: first reconcile starts the Core and serves the node.
	e.reconcileStarts("fixed_first_try")
	if got := e.modes(); got["opt/mmwxc"] != "0711 root:root" || got["etc/mmwxc"] != "0711 root:root" || got["opt/mmwxc/core"] != "0700 root:root" || got["etc/mmwxc/core"] != "0700 root:root" {
		t.Fatalf("parents not opened for search only: %v", got)
	}
	if len(e.changes) != 2 {
		t.Fatalf("one log line per change expected: %q", e.changes)
	}
	e.traffic("fixed_first_try")
	if err = e.m.reconcile(ctx); err != nil || len(e.changes) != 2 {
		t.Fatalf("steady reconcile changed something: %v %q", err, e.changes)
	}

	// 3. A parent the Helper must not change (immutable): no start, no loop,
	// the exact path and mode reported for the share card.
	e.systemctl("stop", e.service)
	before := e.show()
	dir := filepath.Join(e.root, "opt/mmwxc")
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	e.sh("chattr", "+i", dir)
	err = e.m.reconcile(ctx)
	var access *flvxAccessError
	if !errors.As(err, &access) || !strings.Contains(err.Error(), dir+"（0700 root:root）") || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("immutable parent: %v", err)
	}
	e.m.reportFailure(ctx, err)
	time.Sleep(6 * time.Second) // longer than RestartSec=
	after := e.show()
	if after["ActiveState"] != "inactive" || after["NRestarts"] != before["NRestarts"] || after["ExecMainStartTimestampMonotonic"] != before["ExecMainStartTimestampMonotonic"] {
		t.Fatalf("unit started despite the access problem: %v -> %v", before, after)
	}
	if report := e.m.snapshotReport(); report == nil || report.Core.State != "error" || report.Core.Error != err.Error() {
		t.Fatalf("share card error not reported: %+v", report)
	}
	e.evidence["immutable_parent"] = map[string]any{"error": err.Error(), "show_before": before, "show_after_6s": after}
	t.Logf("immutable parent: %s; unit stayed %s, NRestarts %s", err, after["ActiveState"], after["NRestarts"])
	e.sh("chattr", "-i", dir)
	e.reconcileStarts("after_immutable_removed")

	// 4. An ACL denying the account where the mode looks open: only the real
	// pre-flight (as mmwxc-flvx) can see it.
	e.systemctl("stop", e.service)
	if err = syscall.Setxattr(dir, "system.posix_acl_access", flvxDenyACL(e.account.uid), 0); err != nil {
		t.Fatal(err)
	}
	before = e.show()
	err = e.m.reconcile(ctx)
	if !errors.As(err, &access) || !strings.Contains(err.Error(), "试运行失败") || !strings.Contains(err.Error(), dir+"（0711 root:root）") {
		t.Fatalf("ACL deny: %v", err)
	}
	e.m.reportFailure(ctx, err)
	time.Sleep(6 * time.Second)
	if after = e.show(); after["ActiveState"] != "inactive" || after["NRestarts"] != before["NRestarts"] || after["ExecMainStartTimestampMonotonic"] != before["ExecMainStartTimestampMonotonic"] {
		t.Fatalf("unit started despite the failed pre-flight: %v -> %v", before, after)
	}
	e.evidence["acl_deny"] = map[string]any{"error": err.Error(), "mode": e.modes()["opt/mmwxc"], "show_after_6s": after}
	t.Logf("ACL deny: %s; unit stayed %s", err, after["ActiveState"])
	if err = syscall.Removexattr(dir, "system.posix_acl_access"); err != nil {
		t.Fatal(err)
	}
	e.reconcileStarts("after_acl_removed")

	// 5. Fresh install under umask 077 with no parents at all.
	e.systemctl("stop", e.service)
	if err = e.m.removeInstance(ctx); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"opt", "etc"} {
		if err = os.RemoveAll(filepath.Join(e.root, dir)); err != nil {
			t.Fatal(err)
		}
	}
	e.newManager()
	e.writeDropIn()
	umask := syscall.Umask(0077)
	_, err = e.m.installBinary(ctx, e.coreBin, e.hash)
	e.evidence["umask077_install"] = e.modes()
	if err == nil {
		e.reconcileStarts("umask077_first_try")
	}
	syscall.Umask(umask)
	if err != nil {
		t.Fatal(err)
	}
	got := e.modes()
	for path, want := range map[string]string{"opt": "0711 root:root", "opt/mmwxc": "0711 root:root", "opt/mmwxc/flvx-core": "0755 root:root", "opt/mmwxc/flvx-core/mmwxc-flvx-core": "0755 root:root", "etc/mmwxc": "0711 root:root", "etc/mmwxc/flvx-core": "0750 root:mmwxc-flvx", "etc/mmwxc/flvx-core/config.json": "0640 root:mmwxc-flvx"} {
		if got[path] != want {
			t.Fatalf("umask 077: %s = %s, want %s", path, got[path], want)
		}
	}
	e.traffic("umask077_first_try")

	// 6. 「升级 FLVX Core」 as on 「Boil Hinet 马年」 / 「Breadcloud US 年付」:
	// 8f5b614 → cfb0f59 through the real download, pre-flight, stop and start.
	if old := os.Getenv("MMWXC_TEST_FLVX_CORE_OLD"); old != "" {
		e.upgrade(old)
	}

	e.evidence["log_lines"] = e.changes
	e.evidence["finished_at"] = time.Now().UTC().Format(time.RFC3339)
	raw, _ = json.MarshalIndent(e.evidence, "", "  ")
	if path := os.Getenv("MMWXC_PERMS_EVIDENCE"); path != "" {
		if err = os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("evidence:\n%s", raw)
}

func (e *permsE2E) newManager() {
	t := e.t
	socket := "/run/mmwxc-flvx-e2e-" + strconv.Itoa(os.Getpid()) + "/core-control.sock"
	m := &flvxManager{dir: filepath.Join(e.root, "state"), binary: filepath.Join(e.root, "opt/mmwxc/flvx-core/mmwxc-flvx-core"), configPath: filepath.Join(e.root, "etc/mmwxc/flvx-core/config.json"),
		socket: socket, unitPath: e.unit, service: e.service, logs: newPeriodicLogLimiter()}
	m.core = newCoreClient(socket)
	m.lifecycle = newLifecycleManager(m.core)
	m.apiCall = m.api
	m.setup = m.ensureService
	m.stop = m.stopService
	m.cleanup = m.removeInstance
	m.readOfficial = func() ([]int, error) { return []int{}, nil }
	fixer := newFLVXAccessFixer(e.account)
	fixer.logf = func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		e.changes = append(e.changes, line)
		t.Log(line)
	}
	m.accessFixer = fixer
	node := helperFLVXNode("e2e", 30001)
	m.state = flvxLocalState{Payload: flvx.Payload{ServerID: "10", Version: 1, Status: "active", Config: flvx.Config{PortMin: 30000, PortMax: 30999, ResetDay: 1, MemoryMB: 128, CPUPercent: 50, MaxConnections: 500},
		Desired:  flvx.Desired{Revision: 1, Nodes: []flvx.Node{node}},
		Artifact: flvx.Artifact{URL: "https://github.com/ImoLR/mmwx-custom/releases/download/v1.4.72/mmwxc-core-linux-amd64", SHA256: e.hash, Version: "cfb0f59"}},
		APIPort: flvxFreePort(t), Counters: map[string]*flvx.Counter{}, Applied: map[string]string{}, Report: flvx.Report{Generation: "perms-e2e"}}
	e.m = m
}

// reconcileStarts requires one reconcile to start the unit and serve the node.
func (e *permsE2E) reconcileStarts(step string) {
	t := e.t
	started := time.Now()
	if err := e.m.reconcile(context.Background()); err != nil {
		t.Fatalf("%s: first reconcile failed: %v (%s)", step, err, e.journal())
	}
	show := e.show()
	if show["ActiveState"] != "active" || show["NRestarts"] != "0" {
		t.Fatalf("%s: unit not cleanly started: %v", step, show)
	}
	status, _ := os.ReadFile("/proc/" + show["MainPID"] + "/status")
	uid := ""
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			uid = strings.Fields(line)[1]
		}
	}
	if uid != strconv.FormatUint(uint64(e.account.uid), 10) {
		t.Fatalf("%s: Core runs as uid %s", step, uid)
	}
	if n := e.m.state.Report.Nodes; len(n) != 1 || n[0].State != "active" || e.m.state.Report.Core.State != "running" || e.m.state.Report.Core.Error != "" {
		t.Fatalf("%s: report %+v", step, e.m.state.Report.Status)
	}
	e.evidence[step] = map[string]any{"reconcile_ms": time.Since(started).Milliseconds(), "show": show, "core_uid": uid, "modes": e.modes(), "node": e.m.state.Report.Nodes[0].State}
	t.Logf("%s: started on the first reconcile in %s, NRestarts=%s, uid %s, node %s", step, time.Since(started).Round(time.Millisecond), show["NRestarts"], uid, e.m.state.Report.Nodes[0].State)
}

// traffic sends 64 KiB through mihomo → SS2022 node → echo and back.
func (e *permsE2E) traffic(step string) {
	t := e.t
	n := e.m.state.Payload.Desired.Nodes[0]
	port := flvxFreePort(t)
	dir := t.TempDir()
	config := map[string]any{"mixed-port": port, "allow-lan": false, "bind-address": "127.0.0.1", "mode": "rule", "log-level": "warning", "ipv6": false, "dns": map[string]any{"enable": false},
		"proxies": []any{map[string]any{"name": "ss", "type": "ss", "server": "127.0.0.1", "port": n.Port, "cipher": n.SS2022.Method, "password": n.SS2022.ServerKey + ":" + n.SS2022.UserKey, "udp": true}},
		"rules":   []string{"MATCH,ss"}}
	raw, _ := json.Marshal(config)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(e.mihomo, "-d", dir, "-f", path)
	logs := &bytes.Buffer{}
	command.Stdout, command.Stderr = logs, logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { command.Process.Kill(); command.Wait() }()
	flvxWaitFor(t, 10*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
		if err == nil {
			c.Close()
		}
		return err == nil
	})
	c := flvxSOCKS(t, port, lpEchoTCP)
	defer c.Close()
	payload := bytes.Repeat([]byte("mmwxc-flvx parent perms "), 64<<10/24)
	c.SetDeadline(time.Now().Add(10 * time.Second))
	go c.Write(payload)
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(c, echo); err != nil || !bytes.Equal(echo, payload) {
		t.Fatalf("%s: SS2022 echo failed: %v\n%s", step, err, logs)
	}
	e.evidence[step+"_traffic"] = fmt.Sprintf("SS2022 node :%d echoed %d bytes", n.Port, len(payload))
	t.Logf("%s: SS2022 node :%d echoed %d bytes", step, n.Port, len(payload))
}

func (e *permsE2E) ensureAccount() {
	t := e.t
	u, err := user.Lookup("mmwxc-flvx")
	if err != nil {
		e.sh("useradd", "--system", "--user-group", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", "mmwxc-flvx")
		e.createdUser = true
		if u, err = user.Lookup("mmwxc-flvx"); err != nil {
			t.Fatal(err)
		}
	}
	if e.account, err = flvxAccountOf(u); err != nil {
		t.Fatal(err)
	}
}

func (e *permsE2E) writeUnit() {
	t := e.t
	if err := writeFLVXFile(e.unit, []byte(e.m.serviceUnit()), 0644); err != nil {
		t.Fatal(err)
	}
	e.writeDropIn()
	e.systemctl("daemon-reload")
}

// writeDropIn keeps the unit in the test namespace (the Helper writes no
// drop-ins of its own; setup reloads systemd when it writes the unit).
func (e *permsE2E) writeDropIn() {
	dropIn := "[Service]\nNetworkNamespacePath=/run/netns/" + os.Getenv("MMWXC_PERMS_NETNS") + "\n"
	if err := writeFLVXFile(filepath.Join(e.unit+".d", "10-e2e-netns.conf"), []byte(dropIn), 0644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *permsE2E) cleanup() {
	exec.Command("chattr", "-i", filepath.Join(e.root, "opt/mmwxc")).Run()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	systemctl(ctx, "disable", "--now", e.service)
	os.Remove(e.unit)
	os.Remove(e.unit + ".bak")
	os.RemoveAll(e.unit + ".d")
	systemctl(ctx, "daemon-reload")
	systemctl(ctx, "reset-failed", e.service)
	os.RemoveAll(e.root)
	os.RemoveAll("/run/mmwxc-flvx-e2e-" + strconv.Itoa(os.Getpid()))
	if e.createdUser {
		exec.Command("userdel", "mmwxc-flvx").Run()
	}
}

func (e *permsE2E) mkdir(dir string, mode os.FileMode) {
	path := filepath.Join(e.root, dir)
	if err := os.MkdirAll(path, 0755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		e.t.Fatal(err)
	}
}

func (e *permsE2E) modes() map[string]string {
	result := map[string]string{}
	for _, path := range []string{"opt", "opt/mmwxc", "opt/mmwxc/core", "opt/mmwxc/flvx-core", "opt/mmwxc/flvx-core/mmwxc-flvx-core", "etc", "etc/mmwxc", "etc/mmwxc/core", "etc/mmwxc/flvx-core", "etc/mmwxc/flvx-core/config.json"} {
		var st syscall.Stat_t
		if syscall.Lstat(filepath.Join(e.root, path), &st) == nil {
			result[path] = fmt.Sprintf("%04o %s", st.Mode&07777, flvxOwnerName(st.Uid, st.Gid))
		}
	}
	return result
}

func (e *permsE2E) show() map[string]string {
	out, _ := exec.Command("systemctl", "show", e.service, "-p", "ActiveState", "-p", "SubState", "-p", "Result", "-p", "NRestarts", "-p", "ExecMainStatus", "-p", "ExecMainCode", "-p", "MainPID", "-p", "ExecMainStartTimestampMonotonic").Output()
	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			result[key] = value
		}
	}
	return result
}

func (e *permsE2E) journal() string {
	out, _ := exec.Command("journalctl", "-u", e.service, "-o", "cat", "-n", "3", "--no-pager").Output()
	return strings.TrimSpace(string(out))
}

func (e *permsE2E) systemctl(args ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := systemctl(ctx, args...); err != nil {
		e.t.Fatalf("systemctl %v: %v", args, err)
	}
}

func (e *permsE2E) sh(args ...string) {
	if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		e.t.Fatalf("%v: %v %s", args, err, out)
	}
}

// flvxDenyACL is user::rwx user:<uid>:--- group::--x mask::--x other::--x;
// the mode reads 0711, yet the kernel refuses that one account.
func flvxDenyACL(uid uint32) []byte {
	acl := []byte{2, 0, 0, 0}
	for _, entry := range [][3]uint32{{0x01, 7, 0xffffffff}, {0x02, 0, uid}, {0x04, 1, 0xffffffff}, {0x10, 1, 0xffffffff}, {0x20, 1, 0xffffffff}} {
		acl = append(acl, byte(entry[0]), byte(entry[0]>>8), byte(entry[1]), byte(entry[1]>>8), byte(entry[2]), byte(entry[2]>>8), byte(entry[2]>>16), byte(entry[2]>>24))
	}
	return acl
}

func flvxWaitFor(t *testing.T, limit time.Duration, fn func() bool) {
	t.Helper()
	until := time.Now().Add(limit)
	for time.Now().Before(until) {
		if fn() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not reached in", limit)
}

func (e *permsE2E) upgrade(old string) {
	t := e.t
	oldHash, err := flvxFileHash(old)
	if err != nil {
		t.Fatal(err)
	}
	e.systemctl("stop", e.service)
	if _, err = e.m.installBinary(context.Background(), old, oldHash); err != nil {
		t.Fatal(err)
	}
	release := "https://github.com/ImoLR/mmwx-custom/releases/download/"
	e.m.state.Payload.Artifact = flvx.Artifact{URL: release + "v1.4.71/mmwxc-core-linux-amd64", SHA256: oldHash, Version: "8f5b614"}
	e.m.state.Payload.Version++
	e.reconcileStarts("old_core_8f5b614")
	before := e.show()
	if len(e.m.state.Report.Protocols) != 0 {
		t.Fatalf("8f5b614 must not advertise the extra protocols: %v", e.m.state.Report.Protocols)
	}
	var stoppedAt time.Time
	e.m.stop = func(ctx context.Context) error { stoppedAt = time.Now(); return e.m.stopService(ctx) }
	// The namespace has no Internet: the real downloader (staging, checksum)
	// reads the release asset from the local copy.
	for _, dir := range []string{filepath.Dir(stagingRoot), stagingRoot} {
		if _, err = os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			t.Cleanup(func() { os.RemoveAll(dir) }) // only what this run creates
			break
		}
	}
	e.m.lifecycle.httpClient = &http.Client{Transport: flvxFileTransport{release + "v1.4.72/mmwxc-core-linux-amd64": e.coreBin}}
	e.m.state.Payload.Artifact = flvx.Artifact{URL: release + "v1.4.72/mmwxc-core-linux-amd64", SHA256: e.hash, Version: "cfb0f59"}
	e.m.state.Payload.Version++
	e.reconcileStarts("upgraded_core_cfb0f59")
	outage := time.Since(stoppedAt)
	after := e.show()
	if stoppedAt.IsZero() || after["MainPID"] == before["MainPID"] {
		t.Fatalf("the Core was not replaced: %v -> %v", before, after)
	}
	if hash, _ := flvxFileHash(e.m.binary); hash != e.hash || len(e.m.state.Report.Protocols) == 0 {
		t.Fatalf("upgrade incomplete: hash %s, protocols %v", hash, e.m.state.Report.Protocols)
	}
	e.traffic("upgraded_core_cfb0f59")
	e.evidence["upgrade"] = map[string]any{"pid_before": before["MainPID"], "pid_after": after["MainPID"], "stop_to_node_active_ms": outage.Milliseconds(), "protocols_after": e.m.state.Report.Protocols}
	t.Logf("upgrade 8f5b614 -> cfb0f59: PID %s -> %s, stop -> node active %s, protocols %v", before["MainPID"], after["MainPID"], outage.Round(time.Millisecond), e.m.state.Report.Protocols)
}

type flvxFileTransport map[string]string

func (f flvxFileTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	path, ok := f[r.URL.String()]
	if !ok {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, _ := file.Stat()
	return &http.Response{StatusCode: 200, Body: file, ContentLength: info.Size(), Header: http.Header{}, Request: r}, nil
}
