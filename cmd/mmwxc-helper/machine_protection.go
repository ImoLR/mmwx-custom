package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	machineProtectionTableName = "mmwxc_machine_protection"
	machineProtectionVersion   = 2
	// Contract machine-protect-v1 (FLVX ↔ mmwx): the Helper owns protection on
	// every machine it runs on, publishes its state here and reads FLVX ports.
	machineProtectionStatusPath = "/run/mmwxc/machine-protection.json"
	flvxBusinessPortsPath       = "/run/flvx/business-ports.json"
	tcpMaxTWBucketsPath         = "/proc/sys/net/ipv4/tcp_max_tw_buckets"

	defaultMachineTWBuckets        = int64(500)
	defaultMachineCloseWaitSeconds = int64(10)
	// Blocking starts at total >= cap and ends only once total < cap*95/100, so
	// a machine hovering at the cap does not flap on every sample.
	machineReleasePercent = 95
	// A new table switches conntrack on. A UDP flow that began before it is
	// NEW until its next reply, so the guard waits before it may drop.
	machineConntrackWarmup = 5 * time.Second
	// An idle (not blocking) table has no hook, so it costs nothing per packet;
	// its presence is re-checked with one nft call at this interval only.
	machineIdleRecheck     = time.Minute
	machineBlockingRecheck = 3 * time.Second

	flvxBusinessPortsMaxAge   = 10 * time.Minute
	flvxBusinessPortsMaxBytes = 64 << 10
)

// Machine Active deliberately excludes TIME_WAIT, CLOSE and unknown rows. It
// counts TCP states that still represent a live handshake, data path or close
// handshake: ESTABLISHED, SYN_*, FIN_WAIT*, CLOSE_WAIT, LAST_ACK and CLOSING.
// It only backs the old max_active setting; max_total uses the machine total.
func machineActiveTCP(counts tcpStateCounts) int64 {
	return counts.Established + counts.SynSent + counts.SynRecv + counts.FinWait1 + counts.FinWait2 + counts.CloseWait + counts.LastAck + counts.Closing
}

// machineControlledPorts returns the mmwx Core inbound ports. They are guarded
// for TCP and UDP: a port without a UDP service simply never matches.
func machineControlledPorts(snapshot detailedConnectionSnapshot) []uint32 {
	ports := make([]uint32, 0, len(snapshot.Inbounds))
	for _, inbound := range snapshot.Inbounds {
		ports = append(ports, inbound.Port)
	}
	return normalizeMachinePorts(ports)
}

func normalizeMachinePorts(values []uint32) []uint32 {
	seen := make(map[uint32]struct{}, len(values))
	for _, port := range values {
		if port > 0 && port <= 65535 {
			seen[port] = struct{}{}
		}
	}
	ports := make([]uint32, 0, len(seen))
	for port := range seen {
		ports = append(ports, port)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return ports
}

type machinePorts struct {
	TCP []uint32
	UDP []uint32
}

func (ports machinePorts) normalized() machinePorts {
	return machinePorts{TCP: normalizeMachinePorts(ports.TCP), UDP: normalizeMachinePorts(ports.UDP)}
}

func (ports machinePorts) empty() bool { return len(ports.TCP) == 0 && len(ports.UDP) == 0 }

func validateMachineProtection(settings machineProtectionSettings) error {
	if settings.MaxActive != nil && *settings.MaxActive <= 0 {
		return errors.New("machine max_active must be positive")
	}
	if settings.MaxTotal != nil && *settings.MaxTotal <= 0 {
		return errors.New("machine max_total must be positive")
	}
	if settings.TWBuckets != nil && *settings.TWBuckets < 0 {
		return errors.New("machine tw_buckets must be non-negative")
	}
	if settings.CloseWaitSeconds != nil && *settings.CloseWaitSeconds < 0 {
		return errors.New("machine close_wait_seconds must be non-negative")
	}
	if settings.Enabled && settings.MaxActive == nil && settings.MaxTotal == nil {
		return errors.New("enabled machine protection requires max_active or max_total")
	}
	return nil
}

func optionalMachineValue(enabled bool, value *int64, fallback int64) *int64 {
	if !enabled {
		return nil
	}
	if value == nil {
		return &fallback
	}
	if *value <= 0 {
		return nil
	}
	copied := *value
	return &copied
}

// twTarget is the kernel tcp_max_tw_buckets value protection owns; nil = none.
func (settings machineProtectionSettings) twTarget() *int64 {
	if validateMachineProtection(settings) != nil {
		return nil
	}
	return optionalMachineValue(settings.Enabled, settings.TWBuckets, defaultMachineTWBuckets)
}

// closeWaitSeconds is the machine owner's CLOSE_WAIT timeout; nil = none.
func (settings machineProtectionSettings) closeWaitSeconds() *int64 {
	if validateMachineProtection(settings) != nil {
		return nil
	}
	return optionalMachineValue(settings.Enabled, settings.CloseWaitSeconds, defaultMachineCloseWaitSeconds)
}

// effectiveDefaultCloseWait is the Core default CLOSE_WAIT timeout: an explicit
// positive server default wins, otherwise enabled machine protection supplies
// its value (10 s unless set). Per-identity values still override both.
func (settings connectionSettings) effectiveDefaultCloseWait() *int64 {
	explicit := settings.DefaultCloseWaitTimeoutSeconds
	if explicit != nil && *explicit > 0 {
		return explicit
	}
	if settings.MachineProtection != nil {
		if value := settings.MachineProtection.closeWaitSeconds(); value != nil {
			return value
		}
	}
	return explicit
}

func machineReleaseBelow(limit int64) int64 {
	release := limit * machineReleasePercent / 100
	if release >= limit {
		release = limit - 1
	}
	return release
}

// machineProtectionThreshold applies the hysteresis: block at value >= cap,
// keep blocking while value >= ⌊cap × 95 %⌋, release below that.
func machineProtectionThreshold(settings machineProtectionSettings, active, total int64, wasBlocking bool) (bool, string, *int64) {
	value, limit, reason := total, settings.MaxTotal, "total"
	if limit == nil {
		// max_active is retained only as a compatibility fallback for old saved
		// settings. Once max_total exists, the unified Machine Total is authoritative.
		value, limit, reason = active, settings.MaxActive, "active_compat"
	}
	if limit == nil {
		return false, "", nil
	}
	release := machineReleaseBelow(*limit)
	if value >= *limit {
		return true, reason, &release
	}
	if wasBlocking && value >= release {
		return true, reason + "_hysteresis", &release
	}
	return false, "", &release
}

// renderMachineProtectionNft builds one atomic nft batch. The table always
// keeps the port sets, the drop counter and an unhooked chain whose ct rule
// keeps conntrack enabled; only while blocking does a prerouting base chain
// exist, hooked at -150 (after conntrack, before DNAT) so forwarded ports are
// covered. cleanup also removes chains of an earlier layout.
func renderMachineProtectionNft(ports machinePorts, blocking, cleanup bool) string {
	table := "inet " + machineProtectionTableName
	var output strings.Builder
	line := func(format string, args ...any) {
		fmt.Fprintf(&output, format, args...)
		output.WriteByte('\n')
	}
	line("add table %s", table)
	line("add counter %s dropped", table)
	for _, set := range []struct {
		name  string
		ports []uint32
	}{{"tcp_ports", ports.TCP}, {"udp_ports", ports.UDP}} {
		line("add set %s %s { type inet_service; }", table, set.name)
		line("flush set %s %s", table, set.name)
		if len(set.ports) > 0 {
			values := make([]string, len(set.ports))
			for index, port := range set.ports {
				values[index] = strconv.FormatUint(uint64(port), 10)
			}
			line("add element %s %s { %s }", table, set.name, strings.Join(values, ", "))
		}
	}
	line("add chain %s conntrack", table)
	line("flush chain %s conntrack", table)
	line("add rule %s conntrack ct state new comment \"mmwxc keeps conntrack on\"", table)
	if cleanup {
		// v0.7.8 and older hooked an input chain; drop it when migrating.
		line("add chain %s input { type filter hook input priority -4; policy accept; }", table)
		line("flush chain %s input", table)
		line("delete chain %s input", table)
	}
	if blocking {
		line("add chain %s prerouting { type filter hook prerouting priority -150; policy accept; }", table)
		line("flush chain %s prerouting", table)
		line("add rule %s prerouting iifname \"lo\" return comment \"mmwxc loopback exemption\"", table)
		line("add rule %s prerouting ct state new tcp flags & (fin | syn | rst | ack) == syn tcp dport @tcp_ports fib daddr type local counter name \"dropped\" drop comment \"mmwxc machine cap\"", table)
		line("add rule %s prerouting ct state new udp dport @udp_ports fib daddr type local counter name \"dropped\" drop comment \"mmwxc machine cap\"", table)
	} else if cleanup {
		line("add chain %s prerouting { type filter hook prerouting priority -150; policy accept; }", table)
		line("flush chain %s prerouting", table)
		line("delete chain %s prerouting", table)
	}
	return output.String()
}

type machineNftOps struct {
	apply   func(context.Context, []byte) error
	remove  func(context.Context) error
	counter func(context.Context) (int64, error)
}

var nftPacketsPattern = regexp.MustCompile(`packets ([0-9]+)`)

func defaultMachineNftOps() machineNftOps {
	return machineNftOps{
		apply: func(ctx context.Context, script []byte) error {
			command := exec.CommandContext(ctx, "nft", "-f", "-")
			command.Stdin = bytes.NewReader(script)
			if output, err := command.CombinedOutput(); err != nil {
				return fmt.Errorf("apply machine protection rules: %w: %s", err, strings.TrimSpace(string(output)))
			}
			return nil
		},
		remove: func(ctx context.Context) error {
			// Absent is fine; only our own table is ever deleted.
			_ = exec.CommandContext(ctx, "nft", "delete", "table", "inet", machineProtectionTableName).Run()
			return nil
		},
		counter: func(ctx context.Context) (int64, error) {
			output, err := exec.CommandContext(ctx, "nft", "list", "counter", "inet", machineProtectionTableName, "dropped").Output()
			if err != nil {
				return 0, errors.New("machine protection table is missing")
			}
			match := nftPacketsPattern.FindSubmatch(output)
			if match == nil {
				return 0, errors.New("machine protection counter is unreadable")
			}
			return strconv.ParseInt(string(match[1]), 10, 64)
		},
	}
}

type machineSysctl struct {
	read  func() (int64, error)
	write func(int64) error
}

func fileMachineSysctl(path string) machineSysctl {
	return machineSysctl{
		read: func() (int64, error) {
			raw, err := os.ReadFile(path)
			if err != nil {
				return 0, err
			}
			return strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		},
		write: func(value int64) error {
			return os.WriteFile(path, []byte(strconv.FormatInt(value, 10)+"\n"), 0644)
		},
	}
}

// machineTWSaved is persisted in the Helper state before the first change.
type machineTWSaved struct {
	Original int64     `json:"original"`
	Applied  int64     `json:"applied"`
	SavedAt  time.Time `json:"saved_at"`
}

// flvxBusinessPortsFile is contract §3, written by the FLVX agent.
type flvxBusinessPortsFile struct {
	Version   int     `json:"version"`
	UpdatedAt string  `json:"updated_at"`
	TCP       *[]int  `json:"tcp"`
	UDP       *[]int  `json:"udp"`
	updated   time.Time
	ports     machinePorts
	malformed bool
}

type flvxBusinessPortsReader struct {
	path    string
	modTime time.Time
	size    int64
	cached  *flvxBusinessPortsFile
}

// read returns the FLVX ports and the file state: absent, fresh, stale or
// malformed. Only a fresh, well-formed file contributes ports. The file is
// parsed again only when its size or mtime changes.
func (reader *flvxBusinessPortsReader) read(now time.Time) (machinePorts, string, string) {
	if reader.path == "" {
		return machinePorts{}, "absent", ""
	}
	info, err := os.Stat(reader.path)
	if err != nil || !info.Mode().IsRegular() {
		reader.cached = nil
		if err != nil && errors.Is(err, os.ErrNotExist) {
			return machinePorts{}, "absent", ""
		}
		return machinePorts{}, "malformed", ""
	}
	if reader.cached == nil || !info.ModTime().Equal(reader.modTime) || info.Size() != reader.size {
		reader.modTime, reader.size = info.ModTime(), info.Size()
		reader.cached = parseFLVXBusinessPorts(reader.path)
	}
	file := reader.cached
	if file.malformed {
		return machinePorts{}, "malformed", ""
	}
	updated := file.updated.UTC().Format(time.RFC3339)
	if age := now.Sub(file.updated); age > flvxBusinessPortsMaxAge || age < -flvxBusinessPortsMaxAge {
		return machinePorts{}, "stale", updated
	}
	return file.ports, "fresh", updated
}

func parseFLVXBusinessPorts(path string) *flvxBusinessPortsFile {
	invalid := &flvxBusinessPortsFile{malformed: true}
	handle, err := os.Open(path)
	if err != nil {
		return invalid
	}
	defer handle.Close()
	raw, err := io.ReadAll(io.LimitReader(handle, flvxBusinessPortsMaxBytes+1))
	if err != nil || len(raw) > flvxBusinessPortsMaxBytes {
		return invalid
	}
	var file flvxBusinessPortsFile
	if json.Unmarshal(raw, &file) != nil || file.Version != 1 || file.TCP == nil || file.UDP == nil {
		return invalid
	}
	file.updated, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(file.UpdatedAt))
	if err != nil {
		return invalid
	}
	convert := func(values []int) ([]uint32, bool) {
		ports := make([]uint32, 0, len(values))
		for _, value := range values {
			if value < 1 || value > 65535 {
				return nil, false
			}
			ports = append(ports, uint32(value))
		}
		return ports, true
	}
	tcp, okTCP := convert(*file.TCP)
	udp, okUDP := convert(*file.UDP)
	if !okTCP || !okUDP {
		return invalid
	}
	file.ports = machinePorts{TCP: tcp, UDP: udp}.normalized()
	return &file
}

type machineProtectionInput struct {
	System     tcpStateCounts
	Machine    *machineConnectionCounts
	CorePorts  []uint32
	FLVXShared machinePorts
}

type machineProtectionManager struct {
	supported  bool
	nft        machineNftOps
	sysctl     machineSysctl
	now        func() time.Time
	flvxFile   flvxBusinessPortsReader
	statusPath string
	// The saved TIME_WAIT original, loaded from and persisted to Helper state.
	tw     *machineTWSaved
	saveTW func(*machineTWSaved) error

	tablePresent bool
	tableSince   time.Time
	appliedKey   string
	checkedAt    time.Time
	needCleanup  bool
	removed      bool
	blocking     bool
	dropped      int64
	droppedBase  int64
}

func newMachineProtectionManager() *machineProtectionManager {
	_, err := exec.LookPath("nft")
	return &machineProtectionManager{
		supported: err == nil, nft: defaultMachineNftOps(), sysctl: fileMachineSysctl(tcpMaxTWBucketsPath), now: time.Now,
		flvxFile: flvxBusinessPortsReader{path: flvxBusinessPortsPath}, statusPath: machineProtectionStatusPath, needCleanup: true,
	}
}

func (manager *machineProtectionManager) ports(input machineProtectionInput, now time.Time) (machinePorts, *machinePortSources) {
	core := normalizeMachinePorts(input.CorePorts)
	shared := input.FLVXShared.normalized()
	file, state, updated := manager.flvxFile.read(now)
	sources := &machinePortSources{Core: len(core), FLVXShared: len(normalizeMachinePorts(append(append([]uint32(nil), shared.TCP...), shared.UDP...))), FLVXFile: state, FLVXFileTCP: len(file.TCP), FLVXFileUDP: len(file.UDP), FLVXUpdatedAt: updated}
	merged := machinePorts{
		TCP: append(append(append([]uint32(nil), core...), shared.TCP...), file.TCP...),
		UDP: append(append(append([]uint32(nil), core...), shared.UDP...), file.UDP...),
	}
	return merged.normalized(), sources
}

func (manager *machineProtectionManager) reconcile(ctx context.Context, configured *machineProtectionSettings, input machineProtectionInput) (status machineProtectionStatus) {
	now := manager.now()
	settings := machineProtectionSettings{}
	if configured != nil {
		settings = *configured
	}
	ports, sources := manager.ports(input, now)
	status = machineProtectionStatus{
		Version: machineProtectionVersion, Owner: "mmwx", Supported: manager.supported, Configured: settings,
		Active: machineActiveTCP(input.System), Connections: input.Machine, LastReconciledAt: now.UTC(),
		ControlledPorts: normalizeMachinePorts(append(append([]uint32(nil), ports.TCP...), ports.UDP...)),
		TCPPorts:        ports.TCP, UDPPorts: ports.UDP, Sources: sources,
	}
	if input.Machine != nil {
		status.Total = input.Machine.Total
	}
	defer func() {
		status.DroppedTotal = manager.dropped
		if err := manager.writeStatusFile(status, settings); err != nil {
			status.StatusFileError = err.Error()
		}
	}()
	if err := validateMachineProtection(settings); err != nil {
		status.LastError = err.Error()
		manager.releaseGuard(ctx)
		status.TWBuckets = manager.reconcileTW(nil)
		return status
	}
	status.TWBuckets = manager.reconcileTW(settings.twTarget())
	status.CloseWaitSeconds = settings.closeWaitSeconds()
	if !settings.Enabled {
		manager.releaseGuard(ctx)
		status.Effective = manager.supported
		return status
	}
	if !manager.supported {
		status.LastError = "nftables is unavailable"
		return status
	}
	blocking, reason, release := machineProtectionThreshold(settings, status.Active, status.Total, manager.blocking)
	status.MaxTotal, status.ReleaseBelow = settings.MaxTotal, release
	if input.Machine == nil {
		// Fail open: an unknown machine total never blocks anyone.
		blocking, reason = false, ""
		status.LastError = "machine socket tables are unreadable"
	}
	if ports.empty() {
		blocking, reason = false, ""
		status.IdleReason = "no_business_ports"
	}
	if blocking && !manager.blocking && !manager.warm(now) {
		blocking, reason = false, ""
		status.WarmingUp = true
	}
	applied, err := manager.ensure(ctx, ports, blocking, now)
	if err != nil {
		status.LastError = err.Error()
		return status
	}
	if blocking && !applied {
		reason = ""
		status.WarmingUp = true
	}
	status.Effective = true
	status.Blocking, status.ThresholdReason = applied, reason
	return status
}

func (manager *machineProtectionManager) warm(now time.Time) bool {
	return manager.tablePresent && now.Sub(manager.tableSince) >= machineConntrackWarmup
}

// ensure keeps the table in the wanted state with as few nft calls as
// possible: none while nothing changes, one presence/counter check per minute
// when idle, every few seconds while blocking. It returns whether the guard
// is blocking now.
func (manager *machineProtectionManager) ensure(ctx context.Context, ports machinePorts, blocking bool, now time.Time) (bool, error) {
	transition := blocking != manager.blocking
	key := renderMachineProtectionNft(ports, blocking, false)
	if manager.tablePresent && !manager.needCleanup && !transition && key == manager.appliedKey {
		interval := machineIdleRecheck
		if blocking {
			interval = machineBlockingRecheck
		}
		if now.Sub(manager.checkedAt) < interval {
			return blocking, nil
		}
		dropped, err := manager.nft.counter(ctx)
		if err == nil {
			manager.dropped, manager.checkedAt = manager.droppedBase+dropped, now
			return blocking, nil
		}
		// Someone removed the table (for example nft flush ruleset): rebuild it
		// and wait for conntrack again before dropping anything.
		manager.lostTable()
		blocking = false
		key = renderMachineProtectionNft(ports, false, false)
	}
	cleanup := !manager.tablePresent || manager.needCleanup || transition
	if err := manager.nft.apply(ctx, []byte(renderMachineProtectionNft(ports, blocking, cleanup))); err != nil {
		// An unexpected layout: replace only our own table, fail open otherwise.
		_ = manager.nft.remove(ctx)
		manager.lostTable()
		blocking = false
		key = renderMachineProtectionNft(ports, false, false)
		if retryErr := manager.nft.apply(ctx, []byte(renderMachineProtectionNft(ports, false, true))); retryErr != nil {
			_ = manager.nft.remove(ctx)
			manager.appliedKey = ""
			return false, retryErr
		}
	}
	if !manager.tablePresent {
		manager.tablePresent, manager.tableSince = true, now
	}
	wasBlocking := manager.blocking
	manager.appliedKey, manager.needCleanup, manager.removed, manager.blocking = key, false, false, blocking
	manager.checkedAt = now
	if wasBlocking || blocking {
		if dropped, err := manager.nft.counter(ctx); err == nil {
			manager.dropped = manager.droppedBase + dropped
		}
	}
	return blocking, nil
}

// lostTable keeps dropped_total monotonic across a recreated counter.
func (manager *machineProtectionManager) lostTable() {
	manager.droppedBase = manager.dropped
	manager.tablePresent, manager.needCleanup, manager.blocking, manager.appliedKey = false, true, false, ""
}

func (manager *machineProtectionManager) releaseGuard(ctx context.Context) {
	if manager.removed || !manager.supported {
		return
	}
	_ = manager.nft.remove(ctx)
	manager.lostTable()
	manager.removed = true
}

// reconcileTW owns net.ipv4.tcp_max_tw_buckets while target is set. The
// original value is persisted before the first write; it is written back only
// while the kernel still holds our value, never over someone else's change.
func (manager *machineProtectionManager) reconcileTW(target *int64) *machineTWStatus {
	if target == nil && manager.tw == nil {
		return nil
	}
	status := &machineTWStatus{Target: target}
	if manager.tw != nil {
		original := manager.tw.Original
		status.Original = &original
	}
	if manager.sysctl.read == nil {
		status.Error = "tcp_max_tw_buckets is unavailable"
		return status
	}
	current, err := manager.sysctl.read()
	if err != nil {
		status.Error = "read tcp_max_tw_buckets: " + err.Error()
		return status
	}
	status.Current = &current
	if target == nil {
		if current == manager.tw.Applied {
			if err := manager.sysctl.write(manager.tw.Original); err != nil {
				status.Error = "restore tcp_max_tw_buckets: " + err.Error()
				return status
			}
			restored := manager.tw.Original
			status.Current = &restored
		}
		if err := manager.persistTW(nil); err != nil {
			status.Error = "persist tcp_max_tw_buckets restore: " + err.Error()
		}
		return status
	}
	if manager.tw == nil || manager.tw.Applied != *target {
		saved := machineTWSaved{Original: current, Applied: *target, SavedAt: manager.now().UTC()}
		if manager.tw != nil {
			saved.Original, saved.SavedAt = manager.tw.Original, manager.tw.SavedAt
		}
		if err := manager.persistTW(&saved); err != nil {
			status.Error = "save original tcp_max_tw_buckets: " + err.Error()
			return status
		}
		original := saved.Original
		status.Original = &original
	}
	if current != *target {
		if err := manager.sysctl.write(*target); err != nil {
			status.Error = "write tcp_max_tw_buckets: " + err.Error()
			return status
		}
		applied := *target
		status.Current = &applied
	}
	return status
}

func (manager *machineProtectionManager) persistTW(saved *machineTWSaved) error {
	if manager.saveTW != nil {
		if err := manager.saveTW(saved); err != nil {
			return err
		}
	}
	manager.tw = saved
	return nil
}

// release hands the machine back (Helper uninstall): restore TIME_WAIT, drop
// the guard and the status file so the FLVX agent may take over.
func (manager *machineProtectionManager) release(ctx context.Context) {
	if manager == nil {
		return
	}
	manager.releaseGuard(ctx)
	_ = manager.reconcileTW(nil)
	if manager.statusPath != "" {
		_ = os.Remove(manager.statusPath)
	}
}

// machineStatusFile is contract §4; every field is always present.
type machineStatusFile struct {
	Version          int    `json:"version"`
	Owner            string `json:"owner"`
	UpdatedAt        string `json:"updated_at"`
	Enabled          bool   `json:"enabled"`
	MaxTotal         *int64 `json:"max_total"`
	TWBuckets        *int64 `json:"tw_buckets"`
	CloseWaitSeconds *int64 `json:"close_wait_seconds"`
	Total            int64  `json:"total"`
	TCP              int64  `json:"tcp"`
	UDP              int64  `json:"udp"`
	Blocking         bool   `json:"blocking"`
	DroppedTotal     int64  `json:"dropped_total"`
}

func machineStatusFileContents(status machineProtectionStatus, settings machineProtectionSettings) machineStatusFile {
	enabled := settings.Enabled && validateMachineProtection(settings) == nil
	file := machineStatusFile{
		Version: 1, Owner: "mmwx", UpdatedAt: status.LastReconciledAt.UTC().Format(time.RFC3339), Enabled: enabled,
		CloseWaitSeconds: settings.closeWaitSeconds(), TWBuckets: settings.twTarget(),
		Total: status.Total, Blocking: status.Blocking, DroppedTotal: status.DroppedTotal,
	}
	if enabled {
		file.MaxTotal = settings.MaxTotal
	}
	if status.Connections != nil {
		file.TCP, file.UDP = status.Connections.TCP, status.Connections.UDP
	}
	return file
}

// writeStatusFile atomically replaces the status file (dir 0755, file 0644) so
// the FLVX agent, which defers to the Helper, can read the owner's state.
func (manager *machineProtectionManager) writeStatusFile(status machineProtectionStatus, settings machineProtectionSettings) error {
	if manager.statusPath == "" {
		return nil
	}
	data, err := json.Marshal(machineStatusFileContents(status, settings))
	if err != nil {
		return err
	}
	dir := filepath.Dir(manager.statusPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// The Core's RuntimeDirectory is created 0700; its socket stays 0600.
	if info, err := os.Stat(dir); err == nil && info.Mode().Perm() != 0755 {
		if err := os.Chmod(dir, 0755); err != nil {
			return err
		}
	}
	temporary, err := os.CreateTemp(dir, ".machine-protection-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(0644); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), manager.statusPath)
}
