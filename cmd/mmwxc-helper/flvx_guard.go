package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// Reality 防盗 (contract landing-reality-guard-v1) in the shared FLVX Core.
// Each pass, in the order FLVX uses on its own landings: the guard rules go
// into the rule set first (applyEgress), then each needed guard inbound is
// added, and only a node whose guard listens and whose rules are live is
// pointed at it; guards no node points at any more are removed last. Every
// failure leaves the node on its real (pinned) dest, as before the contract
// (fail-open), and is reported on the share card.
const (
	flvxGuardCapability = "flvx.reality_guard.v1"
	// A guard inbound that failed to listen is retried on a fresh port after
	// this delay; nodes keep their real dest meanwhile.
	flvxGuardRetry = time.Minute
)

// guardPass is the guard plan of the current reconcile pass; retryAt, avoid
// and addIssue live across passes.
type guardPass struct {
	byTag      map[string]int       // candidate node tag -> its group's port
	rules      map[int]flvx.Guard   // groups rendered into the rule set this pass
	pinned     map[string]flvx.Node // nodes rendered fresh this pass (pinned dest)
	retained   map[string]bool      // nodes whose applied config was kept as is
	restart    bool                 // the running Core has no RoutingService
	rulesIssue string               // why this pass could not apply the rule set
	addIssue   string               // last guard inbound failure
	retryAt    time.Time            // no new guard inbound before this
	avoid      map[int]bool         // ports a guard inbound failed to listen on
}

// liveGuardPorts are the guard ports the applied node configs point at.
func (m *flvxManager) liveGuardPorts() map[int]bool {
	live := map[int]bool{}
	for tag, config := range m.state.Applied {
		if strings.HasPrefix(tag, "flvxl-") {
			if port := flvx.GuardPortOf(config); port > 0 {
				live[port] = true
			}
		}
	}
	return live
}

// planGuards groups the share's active VLESS Reality nodes by (pinned dest,
// serverNames) while FLVX asks for the guard, gives each group a loopback
// port and decides which groups' rules go into this pass's rule set: every
// candidate group, plus the groups applied nodes still point at until they
// are re-pointed. Ports no group uses any more are freed.
func (m *flvxManager) planGuards(ctx context.Context, official []int, officialErr error) {
	g := &m.guards
	g.byTag, g.rules, g.restart, g.rulesIssue = map[string]int{}, map[int]flvx.Guard{}, false, ""
	p := m.state.Payload
	candidates := p.Status == "active" && p.Desired.GuardOn()
	if !candidates && len(m.state.Guards) == 0 {
		g.addIssue, g.retryAt = "", time.Time{}
		return
	}
	reserved := map[int]bool{m.state.APIPort: true}
	if officialErr != nil {
		// wanted() admits no node without the official inventory either.
		candidates = false
	}
	for _, port := range append(official, p.OfficialPorts...) {
		reserved[port] = true
	}
	for _, n := range p.Desired.Nodes {
		reserved[n.Port] = true
	}
	// A group keeps its port unless the port now collides with the share
	// range, an official inbound, a node or the control API.
	conflict := func(port int) bool {
		return port < 1024 || port > 65535 || reserved[port] || port >= p.Config.PortMin && port <= p.Config.PortMax
	}
	live := m.liveGuardPorts()
	byKey := map[string]int{}
	for port, guard := range m.state.Guards {
		if conflict(port) {
			continue
		}
		// Two ports for one group only after a move; prefer the one in use.
		if other, ok := byKey[guard.Key()]; !ok || live[port] && !live[other] || live[port] == live[other] && port < other {
			byKey[guard.Key()] = port
		}
	}
	if candidates {
		for _, n := range p.Desired.Nodes {
			if n.Protocol != "vless_reality_vision" || n.VLESS == nil || !flvx.Active(p.Status, p.Config, m.state.Report.TrafficUsedBytes, n, m.now()) {
				continue
			}
			node, err := m.pinnedReality(ctx, n)
			if err != nil {
				continue
			}
			guard := flvx.Guard{Dest: node.VLESS.Reality.Dest, Names: flvx.GuardNames(node.VLESS.Reality.ServerNames)}
			port, ok := byKey[guard.Key()]
			if !ok {
				if port, err = m.allocateGuardPort(conflict); err != nil {
					m.logs.report("FLVX Reality guard port", err)
					g.addIssue = "没有可用的本机端口给 Reality 防盗辅助入站，相关节点暂按未开启处理"
					continue
				}
				if m.state.Guards == nil {
					m.state.Guards = map[int]flvx.Guard{}
				}
				m.state.Guards[port] = guard
				byKey[guard.Key()] = port
			}
			g.byTag[n.Tag()] = port
			g.rules[port] = guard
		}
	}
	for port := range live {
		if guard, ok := m.state.Guards[port]; ok {
			g.rules[port] = guard
		}
	}
	for port := range m.state.Guards {
		if _, ok := g.rules[port]; !ok {
			delete(m.state.Guards, port)
		}
	}
	if len(g.byTag) == 0 {
		g.addIssue, g.retryAt = "", time.Time{}
	}
}

// allocateGuardPort asks the kernel for a free loopback port outside every
// port the share, its nodes, the official inbounds and the control API use,
// and outside ports a guard failed on or still holds.
func (m *flvxManager) allocateGuardPort(conflict func(int) bool) (int, error) {
	pick := m.pickPort
	if pick == nil {
		pick = flvxLoopbackPort
	}
	held := map[int]bool{}
	for _, port := range m.state.GuardRulePorts {
		held[port] = true
	}
	for i := 0; i < 16; i++ {
		port, err := pick()
		if err != nil {
			return 0, err
		}
		_, used := m.state.Guards[port]
		if conflict(port) || used || held[port] || m.guards.avoid[port] || m.state.Applied[flvx.GuardTag(port)] != "" {
			continue
		}
		return port, nil
	}
	return 0, errors.New("no free loopback port for the Reality guard")
}

func flvxLoopbackPort() (int, error) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// guardConfig is the canonical JSON of a guard inbound, "" for an unknown port.
func (m *flvxManager) guardConfig(port int) string {
	guard, ok := m.state.Guards[port]
	if !ok {
		return ""
	}
	raw, _ := json.Marshal(guard.Inbound(port))
	return string(raw)
}

// guardNeed lists the guard ports this pass's node configs may use: the
// group of a fresh node once its rules are live (a new guard inbound only
// outside the retry delay), and the port a retained applied config still
// points at.
func (m *flvxManager) guardNeed(wanted map[string]string) map[int]bool {
	need, live := map[int]bool{}, map[int]bool{}
	for _, port := range m.state.GuardRulePorts {
		live[port] = true
	}
	for tag, config := range wanted {
		if m.guards.retained[tag] {
			if port := flvx.GuardPortOf(config); port > 0 {
				need[port] = true
			}
			continue
		}
		port, ok := m.guards.byTag[tag]
		if !ok || !live[port] || m.guardConfig(port) == "" {
			continue
		}
		if m.state.Applied[flvx.GuardTag(port)] == "" && m.now().Before(m.guards.retryAt) {
			continue
		}
		need[port] = true
	}
	return need
}

// guardFinal returns the configs this pass applies: nodes whose guard is up
// point at it, every other node keeps its real dest, plus the guard inbounds
// in use. A retained config whose guard cannot be brought back is withheld
// for this pass rather than served with a dead dest.
func (m *flvxManager) guardFinal(wanted, problems map[string]string, up map[int]bool) map[string]string {
	final := make(map[string]string, len(wanted)+len(up))
	for tag, config := range wanted {
		if m.guards.retained[tag] {
			if port := flvx.GuardPortOf(config); port > 0 && !up[port] {
				problems[strings.TrimPrefix(tag, "flvxl-")] = "Reality 防盗辅助入站暂不可用"
				continue
			}
		} else if port, ok := m.guards.byTag[tag]; ok && up[port] {
			if node, ok := m.guards.pinned[tag]; ok {
				raw, _ := json.Marshal(flvx.WithGuardDest(flvx.Inbound(node), port))
				config = string(raw)
			}
		}
		final[tag] = config
	}
	for port := range up {
		final[flvx.GuardTag(port)] = m.guardConfig(port)
	}
	return final
}

// guardsUp reports the needed guards whose inbound the applied state already
// holds, and whether any needed guard still has to be (re)added.
func (m *flvxManager) guardsUp(need map[int]bool) (map[int]bool, bool) {
	up, missing := map[int]bool{}, false
	for port := range need {
		config := m.guardConfig(port)
		switch {
		case config == "":
			// No definition to (re)add; guardFinal withholds what points here.
		case m.state.Applied[flvx.GuardTag(port)] == config:
			up[port] = true
		default:
			missing = true
		}
	}
	return up, missing
}

// addGuards brings every needed guard inbound up before any node is pointed
// at it. A failure is never fatal: the affected nodes keep their real dest,
// the port is avoided and a fresh one is tried after flvxGuardRetry.
func (m *flvxManager) addGuards(ctx context.Context, need map[int]bool, present map[string]bool) (map[int]bool, error) {
	up := map[int]bool{}
	ports := make([]int, 0, len(need))
	for port := range need {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	attempted, failed := false, false
	for _, port := range ports {
		tag, config := flvx.GuardTag(port), m.guardConfig(port)
		if config == "" {
			continue
		}
		if present[tag] && m.state.Applied[tag] == config {
			up[port] = true
			continue
		}
		attempted = true
		if present[tag] {
			if _, err := m.apiCall(ctx, "rmi", tag); err != nil {
				m.guardFailed(port, err)
				failed = true
				continue
			}
			delete(present, tag)
			delete(m.state.Applied, tag)
		}
		path := filepath.Join(m.dir, "guard-inbound.json")
		err := writeFLVXFile(path, []byte(`{"inbounds":[`+config+`]}`), 0600)
		if err == nil {
			_, err = m.apiCall(ctx, "adi", path)
			os.Remove(path)
		}
		if err != nil {
			m.guardFailed(port, err)
			failed = true
			continue
		}
		present[tag] = true
		m.state.Applied[tag] = config
		up[port] = true
		if err = m.persist(); err != nil {
			return up, err
		}
	}
	if attempted && !failed {
		m.logs.report("FLVX Reality guard inbound", nil)
		m.guards.addIssue = ""
	}
	return up, nil
}

func (m *flvxManager) guardFailed(port int, err error) {
	m.logs.report("FLVX Reality guard inbound", err)
	m.guards.addIssue = fmt.Sprintf("Reality 防盗辅助入站（本机端口 %d）启动失败，相关节点暂按未开启处理", port)
	m.guards.retryAt = m.now().Add(flvxGuardRetry)
	if m.guards.avoid == nil {
		m.guards.avoid = map[int]bool{}
	}
	m.guards.avoid[port] = true
	// The group gets a fresh port next time unless a node still points here.
	if !m.liveGuardPorts()[port] {
		delete(m.state.Guards, port)
	}
}

// removeGuards drops guard inbounds no node points at any more. It runs
// after the nodes were re-pointed; a failed removal leaves an idle loopback
// listener that the next pass removes again.
func (m *flvxManager) removeGuards(ctx context.Context, present map[string]bool, final map[string]string) error {
	tags := []string{}
	for tag := range present {
		if strings.HasPrefix(tag, flvx.GuardTagPrefix) && final[tag] == "" {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	changed := false
	for _, tag := range tags {
		if _, err := m.apiCall(ctx, "rmi", tag); err != nil {
			m.logs.report("FLVX Reality guard removal", err)
			continue
		}
		delete(present, tag)
		delete(m.state.Applied, tag)
		changed = true
	}
	for tag := range m.state.Applied {
		if strings.HasPrefix(tag, flvx.GuardTagPrefix) && final[tag] == "" && !present[tag] {
			delete(m.state.Applied, tag)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return m.persist()
}

// guardStatus is the reality_guard report: omitted while FLVX does not ask
// for the guard (or the share has no VLESS Reality node) and no node is
// guarded. nodes counts the nodes actually pointed at a listening guard.
func (m *flvxManager) guardStatus() *flvx.RealityGuardStatus {
	p := m.state.Payload
	nodes := 0
	for tag, config := range m.state.Applied {
		if strings.HasPrefix(tag, "flvxl-") {
			if port := flvx.GuardPortOf(config); port > 0 && m.state.Applied[flvx.GuardTag(port)] != "" {
				nodes++
			}
		}
	}
	if nodes == 0 && (p.Status != "active" || !p.Desired.GuardOn() || len(m.guards.byTag) == 0) {
		return nil
	}
	s := &flvx.RealityGuardStatus{State: "on", Nodes: nodes}
	issue := m.guards.rulesIssue
	if issue == "" {
		issue = m.guards.addIssue
	}
	switch {
	case nodes == 0 && m.guards.restart:
		s.State, issue = "restart_required", "FLVX Core 启动时未开启路由 API，重启 FLVX Core 后防盗生效；目前按未开启处理"
	case nodes == 0 && issue != "":
		s.State = "error"
	}
	if r := []rune(issue); len(r) > 200 {
		issue = string(r[:200])
	}
	s.Error = issue
	return s
}
