package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// Fastest-IP Reality dest pin (contract reality-pick-v1 §3). REALITY dials
// its dest for every new connection and waits for the dest's ServerHello
// before it answers the client, so the dest RTT is added to every
// handshake. The Helper pins each node's dest to the public IP with the
// lowest TCP connect time from this server instead of the first sorted DNS
// answer (which picked a far CloudFront edge on Boil Hinet 马年: +100–150 ms
// per new connection).
//
//   - Candidates per dest: the IPs nodes are pinned to, the current DNS answer
//     and IPs DNS returned in the last week, at most 8, all public. A DNS
//     answer with any private address is ignored (DNS rebinding protection).
//   - Full check (one DNS lookup, one parallel TCP connect per candidate, 1 s
//     timeout) on Helper start, on a Core restart, every 6 h and after a
//     failed liveness check; a liveness check (one connect per pinned IP)
//     every 10 min in between.
//   - A node moves only if its IP failed or is >= 20 ms slower than the best.
//     A guarded node moves with its whole guard group: only the loopback
//     guard inbound is re-added with the new target, the node inbound and its
//     live connections stay. An unguarded node moves while it has no
//     connections or when its inbound is re-added anyway (Core restart, node
//     change), unless its IP failed.
//   - No candidate answers: a new node gets the first sorted DNS answer as
//     before; a pinned node keeps its pin.
const (
	flvxPinRecheck    = 6 * time.Hour
	flvxPinLiveness   = 10 * time.Minute
	flvxPinFresh      = 10 * time.Minute
	flvxPinRetry      = time.Minute
	flvxPinHysteresis = 20.0 // ms
	flvxPinDial       = time.Second
	flvxPinDNS        = 2 * time.Second
	flvxPinRemember   = 7 * 24 * time.Hour
	flvxPinParallel   = 4
)

// flvxRealityPin is a node's pin. Hash is the node JSON digest older Helpers
// (<= v0.7.21) key the pin on; it is kept current so a downgrade reuses it.
type flvxRealityPin struct {
	Hash     string `json:"hash"`
	Dest     string `json:"dest"`
	Host     string `json:"host,omitempty"`
	Fallback bool   `json:"fallback,omitempty"`
}

// flvxRealityDest is the check state of one dest ("host:port").
type flvxRealityDest struct {
	CheckedAt int64              `json:"checked_at"`
	LiveAt    int64              `json:"live_at,omitempty"`
	MS        map[string]float64 `json:"ms,omitempty"`   // TCP connect ms at the last full check, -1 = no answer
	DNS       []string           `json:"dns,omitempty"`  // last all-public answer, sorted
	Seen      map[string]int64   `json:"seen,omitempty"` // public IPs DNS returned -> last seen (unix ms)
	Due       bool               `json:"due,omitempty"`  // a pinned IP failed its liveness check
}

func (d flvxRealityDest) best() (string, float64) {
	ip, ms := "", -1.0
	for candidate, v := range d.MS {
		if v >= 0 && (ip == "" || v < ms || v == ms && candidate < ip) {
			ip, ms = candidate, v
		}
	}
	return ip, ms
}

func flvxValidRealityPin(dest string) bool {
	host, _, err := net.SplitHostPort(dest)
	return err == nil && net.ParseIP(host) != nil && !flvx.PrivateHost(host)
}

func flvxNodeHash(n flvx.Node) string {
	raw, _ := json.Marshal(n)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func flvxPinIP(pin flvxRealityPin) string {
	host, _, _ := net.SplitHostPort(pin.Dest)
	return host
}

// realityPin returns the node's pin when it belongs to the node's current
// dest; a pre-contract pin is adopted when its node digest still matches.
func (m *flvxManager) realityPin(n flvx.Node) (flvxRealityPin, bool) {
	pin, ok := m.state.RealityPins[n.Tag()]
	if !ok || !flvxValidRealityPin(pin.Dest) {
		return pin, false
	}
	dest := n.VLESS.Reality.Dest
	return pin, pin.Host == dest || pin.Host == "" && pin.Hash == flvxNodeHash(n)
}

// pinnedReality renders a VLESS node with its pinned dest. A node without a
// pin for its dest is checked right away (first sight outside a pass).
func (m *flvxManager) pinnedReality(ctx context.Context, n flvx.Node) (flvx.Node, error) {
	if n.VLESS == nil {
		return n, nil
	}
	pin, ok := m.realityPin(n)
	if !ok {
		m.pinNodes(ctx, []flvx.Node{n}, false)
		if pin, ok = m.realityPin(n); !ok {
			return n, errors.New("Reality destination is unavailable or resolves to a private address")
		}
	}
	v := *n.VLESS
	v.Reality.Dest = pin.Dest
	n.VLESS = &v
	return n, nil
}

// realityNodes are the share's VLESS nodes this pass may render.
func (m *flvxManager) realityNodes() []flvx.Node {
	p := m.state.Payload
	nodes := []flvx.Node{}
	for _, n := range p.Desired.Nodes {
		if n.VLESS != nil && flvx.Active(p.Status, p.Config, m.state.Report.TrafficUsedBytes, n, m.now()) {
			nodes = append(nodes, n)
		}
	}
	return nodes
}

// refreshRealityPins is the pass's dest upkeep: drop the pins of deleted
// nodes and unused dests, run the due checks and re-pin. It never fails the
// pass; epoch is the running Core's.
func (m *flvxManager) refreshRealityPins(ctx context.Context, epoch string) {
	known := map[string]bool{}
	dests := map[string]bool{}
	for _, n := range m.state.Payload.Desired.Nodes {
		if n.VLESS != nil {
			known[n.Tag()] = true
			dests[n.VLESS.Reality.Dest] = true
		}
	}
	for tag := range m.state.RealityPins {
		if !known[tag] {
			delete(m.state.RealityPins, tag)
		}
	}
	for dest := range m.state.RealityDests {
		if !dests[dest] {
			delete(m.state.RealityDests, dest)
		}
	}
	restart := m.pinEpoch != epoch
	m.pinNodes(ctx, m.realityNodes(), restart)
	m.pinEpoch = epoch
}

// pinNodes checks the dests of nodes that are due (all of them when full)
// and decides each node's pin.
func (m *flvxManager) pinNodes(ctx context.Context, nodes []flvx.Node, full bool) {
	now := m.now()
	byDest := map[string][]flvx.Node{}
	for _, n := range nodes {
		byDest[n.VLESS.Reality.Dest] = append(byDest[n.VLESS.Reality.Dest], n)
	}
	type job struct {
		dest   string
		pinned []string
		live   bool
	}
	jobs := []job{}
	for dest, group := range byDest {
		host, _, err := net.SplitHostPort(dest)
		if err != nil || net.ParseIP(host) != nil {
			continue
		}
		d := m.state.RealityDests[dest]
		pinned, unpinned := []string{}, false
		seen := map[string]bool{}
		for _, n := range group {
			pin, ok := m.realityPin(n)
			if !ok {
				unpinned = true
				continue
			}
			if ip := flvxPinIP(pin); !seen[ip] {
				seen[ip] = true
				pinned = append(pinned, ip)
			}
		}
		sort.Strings(pinned)
		age := now.Sub(time.UnixMilli(d.CheckedAt))
		bestIP, _ := d.best()
		switch {
		case full || d.Due || d.CheckedAt == 0 || age >= flvxPinRecheck || age < 0,
			unpinned && (bestIP == "" && age >= flvxPinRetry || age >= flvxPinFresh):
			jobs = append(jobs, job{dest: dest, pinned: pinned})
		case len(pinned) > 0 && now.Sub(time.UnixMilli(max(d.LiveAt, d.CheckedAt))) >= flvxPinLiveness:
			jobs = append(jobs, job{dest: dest, pinned: pinned, live: true})
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].dest < jobs[j].dest })
	results := make([]flvxRealityDest, len(jobs))
	dnsErrs := make([]error, len(jobs))
	sem := make(chan struct{}, flvxPinParallel)
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, j job) {
			defer wg.Done()
			defer func() { <-sem }()
			d := m.state.RealityDests[j.dest]
			if j.live {
				_, port, _ := net.SplitHostPort(j.dest)
				failed := false
				for _, ms := range m.raceTCP(ctx, j.pinned, port) {
					failed = failed || ms < 0
				}
				d.LiveAt = now.UnixMilli()
				if !failed {
					results[i] = d
					return
				}
			}
			results[i], dnsErrs[i] = m.checkRealityDest(ctx, j.dest, d, j.pinned, now)
		}(i, j)
	}
	wg.Wait()
	// The log limiter is not safe for concurrent use: report after the checks.
	if len(jobs) > 0 {
		var dnsErr error
		for i, err := range dnsErrs {
			if err != nil && dnsErr == nil {
				dnsErr = fmt.Errorf("%s: %w", jobs[i].dest, err)
			}
		}
		m.logs.report("FLVX Reality destination DNS", dnsErr)
	}
	if len(jobs) > 0 && m.state.RealityDests == nil {
		m.state.RealityDests = map[string]flvxRealityDest{}
	}
	for i, j := range jobs {
		m.state.RealityDests[j.dest] = results[i]
	}
	for _, n := range nodes {
		m.decidePin(n)
	}
}

// checkRealityDest is one full check: DNS, candidates, parallel connects. It
// runs concurrently for several dests and only returns the DNS error.
func (m *flvxManager) checkRealityDest(ctx context.Context, dest string, d flvxRealityDest, pinned []string, now time.Time) (flvxRealityDest, error) {
	host, port, _ := net.SplitHostPort(dest)
	lookup := m.lookupHost
	if lookup == nil {
		lookup = net.DefaultResolver.LookupHost
	}
	child, cancel := context.WithTimeout(ctx, flvxPinDNS)
	ips, err := lookup(child, host)
	cancel()
	answer := []string{}
	for _, ip := range ips {
		if net.ParseIP(ip) == nil || flvx.PrivateHost(ip) {
			// DNS rebinding protection: an answer with any private address
			// is not used at all.
			err, answer = errors.New("destination resolves to a non-public address"), nil
			break
		}
		answer = append(answer, net.ParseIP(ip).String())
	}
	if err == nil && len(answer) == 0 {
		err = errors.New("no addresses")
	}
	seen := map[string]int64{}
	for ip, at := range d.Seen {
		if now.Sub(time.UnixMilli(at)) < flvxPinRemember && !flvx.PrivateHost(ip) {
			seen[ip] = at
		}
	}
	if err == nil {
		sort.Strings(answer)
		d.DNS = answer
		for _, ip := range answer {
			seen[ip] = now.UnixMilli()
		}
	}
	// Pinned first, then this answer, then the most recently seen others.
	candidates, added := []string{}, map[string]bool{}
	add := func(ip string) {
		if len(candidates) < flvx.ProbeMaxIPs && !added[ip] && net.ParseIP(ip) != nil && !flvx.PrivateHost(ip) {
			added[ip] = true
			candidates = append(candidates, ip)
		}
	}
	for _, ip := range pinned {
		add(ip)
	}
	if err == nil {
		for _, ip := range answer {
			add(ip)
		}
	}
	rest := make([]string, 0, len(seen))
	for ip := range seen {
		rest = append(rest, ip)
	}
	sort.Slice(rest, func(i, j int) bool {
		if seen[rest[i]] != seen[rest[j]] {
			return seen[rest[i]] > seen[rest[j]]
		}
		return rest[i] < rest[j]
	})
	for _, ip := range rest {
		add(ip)
	}
	// Keep only what is still a candidate, so the state stays bounded.
	d.Seen = map[string]int64{}
	for _, ip := range candidates {
		if at, ok := seen[ip]; ok {
			d.Seen[ip] = at
		}
	}
	d.MS = m.raceTCP(ctx, candidates, port)
	d.CheckedAt, d.LiveAt, d.Due = now.UnixMilli(), now.UnixMilli(), false
	return d, err
}

// raceTCP connects to every ip:port in parallel (1 s timeout each) and
// returns the connect times in ms, -1 for no answer.
func (m *flvxManager) raceTCP(ctx context.Context, ips []string, port string) map[string]float64 {
	connect := m.connectTime
	if connect == nil {
		connect = flvxConnectTime
	}
	out := make(map[string]float64, len(ips))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, ip := range ips {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			child, cancel := context.WithTimeout(ctx, flvxPinDial)
			elapsed, err := connect(child, net.JoinHostPort(ip, port))
			cancel()
			ms := -1.0
			if err == nil {
				ms = math.Round(float64(elapsed)/float64(time.Millisecond)*10) / 10
			}
			mu.Lock()
			out[ip] = ms
			mu.Unlock()
		}(ip)
	}
	wg.Wait()
	return out
}

func flvxConnectTime(ctx context.Context, address string) (time.Duration, error) {
	start := time.Now()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return 0, err
	}
	elapsed := time.Since(start)
	conn.Close()
	return elapsed, nil
}

// decidePin sets or keeps one node's pin from its dest's last check.
func (m *flvxManager) decidePin(n flvx.Node) {
	tag, dest := n.Tag(), n.VLESS.Reality.Dest
	host, port, err := net.SplitHostPort(dest)
	if err != nil {
		return
	}
	if m.state.RealityPins == nil {
		m.state.RealityPins = map[string]flvxRealityPin{}
	}
	hash := flvxNodeHash(n)
	if net.ParseIP(host) != nil {
		if !flvx.PrivateHost(host) {
			m.state.RealityPins[tag] = flvxRealityPin{Hash: hash, Dest: dest, Host: dest}
		}
		return
	}
	d := m.state.RealityDests[dest]
	best, bestMS := d.best()
	pin, ok := m.realityPin(n)
	if !ok {
		switch {
		case best != "":
			m.state.RealityPins[tag] = flvxRealityPin{Hash: hash, Dest: net.JoinHostPort(best, port), Host: dest}
		case len(d.DNS) > 0:
			// No candidate answered: the old behaviour.
			m.state.RealityPins[tag] = flvxRealityPin{Hash: hash, Dest: net.JoinHostPort(d.DNS[0], port), Host: dest, Fallback: true}
		}
		// Otherwise keep the old entry: wanted() retains the applied inbound.
		return
	}
	pin.Hash, pin.Host = hash, dest
	current := flvxPinIP(pin)
	if best != "" && best != current {
		ms, measured := d.MS[current]
		failed := measured && ms < 0
		slower := measured && ms-bestMS >= flvxPinHysteresis
		if failed || (slower || pin.Fallback) && m.pinMoveFree(n) {
			m.logs.print("[mmwxc-helper] FLVX Reality dest %s: node %s %s (%s) -> %s (%.1f ms)", dest, n.ID, current, flvxPinMSText(ms, measured), best, bestMS)
			pin.Dest, pin.Fallback = net.JoinHostPort(best, port), false
		}
	}
	m.state.RealityPins[tag] = pin
}

func flvxPinMSText(ms float64, measured bool) string {
	switch {
	case !measured:
		return "not measured"
	case ms < 0:
		return "no answer"
	}
	return fmt.Sprintf("%.1f ms", ms)
}

// pinMoveFree reports whether moving the node's pin keeps its live
// connections: not applied yet, guarded (only the guard target moves) or idle.
func (m *flvxManager) pinMoveFree(n flvx.Node) bool {
	applied := m.state.Applied[n.Tag()]
	if applied == "" || flvx.GuardPortOf(applied) > 0 {
		return true
	}
	for _, s := range m.state.Report.Nodes {
		if s.ID == n.ID {
			return s.Connections == 0
		}
	}
	return true
}

// retargetGuards moves a live guard group to its nodes' new pin in place: the
// guard keeps its loopback port, so the nodes pointing at it are not re-added
// and keep their connections; only the guard inbound is.
func (m *flvxManager) retargetGuards(candidates map[string]flvx.Guard) {
	byPort := map[int]map[string]bool{}
	for tag, guard := range candidates {
		port := flvx.GuardPortOf(m.state.Applied[tag])
		if old, ok := m.state.Guards[port]; port > 0 && ok && strings.Join(old.Names, ",") == strings.Join(guard.Names, ",") {
			if byPort[port] == nil {
				byPort[port] = map[string]bool{}
			}
			byPort[port][guard.Key()] = true
		}
	}
	// Every applied node on the port must move the same way, retained or
	// inactive ones included.
	for tag, config := range m.state.Applied {
		if port := flvx.GuardPortOf(config); strings.HasPrefix(tag, "flvxl-") && byPort[port] != nil {
			if _, ok := candidates[tag]; !ok {
				delete(byPort, port)
			}
		}
	}
	for port, keys := range byPort {
		if len(keys) != 1 {
			continue
		}
		for tag, guard := range candidates {
			if flvx.GuardPortOf(m.state.Applied[tag]) == port && m.state.Guards[port].Key() != guard.Key() {
				m.state.Guards[port] = guard
				break
			}
		}
	}
}

// realityPinStatus is the share report's reality_pins: one entry per (dest,
// pinned IP) of the active nodes.
func (m *flvxManager) realityPinStatus() []flvx.RealityPinStatus {
	type key struct{ dest, ip string }
	groups := map[key]*flvx.RealityPinStatus{}
	for _, n := range m.realityNodes() {
		pin, ok := m.realityPin(n)
		if !ok {
			continue
		}
		dest := n.VLESS.Reality.Dest
		k := key{dest, flvxPinIP(pin)}
		s := groups[k]
		if s == nil {
			d := m.state.RealityDests[dest]
			s = &flvx.RealityPinStatus{Host: dest, PinnedIP: k.ip, PinnedMS: -1, BestMS: -1, CheckedAt: d.CheckedAt, Fallback: pin.Fallback, Nodes: []string{}}
			if ms, ok := d.MS[k.ip]; ok {
				s.PinnedMS = ms
			}
			if best, ms := d.best(); best != "" {
				s.BestIP, s.BestMS = best, ms
			}
			groups[k] = s
		}
		s.Nodes = append(s.Nodes, n.ID)
	}
	out := make([]flvx.RealityPinStatus, 0, len(groups))
	for _, s := range groups {
		sort.Strings(s.Nodes)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].PinnedIP < out[j].PinnedIP
	})
	if len(out) == 0 {
		return nil
	}
	return out
}
