package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestFLVXCoreUnlimitedNetnsE2E: 共享资源上限 「不限制」 with real systemd and
// the real FLVX Core. A running Core limited at 128 MB / 50 % (plus an
// operator's runtime 4096 MB / 400 %) goes to memory_mb / cpu_percent 0: no
// MemoryMax / CPUQuota in the unit, infinity on the running Core, same PID,
// still serving; limits come back the same way. (permsE2E.traffic's SS2022
// echo times out on the dev box even at v1.4.86, so serving checks the
// running Core instead; real traffic is checked on the servers.) The unit joins an isolated
// network namespace, so nothing listens on the host.
func TestFLVXCoreUnlimitedNetnsE2E(t *testing.T) {
	core, mihomo := os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED"), os.Getenv("MMWXC_TEST_MIHOMO")
	if core == "" || mihomo == "" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_TEST_FLVX_CORE_FIXED, MMWXC_TEST_MIHOMO and run as root")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Skip("needs systemd as PID 1")
	}
	name := "cul" + strconv.Itoa(os.Getpid()%100000)
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
	evidence := os.Getenv("MMWXC_TEST_UNLIMITED_EVIDENCE")
	if evidence == "" {
		evidence = filepath.Join(t.TempDir(), "evidence.json")
	}
	command := exec.Command("ip", "netns", "exec", name, os.Args[0], "-test.run", "^TestFLVXCoreUnlimitedNetnsInner$", "-test.v", "-test.timeout", "10m")
	command.Env = append(os.Environ(), "MMWXC_UNLIMITED_INNER=1", "MMWXC_PERMS_NETNS="+name, "MMWXC_PERMS_EVIDENCE="+evidence)
	out, err := command.CombinedOutput()
	t.Logf("inner run:\n%s", out)
	if err != nil {
		t.Fatalf("inner E2E failed: %v", err)
	}
}

func TestFLVXCoreUnlimitedNetnsInner(t *testing.T) {
	if os.Getenv("MMWXC_UNLIMITED_INNER") != "1" {
		t.Skip("runs only inside the E2E namespace")
	}
	ctx := context.Background()
	id := strconv.Itoa(os.Getpid())
	e := &permsE2E{t: t, coreBin: os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED"), mihomo: os.Getenv("MMWXC_TEST_MIHOMO"), evidence: map[string]any{"started_at": time.Now().UTC().Format(time.RFC3339)},
		root: "/opt/mmwxc-e2e-" + id, service: "mmwxc-flvx-core-e2e-" + id + ".service"}
	e.unit = "/etc/systemd/system/" + e.service
	var err error
	if e.hash, err = flvxFileHash(e.coreBin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.cleanup)
	t.Cleanup(func() { os.RemoveAll("/run/systemd/system.control/" + e.service + ".d") })
	lpStartFixtures(t)
	e.mkdir("", 0755)
	e.ensureAccount()
	e.newManager()
	e.writeDropIn()
	if _, err = e.m.installBinary(ctx, e.coreBin, e.hash); err != nil {
		t.Fatal(err)
	}

	// 1. Today's share: 128 MB / 50 %, then the operator's runtime 4096 / 400.
	e.reconcileStarts("limited")
	e.serving("limited")
	want := func(step, memory, cpu string) map[string]string {
		t.Helper()
		show := e.limits()
		if show["MemoryMax"] != memory || show["CPUQuotaPerSecUSec"] != cpu {
			t.Fatalf("%s: limits %v, want MemoryMax=%s CPUQuotaPerSecUSec=%s", step, show, memory, cpu)
		}
		return show
	}
	first := want("limited", "134217728", "500ms")
	e.systemctl("set-property", "--runtime", e.service, "MemoryMax=4096M", "CPUQuota=400%")
	want("operator_runtime", "4294967296", "4s")
	same := func(step string, show map[string]string) {
		t.Helper()
		for _, key := range []string{"MainPID", "ExecMainStartTimestampMonotonic", "NRestarts", "ActiveState"} {
			if show[key] != first[key] {
				t.Fatalf("%s: Core restarted or stopped: %s %s -> %s", step, key, first[key], show[key])
			}
		}
	}

	// 2. 不限制: no lines in the unit, infinity on the running Core, no restart.
	e.m.state.Payload.Config.MemoryMB, e.m.state.Payload.Config.CPUPercent = 0, 0
	if err = e.m.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	unit, _ := os.ReadFile(e.unit)
	if strings.Contains(string(unit), "MemoryMax") || strings.Contains(string(unit), "CPUQuota") {
		t.Fatalf("unlimited unit still limits:\n%s", unit)
	}
	show := want("unlimited", "infinity", "infinity")
	same("unlimited", show)
	cgroup := e.cgroup(show["ControlGroup"])
	if cgroup["memory.max"] != "max" || !strings.HasPrefix(cgroup["cpu.max"], "max ") {
		t.Fatalf("unlimited cgroup: %v", cgroup)
	}
	e.evidence["unlimited"] = map[string]any{"show": show, "cgroup": cgroup}
	e.serving("unlimited")
	if err = e.m.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	same("unlimited_steady", want("unlimited_steady", "infinity", "infinity"))

	// 3. As after a reboot: the runtime drop-ins are gone, the unit alone.
	os.RemoveAll("/run/systemd/system.control/" + e.service + ".d")
	e.systemctl("daemon-reload")
	same("unlimited_unit_only", want("unlimited_unit_only", "infinity", "infinity"))

	// 4. A number again (the admin may still type one), then 不限制 again.
	e.m.state.Payload.Config.MemoryMB, e.m.state.Payload.Config.CPUPercent = 256, 100
	if err = e.m.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	same("limited_again", want("limited_again", "268435456", "1s"))
	e.m.state.Payload.Config.MemoryMB, e.m.state.Payload.Config.CPUPercent = 0, 0
	if err = e.m.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	show = want("unlimited_again", "infinity", "infinity")
	same("unlimited_again", show)
	e.serving("unlimited_again")
	e.evidence["final"] = show

	e.evidence["finished_at"] = time.Now().UTC().Format(time.RFC3339)
	raw, _ := json.MarshalIndent(e.evidence, "", "  ")
	if path := os.Getenv("MMWXC_PERMS_EVIDENCE"); path != "" {
		if err = os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("evidence:\n%s", raw)
}

// serving: the running Core answers on its control socket with the node's
// inbound loaded and accepts TCP on the node port.
func (e *permsE2E) serving(step string) {
	t := e.t
	snapshot, err := e.m.core.snapshot(context.Background())
	if err != nil {
		t.Fatalf("%s: Core snapshot: %v", step, err)
	}
	raw, _ := json.Marshal(snapshot)
	if !strings.Contains(string(raw), `"flvxl-e2e"`) {
		t.Fatalf("%s: node inbound missing: %s", step, raw)
	}
	port := e.m.state.Payload.Desired.Nodes[0].Port
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 2*time.Second)
	if err != nil {
		t.Fatalf("%s: node port %d: %v", step, port, err)
	}
	c.Close()
	e.evidence[step+"_serving"] = map[string]any{"node_port": port, "snapshot_bytes": len(raw)}
	t.Logf("%s: Core serving, node port %d accepts", step, port)
}

func (e *permsE2E) limits() map[string]string {
	out, _ := exec.Command("systemctl", "show", e.service, "-p", "MemoryMax", "-p", "CPUQuotaPerSecUSec", "-p", "MainPID", "-p", "ExecMainStartTimestampMonotonic", "-p", "NRestarts", "-p", "ActiveState", "-p", "ControlGroup").Output()
	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			result[key] = value
		}
	}
	return result
}

func (e *permsE2E) cgroup(path string) map[string]string {
	result := map[string]string{}
	for _, file := range []string{"memory.max", "cpu.max"} {
		// ip netns exec remounts /sys: read the host's cgroup tree.
		raw, _ := exec.Command("nsenter", "-t", "1", "-m", "cat", filepath.Join("/sys/fs/cgroup", path, file)).Output()
		result[file] = strings.TrimSpace(string(raw))
	}
	return result
}
