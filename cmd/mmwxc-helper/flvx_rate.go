package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// Per-user 速率 on the FLVX Core instance (contract landing-user-limits-v1
// §7, version 2). The Core has no bandwidth limiter, so the Helper shapes in
// the kernel (contract §7.4 alternative) with the objects of FLVX's own
// landing agent (go-gost/x/landing/user_rate_linux.go, plan 161): one owner's
// connections share its rate fairly, and what an idle one leaves goes to the
// busy ones.
//
//   - IFB devices mmwxcr-up (client → node: redirected ingress) and mmwxcr-dn
//     (node → client: redirected egress), each with root HTB 7f1b:
//     (unclassified = direct, unshaped) and one class 7f1b:<slot·16> per owner
//     with a 速率 (rate = ceil) with an fq_codel leaf <slot>:. Owners without
//     a 速率 have no class.
//   - On every L3 device with a global address (not lo / IFB, not a bridge /
//     bond port, not a VLAN on top of another selected device): a clsact
//     qdisc (reused when present) and one flower filter per (port, tcp|udp,
//     IPv4|IPv6, direction) at pref 61870 (IPv4) / 61871 (IPv6): skbedit
//     priority 7f1b:<slot·16> + mirred egress redirect to the IFB. Only the
//     owner's FLVX node ports match, never mmwx's own Core; relayed traffic
//     arrives on the same ports and is included, as the contract asks.
//
// Names, handles and prefs differ from FLVX's agent (flvxlr-*, 7f1a:,
// 61860/61861), so both can run on one machine; each removes only its own
// filters, and a clsact only when nothing else is left on it. Root qdiscs of
// the machine's devices are never touched. A mirred redirect to a missing or
// down IFB drops the packet, so filters always go before the IFBs, and an IFB
// found missing or down makes the check rebuild everything. The Helper drives
// ip / tc (iproute2) like it drives nft; nothing runs per packet in
// userspace. Steady state makes no call apart from one device check a minute
// while a rate is in force.
const (
	flvxRateIfbUp   = "mmwxcr-up"
	flvxRateIfbDown = "mmwxcr-dn"
	flvxRateMajor   = 0x7f1b
	flvxRatePrefV4  = 61870
	flvxRatePrefV6  = 61871
	// The devices and IFBs are re-checked with one ip call at this interval
	// while a rate is in force; also the retry delay after a failure.
	flvxRateRecheck = time.Minute
	// A failed probe (no tc, kernel without ifb / htb / flower) is retried
	// this rarely; until it succeeds flvx.user_rate.v1 is not advertised.
	flvxRateProbeRetry = 10 * time.Minute
	// An owner whose rate goes keeps class and filters until a later pass,
	// the class set unlimited: what is queued leaves at once and in order
	// (deleting it would drop it and shrink the connections' windows right
	// when the limit is lifted). FLVX's agent does the same.
	flvxRateDrain      = 2 * time.Second
	flvxRateLeafFlows  = 128     // fq_codel queues per owner and direction
	flvxRateLeafMemory = 4 << 20 // fq_codel bytes per owner and direction
	flvxRateQuantum    = 60000   // HTB quantum (classes never borrow)
	flvxRateMaxSlot    = 0xfff   // slot·16 is the class minor
	// Helper v0.7.15 policed with this nftables table; removed on start.
	flvxRateNftTable = "mmwxc_flvx_rate"
)

// flvxRateUnlimited is the HTB rate of a class whose limit is being lifted.
var flvxRateUnlimited = flvxRateWire(flvx.MaxUserRateMbps * flvx.RateBytesPerMbps)

// flvxRateWire converts a FLVX rate (payload bytes/s) into the HTB rate in
// bit/s. HTB counts whole frames; FLVX's lab measured payload = 0.935–0.945
// of the HTB rate through the Core, so the class gets 6 % on top, as there.
func flvxRateWire(bytesPerSec int64) uint64 { return uint64(bytesPerSec) * 8 * 106 / 100 }

// flvxRateOwner is one owner's 速率 and the ports of its live nodes.
type flvxRateOwner struct {
	Owner string
	Bytes int64 // per second, each direction
	TCP   []int
	UDP   []int
}

// flvxRateKey is one filter; its handle is unique within its tcf_proto.
type flvxRateKey struct {
	egress, v6, udp bool
	port            uint16
}

func (k flvxRateKey) handle() uint32 {
	if k.udp {
		return uint32(k.port)<<2 | 2
	}
	return uint32(k.port)<<2 | 1
}

func (k flvxRateKey) hook() string {
	if k.egress {
		return "egress"
	}
	return "ingress"
}

func (k flvxRateKey) proto() (string, int) {
	if k.v6 {
		return "ipv6", flvxRatePrefV6
	}
	return "ip", flvxRatePrefV4
}

func flvxRateKeyLess(a, b flvxRateKey) bool {
	if a.port != b.port {
		return a.port < b.port
	}
	if a.udp != b.udp {
		return !a.udp
	}
	if a.v6 != b.v6 {
		return !a.v6
	}
	return !a.egress && b.egress
}

func flvxRateClassID(slot uint16) string {
	return fmt.Sprintf("%x:%x", flvxRateMajor, uint32(slot)<<4)
}

// flvxRateClassLines creates (or, existing, changes in place: queue and
// leaf kept) one owner's class on both IFBs.
func flvxRateClassLines(slot uint16, rate uint64, exists bool) []string {
	var lines []string
	for _, ifb := range []string{flvxRateIfbUp, flvxRateIfbDown} {
		verb := "replace"
		if exists {
			verb = "change"
		}
		lines = append(lines, fmt.Sprintf("class %s dev %s parent %x: classid %s htb rate %dbit ceil %dbit quantum %d", verb, ifb, flvxRateMajor, flvxRateClassID(slot), rate, rate, flvxRateQuantum))
		if !exists {
			lines = append(lines, fmt.Sprintf("qdisc replace dev %s parent %s handle %x: fq_codel flows %d memory_limit %d", ifb, flvxRateClassID(slot), slot, flvxRateLeafFlows, flvxRateLeafMemory))
		}
	}
	return lines
}

// flvxRateFilterLine attaches one port to an owner's class (replace: a port
// that changed owner moves in one step).
func flvxRateFilterLine(dev string, k flvxRateKey, slot uint16) string {
	proto, pref := k.proto()
	ip := "tcp"
	if k.udp {
		ip = "udp"
	}
	port, target := "dst_port", flvxRateIfbUp
	if k.egress {
		port, target = "src_port", flvxRateIfbDown
	}
	return fmt.Sprintf("filter replace dev %s %s protocol %s pref %d handle 0x%x flower ip_proto %s %s %d action skbedit priority %s action mirred egress redirect dev %s", dev, k.hook(), proto, pref, k.handle(), ip, port, k.port, flvxRateClassID(slot), target)
}

func flvxRateFilterDelLine(dev string, k flvxRateKey) string {
	proto, pref := k.proto()
	return fmt.Sprintf("filter del dev %s %s protocol %s pref %d handle 0x%x flower", dev, k.hook(), proto, pref, k.handle())
}

// flvxRateRun runs one tool (ip / tc / nft / unshare) with stdin.
type flvxRateRun func(ctx context.Context, stdin []byte, tool string, args ...string) ([]byte, error)

// flvxRateCommand runs the tools directly (tests prefix a namespace).
func flvxRateCommand(prefix ...string) flvxRateRun {
	return func(ctx context.Context, stdin []byte, tool string, args ...string) ([]byte, error) {
		argv := append(append(append([]string{}, prefix...), tool), args...)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return out, fmt.Errorf("%s %s: %w: %s", tool, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
}

// loadIfbModule loads ifb with numifbs=0 when it is not loaded yet; the
// kernel's auto-load on the first IFB link would also create ifb0 / ifb1.
func loadIfbModule() {
	if _, err := os.Stat("/sys/module/ifb"); err == nil {
		return
	}
	_ = exec.Command("modprobe", "ifb", "numifbs=0").Run()
}

type flvxRateDev struct {
	name    string
	filters map[flvxRateKey]uint16 // → slot
}

type flvxRateLimiter struct {
	run        flvxRateRun
	loadModule func()
	now        func() time.Time
	// known: this process removed what an earlier one left (first pass).
	known  bool
	active bool // IFBs with their root HTBs exist
	up, dn int  // IFB ifindex
	// owners in force (the last successful sync); slots: owner → slot, kept
	// while its class exists (a releasing owner that comes back reuses it).
	owners    []flvxRateOwner
	slots     map[string]uint16
	classes   map[uint16]uint64 // slot → HTB rate (bit/s)
	devs      map[int]*flvxRateDev
	releasing map[uint16]time.Time
	checkedAt time.Time
	// failure is retried when the owners change or after flvxRateRecheck.
	failure      error
	failedAt     time.Time
	failedOwners []flvxRateOwner

	probeMu  sync.Mutex
	probed   bool
	probeOK  bool
	probedAt time.Time
}

func newFLVXRateLimiter() *flvxRateLimiter {
	return &flvxRateLimiter{run: flvxRateCommand(), loadModule: loadIfbModule, now: time.Now}
}

// flvxRateScan is the device state of one check.
type flvxRateScan struct {
	devs         map[int]string // selected devices
	links        map[int]bool   // every link that exists
	upIdx, dnIdx int
	upOK, dnOK   bool // exists and up
}

// scan reads links and addresses with one ip call and selects the devices
// to attach to, as FLVX's agent does.
func (r *flvxRateLimiter) scan(ctx context.Context) (flvxRateScan, error) {
	s := flvxRateScan{devs: map[int]string{}, links: map[int]bool{}}
	raw, err := r.run(ctx, nil, "ip", "-j", "-d", "addr", "show")
	if err != nil {
		return s, err
	}
	var links []struct {
		Index    int      `json:"ifindex"`
		Name     string   `json:"ifname"`
		Flags    []string `json:"flags"`
		Master   string   `json:"master"`
		Link     string   `json:"link"`
		LinkInfo struct {
			Kind string `json:"info_kind"`
		} `json:"linkinfo"`
		Addrs []struct {
			Local string `json:"local"`
			Scope string `json:"scope"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(raw, &links); err != nil {
		return s, fmt.Errorf("ip -j addr: %w", err)
	}
	candidates := map[string]int{}
	kinds := map[int][2]string{} // kind, parent name
	for _, l := range links {
		s.links[l.Index] = true
		up, loopback := false, false
		for _, f := range l.Flags {
			up = up || f == "UP"
			loopback = loopback || f == "LOOPBACK"
		}
		switch l.Name {
		case flvxRateIfbUp:
			s.upIdx, s.upOK = l.Index, up
			continue
		case flvxRateIfbDown:
			s.dnIdx, s.dnOK = l.Index, up
			continue
		}
		global := false
		for _, a := range l.Addrs {
			if ip := net.ParseIP(a.Local); a.Scope == "global" && ip != nil && ip.IsGlobalUnicast() {
				global = true
			}
		}
		if loopback || !up || l.LinkInfo.Kind == "ifb" || l.Master != "" || !global {
			continue
		}
		candidates[l.Name] = l.Index
		kinds[l.Index] = [2]string{l.LinkInfo.Kind, l.Link}
	}
	for name, idx := range candidates {
		// A VLAN (or macvlan) on top of another selected device sends through
		// it: shaping both would count its packets twice.
		switch kinds[idx][0] {
		case "vlan", "macvlan", "macvtap", "ipvlan":
			if _, ok := candidates[kinds[idx][1]]; ok {
				continue
			}
		}
		s.devs[idx] = name
	}
	return s, nil
}

// hookQdiscs maps every device with a qdisc on its ingress slot to its kind:
// "clsact", or "ingress" (legacy, another program's: no egress hook).
func (r *flvxRateLimiter) hookQdiscs(ctx context.Context) (map[string]string, error) {
	raw, err := r.run(ctx, nil, "tc", "-j", "qdisc", "show")
	if err != nil {
		return nil, err
	}
	var qdiscs []struct {
		Kind   string `json:"kind"`
		Dev    string `json:"dev"`
		Parent string `json:"parent"`
	}
	if err := json.Unmarshal(raw, &qdiscs); err != nil {
		return nil, fmt.Errorf("tc -j qdisc: %w", err)
	}
	out := map[string]string{}
	for _, q := range qdiscs {
		if q.Parent == "ffff:fff1" && (q.Kind == "clsact" || q.Kind == "ingress") {
			out[q.Dev] = q.Kind
		}
	}
	return out, nil
}

// removeDev deletes our filters on one device (whole tcf_protos at our
// prefs) and its clsact when nothing else is left on it.
func (r *flvxRateLimiter) removeDev(ctx context.Context, dev, kind string) error {
	hooks := []string{"ingress"}
	if kind == "clsact" {
		hooks = append(hooks, "egress")
	}
	var lines []string
	others := 0
	for _, hook := range hooks {
		raw, err := r.run(ctx, nil, "tc", "-j", "filter", "show", "dev", dev, hook)
		if err != nil {
			return err
		}
		var filters []struct {
			Protocol string `json:"protocol"`
			Pref     int    `json:"pref"`
		}
		if err := json.Unmarshal(raw, &filters); err != nil {
			return fmt.Errorf("tc -j filter: %w", err)
		}
		ours := map[string]bool{}
		for _, f := range filters {
			if f.Pref != flvxRatePrefV4 && f.Pref != flvxRatePrefV6 {
				others++
				continue
			}
			line := fmt.Sprintf("filter del dev %s %s protocol %s pref %d", dev, hook, f.Protocol, f.Pref)
			if !ours[line] {
				ours[line] = true
				lines = append(lines, line)
			}
		}
	}
	if kind == "clsact" && others == 0 {
		lines = append(lines, "qdisc del dev "+dev+" clsact")
	}
	return r.tcBatch(ctx, lines)
}

func (r *flvxRateLimiter) tcBatch(ctx context.Context, lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	_, err := r.run(ctx, []byte(strings.Join(lines, "\n")+"\n"), "tc", "-batch", "-")
	return err
}

// teardown removes every object of ours: filters on all devices first, then
// the IFBs (only when every filter is gone: never a redirect into nothing).
// start also removes Helper v0.7.15's nftables policer.
func (r *flvxRateLimiter) teardown(ctx context.Context, start bool) error {
	if start {
		// Absent table or no nft at all is fine; only our own table is deleted.
		_, _ = r.run(ctx, nil, "nft", "delete", "table", "inet", flvxRateNftTable)
	}
	hooked, err := r.hookQdiscs(ctx)
	if err != nil {
		return err
	}
	devs := make([]string, 0, len(hooked))
	for dev := range hooked {
		if dev != flvxRateIfbUp && dev != flvxRateIfbDown {
			devs = append(devs, dev)
		}
	}
	sort.Strings(devs)
	var errs []error
	for _, dev := range devs {
		if err := r.removeDev(ctx, dev, hooked[dev]); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	scan, err := r.scan(ctx)
	if err != nil {
		return err
	}
	var lines []string
	if scan.upIdx != 0 {
		lines = append(lines, "link del "+flvxRateIfbUp)
	}
	if scan.dnIdx != 0 {
		lines = append(lines, "link del "+flvxRateIfbDown)
	}
	if len(lines) > 0 {
		if _, err := r.run(ctx, []byte(strings.Join(lines, "\n")+"\n"), "ip", "-batch", "-"); err != nil {
			return err
		}
	}
	r.active, r.up, r.dn = false, 0, 0
	r.owners, r.slots, r.classes, r.devs, r.releasing = nil, nil, nil, nil, nil
	return nil
}

// setup creates the IFB pair with their root HTBs.
func (r *flvxRateLimiter) setup(ctx context.Context) error {
	if r.loadModule != nil {
		r.loadModule()
	}
	var links, roots []string
	for _, ifb := range []string{flvxRateIfbUp, flvxRateIfbDown} {
		links = append(links, "link add "+ifb+" txqueuelen 1000 type ifb")
		roots = append(roots, fmt.Sprintf("qdisc replace dev %s root handle %x: htb default 0", ifb, flvxRateMajor))
	}
	links = append(links, "link set "+flvxRateIfbUp+" up", "link set "+flvxRateIfbDown+" up")
	if _, err := r.run(ctx, []byte(strings.Join(links, "\n")+"\n"), "ip", "-batch", "-"); err != nil {
		return fmt.Errorf("创建 IFB 设备失败（内核可能不支持 ifb）: %w", err)
	}
	if err := r.tcBatch(ctx, roots); err != nil {
		return fmt.Errorf("创建 HTB 失败: %w", err)
	}
	scan, err := r.scan(ctx)
	if err != nil {
		return err
	}
	if !scan.upOK || !scan.dnOK {
		return errors.New("IFB 设备没有启用")
	}
	r.up, r.dn, r.active = scan.upIdx, scan.dnIdx, true
	r.slots, r.classes, r.devs, r.releasing = map[string]uint16{}, map[uint16]uint64{}, map[int]*flvxRateDev{}, map[uint16]time.Time{}
	return nil
}

// release removes everything at once (the share is removed).
func (r *flvxRateLimiter) release(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if err := r.teardown(ctx, !r.known); err != nil {
		return err
	}
	r.known, r.failure = true, nil
	return nil
}

// ensure puts the kernel in the wanted state with as few calls as possible:
// none in steady state apart from the device check once a minute while a
// rate is in force. Any failure removes everything (fail open, no redirect
// into nothing) and is retried when the owners change or a minute later.
func (r *flvxRateLimiter) ensure(ctx context.Context, owners []flvxRateOwner) error {
	if len(owners) == 0 {
		owners = nil
	}
	now := r.now()
	if r.failure != nil && reflect.DeepEqual(owners, r.failedOwners) && now.Sub(r.failedAt) < flvxRateRecheck {
		return r.failure
	}
	if !r.known {
		// Objects of an earlier process: remove them, build from scratch once.
		if err := r.teardown(ctx, true); err != nil {
			r.failure, r.failedAt, r.failedOwners = err, now, owners
			return err
		}
		r.known = true
	}
	if !r.active && owners == nil {
		r.failure = nil
		return nil
	}
	if r.failure == nil && r.active && len(r.releasing) == 0 && reflect.DeepEqual(owners, r.owners) && now.Sub(r.checkedAt) < flvxRateRecheck {
		return nil
	}
	err := r.reconcile(ctx, owners, now)
	if err != nil {
		if terr := r.teardown(ctx, false); terr != nil {
			r.known = false
			err = errors.Join(err, terr)
		}
		r.failure, r.failedAt, r.failedOwners = err, now, owners
		return err
	}
	r.failure = nil
	return nil
}

func (r *flvxRateLimiter) reconcile(ctx context.Context, owners []flvxRateOwner, now time.Time) error {
	scan, err := r.scan(ctx)
	if err != nil {
		return err
	}
	r.checkedAt = now
	if r.active && (!scan.upOK || !scan.dnOK || scan.upIdx != r.up || scan.dnIdx != r.dn) {
		// Someone removed or downed an IFB: a filter may be dropping the
		// owner's packets now. Start over, filters first.
		if err := r.teardown(ctx, false); err != nil {
			return err
		}
	}
	if !r.active {
		if owners == nil {
			return nil
		}
		if err := r.setup(ctx); err != nil {
			return err
		}
	}
	if err := r.sync(ctx, owners, scan, now); err != nil {
		return err
	}
	r.owners = owners
	if owners == nil && len(r.classes) == 0 {
		return r.teardown(ctx, false)
	}
	return nil
}

// sync applies the difference between the kernel (as recorded) and owners
// in one tc batch: stale filters first, then classes, then new filters.
// State is recorded before the batch runs; a failed batch makes ensure tear
// everything down, so the record is never trusted after a failure.
func (r *flvxRateLimiter) sync(ctx context.Context, owners []flvxRateOwner, scan flvxRateScan, now time.Time) error {
	wantClasses := map[uint16]uint64{}
	wantFilters := map[flvxRateKey]uint16{}
	for _, o := range owners {
		slot, ok := r.slots[o.Owner]
		if !ok {
			if slot = r.freeSlot(); slot == 0 {
				return errors.New("限速用户太多")
			}
			r.slots[o.Owner] = slot
		}
		wantClasses[slot] = flvxRateWire(o.Bytes)
		for _, set := range []struct {
			udp   bool
			ports []int
		}{{false, o.TCP}, {true, o.UDP}} {
			for _, p := range set.ports {
				for _, egress := range []bool{false, true} {
					for _, v6 := range []bool{false, true} {
						wantFilters[flvxRateKey{egress: egress, v6: v6, udp: set.udp, port: uint16(p)}] = slot
					}
				}
			}
		}
	}
	var lines []string
	// 1. Filters that go: devices no longer selected, ports no longer limited.
	idxs := make([]int, 0, len(r.devs))
	for idx := range r.devs {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)
	for _, idx := range idxs {
		d := r.devs[idx]
		if _, selected := scan.devs[idx]; !selected {
			if scan.links[idx] {
				hooked, err := r.hookQdiscs(ctx)
				if err != nil {
					return err
				}
				if kind := hooked[d.name]; kind != "" {
					if err := r.removeDev(ctx, d.name, kind); err != nil {
						return err
					}
				}
			}
			delete(r.devs, idx)
			continue
		}
		for _, k := range sortedFLVXRateKeys(d.filters) {
			slot := d.filters[k]
			if _, ok := wantFilters[k]; ok {
				continue // kept, or moved to another owner in step 3
			}
			// A removed node's port goes at once; the ports of an owner whose
			// rate went stay until its release is over.
			if _, wanted := wantClasses[slot]; !wanted {
				if since, releasing := r.releasing[slot]; !releasing || now.Sub(since) < flvxRateDrain {
					continue
				}
			}
			lines = append(lines, flvxRateFilterDelLine(d.name, k))
			delete(d.filters, k)
		}
	}
	// 2. Classes: those no longer wanted are released first (unlimited, the
	// queue leaves at once), deleted (with their leaf) once released long
	// enough and without filters; new ones created, changed ones in place.
	for _, slot := range sortedFLVXRateSlots(r.classes) {
		rate := r.classes[slot]
		if _, ok := wantClasses[slot]; ok {
			delete(r.releasing, slot)
			continue
		}
		since, ok := r.releasing[slot]
		if !ok {
			if rate != flvxRateUnlimited {
				lines = append(lines, flvxRateClassLines(slot, flvxRateUnlimited, true)...)
				r.classes[slot] = flvxRateUnlimited
			}
			r.releasing[slot] = now
			continue
		}
		if now.Sub(since) < flvxRateDrain || r.slotHasFilters(slot) {
			continue
		}
		for _, ifb := range []string{flvxRateIfbUp, flvxRateIfbDown} {
			lines = append(lines, fmt.Sprintf("class del dev %s parent %x: classid %s", ifb, flvxRateMajor, flvxRateClassID(slot)))
		}
		delete(r.classes, slot)
		delete(r.releasing, slot)
		for owner, s := range r.slots {
			if s == slot {
				delete(r.slots, owner)
			}
		}
	}
	for _, slot := range sortedFLVXRateSlots(wantClasses) {
		rate := wantClasses[slot]
		cur, exists := r.classes[slot]
		if exists && cur == rate {
			continue
		}
		lines = append(lines, flvxRateClassLines(slot, rate, exists)...)
		r.classes[slot] = rate
	}
	// 3. New filters on every selected device.
	keys := sortedFLVXRateKeys(wantFilters)
	var hooked map[string]string
	for _, idx := range sortedFLVXRateDevs(scan.devs) {
		d := r.devs[idx]
		if d == nil {
			if len(keys) == 0 {
				continue
			}
			if hooked == nil {
				var err error
				if hooked, err = r.hookQdiscs(ctx); err != nil {
					return err
				}
			}
			switch hooked[scan.devs[idx]] {
			case "ingress":
				return fmt.Errorf("网卡 %s 已有其他程序的 ingress qdisc，无法在这里限速", scan.devs[idx])
			case "":
				lines = append(lines, "qdisc add dev "+scan.devs[idx]+" clsact")
			}
			d = &flvxRateDev{name: scan.devs[idx], filters: map[flvxRateKey]uint16{}}
			r.devs[idx] = d
		}
		for _, k := range keys {
			slot := wantFilters[k]
			if d.filters[k] == slot {
				continue
			}
			lines = append(lines, flvxRateFilterLine(d.name, k, slot))
			d.filters[k] = slot
		}
	}
	return r.tcBatch(ctx, lines)
}

func (r *flvxRateLimiter) freeSlot() uint16 {
	used := map[uint16]bool{}
	for _, s := range r.slots {
		used[s] = true
	}
	for s := range r.classes {
		used[s] = true
	}
	for s := uint16(1); s <= flvxRateMaxSlot; s++ {
		if !used[s] {
			return s
		}
	}
	return 0
}

func (r *flvxRateLimiter) slotHasFilters(slot uint16) bool {
	for _, d := range r.devs {
		for _, s := range d.filters {
			if s == slot {
				return true
			}
		}
	}
	return false
}

func sortedFLVXRateKeys(m map[flvxRateKey]uint16) []flvxRateKey {
	keys := make([]flvxRateKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return flvxRateKeyLess(keys[i], keys[j]) })
	return keys
}

func sortedFLVXRateSlots(m map[uint16]uint64) []uint16 {
	slots := make([]uint16, 0, len(m))
	for s := range m {
		slots = append(slots, s)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
	return slots
}

func sortedFLVXRateDevs(m map[int]string) []int {
	idxs := make([]int, 0, len(m))
	for idx := range m {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)
	return idxs
}

// flvxRateProbeScript builds a class and a filter of each family exactly as
// sync does, inside a throwaway network namespace, and prints what the
// parsers read back.
func flvxRateProbeScript() string {
	probe := []string{fmt.Sprintf("qdisc replace dev %s root handle %x: htb default 0", flvxRateIfbUp, flvxRateMajor), fmt.Sprintf("qdisc replace dev %s root handle %x: htb default 0", flvxRateIfbDown, flvxRateMajor)}
	probe = append(probe, flvxRateClassLines(1, flvxRateWire(flvx.RateBytesPerMbps), false)...)
	probe = append(probe, "qdisc add dev lo clsact",
		flvxRateFilterLine("lo", flvxRateKey{port: 1}, 1),
		flvxRateFilterLine("lo", flvxRateKey{egress: true, v6: true, udp: true, port: 1}, 1))
	return "set -e\n" +
		"ip link set lo up\n" +
		"ip link add " + flvxRateIfbUp + " txqueuelen 1000 type ifb\n" +
		"ip link add " + flvxRateIfbDown + " txqueuelen 1000 type ifb\n" +
		"ip link set " + flvxRateIfbUp + " up\nip link set " + flvxRateIfbDown + " up\n" +
		"tc -batch - <<'EOF'\n" + strings.Join(probe, "\n") + "\nEOF\n" +
		"tc -j filter show dev lo egress\n"
}

// supported probes once (and again every flvxRateProbeRetry after a failure)
// whether ip, tc and the kernel take these objects: flvx.user_rate.v1 is
// advertised only then.
func (r *flvxRateLimiter) supported(ctx context.Context) bool {
	if r == nil {
		return false
	}
	r.probeMu.Lock()
	defer r.probeMu.Unlock()
	now := r.now()
	if r.probed && (r.probeOK || now.Sub(r.probedAt) < flvxRateProbeRetry) {
		return r.probeOK
	}
	r.probed, r.probedAt = true, now
	if r.loadModule != nil {
		r.loadModule()
	}
	child, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := r.run(child, nil, "unshare", "--net", "sh", "-c", flvxRateProbeScript())
	var filters []struct {
		Protocol string `json:"protocol"`
		Pref     int    `json:"pref"`
	}
	r.probeOK = err == nil && json.Unmarshal(raw, &filters) == nil && len(filters) > 0 && filters[0].Pref == flvxRatePrefV6 && filters[0].Protocol == "ipv6"
	return r.probeOK
}

// rateOwners are the owners with a 速率 in the budgets in force and the
// ports of their nodes that run in the FLVX Core now.
func (m *flvxManager) rateOwners() []flvxRateOwner {
	view := m.budgetView()
	if view == nil || m.removed || m.state.Payload.Status != "active" {
		return nil
	}
	byOwner := map[string]*flvxRateOwner{}
	for _, n := range m.state.Payload.Desired.Nodes {
		b, ok := view.users[n.Owner.ID]
		if !ok || b.MaxRateMbps == nil || *b.MaxRateMbps <= 0 || m.state.Applied[n.Tag()] == "" || n.Port <= 0 || n.Port > 65535 {
			continue
		}
		o := byOwner[string(n.Owner.ID)]
		if o == nil {
			o = &flvxRateOwner{Owner: string(n.Owner.ID), Bytes: int64(*b.MaxRateMbps) * flvx.RateBytesPerMbps}
			byOwner[o.Owner] = o
		}
		o.TCP = append(o.TCP, n.Port)
		if flvx.UDPProtocol(n.Protocol) {
			o.UDP = append(o.UDP, n.Port)
		}
	}
	owners := make([]flvxRateOwner, 0, len(byOwner))
	for _, o := range byOwner {
		sort.Ints(o.TCP)
		sort.Ints(o.UDP)
		owners = append(owners, *o)
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i].Owner < owners[j].Owner })
	return owners
}

// reconcileRates runs after every pass on the pass's goroutine.
func (m *flvxManager) reconcileRates(ctx context.Context) {
	if m.rate == nil {
		return
	}
	owners := m.rateOwners()
	if len(owners) > 0 && !m.rate.supported(ctx) {
		owners = nil
	}
	child, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := m.rate.ensure(child, owners)
	cancel()
	m.logs.report("FLVX user rate", err)
	rates := map[string]bool{}
	if err == nil {
		for _, o := range owners {
			rates[o.Owner] = true
		}
	}
	m.mu.Lock()
	m.userLimits.rates = rates
	m.mu.Unlock()
}

// rateCapable is whether this Helper advertises flvx.user_rate.v1.
func (m *flvxManager) rateCapable(ctx context.Context) bool {
	return m != nil && m.rate.supported(ctx)
}
