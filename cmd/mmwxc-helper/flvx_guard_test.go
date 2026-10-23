package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

func helperRealityNode(id string, port int, dest string, names ...string) flvx.Node {
	n := helperFLVXNode(id, port)
	n.Protocol, n.SS2022 = "vless_reality_vision", nil
	n.VLESS = &flvx.VLESS{UUID: "12345678-1234-4234-8234-123456789abc", Flow: "xtls-rprx-vision", Reality: flvx.Reality{Dest: dest, ServerNames: names, PrivateKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ShortIDs: []string{"", "a1b2"}}}
	return n
}

// guardFixture: an SS2022 node, two Reality nodes sharing dest and names
// (different case and order) and one with other names, on a fake Core whose
// API calls are logged with their inbound tags.
type guardFixture struct {
	m        *flvxManager
	f        *fakeEgressCore
	calls    []string
	failAdd  map[string]bool
	ports    []int
	lookups  int
	nodeTags []string
}

func newGuardFixture(t *testing.T) *guardFixture {
	t.Helper()
	m, f := fakeEgressManager(t)
	g := &guardFixture{m: m, f: f, failAdd: map[string]bool{}}
	m.state.Payload.Desired.Nodes = []flvx.Node{
		helperFLVXNode("ss", 30001),
		helperRealityNode("r1", 30010, "www.example.com:443", "www.example.com", "example.com"),
		helperRealityNode("r2", 30011, "www.example.com:443", "Example.com", "WWW.example.com"),
		helperRealityNode("r3", 30012, "www.example.com:443", "cdn.example.net"),
	}
	g.nodeTags = []string{"flvxl-r1", "flvxl-r2", "flvxl-r3"}
	m.lookupHost = func(context.Context, string) ([]string, error) {
		g.lookups++
		return []string{"203.0.113.80"}, nil
	}
	// Kernel answers in order; 30500 (share range), 30003 (official) and
	// 49000 (control API) must be skipped.
	g.ports = []int{30500, 30003, 49000, 41001, 41002, 41003, 41004, 41005}
	m.pickPort = func() (int, error) {
		if len(g.ports) == 0 {
			return 0, errors.New("no port")
		}
		p := g.ports[0]
		g.ports = g.ports[1:]
		return p, nil
	}
	inner := m.apiCall
	m.apiCall = func(ctx context.Context, command string, args ...string) ([]byte, error) {
		entry := command
		switch command {
		case "adi":
			raw, _ := os.ReadFile(args[0])
			var c struct {
				Inbounds []struct {
					Tag string `json:"tag"`
				} `json:"inbounds"`
			}
			json.Unmarshal(raw, &c)
			entry += " " + c.Inbounds[0].Tag
			if g.failAdd[c.Inbounds[0].Tag] {
				g.calls = append(g.calls, entry+" FAILED")
				return nil, flvxCLIError{command: command}
			}
		case "rmi":
			entry += " " + args[0]
		}
		if command != "statsquery" {
			g.calls = append(g.calls, entry)
		}
		return inner(ctx, command, args...)
	}
	return g
}

func (g *guardFixture) guardOn(on bool) {
	g.m.state.Payload.Desired.RealityGuard = &on
	g.m.state.Payload.Desired.Revision++
	g.m.state.Payload.Version++
}

func (g *guardFixture) apply(t *testing.T) {
	t.Helper()
	g.calls = nil
	if err := g.m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (g *guardFixture) reality(t *testing.T, tag string) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal([]byte(g.m.state.Applied[tag]), &cfg); err != nil {
		t.Fatalf("%s not applied: %v", tag, err)
	}
	return cfg["streamSettings"].(map[string]any)
}

func (g *guardFixture) index(t *testing.T, entry string) int {
	t.Helper()
	for i, c := range g.calls {
		if c == entry {
			return i
		}
	}
	t.Fatalf("%q not called: %v", entry, g.calls)
	return -1
}

func guardTags(applied map[string]string) []string {
	tags := []string{}
	for tag := range applied {
		if strings.HasPrefix(tag, flvx.GuardTagPrefix) {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return tags
}

// Old FLVX panels never send reality_guard: nothing changes, not even the
// rule set, and the node configs stay exactly as rendered before.
func TestFLVXGuardAbsentKeepsTodaysBehaviour(t *testing.T) {
	for _, value := range []*bool{nil, new(bool)} {
		g := newGuardFixture(t)
		g.m.state.Payload.Desired.RealityGuard = value
		g.apply(t)
		for _, c := range g.calls {
			if c == "adrules" || strings.Contains(c, flvx.GuardTagPrefix) {
				t.Fatalf("guard work without the switch: %v", g.calls)
			}
		}
		for _, tag := range g.nodeTags {
			n := g.m.state.Payload.Desired.Nodes[0]
			for _, candidate := range g.m.state.Payload.Desired.Nodes {
				if candidate.Tag() == tag {
					n = candidate
				}
			}
			v := *n.VLESS
			v.Reality.Dest = "203.0.113.80:443"
			n.VLESS = &v
			raw, _ := json.Marshal(flvx.Inbound(n))
			if g.m.state.Applied[tag] != string(raw) {
				t.Fatalf("%s rendered differently:\n%s\n%s", tag, g.m.state.Applied[tag], raw)
			}
		}
		if g.m.state.Report.RealityGuard != nil || len(g.m.state.Guards) != 0 || len(guardTags(g.m.state.Applied)) != 0 {
			t.Fatalf("guard state without the switch: %+v %+v", g.m.state.Report.RealityGuard, g.m.state.Guards)
		}
	}
}

func TestFLVXGuardGroupsOrderAndRender(t *testing.T) {
	g := newGuardFixture(t)
	g.apply(t)
	before := map[string]string{}
	for tag, cfg := range g.m.state.Applied {
		before[tag] = cfg
	}
	g.guardOn(true)
	g.apply(t)

	// One port per (pinned dest, serverNames): r1 and r2 share a group.
	if len(g.m.state.Guards) != 2 || !reflect.DeepEqual(guardTags(g.m.state.Applied), []string{"flvxg-41001", "flvxg-41002"}) {
		t.Fatalf("groups %+v applied %v", g.m.state.Guards, guardTags(g.m.state.Applied))
	}
	shared := g.m.guards.byTag["flvxl-r1"]
	if shared != g.m.guards.byTag["flvxl-r2"] || shared == g.m.guards.byTag["flvxl-r3"] {
		t.Fatalf("grouping %v", g.m.guards.byTag)
	}
	if got := g.m.state.Guards[shared]; got.Dest != "203.0.113.80:443" || !reflect.DeepEqual(got.Names, []string{"example.com", "www.example.com"}) {
		t.Fatalf("group %+v", got)
	}
	// Rules, then guard inbounds, then the re-pointed nodes.
	rules := g.index(t, "adrules")
	for _, tag := range []string{"flvxg-41001", "flvxg-41002"} {
		if add := g.index(t, "adi "+tag); add < rules {
			t.Fatalf("guard added before its rules: %v", g.calls)
		}
		for _, node := range g.nodeTags {
			if g.index(t, "adi "+tag) > g.index(t, "adi "+node) {
				t.Fatalf("node re-pointed before its guard listened: %v", g.calls)
			}
		}
	}
	if strings.Contains(strings.Join(g.calls, ","), "flvxl-ss") {
		t.Fatalf("SS2022 node touched: %v", g.calls)
	}
	// Guard rules lead the rule set, before the safety rule, allow before block.
	want := []string{"flvxg-41001-allow", "flvxg-41001-block", "flvxg-41002-allow", "flvxg-41002-block", "flvx-safety"}
	got := []string{}
	for _, r := range g.f.rules {
		got = append(got, r["ruleTag"].(string))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rule order %v", got)
	}
	allow := g.f.rules[0]
	if !reflect.DeepEqual(allow["protocol"], []any{"tls"}) || allow["outboundTag"] != flvx.GuardOutboundTag || g.f.rules[1]["outboundTag"] != "block" {
		t.Fatalf("allow rule %v", allow)
	}
	// The guard's own direct outbound (short half-close timeouts) went in
	// before the rules referencing it (the fake Core checks the order).
	if g.f.outbounds[flvx.GuardOutboundTag] == "" || g.index(t, "ado") > rules {
		t.Fatalf("guard outbound %v: %v", g.f.outbounds, g.calls)
	}
	// Re-pointed node: dest and TCP_DEFER_ACCEPT only; port, keys, names and
	// short ids byte-identical to the unguarded render.
	for _, tag := range g.nodeTags {
		stream := g.reality(t, tag)
		reality := stream["realitySettings"].(map[string]any)
		port := g.m.guards.byTag[tag]
		if reality["dest"] != "127.0.0.1:"+strconv.Itoa(port) {
			t.Fatalf("%s dest %v", tag, reality["dest"])
		}
		sockopt, _ := json.Marshal(stream["sockopt"])
		if string(sockopt) != `{"customSockopt":[{"level":"6","network":"tcp","opt":"9","system":"linux","type":"int","value":"5"}]}` {
			t.Fatalf("sockopt %s", sockopt)
		}
		var old, now map[string]any
		json.Unmarshal([]byte(before[tag]), &old)
		json.Unmarshal([]byte(g.m.state.Applied[tag]), &now)
		oldStream, nowStream := old["streamSettings"].(map[string]any), now["streamSettings"].(map[string]any)
		delete(oldStream["realitySettings"].(map[string]any), "dest")
		delete(nowStream["realitySettings"].(map[string]any), "dest")
		delete(nowStream, "sockopt")
		if !reflect.DeepEqual(old, now) {
			t.Fatalf("%s changed beyond dest/sockopt:\n%v\n%v", tag, old, now)
		}
	}
	if s := g.m.state.Report.RealityGuard; s == nil || s.State != "on" || s.Nodes != 3 || s.Error != "" {
		t.Fatalf("status %+v", s)
	}
	for _, n := range g.m.state.Report.Nodes {
		if n.State != "active" {
			t.Fatalf("node %+v", n)
		}
	}
	// Steady state: no Core API call besides the stats sample, no DNS.
	lookups := g.lookups
	for i := 0; i < 5; i++ {
		g.apply(t)
		if len(g.calls) != 0 {
			t.Fatalf("steady pass called %v", g.calls)
		}
	}
	if g.lookups != lookups {
		t.Fatal("steady passes resolved the dest again")
	}
	// Ports survive a Helper restart; nothing is re-pointed.
	applied := g.m.state.Applied
	g.m.state = flvxLocalState{}
	if err := g.m.load(); err != nil {
		t.Fatal(err)
	}
	g.m.guards = guardPass{}
	g.apply(t)
	if !reflect.DeepEqual(g.m.state.Applied, applied) || len(g.calls) > 2 {
		t.Fatalf("restart re-rendered: %v", g.calls)
	}
}

func TestFLVXGuardRemovalAndPortRelease(t *testing.T) {
	g := newGuardFixture(t)
	g.guardOn(true)
	g.apply(t)
	r3 := g.m.guards.byTag["flvxl-r3"]
	// Deleting the last node of a group frees its guard and port.
	nodes := g.m.state.Payload.Desired.Nodes
	g.m.state.Payload.Desired.Nodes = nodes[:3]
	g.m.state.Payload.Desired.Revision++
	g.m.state.Payload.Version++
	g.apply(t)
	if g.m.state.Applied[flvx.GuardTag(r3)] != "" || g.f.present[flvx.GuardTag(r3)] {
		t.Fatalf("guard of the deleted group left behind: %v", g.calls)
	}
	g.apply(t) // the next pass drops its rules
	if _, ok := g.m.state.Guards[r3]; ok {
		t.Fatalf("port %d not freed: %+v", r3, g.m.state.Guards)
	}
	for _, r := range g.f.rules {
		if strings.HasPrefix(r["ruleTag"].(string), flvx.GuardTag(r3)) {
			t.Fatalf("rules of the freed guard left: %v", g.f.rules)
		}
	}
	// Switching the guard off: nodes go back to the real dest first, the
	// guard goes last, and the rule set returns to the safety rule alone.
	shared := g.m.guards.byTag["flvxl-r1"]
	g.guardOn(false)
	g.apply(t)
	if g.index(t, "rmi "+flvx.GuardTag(shared)) < g.index(t, "adi flvxl-r1") {
		t.Fatalf("guard removed while a node still pointed at it: %v", g.calls)
	}
	for _, tag := range []string{"flvxl-r1", "flvxl-r2"} {
		if dest := g.reality(t, tag)["realitySettings"].(map[string]any)["dest"]; dest != "203.0.113.80:443" {
			t.Fatalf("%s dest %v", tag, dest)
		}
		if _, ok := g.reality(t, tag)["sockopt"]; ok {
			t.Fatalf("%s kept the guard sockopt", tag)
		}
	}
	g.apply(t)
	if len(g.m.state.Guards) != 0 || len(guardTags(g.m.state.Applied)) != 0 || g.m.state.Report.RealityGuard != nil || g.m.state.EgressApplied != "" {
		t.Fatalf("guard state after off: %+v %v %+v", g.m.state.Guards, guardTags(g.m.state.Applied), g.m.state.Report.RealityGuard)
	}
	if len(g.f.rules) != 1 || g.f.rules[0]["ruleTag"] != "flvx-safety" || g.f.outbounds[flvx.GuardOutboundTag] != "" {
		t.Fatalf("rules / outbounds after off: %v %v", g.f.rules, g.f.outbounds)
	}
	for tag := range g.f.present {
		if strings.HasPrefix(tag, flvx.GuardTagPrefix) {
			t.Fatalf("guard inbound %s still in the Core", tag)
		}
	}
}

func TestFLVXGuardFailOpen(t *testing.T) {
	t.Run("rules", func(t *testing.T) {
		g := newGuardFixture(t)
		now := time.Now()
		g.m.clock = func() time.Time { return now }
		g.f.failRules = true
		g.guardOn(true)
		g.apply(t)
		if len(guardTags(g.m.state.Applied)) != 0 {
			t.Fatalf("guard added without its rules: %v", g.calls)
		}
		for _, tag := range g.nodeTags {
			if dest := g.reality(t, tag)["realitySettings"].(map[string]any)["dest"]; dest != "203.0.113.80:443" {
				t.Fatalf("%s left its real dest: %v", tag, dest)
			}
		}
		if s := g.m.state.Report.RealityGuard; s == nil || s.State != "error" || s.Nodes != 0 || !strings.Contains(s.Error, "规则未生效") {
			t.Fatalf("status %+v", s)
		}
		if e := g.m.state.Report.Egress; e == nil || e.State != "none" {
			t.Fatalf("plan-less egress status became %+v", e)
		}
		// A failed rule update is retried with the egress error cadence.
		g.f.failRules = false
		now = now.Add(flvxEgressRetry)
		g.apply(t)
		if s := g.m.state.Report.RealityGuard; s.State != "on" || s.Nodes != 3 {
			t.Fatalf("recovery %+v", s)
		}
	})
	t.Run("inbound", func(t *testing.T) {
		g := newGuardFixture(t)
		now := time.Now()
		g.m.clock = func() time.Time { return now }
		g.failAdd["flvxg-41001"] = true
		g.guardOn(true)
		g.apply(t)
		bad, good := g.m.guards.byTag["flvxl-r1"], g.m.guards.byTag["flvxl-r3"]
		if bad != 41001 || good != 41002 {
			t.Fatalf("ports %v", g.m.guards.byTag)
		}
		// r1/r2 keep the real dest; r3's own guard is unaffected.
		for _, tag := range []string{"flvxl-r1", "flvxl-r2"} {
			if dest := g.reality(t, tag)["realitySettings"].(map[string]any)["dest"]; dest != "203.0.113.80:443" {
				t.Fatalf("%s dest %v", tag, dest)
			}
		}
		if dest := g.reality(t, "flvxl-r3")["realitySettings"].(map[string]any)["dest"]; dest != "127.0.0.1:41002" {
			t.Fatalf("r3 dest %v", dest)
		}
		if s := g.m.state.Report.RealityGuard; s == nil || s.State != "on" || s.Nodes != 1 || !strings.Contains(s.Error, "41001") {
			t.Fatalf("partial status %+v", s)
		}
		for _, n := range g.m.state.Report.Nodes {
			if n.State != "active" {
				t.Fatalf("a failed guard took node %s down", n.ID)
			}
		}
		// Within the retry delay no guard inbound is retried and no node
		// flaps; the group's fresh port only has its rules staged once.
		now = now.Add(20 * time.Second)
		g.apply(t)
		for _, c := range g.calls {
			if strings.HasPrefix(c, "adi") || strings.HasPrefix(c, "rmi") {
				t.Fatalf("retried early: %v", g.calls)
			}
		}
		now = now.Add(20 * time.Second)
		g.apply(t)
		if len(g.calls) != 0 {
			t.Fatalf("calls while waiting: %v", g.calls)
		}
		// Afterwards the group moves to a fresh port and comes up.
		now = now.Add(time.Minute)
		g.apply(t)
		g.apply(t)
		moved := g.m.guards.byTag["flvxl-r1"]
		if moved == 41001 || moved == 0 || g.m.state.Applied[flvx.GuardTag(moved)] == "" {
			t.Fatalf("no fresh port: %v %v", g.m.guards.byTag, g.calls)
		}
		if s := g.m.state.Report.RealityGuard; s.State != "on" || s.Nodes != 3 || s.Error != "" {
			t.Fatalf("status after retry %+v", s)
		}
	})
	t.Run("all inbounds", func(t *testing.T) {
		g := newGuardFixture(t)
		g.failAdd["flvxg-41001"], g.failAdd["flvxg-41002"] = true, true
		g.guardOn(true)
		g.apply(t)
		if s := g.m.state.Report.RealityGuard; s == nil || s.State != "error" || s.Nodes != 0 {
			t.Fatalf("status %+v", s)
		}
	})
	t.Run("old Core without routing API", func(t *testing.T) {
		g := newGuardFixture(t)
		g.f.unsupported = true
		g.guardOn(true)
		g.apply(t) // restartCore fails the test if called
		if len(guardTags(g.m.state.Applied)) != 0 {
			t.Fatal("guard without routing API")
		}
		if s := g.m.state.Report.RealityGuard; s == nil || s.State != "restart_required" || s.Nodes != 0 {
			t.Fatalf("status %+v", s)
		}
	})
}

// A guard port that becomes an official inbound (or enters the share range)
// moves: nodes are re-pointed to the new port before the old guard goes.
func TestFLVXGuardPortConflictMoves(t *testing.T) {
	g := newGuardFixture(t)
	g.guardOn(true)
	g.apply(t)
	old := g.m.guards.byTag["flvxl-r3"]
	g.m.readOfficial = func() ([]int, error) { return []int{30003, old}, nil }
	g.apply(t)
	moved := g.m.guards.byTag["flvxl-r3"]
	if moved == old || g.m.state.Applied[flvx.GuardTag(old)] != "" || g.index(t, "adi "+flvx.GuardTag(moved)) > g.index(t, "adi flvxl-r3") || g.index(t, "rmi "+flvx.GuardTag(old)) < g.index(t, "adi flvxl-r3") {
		t.Fatalf("move %d -> %d: %v", old, moved, g.calls)
	}
	g.apply(t)
	if _, ok := g.m.state.Guards[old]; ok {
		t.Fatalf("old port kept: %+v", g.m.state.Guards)
	}
}

// With an egress plan the order is guard rules, safety rule, plan rules.
func TestFLVXGuardWithEgressPlan(t *testing.T) {
	g := newGuardFixture(t)
	plan := egressPlan()
	plan.Rules[0].InboundTags = []string{"flvxl-ss", "flvxl-r1"}
	plan.Rules[1].InboundTags = []string{"flvxl-ss"}
	g.m.state.Payload.Desired.Egress = plan
	g.guardOn(true)
	g.apply(t)
	got := []string{}
	for _, r := range g.f.rules {
		got = append(got, r["ruleTag"].(string))
	}
	want := []string{"flvxg-41001-allow", "flvxg-41001-block", "flvxg-41002-allow", "flvxg-41002-block", "flvx-safety", "flvxr-1", "flvxr-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rules %v", got)
	}
	if e := g.m.state.Report.Egress; e == nil || e.State != "applied" {
		t.Fatalf("egress %+v", e)
	}
	if s := g.m.state.Report.RealityGuard; s == nil || s.State != "on" || s.Nodes != 3 {
		t.Fatalf("guard %+v", s)
	}
	// Removing the plan keeps the guard rules.
	g.m.state.Payload.Desired.Egress = nil
	g.m.state.Payload.Desired.Revision++
	g.m.state.Payload.Version++
	g.apply(t)
	if len(g.f.rules) != 5 || g.m.state.Report.Egress.State != "none" || g.m.state.Report.RealityGuard.Nodes != 3 {
		t.Fatalf("after plan removal: %v %+v", g.f.rules, g.m.state.Report.Egress)
	}
}

// Paused and quota-exhausted shares serve no node and keep no guard.
func TestFLVXGuardPausedShare(t *testing.T) {
	g := newGuardFixture(t)
	g.guardOn(true)
	g.apply(t)
	g.m.state.Payload.Status = "paused"
	g.m.state.Payload.Version++
	g.apply(t)
	g.apply(t)
	if len(g.m.state.Applied) != 0 || len(g.m.state.Guards) != 0 || g.m.state.Report.RealityGuard != nil {
		t.Fatalf("paused share kept %v %+v %+v", g.m.state.Applied, g.m.state.Guards, g.m.state.Report.RealityGuard)
	}
}
