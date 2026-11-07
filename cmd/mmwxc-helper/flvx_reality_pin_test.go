package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// pinNet is a fake resolver plus per-IP TCP connect times (ms; -1 = no
// answer) with a fake clock, counting what a real server would send.
type pinNet struct {
	mu       sync.Mutex
	answer   []string
	dnsErr   error
	ms       map[string]float64
	lookups  int
	connects int
	now      time.Time
}

func (p *pinNet) install(m *flvxManager) {
	m.lookupHost = func(context.Context, string) ([]string, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.lookups++
		return append([]string(nil), p.answer...), p.dnsErr
	}
	m.connectTime = func(_ context.Context, address string) (time.Duration, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.connects++
		host, _, _ := net.SplitHostPort(address)
		ms, ok := p.ms[host]
		if !ok || ms < 0 {
			return 0, errors.New("timeout")
		}
		return time.Duration(ms * float64(time.Millisecond)), nil
	}
	m.clock = func() time.Time {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.now
	}
}

func (p *pinNet) advance(d time.Duration) {
	p.mu.Lock()
	p.now = p.now.Add(d)
	p.mu.Unlock()
}

func pinFixture(t *testing.T, nodes ...flvx.Node) (*flvxManager, *fakeFLVXCore, *pinNet) {
	t.Helper()
	m, f := fakeFLVXManager(t)
	m.state.Payload.Desired.Nodes = nodes
	p := &pinNet{now: time.Now(), ms: map[string]float64{}}
	p.install(m)
	return m, f, p
}

func reconcileOK(t *testing.T, m *flvxManager) {
	t.Helper()
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func appliedDest(t *testing.T, m *flvxManager, tag string) string {
	t.Helper()
	var cfg struct {
		StreamSettings struct {
			RealitySettings struct {
				Dest string `json:"dest"`
			} `json:"realitySettings"`
		} `json:"streamSettings"`
	}
	if err := json.Unmarshal([]byte(m.state.Applied[tag]), &cfg); err != nil {
		t.Fatalf("%s not applied: %v", tag, err)
	}
	return cfg.StreamSettings.RealitySettings.Dest
}

// The fastest public IP wins, not the first sorted DNS answer (Boil Hinet
// 马年: 54.230.70.30 sorted first but is ~60 ms away, 65.9.180.x ~4 ms).
func TestFLVXRealityPinPicksFastestIP(t *testing.T) {
	m, _, p := pinFixture(t, helperRealityNode("r", 30010, "www.lovelive-anime.jp:443", "www.lovelive-anime.jp"))
	p.answer = []string{"65.9.180.116", "54.230.70.30", "65.9.180.62"}
	p.ms = map[string]float64{"54.230.70.30": 61.3, "65.9.180.116": 4.2, "65.9.180.62": 3.5}
	reconcileOK(t, m)
	if got := appliedDest(t, m, "flvxl-r"); got != "65.9.180.62:443" {
		t.Fatalf("pinned %s", got)
	}
	want := []flvx.RealityPinStatus{{Host: "www.lovelive-anime.jp:443", PinnedIP: "65.9.180.62", PinnedMS: 3.5, BestIP: "65.9.180.62", BestMS: 3.5, CheckedAt: p.now.UnixMilli(), Nodes: []string{"r"}}}
	if !reflect.DeepEqual(m.state.Report.RealityPins, want) {
		t.Fatalf("status %+v", m.state.Report.RealityPins)
	}
	if p.lookups != 1 || p.connects != 3 {
		t.Fatalf("lookups=%d connects=%d", p.lookups, p.connects)
	}
	// The pin persists and the published report carries it.
	raw, _ := json.Marshal(m.snapshotReport())
	if !strings.Contains(string(raw), `"reality_pins":[{"host":"www.lovelive-anime.jp:443","pinned_ip":"65.9.180.62"`) {
		t.Fatalf("report %s", raw)
	}
}

// Re-pin only when >= 20 ms slower (or failed); the cost of one 6 h window
// is one DNS lookup + one connect per candidate, plus one liveness connect
// per pinned IP every 10 min.
func TestFLVXRealityPinHysteresisAndSchedule(t *testing.T) {
	m, f, p := pinFixture(t, helperRealityNode("r", 30010, "d.example:443", "d.example"))
	p.answer = []string{"203.0.113.1", "203.0.113.2"}
	p.ms = map[string]float64{"203.0.113.1": 10, "203.0.113.2": 40}
	reconcileOK(t, m)
	if appliedDest(t, m, "flvxl-r") != "203.0.113.1:443" {
		t.Fatal("first pin")
	}
	p.lookups, p.connects, f.adds, f.removes = 0, 0, 0, 0
	// A new, faster IP appears; the pin is only 15 ms slower: kept.
	p.answer = []string{"203.0.113.1", "203.0.113.3"}
	p.ms = map[string]float64{"203.0.113.1": 20, "203.0.113.2": 40, "203.0.113.3": 5}
	for elapsed := time.Duration(0); elapsed < flvxPinRecheck; elapsed += time.Minute {
		p.advance(time.Minute)
		reconcileOK(t, m)
	}
	// 359 passes: 35 liveness connects (10, 20, … 350 min) and one full
	// check at 360 min (DNS + pinned + answer + the remembered .2).
	if p.lookups != 1 || p.connects != 35+3 {
		t.Fatalf("6 h cost lookups=%d connects=%d", p.lookups, p.connects)
	}
	if appliedDest(t, m, "flvxl-r") != "203.0.113.1:443" || f.adds != 0 || f.removes != 0 {
		t.Fatalf("15 ms difference re-pinned: adds=%d removes=%d", f.adds, f.removes)
	}
	// 25 ms slower: moves (the node is idle).
	p.ms["203.0.113.1"] = 30
	p.advance(flvxPinRecheck)
	reconcileOK(t, m)
	if appliedDest(t, m, "flvxl-r") != "203.0.113.3:443" {
		t.Fatalf("25 ms difference kept %s", appliedDest(t, m, "flvxl-r"))
	}
}

// An unguarded node with live connections is not re-added for a faster IP;
// it moves once idle, or right away when its IP stopped answering.
func TestFLVXRealityPinUnguardedWaitsForIdle(t *testing.T) {
	m, f, p := pinFixture(t, helperRealityNode("r", 30010, "d.example:443", "d.example"))
	p.answer = []string{"203.0.113.1", "203.0.113.2"}
	p.ms = map[string]float64{"203.0.113.1": 10, "203.0.113.2": 40}
	reconcileOK(t, m)
	f.snap.Users = []coreUserSnapshot{{Identity: coreIdentity{InboundTag: "flvxl-r", User: "flvxl-r"}, CurrentTotal: 4}}
	reconcileOK(t, m)
	p.ms = map[string]float64{"203.0.113.1": 50, "203.0.113.2": 5}
	f.adds, f.removes, f.blocks = 0, 0, 0
	p.advance(flvxPinRecheck)
	reconcileOK(t, m)
	if appliedDest(t, m, "flvxl-r") != "203.0.113.1:443" || f.adds+f.removes+f.blocks != 0 {
		t.Fatalf("busy node re-added: adds=%d removes=%d blocks=%d", f.adds, f.removes, f.blocks)
	}
	if s := m.state.Report.RealityPins; len(s) != 1 || s[0].PinnedMS != 50 || s[0].BestIP != "203.0.113.2" {
		t.Fatalf("status %+v", s)
	}
	f.snap.Users = nil
	reconcileOK(t, m)
	reconcileOK(t, m)
	if appliedDest(t, m, "flvxl-r") != "203.0.113.2:443" {
		t.Fatal("idle node did not move")
	}
	// Busy again, and its IP stops answering: liveness fails, the full check
	// confirms, and the node moves at once.
	f.snap.Users = []coreUserSnapshot{{Identity: coreIdentity{InboundTag: "flvxl-r", User: "flvxl-r"}, CurrentTotal: 4}}
	reconcileOK(t, m)
	p.ms = map[string]float64{"203.0.113.1": 12, "203.0.113.2": -1}
	p.advance(flvxPinLiveness)
	reconcileOK(t, m)
	if appliedDest(t, m, "flvxl-r") != "203.0.113.1:443" {
		t.Fatal("failed pin kept")
	}
}

// DNS rebinding protection: an answer with any private address is never
// used; a pinned node keeps its pin, a new one gets none.
func TestFLVXRealityPinPublicOnly(t *testing.T) {
	m, f, p := pinFixture(t, helperRealityNode("r", 30010, "d.example:443", "d.example"))
	p.answer = []string{"203.0.113.1"}
	p.ms = map[string]float64{"203.0.113.1": 10, "127.0.0.1": 0.1, "10.0.0.1": 0.1}
	reconcileOK(t, m)
	p.answer = []string{"10.0.0.1", "203.0.113.1", "127.0.0.1"}
	p.advance(flvxPinRecheck)
	reconcileOK(t, m)
	d := m.state.RealityDests["d.example:443"]
	if appliedDest(t, m, "flvxl-r") != "203.0.113.1:443" || d.Seen["10.0.0.1"] != 0 || d.Seen["127.0.0.1"] != 0 {
		t.Fatalf("private answer used: %+v", d)
	}
	for ip := range d.MS {
		if flvx.PrivateHost(ip) {
			t.Fatalf("private candidate probed: %v", d.MS)
		}
	}
	other := helperRealityNode("n", 30011, "e.example:443", "e.example")
	m.state.Payload.Desired.Nodes = append(m.state.Payload.Desired.Nodes, other)
	reconcileOK(t, m)
	if f.present["flvxl-n"] || m.state.RealityPins["flvxl-n"].Dest != "" {
		t.Fatal("private-only destination admitted")
	}
}

// DNS from 1.1.1.1 flips between near and far edge sets within seconds on
// Boil Hinet 158. Since v0.7.24 the pin follows the DNS answer as FLVX's
// agent does: a pin that left the answer moves to the answer's fastest IP
// (failover), even a far one. Remembered and pinned IPs stay candidates for
// when the lookup fails.
func TestFLVXRealityPinRemembersAnswers(t *testing.T) {
	m, _, p := pinFixture(t, helperRealityNode("r", 30010, "www.lovelive-anime.jp:443", "www.lovelive-anime.jp"))
	p.answer = []string{"65.9.180.116", "65.9.180.32"}
	p.ms = map[string]float64{"65.9.180.116": 3.8, "65.9.180.32": 5.1, "54.230.70.30": 54.6, "54.230.70.97": 52.8}
	reconcileOK(t, m)
	// The lookup fails: the near pin stays, remembered IPs are still measured.
	p.answer, p.dnsErr = nil, errors.New("timeout")
	p.advance(flvxPinRecheck)
	reconcileOK(t, m)
	d := m.state.RealityDests["www.lovelive-anime.jp:443"]
	if appliedDest(t, m, "flvxl-r") != "65.9.180.116:443" || len(d.MS) != 2 {
		t.Fatalf("lookup failure moved the pin: %s %v", appliedDest(t, m, "flvxl-r"), d.MS)
	}
	// Only the far set answers: the pin follows it.
	p.answer, p.dnsErr = []string{"54.230.70.30", "54.230.70.97"}, nil
	p.advance(flvxPinRecheck)
	reconcileOK(t, m)
	d = m.state.RealityDests["www.lovelive-anime.jp:443"]
	if appliedDest(t, m, "flvxl-r") != "54.230.70.97:443" || len(d.MS) != 4 || m.state.Report.RealityPins[0].MoveReason != flvx.RealityPinMoveFailover {
		t.Fatalf("pin did not follow the answer: %s %v %+v", appliedDest(t, m, "flvxl-r"), d.MS, m.state.Report.RealityPins)
	}
	// A new node on the same dest gets the answer's best IP too.
	m.state.Payload.Desired.Nodes = append(m.state.Payload.Desired.Nodes, helperRealityNode("s", 30011, "www.lovelive-anime.jp:443", "www.lovelive-anime.jp"))
	reconcileOK(t, m)
	if appliedDest(t, m, "flvxl-s") != "54.230.70.97:443" {
		t.Fatalf("new node pinned %s", appliedDest(t, m, "flvxl-s"))
	}
	// Remembered IPs age out after a week.
	for i := 0; i < 30; i++ {
		p.advance(flvxPinRecheck)
		reconcileOK(t, m)
	}
	if _, ok := m.state.RealityDests["www.lovelive-anime.jp:443"].Seen["65.9.180.32"]; ok {
		t.Fatal("old answer remembered for over a week")
	}
}

// No candidate answers: a new node falls back to the first sorted DNS
// answer, as before the contract, and moves once one answers.
func TestFLVXRealityPinFallbackWhenNothingAnswers(t *testing.T) {
	m, _, p := pinFixture(t, helperRealityNode("r", 30010, "d.example:443", "d.example"))
	p.answer = []string{"203.0.113.9", "203.0.113.1"}
	reconcileOK(t, m)
	if appliedDest(t, m, "flvxl-r") != "203.0.113.1:443" || !m.state.RealityPins["flvxl-r"].Fallback || !m.state.Report.RealityPins[0].Fallback {
		t.Fatalf("fallback %+v", m.state.RealityPins)
	}
	p.ms = map[string]float64{"203.0.113.9": 7}
	p.advance(flvxPinRecheck)
	reconcileOK(t, m)
	if appliedDest(t, m, "flvxl-r") != "203.0.113.9:443" || m.state.RealityPins["flvxl-r"].Fallback {
		t.Fatal("fallback pin kept after a candidate answered")
	}
}

// Pins of deleted nodes and dests no node uses are dropped (Boil Hinet 马年
// kept 142 / 143 / 20 … 82 forever).
func TestFLVXRealityPinCleansDeletedNodes(t *testing.T) {
	m, _, p := pinFixture(t, helperRealityNode("r", 30010, "d.example:443", "d.example"), helperFLVXNode("ss", 30001))
	p.answer, p.ms = []string{"203.0.113.1"}, map[string]float64{"203.0.113.1": 10}
	m.state.RealityPins = map[string]flvxRealityPin{"flvxl-142": {Hash: "x", Dest: "54.230.70.30:443"}, "flvxl-20": {Hash: "y", Dest: "65.9.180.116:443"}}
	m.state.RealityDests = map[string]flvxRealityDest{"gone.example:443": {CheckedAt: 1}}
	reconcileOK(t, m)
	if len(m.state.RealityPins) != 1 || m.state.RealityPins["flvxl-r"].Dest != "203.0.113.1:443" || len(m.state.RealityDests) != 1 {
		t.Fatalf("stale state %+v %+v", m.state.RealityPins, m.state.RealityDests)
	}
	m.state.Payload.Desired.Nodes = m.state.Payload.Desired.Nodes[1:]
	reconcileOK(t, m)
	if len(m.state.RealityPins) != 0 || len(m.state.RealityDests) != 0 || m.state.Report.RealityPins != nil {
		t.Fatalf("deleted node kept %+v %+v", m.state.RealityPins, m.state.RealityDests)
	}
}

// A pin from v0.7.21 and older (node digest, no host) is adopted, not
// re-resolved, and a near one in the answer stays (usb2's node on Boil Hinet
// 158).
func TestFLVXRealityPinAdoptsLegacyPin(t *testing.T) {
	n := helperRealityNode("132", 30000, "www.lovelive-anime.jp:443", "www.lovelive-anime.jp")
	m, f, p := pinFixture(t, n)
	m.state.RealityPins = map[string]flvxRealityPin{n.Tag(): {Hash: flvxNodeHash(n), Dest: "65.9.180.116:443"}}
	cfg := n
	v := *n.VLESS
	v.Reality.Dest = "65.9.180.116:443"
	cfg.VLESS = &v
	raw, _ := json.Marshal(flvx.Inbound(cfg))
	m.state.Applied[n.Tag()], f.present[n.Tag()] = string(raw), true
	m.state.AppliedEpoch = flvxEpoch(coreSnapshotResponse{StartedAt: f.epoch})
	f.snap.Users = []coreUserSnapshot{{Identity: coreIdentity{InboundTag: n.Tag(), User: n.Tag()}, CurrentTotal: 77}}
	p.answer = []string{"54.230.70.30", "54.230.70.84", "54.230.70.9", "54.230.70.97", "65.9.180.116"}
	p.ms = map[string]float64{"65.9.180.116": 3.3, "54.230.70.30": 54.6, "54.230.70.84": 82.4, "54.230.70.9": 78.6, "54.230.70.97": 52.8}
	reconcileOK(t, m)
	pin := m.state.RealityPins[n.Tag()]
	if pin.Dest != "65.9.180.116:443" || pin.Host != "www.lovelive-anime.jp:443" || pin.Hash != flvxNodeHash(n) || f.adds+f.removes+f.blocks != 0 || m.state.Applied[n.Tag()] != string(raw) {
		t.Fatalf("legacy pin not adopted: %+v adds=%d removes=%d blocks=%d", pin, f.adds, f.removes, f.blocks)
	}
}

// Guarded nodes move with their group: only the loopback guard inbound is
// re-added with the new target; the node inbounds and their connections
// stay (no rmi, no block of the node).
func TestFLVXRealityPinRetargetsGuardOnly(t *testing.T) {
	g := newGuardFixture(t)
	p := &pinNet{now: time.Now(), answer: []string{"203.0.113.80", "203.0.113.81"}, ms: map[string]float64{"203.0.113.80": 10, "203.0.113.81": 60}}
	p.install(g.m)
	g.guardOn(true)
	g.apply(t)
	g.apply(t)
	shared := g.m.guards.byTag["flvxl-r1"]
	before := map[string]string{}
	for tag, cfg := range g.m.state.Applied {
		before[tag] = cfg
	}
	g.f.snap.Users = []coreUserSnapshot{{Identity: coreIdentity{InboundTag: "flvxl-r1", User: "flvxl-r1"}, CurrentTotal: 9}}
	g.apply(t)
	g.f.blocks = 0
	p.ms = map[string]float64{"203.0.113.80": 45, "203.0.113.81": 6}
	p.advance(flvxPinRecheck)
	g.apply(t)
	if got := g.m.state.Guards[shared]; got.Dest != "203.0.113.81:443" {
		t.Fatalf("guard %d not retargeted: %+v", shared, got)
	}
	for _, tag := range g.nodeTags {
		if g.m.state.Applied[tag] != before[tag] {
			t.Fatalf("%s re-rendered", tag)
		}
	}
	for _, c := range g.calls {
		if strings.HasPrefix(c, "rmi flvxl-") || strings.HasPrefix(c, "adi flvxl-") {
			t.Fatalf("node inbound touched: %v", g.calls)
		}
	}
	if g.f.blocks != 0 {
		t.Fatalf("nodes blocked during the retarget: %d", g.f.blocks)
	}
	guardTag := flvx.GuardTag(shared)
	if g.index(t, "rmi "+guardTag) > g.index(t, "adi "+guardTag) || !strings.Contains(g.m.state.Applied[guardTag], `"address":"203.0.113.81"`) {
		t.Fatalf("guard inbound not re-added with the new target: %v", g.calls)
	}
}

// Several dests are checked in parallel (run with -race), each node gets
// its own dest's fastest IP and a DNS failure is logged once per pass.
func TestFLVXRealityPinParallelDests(t *testing.T) {
	m, _, p := pinFixture(t, helperRealityNode("a", 30010, "a.example:443", "a.example"), helperRealityNode("b", 30011, "b.example:443", "b.example"), helperRealityNode("c", 30012, "c.example:443", "c.example"))
	p.ms = map[string]float64{"203.0.113.1": 30, "203.0.113.2": 3}
	p.answer = []string{"203.0.113.1", "203.0.113.2"}
	logs := 0
	m.logs.print = func(string, ...any) { logs++ }
	reconcileOK(t, m)
	for _, tag := range []string{"flvxl-a", "flvxl-b", "flvxl-c"} {
		if appliedDest(t, m, tag) != "203.0.113.2:443" {
			t.Fatalf("%s pinned %s", tag, appliedDest(t, m, tag))
		}
	}
	p.dnsErr = errors.New("resolver down")
	p.advance(flvxPinRecheck)
	reconcileOK(t, m)
	if logs != 1 || appliedDest(t, m, "flvxl-a") != "203.0.113.2:443" {
		t.Fatalf("logs=%d pin %s", logs, appliedDest(t, m, "flvxl-a"))
	}
}
