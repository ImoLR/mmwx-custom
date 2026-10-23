package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
	"github.com/ImoLR/mmwx-custom/internal/releaseurl"
)

// Landing egress (contract landing-egress-v1 §5): FLVX's plan is rendered into
// the FLVX Core and applied hot through its API. Steady state makes no Core
// API call; DNS checks and local-address probes run at most every 5 minutes
// or when the plan or the Core epoch changes.
const (
	flvxEgressRefresh  = 5 * time.Minute
	flvxEgressRetry    = time.Minute
	flvxEgressCleanup  = 2 * time.Minute
	flvxGeoRetryMin    = time.Minute
	flvxGeoRetryMax    = 30 * time.Minute
	flvxGeoDiskReserve = 64 << 20
	flvxRestartNotice  = 24 * time.Hour
	flvxEgressDNS      = 3 * time.Second
)

const flvxCoreCgroup = "/sys/fs/cgroup/system.slice/" + flvxServiceName

type flvxEgressRender struct {
	outbounds map[string]string // Core tag -> canonical JSON
	rules     []any
	hash      string
	warnings  []string
	covered   []string
	geoBytes  int64
	retrySoon bool
}

type flvxEgressCache struct {
	key, epoch string
	at         time.Time
	render     flvxEgressRender
	err        error
}

type flvxFileStamp struct {
	size, mtime, ctime int64
	inode              uint64
}

type flvxRetry struct {
	failures int
	next     time.Time
}

// flvxCLIError keeps the Core CLI output out of logs but remembers whether
// the gRPC service itself is missing (a Core started from an older config).
type flvxCLIError struct {
	command       string
	unimplemented bool
}

func (e flvxCLIError) Error() string { return "FLVX gRPC " + e.command + " failed" }

func flvxStamp(info os.FileInfo) flvxFileStamp {
	s := flvxFileStamp{size: info.Size(), mtime: info.ModTime().UnixNano()}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		s.inode, s.ctime = st.Ino, st.Ctim.Nano()
	}
	return s
}

func (m *flvxManager) geoDir() string { return filepath.Join(m.dir, "geo") }

func (m *flvxManager) freeDisk(dir string) (int64, error) {
	if m.diskFree != nil {
		return m.diskFree(dir)
	}
	return flvxFreeDisk(dir)
}

// geoVerified hashes a geo file only when its stat stamp changed.
func (m *flvxManager) geoVerified(name string) bool {
	path := filepath.Join(m.geoDir(), name)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != flvx.GeoSize[name] {
		delete(m.geoStamps, path)
		return false
	}
	stamp := flvxStamp(info)
	if m.geoStamps[path] == stamp {
		return true
	}
	delete(m.geoStamps, path)
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil || hex.EncodeToString(h.Sum(nil)) != flvx.GeoSHA256[name] {
		return false
	}
	if m.geoStamps == nil {
		m.geoStamps = map[string]flvxFileStamp{}
	}
	m.geoStamps[path] = stamp
	return true
}

func flvxFreeDisk(dir string) (int64, error) {
	for {
		var st syscall.Statfs_t
		err := syscall.Statfs(dir, &st)
		if err == nil {
			return int64(st.Bavail) * int64(st.Bsize), nil
		}
		parent := filepath.Dir(dir)
		if !errors.Is(err, syscall.ENOENT) || parent == dir {
			return 0, err
		}
		dir = parent
	}
}

// egressCapability reports geo support: the pinned file is verified, or the
// disk can hold it plus the reserve. Cached with the render cadence.
func (m *flvxManager) egressCapability() (bool, bool) {
	if !m.capabilityAt.IsZero() && m.now().Sub(m.capabilityAt) < flvxEgressRefresh {
		return m.capability[0], m.capability[1]
	}
	free, err := m.freeDisk(m.geoDir())
	ok := func(name string) bool {
		return m.geoVerified(name) || err == nil && free >= flvx.GeoSize[name]+flvxGeoDiskReserve
	}
	m.capability = [2]bool{ok("geoip.dat"), ok("geosite.dat")}
	m.capabilityAt = m.now()
	return m.capability[0], m.capability[1]
}

// ensureGeo installs only the referenced pinned files, after a disk check,
// with per-file backoff (1 min doubling to 30 min) and rate-limited logs.
func (m *flvxManager) ensureGeo(ctx context.Context, names []string) error {
	var missing []string
	var need int64
	for _, name := range names {
		if !m.geoVerified(name) {
			missing = append(missing, name)
			need += flvx.GeoSize[name]
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if err := os.MkdirAll(m.geoDir(), 0755); err != nil {
		return err
	}
	if free, err := m.freeDisk(m.geoDir()); err != nil || free < need+flvxGeoDiskReserve {
		return fmt.Errorf("服务器磁盘空间不足：geo 数据需要约 %d MiB 可用空间", (need+flvxGeoDiskReserve)>>20)
	}
	m.capabilityAt = time.Time{}
	for _, name := range missing {
		now := m.now()
		r := m.geoRetry[name]
		if r != nil && now.Before(r.next) {
			return fmt.Errorf("geo 数据 %s 下载失败，%s后重试", name, flvxRetryIn(r.next.Sub(now)))
		}
		download := m.geoDownload
		if download == nil {
			download = m.downloadGeo
		}
		err := download(ctx, name, filepath.Join(m.geoDir(), name))
		if err == nil && !m.geoVerified(name) {
			err = errors.New("checksum mismatch")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m.logs.report("FLVX geo download "+name, err)
		if err != nil {
			if r == nil {
				r = &flvxRetry{}
				if m.geoRetry == nil {
					m.geoRetry = map[string]*flvxRetry{}
				}
				m.geoRetry[name] = r
			}
			delay := flvxGeoRetryMin << min(r.failures, 5)
			if delay > flvxGeoRetryMax {
				delay = flvxGeoRetryMax
			}
			r.failures++
			r.next = now.Add(delay)
			return fmt.Errorf("geo 数据 %s 下载或校验失败，%s后重试", name, flvxRetryIn(delay))
		}
		delete(m.geoRetry, name)
	}
	return nil
}

func flvxRetryIn(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int((d+time.Second-1)/time.Second))
	}
	return fmt.Sprintf("%d 分钟", int((d+time.Minute-1)/time.Minute))
}

type flvxIdleReader struct {
	r     io.Reader
	timer *time.Timer
}

func (r flvxIdleReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.timer.Reset(30 * time.Second)
	}
	return n, err
}

// downloadGeo uses the same accelerator path and redirect policy as the Core
// binary; each source gets a 30 s idle timer and a 5 min hard limit.
func (m *flvxManager) downloadGeo(ctx context.Context, name, path string) error {
	candidates, err := releaseurl.Candidates(m.state.Payload.Artifact.GitHubAccelerator, flvx.GeoURL(name))
	if err != nil {
		return err
	}
	lifecycle := m.lifecycle
	if lifecycle == nil {
		lifecycle = newLifecycleManager(nil)
	}
	var last error
	for _, candidate := range candidates {
		if last = m.downloadGeoOnce(ctx, lifecycle.clientForArtifactCandidate(candidate, flvx.GeoURL(name)), candidate, name, path); last == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return last
}

func (m *flvxManager) downloadGeoOnce(ctx context.Context, client *http.Client, url, name, path string) error {
	child, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	timer := time.AfterFunc(30*time.Second, cancel)
	defer timer.Stop()
	request, err := http.NewRequestWithContext(child, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	tmp := path + ".download"
	defer os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(flvxIdleReader{response.Body, timer}, flvx.GeoSize[name]+1))
	if err == nil && n != flvx.GeoSize[name] {
		err = errors.New("size mismatch")
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != flvx.GeoSHA256[name] {
		err = errors.New("checksum mismatch")
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	return err
}

// detectLocalAddrs returns the kernel-selected source address per family. A
// UDP connect only consults the routing table; no packet is sent.
func detectLocalAddrs() flvx.LocalAddrs {
	probe := func(network, target string) netip.Addr {
		c, err := net.DialTimeout(network, target, time.Second)
		if err != nil {
			return netip.Addr{}
		}
		defer c.Close()
		a := c.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
		if !a.IsValid() || a.IsLoopback() || a.IsUnspecified() || a.IsLinkLocalUnicast() {
			return netip.Addr{}
		}
		return a
	}
	return flvx.LocalAddrs{V4: probe("udp4", "1.1.1.1:53"), V6: probe("udp6", "[2606:4700:4700::1111]:53")}
}

// pinUpstream resolves a hostname upstream and renders the validated address
// instead of the name, like the Reality destination pin: the Core would
// otherwise resolve again at dial time, and a rebinding answer could reach a
// private address. Any private answer disables the outbound; an unresolvable
// name blocks it until the next render (1 minute).
func (m *flvxManager) pinUpstream(ctx context.Context, o flvx.EgressOutbound, local flvx.LocalAddrs, previous map[string]netip.Addr) (flvx.EgressOutbound, string, bool) {
	host, port := o.Server, ""
	if o.Type == "wireguard" {
		if o.WireGuard == nil || o.WireGuard.Endpoint == "" {
			return o, "", false
		}
		host, port, _ = net.SplitHostPort(o.WireGuard.Endpoint)
	}
	if host == "" {
		return o, "", false
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if flvx.BlockedAddr(a) {
			return o, o.Tag + ": 上游地址是内网或保留地址，已封锁", false
		}
		return o, "", false
	}
	lookup := m.lookupIP
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	child, cancel := context.WithTimeout(ctx, flvxEgressDNS)
	addrs, err := lookup(child, host)
	cancel()
	if err != nil || len(addrs) == 0 {
		return o, o.Tag + ": 上游域名暂时无法解析，暂按封锁处理，1 分钟后重试", true
	}
	var v4, v6 []netip.Addr
	for _, a := range addrs {
		a = a.Unmap()
		if flvx.BlockedAddr(a) {
			return o, o.Tag + ": 上游域名解析到内网或保留地址，已封锁", false
		}
		if a.Is4() {
			v4 = append(v4, a)
		} else {
			v6 = append(v6, a)
		}
	}
	for _, list := range [][]netip.Addr{v4, v6} {
		sort.Slice(list, func(i, j int) bool { return list[i].Less(list[j]) })
	}
	family := o.IPVersion
	if o.Type == "wireguard" {
		family = "system" // the endpoint follows the server, not the tunnel family
	}
	var order [][]netip.Addr
	switch {
	case family == "force_ipv4":
		order = [][]netip.Addr{v4}
	case family == "force_ipv6":
		order = [][]netip.Addr{v6}
	case family == "prefer_ipv6" && local.V6.IsValid() || !local.V4.IsValid() && local.V6.IsValid():
		order = [][]netip.Addr{v6, v4}
	default:
		order = [][]netip.Addr{v4, v6}
	}
	key := o.Tag + "\x00" + host
	var pick netip.Addr
	for _, list := range order {
		if len(list) == 0 {
			continue
		}
		pick = list[0]
		for _, a := range list {
			if a == previous[key] { // sticky while the answer still contains it
				pick = a
			}
		}
		break
	}
	if !pick.IsValid() {
		return o, o.Tag + ": 上游域名没有强制 IP 版本对应的地址，已封锁", false
	}
	m.pins[key] = pick
	if o.Type == "wireguard" {
		w := *o.WireGuard
		w.Endpoint = net.JoinHostPort(pick.String(), port)
		o.WireGuard = &w
	} else {
		o.Server = pick.String()
	}
	return o, "", false
}

func (m *flvxManager) localAddrs() flvx.LocalAddrs {
	if m.detectLocal != nil {
		return m.detectLocal()
	}
	return detectLocalAddrs()
}

func (m *flvxManager) renderEgress(ctx context.Context, plan *flvx.Egress) (flvxEgressRender, error) {
	r := flvxEgressRender{outbounds: map[string]string{}}
	if !plan.Active() {
		r.rules, _ = flvx.RenderRules(nil, nil, nil)
		r.hash = flvxHash(map[string]any{"rules": r.rules})
		return r, nil
	}
	local := m.localAddrs()
	tagMap := map[string]string{"direct": "direct", "block": "block"}
	previousPins := m.pins
	m.pins = map[string]netip.Addr{}
	for _, o := range plan.Outbounds {
		pinned, warning, transient := m.pinUpstream(ctx, o, local, previousPins)
		var cfg map[string]any
		if warning != "" {
			cfg = flvx.Blackhole(o.Tag)
		} else {
			cfg, warning = flvx.RenderOutbound(pinned, local)
		}
		if warning != "" {
			r.warnings = append(r.warnings, warning)
		}
		r.retrySoon = r.retrySoon || transient
		coreTag := flvx.CoreTag(o.Tag, cfg)
		cfg["tag"] = coreTag
		r.outbounds[coreTag] = flvx.CanonicalJSON(cfg)
		tagMap[o.Tag] = coreTag
	}
	if files := plan.GeoFiles(); len(files) > 0 {
		if err := m.ensureGeo(ctx, files); err != nil {
			return r, err
		}
	}
	mapped := *plan
	mapped.Rules = make([]flvx.EgressRule, len(plan.Rules))
	for i, rule := range plan.Rules {
		rule.Outbound = tagMap[rule.Outbound]
		mapped.Rules[i] = rule
		for _, list := range [][]string{rule.Domain, rule.IP} {
			for _, entry := range list {
				_, size := flvx.GeoEntry(entry)
				r.geoBytes += int64(size)
			}
		}
	}
	geoIP := filepath.Join(m.geoDir(), "geoip.dat")
	rules, err := flvx.RenderRules(&mapped, func(code string) ([]netip.Prefix, error) { return flvx.ReadGeoIP(geoIP, code) }, &m.egressSources)
	if err != nil {
		m.egressSources.Discard()
		return r, err
	}
	m.egressSources.Rotate()
	for _, rule := range rules {
		if sources, ok := rule.(map[string]any)["sourceIP"].([]string); ok {
			for _, entry := range sources {
				_, size := flvx.GeoEntry(entry)
				r.geoBytes += int64(size + len(entry))
			}
		}
	}
	r.rules = rules
	for tag := range plan.CoveredTags() {
		r.covered = append(r.covered, tag)
	}
	sort.Strings(r.covered)
	r.hash = flvxHash(map[string]any{"outbounds": r.outbounds, "rules": rules})
	return r, nil
}

func flvxHash(v any) string {
	sum := sha256.Sum256([]byte(flvx.CanonicalJSON(v)))
	return hex.EncodeToString(sum[:])
}

func flvxReadInt(path string) (int64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	return v, err == nil
}

func flvxMemAvailable() (int64, bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) >= 2 && fields[0] == "MemAvailable:" {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			return kb << 10, err == nil
		}
	}
	return 0, false
}

// egressMemoryCheck refuses adding large geo rules when the FLVX Core's
// MemoryMax or the host cannot hold them. The estimate (8x the protobuf geo
// payload + 4 MiB) covers the measured matcher heap growth (contract §5.5:
// +14 MiB settled for ~2.3 MB of 「屏蔽中国大陆」 geo data).
func (m *flvxManager) egressMemoryCheck(r flvxEgressRender) error {
	growth := r.geoBytes - m.state.EgressGeoBytes
	if growth <= 0 {
		return nil
	}
	need := growth*8 + 4<<20
	current, limit := int64(0), int64(m.state.Payload.Config.MemoryMB)<<20
	available, availableOK := int64(0), false
	if m.memory != nil {
		current, available, availableOK = m.memory()
	} else {
		current, _ = flvxReadInt(flvxCoreCgroup + "/memory.current")
		available, availableOK = flvxMemAvailable()
	}
	if limit > 0 && current > 0 && current+need > limit*9/10 {
		return fmt.Errorf("FLVX Core 内存不足：上限 %d MB，已用 %d MB，这组 geo 规则预计还需约 %d MB；请调高共享设置的内存上限或改用更小的分类", limit>>20, current>>20, (need+(1<<20)-1)>>20)
	}
	if availableOK && available < need+64<<20 {
		return fmt.Errorf("服务器可用内存不足（剩余 %d MB），暂不添加约 %d MB 的 geo 规则", available>>20, (need+(1<<20)-1)>>20)
	}
	return nil
}

// routingReady probes RoutingService once per Core epoch.
func (m *flvxManager) routingReady(ctx context.Context, epoch string) (bool, bool) {
	if m.routingEpoch == epoch && epoch != "" {
		return m.routingOK, m.routingMissing
	}
	_, err := m.apiCall(ctx, "lsrules")
	var cli flvxCLIError
	missing := errors.As(err, &cli) && cli.unimplemented
	if err == nil || missing {
		m.routingEpoch, m.routingOK, m.routingMissing = epoch, err == nil, missing
	}
	return err == nil, missing
}

// ensureRouting restarts the FLVX Core once when an active plan needs hot
// rule updates and the running Core was started from a config without
// RoutingService. The instance holds only FLVX nodes; the restart is recorded
// and reported, and never repeated for plan changes.
func (m *flvxManager) ensureRouting(ctx context.Context, snapshot coreSnapshotResponse) (coreSnapshotResponse, error) {
	p := m.state.Payload
	if !p.Desired.Egress.Active() || p.Status == "revoked" || m.state.RoutingRestartAt != 0 {
		return snapshot, nil
	}
	if ready, missing := m.routingReady(ctx, flvxEpoch(snapshot)); ready || !missing {
		return snapshot, nil
	}
	if err := m.collect(ctx, snapshot); err != nil {
		return snapshot, err
	}
	m.state.RoutingRestartAt = m.now().UnixMilli()
	if err := m.persist(); err != nil {
		return snapshot, err
	}
	m.logs.report("FLVX Core routing API restart", errors.New("restarting the FLVX Core once to enable hot routing updates"))
	restart := m.restartCore
	if restart == nil {
		restart = func(ctx context.Context) error { return systemctl(ctx, "restart", m.service) }
	}
	if err := restart(ctx); err != nil {
		return snapshot, err
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if next, err := m.core.snapshot(ctx); err == nil && !next.StartedAt.Equal(snapshot.StartedAt) {
			return next, nil
		}
		select {
		case <-ctx.Done():
			return snapshot, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return snapshot, errors.New("FLVX Core readiness timed out after the routing restart")
}

func flvxEpoch(s coreSnapshotResponse) string { return s.StartedAt.UTC().Format(time.RFC3339Nano) }

// withhold keeps nodes newly covered by a failed plan offline (fail closed);
// nodes the applied plan already covers keep its rules.
func (m *flvxManager) withhold(plan *flvx.Egress, reason string) {
	old := map[string]bool{}
	for _, tag := range m.state.EgressCovered {
		old[tag] = true
	}
	for tag := range plan.CoveredTags() {
		if !old[tag] {
			m.withheld[tag] = "出站与路由未生效，节点暂停：" + reason
		}
	}
}

func (m *flvxManager) apiWithFile(ctx context.Context, command, name string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	path := filepath.Join(m.dir, name)
	if err = writeFLVXFile(path, raw, 0600); err != nil {
		return err
	}
	defer os.Remove(path)
	_, err = m.apiCall(ctx, command, path)
	return err
}

func (m *flvxManager) listFLVXOutbounds(ctx context.Context) (map[string]bool, error) {
	raw, err := m.apiCall(ctx, "lso")
	if err != nil {
		return nil, err
	}
	var listed struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err = json.Unmarshal(raw, &listed); err != nil {
		return nil, errors.New("invalid FLVX outbound list")
	}
	out := map[string]bool{}
	for _, o := range listed.Outbounds {
		out[o.Tag] = true
	}
	return out, nil
}

// removeStale removes FLVX plan outbounds (and the Reality guard outbound)
// the rules no longer reference. Unreferenced outbounds are inert, so a
// failure only schedules a slow retry.
func (m *flvxManager) removeStale(ctx context.Context, present map[string]bool, keep map[string]string) bool {
	var stale []string
	for tag := range present {
		if (strings.HasPrefix(tag, "flvxo-") || tag == flvx.GuardOutboundTag) && keep[tag] == "" {
			stale = append(stale, tag)
		}
	}
	if len(stale) == 0 {
		return true
	}
	sort.Strings(stale)
	_, err := m.apiCall(ctx, "rmo", stale...)
	m.logs.report("FLVX stale outbound removal", err)
	return err == nil
}

// applyEgress makes the running Core match the plan before inbounds are
// added: add new outbounds, replace the whole rule set atomically, then
// remove outbounds no rule references. It sets the reported egress status and
// the nodes to withhold. The Reality guard rules of this pass (planGuards)
// lead the rule set, before the safety rule; GuardRulePorts records which
// guards' rules the Core holds.
func (m *flvxManager) applyEgress(ctx context.Context, epoch string) {
	p := m.state.Payload
	plan := p.Desired.Egress
	active := plan.Active()
	guards := m.guards.rules
	geoip, geosite := m.egressCapability()
	status := flvx.EgressStatus{Versions: []int{flvx.EgressVersion}, GeoIP: geoip, GeoSite: geosite, State: "none", AppliedRevision: m.state.EgressRevision}
	m.withheld = map[string]string{}
	defer func() {
		if m.state.RoutingRestartAt > 0 && m.now().Sub(time.UnixMilli(m.state.RoutingRestartAt)) < flvxRestartNotice {
			status.Warnings = append(status.Warnings, "已重启 FLVX Core 一次以启用热更新路由（当时 FLVX 节点短暂断线），之后修改出站与路由不再重启")
		}
		if len(status.Warnings) > 10 {
			status.Warnings = status.Warnings[:10]
		}
		for i, w := range status.Warnings {
			if r := []rune(w); len(r) > 200 {
				status.Warnings[i] = string(r[:200])
			}
		}
		m.state.Report.Egress = &status
	}()
	if !active && len(guards) == 0 && m.state.EgressApplied == "" && !m.state.EgressStale {
		m.state.EgressRevision = p.Desired.Revision
		status.AppliedRevision = p.Desired.Revision
		return
	}
	fail := func(reason string) {
		// Guard rules alone never turn the (plan-less) egress status into an
		// error; the guard reports its own state.
		if active || len(guards) == 0 {
			status.State, status.Error = "error", reason
		}
		if active {
			m.withhold(plan, reason)
		}
		if len(guards) > 0 {
			m.guards.rulesIssue = "Reality 防盗规则未生效：" + reason
		}
	}
	// Custom validated and signed the plan; check again once per plan, and
	// fail closed for its nodes only so node updates keep flowing even if a
	// newer Custom sent something this Helper cannot render.
	key := flvxHash(plan)
	if active && m.egressValidKey != key {
		tags := map[string]bool{}
		for _, n := range p.Desired.Nodes {
			tags[n.Tag()] = true
		}
		m.egressInvalid = ""
		if problems := flvx.ValidateEgress(plan, tags, true, true); len(problems) > 0 {
			m.egressInvalid = "出站与路由方案无效（" + problems[0].Field + "）：" + problems[0].Message
		}
		m.egressValidKey = key
	}
	if active && m.egressInvalid != "" {
		fail(m.egressInvalid)
		return
	}
	ready, missing := m.routingReady(ctx, epoch)
	if !ready {
		// Guards wait for a Core restart (fail-open meanwhile); the egress
		// plan restarts the Core itself (ensureRouting).
		m.guards.restart = missing
		if !active {
			// A Core without RoutingService never received our rules.
			m.state.EgressApplied, m.state.EgressCovered, m.state.EgressGeoBytes, m.state.EgressStale, m.state.EgressRevision = "", nil, 0, false, p.Desired.Revision
			m.state.GuardRulePorts = nil
			status.AppliedRevision = p.Desired.Revision
			if len(guards) > 0 && !missing {
				m.guards.rulesIssue = "Reality 防盗规则未生效：FLVX Core 的路由 API 暂不可用"
			}
			return
		}
		fail("FLVX Core 的路由 API 暂不可用")
		return
	}
	rulePorts := make([]int, 0, len(guards))
	for port := range guards {
		rulePorts = append(rulePorts, port)
	}
	sort.Ints(rulePorts)
	renderKey := key
	if len(guards) > 0 {
		renderKey = flvxHash(map[string]any{"plan": plan, "guards": guards})
	}
	now := m.now()
	c := &m.egress
	wait := flvxEgressRefresh
	if c.err != nil || c.render.retrySoon {
		wait = flvxEgressRetry
	}
	if c.key != renderKey || c.epoch != epoch || now.Sub(c.at) >= wait {
		render, err := m.renderEgress(ctx, plan)
		if err == nil && len(guards) > 0 {
			render.outbounds[flvx.GuardOutboundTag] = flvx.CanonicalJSON(flvx.GuardOutbound())
			render.rules = append(flvx.GuardRules(guards), render.rules...)
			render.hash = flvxHash(map[string]any{"outbounds": render.outbounds, "rules": render.rules})
		}
		*c = flvxEgressCache{key: renderKey, epoch: epoch, at: now, render: render, err: err}
	}
	if c.err != nil {
		fail(c.err.Error())
		return
	}
	r := c.render
	status.Warnings = append(status.Warnings, r.warnings...)
	applied := r.hash
	if !active && len(guards) == 0 {
		applied = ""
	}
	if applied == m.state.EgressApplied && !m.state.EgressStale {
		if active {
			status.State = "applied"
		}
		m.state.EgressRevision, status.AppliedRevision = p.Desired.Revision, p.Desired.Revision
		m.state.GuardRulePorts = rulePorts
		return
	}
	if applied == m.state.EgressApplied && m.state.EgressStale && now.Before(m.egressCleanupAt) {
		if active {
			status.State = "applied"
		}
		status.AppliedRevision = m.state.EgressRevision
		status.Warnings = append(status.Warnings, "旧出站清理失败，稍后重试")
		m.state.GuardRulePorts = rulePorts
		return
	}
	if err := m.egressMemoryCheck(r); err != nil {
		c.err = err
		fail(err.Error())
		return
	}
	present, err := m.listFLVXOutbounds(ctx)
	if err == nil && applied != m.state.EgressApplied {
		var add []any
		tags := make([]string, 0, len(r.outbounds))
		for tag := range r.outbounds {
			tags = append(tags, tag)
		}
		sort.Strings(tags)
		for _, tag := range tags {
			if !present[tag] {
				add = append(add, json.RawMessage(r.outbounds[tag]))
			}
		}
		if len(add) > 0 {
			err = m.apiWithFile(ctx, "ado", "egress-outbounds.json", map[string]any{"outbounds": add})
		}
		if err == nil {
			// Without -append this Core build replaces the rule set atomically
			// and keeps the old rules if building the new ones fails.
			err = m.apiWithFile(ctx, "adrules", "egress-rules.json", map[string]any{"routing": map[string]any{"rules": r.rules}})
		}
	}
	if err != nil {
		// Retry with the error cadence; the previous plan stays in force.
		c.err = errors.New("更新 FLVX Core 出站与路由失败")
		m.logs.report("FLVX egress apply", err)
		fail(c.err.Error())
		return
	}
	m.logs.report("FLVX egress apply", nil)
	cleaned := m.removeStale(ctx, present, r.outbounds)
	m.state.EgressApplied, m.state.EgressCovered, m.state.EgressRevision = applied, r.covered, p.Desired.Revision
	m.state.EgressGeoBytes, m.state.EgressStale = r.geoBytes, !cleaned
	m.state.GuardRulePorts = rulePorts
	if !active {
		m.state.EgressCovered, m.state.EgressGeoBytes = nil, 0
	}
	if !cleaned {
		m.egressCleanupAt = now.Add(flvxEgressCleanup)
		status.Warnings = append(status.Warnings, "旧出站清理失败，稍后重试")
	}
	status.AppliedRevision = p.Desired.Revision
	if active {
		status.State = "applied"
	}
	if err := m.persist(); err != nil {
		m.logs.report("FLVX egress state persistence", err)
	}
}
