package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// The FLVX shared Core runs as the unprivileged mmwxc-flvx account, so every
// directory on the way to its binary and config must let that account search
// (x). Hosts that once ran Custom's own Core keep /opt/mmwxc and /etc/mmwxc at
// 0700 root; a fresh lab never has them. Missing search bits are added for
// group and other only (0700 becomes 0711): read/list bits, owners and
// sibling directories (e.g. /opt/mmwxc/core) are never touched.

type flvxAccount struct {
	uid, gid uint32
	groups   map[uint32]bool
}

func flvxAccountOf(u *user.User) (flvxAccount, error) {
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return flvxAccount{}, err
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return flvxAccount{}, err
	}
	a := flvxAccount{uid: uint32(uid), gid: uint32(gid), groups: map[uint32]bool{uint32(gid): true}}
	ids, _ := u.GroupIds()
	for _, id := range ids {
		if v, err := strconv.ParseUint(id, 10, 32); err == nil {
			a.groups[uint32(v)] = true
		}
	}
	return a, nil
}

func (a flvxAccount) groupList() []uint32 {
	list := []uint32{}
	for gid := range a.groups {
		list = append(list, gid)
	}
	return list
}

// permits applies the one permission class the kernel uses for this account
// (owner, else group, else other); bit is 1 for search/exec, 4 for read.
func (a flvxAccount) permits(uid, gid, mode, bit uint32) bool {
	switch {
	case uid == a.uid:
		return mode&(bit<<6) != 0
	case a.groups[gid]:
		return mode&(bit<<3) != 0
	}
	return mode&bit != 0
}

// flvxAccessError names the exact path and mode the FLVX Core account cannot
// use. It is reported on the share card instead of a crash-looping unit.
type flvxAccessError struct {
	path, detail   string
	mode, uid, gid uint32
	stat           bool
}

func (e *flvxAccessError) Error() string {
	where := e.path
	if e.stat {
		where = fmt.Sprintf("%s（%04o %s）", e.path, e.mode&07777, flvxOwnerName(e.uid, e.gid))
	}
	return "FLVX Core 账户 mmwxc-flvx 无法访问 " + where + "：" + e.detail
}

func flvxOwnerName(uid, gid uint32) string {
	owner, group := strconv.FormatUint(uint64(uid), 10), strconv.FormatUint(uint64(gid), 10)
	if u, err := user.LookupId(owner); err == nil {
		owner = u.Username
	}
	if g, err := user.LookupGroupId(group); err == nil {
		group = g.Name
	}
	return owner + ":" + group
}

func flvxStatError(path string, st *syscall.Stat_t, detail string) error {
	return &flvxAccessError{path: path, detail: detail, mode: st.Mode, uid: st.Uid, gid: st.Gid, stat: true}
}

type flvxAccessFixer struct {
	account   flvxAccount
	chmod     func(string, uint32) error
	chown     func(string, int, int) error
	hasACL    func(string) bool
	immutable func(string) bool
	noexec    func(string) bool
	logf      func(string, ...any)
	seen      map[[2]uint64]bool
}

func newFLVXAccessFixer(account flvxAccount) *flvxAccessFixer {
	return &flvxAccessFixer{account: account, chmod: syscall.Chmod, chown: syscall.Lchown, hasACL: flvxHasACL, immutable: flvxImmutable, noexec: flvxNoexec, logf: log.Printf}
}

// flvxAncestors lists / and every directory down to dir.
func flvxAncestors(dir string) []string {
	dir = filepath.Clean(dir)
	list := []string{dir}
	for dir != "/" && dir != "." {
		dir = filepath.Dir(dir)
		list = append([]string{dir}, list...)
	}
	return list
}

// searchable makes / and every directory down to dir searchable by the
// account; a symlinked component is checked along its resolved path too.
func (f *flvxAccessFixer) searchable(dir string) error {
	paths := flvxAncestors(dir)
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != filepath.Clean(dir) {
		paths = append(paths, flvxAncestors(real)...)
	}
	for _, path := range paths {
		if err := f.search(path); err != nil {
			return err
		}
	}
	return nil
}

func (f *flvxAccessFixer) search(path string) error {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return &flvxAccessError{path: path, detail: "无法读取目录信息（" + err.Error() + "）"}
	}
	if f.seen == nil {
		f.seen = map[[2]uint64]bool{}
	}
	key := [2]uint64{uint64(st.Dev), st.Ino}
	if f.seen[key] {
		return nil
	}
	f.seen[key] = true
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return flvxStatError(path, &st, "不是目录")
	}
	if f.account.permits(st.Uid, st.Gid, st.Mode, 1) {
		return nil
	}
	if f.hasACL(path) {
		return flvxStatError(path, &st, "目录带 ACL，Helper 不改动；请手动让 mmwxc-flvx 能进入（x）这个目录")
	}
	next := st.Mode&07777 | 0011
	if !f.account.permits(st.Uid, st.Gid, next, 1) {
		return flvxStatError(path, &st, "目录属主是 mmwxc-flvx 本身但没有 x 权限，Helper 不改动")
	}
	if err := f.chmod(path, next); err != nil {
		return flvxStatError(path, &st, flvxChangeFailure(path, err, f.immutable))
	}
	f.logf("[mmwxc-helper] FLVX Core access: %s %04o -> %04o (search for mmwxc-flvx)", path, st.Mode&07777, next)
	return nil
}

// own enforces owner and mode on a path the Helper creates for the FLVX Core
// (the flvx-core directories, the binary and its config).
func (f *flvxAccessFixer) own(path string, uid, gid int, mode uint32, directory bool) error {
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return &flvxAccessError{path: path, detail: "无法读取文件信息（" + err.Error() + "）"}
	}
	kind := uint32(syscall.S_IFREG)
	if directory {
		kind = syscall.S_IFDIR
	}
	if st.Mode&syscall.S_IFMT != kind {
		return flvxStatError(path, &st, "类型不对（可能是符号链接），Helper 不跟随")
	}
	if st.Uid != uint32(uid) || st.Gid != uint32(gid) {
		if err := f.chown(path, uid, gid); err != nil {
			return flvxStatError(path, &st, flvxChangeFailure(path, err, f.immutable))
		}
		f.logf("[mmwxc-helper] FLVX Core access: %s owner %s -> %s", path, flvxOwnerName(st.Uid, st.Gid), flvxOwnerName(uint32(uid), uint32(gid)))
		// chown clears set-id bits; read the mode again.
		if err := syscall.Lstat(path, &st); err != nil {
			return &flvxAccessError{path: path, detail: "无法读取文件信息（" + err.Error() + "）"}
		}
	}
	if st.Mode&07777 != mode {
		if err := f.chmod(path, mode); err != nil {
			return flvxStatError(path, &st, flvxChangeFailure(path, err, f.immutable))
		}
		f.logf("[mmwxc-helper] FLVX Core access: %s %04o -> %04o", path, st.Mode&07777, mode)
	}
	return nil
}

func flvxChangeFailure(path string, err error, immutable func(string) bool) string {
	switch {
	case immutable(path):
		return "带 immutable / append-only 属性（chattr），Helper 不改动；请手动处理"
	case errors.Is(err, syscall.EROFS):
		return "所在文件系统只读"
	}
	return "修改权限失败（" + err.Error() + "）"
}

// ensureAccess runs on every setup so the account reaches its binary, its
// config and /run (for RuntimeDirectory=) whatever modes an earlier install or
// the host's umask left on the parent directories.
func (m *flvxManager) ensureAccess(account flvxAccount) error {
	f := m.accessFixer
	if f == nil {
		f = newFLVXAccessFixer(account)
	}
	f.account, f.seen = account, nil
	binDir, configDir := filepath.Dir(m.binary), filepath.Dir(m.configPath)
	if err := f.searchable(filepath.Dir(binDir)); err != nil {
		return err
	}
	if err := f.own(binDir, 0, 0, 0755, true); err != nil {
		return err
	}
	if err := f.own(m.binary, 0, 0, 0755, false); err != nil {
		return err
	}
	if f.noexec(binDir) {
		return &flvxAccessError{path: binDir, detail: "所在分区以 noexec 挂载，FLVX Core 无法在这里运行"}
	}
	if err := f.searchable(filepath.Dir(configDir)); err != nil {
		return err
	}
	if err := f.own(configDir, 0, int(account.gid), 0750, true); err != nil {
		return err
	}
	if err := f.own(m.configPath, 0, int(account.gid), 0640, false); err != nil {
		return err
	}
	// systemd creates the socket's RuntimeDirectory= for the account itself.
	return f.searchable(filepath.Dir(filepath.Dir(m.socket)))
}

// preflightCore runs the installed binary with -test exactly as the unit will
// (same account, binary and config), so a path the kernel still refuses (ACL,
// LSM) is reported once instead of crash-looping the unit. The control socket
// variable is not set, so the test run never touches the live socket.
func (m *flvxManager) preflightCore(ctx context.Context, account flvxAccount) error {
	child, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(child, m.binary, "run", "-test", "-config", m.configPath)
	command.Dir = "/"
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: account.uid, Gid: account.gid, Groups: account.groupList()}}
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if index := strings.LastIndex(detail, "\n"); index >= 0 {
		detail = detail[index+1:]
	}
	if detail == "" {
		detail = err.Error()
	}
	if runes := []rune(detail); len(runes) > 200 {
		detail = string(runes[len(runes)-200:])
	}
	path := m.binary
	if strings.Contains(detail, m.configPath) {
		path = m.configPath
	}
	// The mode check passed, so an ACL on the way is the usual culprit.
	for _, dir := range append(flvxAncestors(filepath.Dir(m.binary)), flvxAncestors(filepath.Dir(m.configPath))...) {
		if flvxHasACL(dir) {
			path, detail = dir, detail+"；该目录带 ACL"
			break
		}
	}
	var st syscall.Stat_t
	if syscall.Stat(path, &st) == nil {
		return flvxStatError(path, &st, "以 mmwxc-flvx 身份试运行失败（"+detail+"），已暂停启动")
	}
	return &flvxAccessError{path: path, detail: "以 mmwxc-flvx 身份试运行失败（" + detail + "），已暂停启动"}
}

func flvxHasACL(path string) bool {
	for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		if n, err := syscall.Getxattr(path, name, nil); err == nil && n > 0 {
			return true
		}
	}
	return false
}

// FS_IOC_GETFLAGS = _IOR('f', 1, long); the kernel stores an int.
const (
	flvxGetFlags       = 2<<30 | uintptr(unsafe.Sizeof(int(0)))<<16 | 'f'<<8 | 1
	flvxImmutableFlags = 0x10 | 0x20 // FS_IMMUTABLE_FL | FS_APPEND_FL
)

func flvxImmutable(path string) bool {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer syscall.Close(fd)
	var flags int
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), flvxGetFlags, uintptr(unsafe.Pointer(&flags))); errno != 0 {
		return false
	}
	return int32(flags)&flvxImmutableFlags != 0
}

func flvxNoexec(path string) bool {
	var st syscall.Statfs_t
	return syscall.Statfs(path, &st) == nil && st.Flags&0x8 != 0 // ST_NOEXEC
}
