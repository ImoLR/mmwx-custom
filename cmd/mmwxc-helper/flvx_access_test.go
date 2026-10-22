package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

const flvxTestUID, flvxTestGID = 4242, 4243

var flvxTestAccount = flvxAccount{uid: flvxTestUID, gid: flvxTestGID, groups: map[uint32]bool{flvxTestGID: true}}

// flvxAccessTree lays out the Dmit T1 state of 10-22 under a temp root:
// /opt/mmwxc and /etc/mmwxc 0700 root (left by Custom's own Core) holding a
// 0700 core sibling and the FLVX Core's own subdirectories.
type flvxAccessTree struct {
	root string
	m    *flvxManager
	logs []string
}

func newFLVXAccessTree(t *testing.T) *flvxAccessTree {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root for owners")
	}
	root := t.TempDir()
	// t.TempDir creates 0700 directories; open them like / is on a host.
	for _, dir := range []string{filepath.Dir(root), root} {
		if err := os.Chmod(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	tree := &flvxAccessTree{root: root}
	for _, dir := range []string{"opt/mmwxc/core", "opt/mmwxc/flvx-core", "etc/mmwxc/core", "etc/mmwxc/flvx-core", "run"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"opt/mmwxc/flvx-core/mmwxc-flvx-core", "etc/mmwxc/flvx-core/config.json"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tree.chmod(t, "opt/mmwxc", 0700)
	tree.chmod(t, "opt/mmwxc/core", 0700)
	tree.chmod(t, "etc/mmwxc", 0700)
	tree.chmod(t, "etc/mmwxc/core", 0700)
	tree.chmod(t, "opt/mmwxc/flvx-core/mmwxc-flvx-core", 0755)
	tree.chmod(t, "etc/mmwxc/flvx-core", 0750)
	tree.chmod(t, "etc/mmwxc/flvx-core/config.json", 0640)
	for _, path := range []string{"etc/mmwxc/flvx-core", "etc/mmwxc/flvx-core/config.json"} {
		if err := os.Chown(filepath.Join(root, path), 0, flvxTestGID); err != nil {
			t.Fatal(err)
		}
	}
	tree.m = &flvxManager{binary: filepath.Join(root, "opt/mmwxc/flvx-core/mmwxc-flvx-core"), configPath: filepath.Join(root, "etc/mmwxc/flvx-core/config.json"), socket: filepath.Join(root, "run/mmwxc-flvx/core-control.sock")}
	fixer := newFLVXAccessFixer(flvxTestAccount)
	fixer.logf = func(format string, args ...any) { tree.logs = append(tree.logs, fmt.Sprintf(format, args...)) }
	tree.m.accessFixer = fixer
	return tree
}

func (tree *flvxAccessTree) chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(filepath.Join(tree.root, path), mode); err != nil {
		t.Fatal(err)
	}
}

func (tree *flvxAccessTree) expect(t *testing.T, path string, mode uint32, uid, gid uint32) {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Lstat(filepath.Join(tree.root, path), &st); err != nil {
		t.Fatal(err)
	}
	if st.Mode&07777 != mode || st.Uid != uid || st.Gid != gid {
		t.Fatalf("%s = %04o %d:%d, want %04o %d:%d", path, st.Mode&07777, st.Uid, st.Gid, mode, uid, gid)
	}
}

func TestFLVXAccessOpensOnlyBlockingParentsUnit(t *testing.T) {
	tree := newFLVXAccessTree(t)
	if err := tree.m.ensureAccess(flvxTestAccount); err != nil {
		t.Fatal(err)
	}
	tree.expect(t, "opt/mmwxc", 0711, 0, 0)
	tree.expect(t, "etc/mmwxc", 0711, 0, 0)
	// Siblings, the account's own directories and files keep their modes.
	tree.expect(t, "opt/mmwxc/core", 0700, 0, 0)
	tree.expect(t, "etc/mmwxc/core", 0700, 0, 0)
	tree.expect(t, "opt/mmwxc/flvx-core", 0755, 0, 0)
	tree.expect(t, "opt/mmwxc/flvx-core/mmwxc-flvx-core", 0755, 0, 0)
	tree.expect(t, "etc/mmwxc/flvx-core", 0750, 0, flvxTestGID)
	tree.expect(t, "etc/mmwxc/flvx-core/config.json", 0640, 0, flvxTestGID)
	if len(tree.logs) != 2 || !strings.Contains(tree.logs[0], "/opt/mmwxc 0700 -> 0711") || !strings.Contains(tree.logs[1], "/etc/mmwxc 0700 -> 0711") {
		t.Fatalf("one log line per change expected: %q", tree.logs)
	}
	// Every later reconcile is quiet and changes nothing.
	tree.m.accessFixer.chmod = func(path string, mode uint32) error { t.Fatalf("unexpected chmod %s", path); return nil }
	for i := 0; i < 3; i++ {
		if err := tree.m.ensureAccess(flvxTestAccount); err != nil {
			t.Fatal(err)
		}
	}
	if len(tree.logs) != 2 {
		t.Fatalf("repeated reconciles logged again: %q", tree.logs)
	}
}

func TestFLVXAccessAlreadyOpenIsUntouchedUnit(t *testing.T) {
	tree := newFLVXAccessTree(t)
	tree.chmod(t, "opt/mmwxc", 0755)
	tree.chmod(t, "etc/mmwxc", 0711) // Dmit T1 after the 10-22 hand fix
	tree.m.accessFixer.chmod = func(path string, mode uint32) error { t.Fatalf("unexpected chmod %s", path); return nil }
	tree.m.accessFixer.chown = func(path string, uid, gid int) error { t.Fatalf("unexpected chown %s", path); return nil }
	if err := tree.m.ensureAccess(flvxTestAccount); err != nil {
		t.Fatal(err)
	}
	tree.expect(t, "opt/mmwxc", 0755, 0, 0)
	tree.expect(t, "etc/mmwxc", 0711, 0, 0)
	if len(tree.logs) != 0 {
		t.Fatalf("no change expected: %q", tree.logs)
	}
}

func TestFLVXAccessForeignOwnerKeepsOwnerUnit(t *testing.T) {
	tree := newFLVXAccessTree(t)
	for _, path := range []string{"opt/mmwxc", "etc/mmwxc"} {
		if err := os.Chown(filepath.Join(tree.root, path), 5000, 5001); err != nil {
			t.Fatal(err)
		}
	}
	tree.chmod(t, "opt/mmwxc", 0750)
	if err := tree.m.ensureAccess(flvxTestAccount); err != nil {
		t.Fatal(err)
	}
	tree.expect(t, "opt/mmwxc", 0751, 5000, 5001)
	tree.expect(t, "etc/mmwxc", 0711, 5000, 5001)
}

func TestFLVXAccessGroupClassUnit(t *testing.T) {
	a := flvxTestAccount
	cases := []struct {
		uid, gid, mode uint32
		want           bool
	}{
		{0, 0, 0700, false},
		{0, 0, 0711, true},
		{0, 0, 0701, true},
		{flvxTestUID, 0, 0601, false}, // owner class applies, not other
		{0, flvxTestGID, 0705, false}, // group class applies, not other
		{0, flvxTestGID, 0710, true},
		{0, 9, 0710, false},
	}
	for _, c := range cases {
		if got := a.permits(c.uid, c.gid, c.mode, 1); got != c.want {
			t.Errorf("permits(%d:%d %04o) = %t", c.uid, c.gid, c.mode, got)
		}
	}
	tree := newFLVXAccessTree(t)
	// A parent in the account's group without group x: other x would not help.
	if err := os.Chown(filepath.Join(tree.root, "opt/mmwxc"), 0, flvxTestGID); err != nil {
		t.Fatal(err)
	}
	tree.chmod(t, "opt/mmwxc", 0745)
	if err := tree.m.ensureAccess(flvxTestAccount); err != nil {
		t.Fatal(err)
	}
	tree.expect(t, "opt/mmwxc", 0755, 0, flvxTestGID)
}

// umask 077 on the host (or the Helper) turns every MkdirAll/copyFile of the
// FLVX install into 0700 / 0600; setup restores the intended modes.
func TestFLVXAccessUmask077Unit(t *testing.T) {
	tree := newFLVXAccessTree(t)
	for _, dir := range []string{"opt", "etc"} {
		if err := os.RemoveAll(filepath.Join(tree.root, dir)); err != nil {
			t.Fatal(err)
		}
	}
	old := syscall.Umask(0077)
	source := filepath.Join(tree.root, "source")
	err := os.WriteFile(source, []byte("x"), 0755)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(tree.m.binary), 0755)
	}
	if err == nil {
		err = copyFile(source, tree.m.binary, 0755)
	}
	if err == nil {
		err = writeFLVXFile(tree.m.configPath, []byte("{}"), 0640)
	}
	syscall.Umask(old)
	if err != nil {
		t.Fatal(err)
	}
	tree.expect(t, "opt/mmwxc", 0700, 0, 0)
	tree.expect(t, "opt/mmwxc/flvx-core/mmwxc-flvx-core", 0700, 0, 0)
	if err = tree.m.ensureAccess(flvxTestAccount); err != nil {
		t.Fatal(err)
	}
	tree.expect(t, "opt", 0711, 0, 0)
	tree.expect(t, "opt/mmwxc", 0711, 0, 0)
	tree.expect(t, "opt/mmwxc/flvx-core", 0755, 0, 0)
	tree.expect(t, "opt/mmwxc/flvx-core/mmwxc-flvx-core", 0755, 0, 0)
	tree.expect(t, "etc/mmwxc", 0711, 0, 0)
	tree.expect(t, "etc/mmwxc/flvx-core", 0750, 0, flvxTestGID)
	tree.expect(t, "etc/mmwxc/flvx-core/config.json", 0640, 0, flvxTestGID)
}

func TestFLVXAccessSymlinkedParentUnit(t *testing.T) {
	tree := newFLVXAccessTree(t)
	data := filepath.Join(tree.root, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(tree.root, "opt"), filepath.Join(data, "opt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(data, "opt"), filepath.Join(tree.root, "opt")); err != nil {
		t.Fatal(err)
	}
	if err := tree.m.ensureAccess(flvxTestAccount); err != nil {
		t.Fatal(err)
	}
	tree.expect(t, "data", 0711, 0, 0)
	tree.expect(t, "data/opt/mmwxc", 0711, 0, 0)
	tree.expect(t, "data/opt/mmwxc/core", 0700, 0, 0)
}

func TestFLVXAccessReportsWithoutStrippingUnit(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*flvxAccessFixer)
		want  string
	}{
		{"acl", func(f *flvxAccessFixer) {
			f.hasACL = func(path string) bool { return strings.HasSuffix(path, "/opt/mmwxc") }
		}, "带 ACL"},
		{"immutable", func(f *flvxAccessFixer) {
			f.chmod = func(string, uint32) error { return syscall.EPERM }
			f.immutable = func(string) bool { return true }
		}, "immutable"},
		{"read-only", func(f *flvxAccessFixer) { f.chmod = func(string, uint32) error { return syscall.EROFS } }, "只读"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tree := newFLVXAccessTree(t)
			chmods := 0
			tree.m.accessFixer.chmod = func(path string, mode uint32) error { chmods++; return syscall.Chmod(path, mode) }
			c.setup(tree.m.accessFixer)
			err := tree.m.ensureAccess(flvxTestAccount)
			var access *flvxAccessError
			if !errors.As(err, &access) {
				t.Fatalf("access error expected, got %v", err)
			}
			text := err.Error()
			if !strings.Contains(text, filepath.Join(tree.root, "opt/mmwxc")+"（0700 root:root）") || !strings.Contains(text, c.want) {
				t.Fatalf("error must name the path, mode and cause: %s", text)
			}
			if c.name == "acl" && chmods != 0 {
				t.Fatal("ACL directory was changed")
			}
			tree.expect(t, "opt/mmwxc", 0700, 0, 0)
		})
	}
}

func TestFLVXAccessOwnedDirectoryOfAccountUnit(t *testing.T) {
	tree := newFLVXAccessTree(t)
	if err := os.Chown(filepath.Join(tree.root, "opt/mmwxc"), flvxTestUID, 0); err != nil {
		t.Fatal(err)
	}
	tree.chmod(t, "opt/mmwxc", 0601)
	err := tree.m.ensureAccess(flvxTestAccount)
	if err == nil || !strings.Contains(err.Error(), "0601") || !strings.Contains(err.Error(), "属主是 mmwxc-flvx") {
		t.Fatalf("owner-without-x must be reported: %v", err)
	}
	tree.expect(t, "opt/mmwxc", 0601, flvxTestUID, 0)
}

func TestFLVXAccessRepairsOwnFilesUnit(t *testing.T) {
	tree := newFLVXAccessTree(t)
	tree.chmod(t, "opt/mmwxc/flvx-core", 0700)
	tree.chmod(t, "opt/mmwxc/flvx-core/mmwxc-flvx-core", 0700)
	tree.chmod(t, "etc/mmwxc/flvx-core/config.json", 0600)
	if err := os.Chown(tree.m.binary, 5000, 5000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(tree.m.configPath, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := tree.m.ensureAccess(flvxTestAccount); err != nil {
		t.Fatal(err)
	}
	tree.expect(t, "opt/mmwxc/flvx-core", 0755, 0, 0)
	tree.expect(t, "opt/mmwxc/flvx-core/mmwxc-flvx-core", 0755, 0, 0)
	tree.expect(t, "etc/mmwxc/flvx-core/config.json", 0640, 0, flvxTestGID)
	// A symlink in place of the binary is never followed.
	if err := os.Remove(tree.m.binary); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/sh", tree.m.binary); err != nil {
		t.Fatal(err)
	}
	if err := tree.m.ensureAccess(flvxTestAccount); err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("symlinked binary accepted: %v", err)
	}
}

func TestFLVXAccessNoexecUnit(t *testing.T) {
	tree := newFLVXAccessTree(t)
	tree.m.accessFixer.noexec = func(path string) bool { return path == filepath.Dir(tree.m.binary) }
	if err := tree.m.ensureAccess(flvxTestAccount); err == nil || !strings.Contains(err.Error(), "noexec") {
		t.Fatalf("noexec mount must be reported: %v", err)
	}
}

// A failed start is reported on the share card (operator only); FLVX keeps
// seeing the generic node error.
func TestFLVXFailureReportsCoreErrorUnit(t *testing.T) {
	m, _ := fakeFLVXManager(t)
	stopped := 0
	m.stop = func(context.Context) error { stopped++; return nil }
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	access := &flvxAccessError{path: "/opt/mmwxc", detail: "目录带 ACL，Helper 不改动", mode: syscall.S_IFDIR | 0700, stat: true}
	m.reportFailure(context.Background(), access)
	if stopped != 1 || m.state.Report.Core.State != "error" || m.state.Report.Core.Error != "FLVX Core 账户 mmwxc-flvx 无法访问 /opt/mmwxc（0700 root:root）：目录带 ACL，Helper 不改动" {
		t.Fatalf("core = %+v, stopped %d", m.state.Report.Core, stopped)
	}
	if n := m.state.Report.Nodes[0]; n.State != "error" || n.Error != "FLVX Core reconciliation failed" {
		t.Fatalf("node = %+v", n)
	}
	if r := m.snapshotReport(); r == nil || r.Core.Error == "" {
		t.Fatal("core error not published to Custom")
	}
	// The next successful reconcile clears it.
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.state.Report.Core.State != "running" || m.state.Report.Core.Error != "" {
		t.Fatalf("core = %+v", m.state.Report.Core)
	}
	long := flvxCoreErrorText(errors.New(strings.Repeat("长", 400)))
	if len([]rune(long)) != 300 {
		t.Fatalf("error text not bounded: %d", len([]rune(long)))
	}
}

func TestFLVXServiceUnitUnchangedUnit(t *testing.T) {
	// The v0.7.11 unit, byte for byte: an upgraded Helper must not rewrite it.
	want := `[Unit]
Description=MMWXC FLVX shared Core
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=mmwxc-flvx
Group=mmwxc-flvx
Environment=MMWXC_CORE_CONTROL_SOCKET=/run/mmwxc-flvx/core-control.sock
ExecStart=/opt/mmwxc/flvx-core/mmwxc-flvx-core run -config /etc/mmwxc/flvx-core/config.json
RuntimeDirectory=mmwxc-flvx
RuntimeDirectoryMode=0700
UMask=0077
MemoryMax=128M
CPUQuota=50%
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=/run/mmwxc-flvx
CapabilityBoundingSet=
RestrictSUIDSGID=yes
LimitNOFILE=65535
LogRateLimitIntervalSec=60s
LogRateLimitBurst=10
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`
	m := &flvxManager{binary: flvxBinaryPath, configPath: flvxConfigPath, socket: flvxSocketPath}
	m.state.Payload.Config = flvx.Config{MemoryMB: 128, CPUPercent: 50}
	if got := m.serviceUnit(); got != want {
		t.Fatalf("unit changed:\n%s", got)
	}
}

// The real kernel paths behind hasACL / immutable (tmpfs or ext4).
func TestFLVXAccessRealACLAndImmutableUnit(t *testing.T) {
	tree := newFLVXAccessTree(t)
	dir := filepath.Join(tree.root, "opt/mmwxc")
	// version 2; USER_OBJ rwx, USER 4242 ---, GROUP_OBJ ---, MASK ---, OTHER ---
	acl := []byte{2, 0, 0, 0}
	for _, e := range [][3]uint32{{0x01, 7, 0xffffffff}, {0x02, 0, flvxTestUID}, {0x04, 0, 0xffffffff}, {0x10, 0, 0xffffffff}, {0x20, 0, 0xffffffff}} {
		acl = append(acl, byte(e[0]), byte(e[0]>>8), byte(e[1]), byte(e[1]>>8), byte(e[2]), byte(e[2]>>8), byte(e[2]>>16), byte(e[2]>>24))
	}
	if err := syscall.Setxattr(dir, "system.posix_acl_access", acl, 0); err != nil {
		t.Skipf("ACLs unsupported here: %v", err)
	}
	if !flvxHasACL(dir) {
		t.Fatal("ACL not detected")
	}
	if err := tree.m.ensureAccess(flvxTestAccount); err == nil || !strings.Contains(err.Error(), "带 ACL") {
		t.Fatalf("ACL directory must be reported: %v", err)
	}
	if err := syscall.Removexattr(dir, "system.posix_acl_access"); err != nil {
		t.Fatal(err)
	}
	tree.chmod(t, "opt/mmwxc", 0700)
	if out, err := exec.Command("chattr", "+i", dir).CombinedOutput(); err != nil {
		t.Skipf("chattr unavailable: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command("chattr", "-i", dir).Run() })
	if !flvxImmutable(dir) {
		t.Fatal("immutable attribute not detected")
	}
	if err := tree.m.ensureAccess(flvxTestAccount); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("immutable directory must be reported: %v", err)
	}
	tree.expect(t, "opt/mmwxc", 0700, 0, 0)
	if err := exec.Command("chattr", "-i", dir).Run(); err != nil {
		t.Fatal(err)
	}
	if err := tree.m.ensureAccess(flvxTestAccount); err != nil {
		t.Fatal(err)
	}
	tree.expect(t, "opt/mmwxc", 0711, 0, 0)
}
