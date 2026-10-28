package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

func ulInt64(v int64) *int64 { return &v }
func ulInt(v int) *int       { return &v }

func ownedNode(id string, port int, owner string) flvx.Node {
	n := helperFLVXNode(id, port)
	n.Owner = flvx.Owner{ID: flvx.OwnerID(owner), Name: "user " + owner}
	return n
}

// signedBudgets is what Custom would hand over with an upload answer.
func signedBudgets(m *flvxManager, now time.Time, sequence int64, relay []string, users ...flvx.UserLimitBudget) *flvx.UserLimitsEnvelope {
	if users == nil {
		users = []flvx.UserLimitBudget{}
	}
	e := &flvx.UserLimitsEnvelope{Payload: flvx.UserLimitsPayload{ServerID: m.config.ServerID, Sequence: sequence, Users: users, RelaySources: flvx.RelaySources(relay)}, ExpiresAt: now.Add(flvx.UserLimitsTTL).UnixMilli()}
	e.Sign(m.config.Token)
	return e
}

func portLimit(t *testing.T, c coreConfig, tag string) portConnectionSettings {
	t.Helper()
	for _, p := range c.PortLimits {
		if p.InboundTag == tag {
			return p
		}
	}
	t.Fatalf("no port limit for %s in %+v", tag, c.PortLimits)
	return portConnectionSettings{}
}

func groupLimit(c coreConfig, group string) *coreManagementLimit {
	for i := range c.ManagementLimits {
		if c.ManagementLimits[i].Group == group {
			return &c.ManagementLimits[i]
		}
	}
	return nil
}

// §3 / §8.4: conn_mode "inbound" maps the node limit to max_inbound only,
// absent keeps max_total only, exactly as before.
func TestFLVXConnModeMapsNodeLimit(t *testing.T) {
	m, f := fakeFLVXManager(t)
	inbound := helperFLVXNode("two", 30002)
	inbound.ConnMode = flvx.ConnModeInbound
	total := helperFLVXNode("three", 30004)
	total.ConnMode = flvx.ConnModeTotal
	m.state.Payload.Desired.Nodes = append(m.state.Payload.Desired.Nodes, inbound, total)
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	for tag, wantInbound := range map[string]bool{"flvxl-one": false, "flvxl-two": true, "flvxl-three": false} {
		p := portLimit(t, f.limits, tag)
		if wantInbound != (p.MaxInboundConnections != nil && *p.MaxInboundConnections == 4 && p.MaxTotalConnections == nil) ||
			!wantInbound != (p.MaxTotalConnections != nil && *p.MaxTotalConnections == 4 && p.MaxInboundConnections == nil) {
			t.Fatalf("%s: total=%v inbound=%v", tag, p.MaxTotalConnections, p.MaxInboundConnections)
		}
	}
	// No budgets: no groups, the Core config has today's shape.
	if len(f.limits.ManagementMappings) != 0 || len(f.limits.ManagementLimits) != 0 {
		t.Fatalf("groups without budgets: %+v %+v", f.limits.ManagementMappings, f.limits.ManagementLimits)
	}
}

// The FLVX agent's formulas (go-gost/x/landing/user_limits.go), case by case.
func TestFLVXGroupLimitFormulas(t *testing.T) {
	cases := []struct {
		name       string
		b          flvx.UserLimitBudget
		live       flvxGroupLive
		conns      *int64
		blockConns bool
		ips        *int
		blockIPs   bool
	}{
		{name: "unlimited", b: flvx.UserLimitBudget{}, live: flvxGroupLive{current: 5}},
		{name: "relay add-back", b: flvx.UserLimitBudget{MaxInboundConnections: ulInt64(2)}, live: flvxGroupLive{current: 6, relayConns: 4}, conns: ulInt64(6)},
		{name: "server cap below the total", b: flvx.UserLimitBudget{MaxInboundConnections: ulInt64(40), ServerMaxInboundConnections: ulInt64(7)}, live: flvxGroupLive{relayConns: 3}, conns: ulInt64(7)},
		{name: "server cap only", b: flvx.UserLimitBudget{ServerMaxInboundConnections: ulInt64(100)}, conns: ulInt64(100)},
		{name: "full and idle is blocked", b: flvx.UserLimitBudget{MaxInboundConnections: ulInt64(0)}, blockConns: true},
		{name: "full with open leases gets 1", b: flvx.UserLimitBudget{MaxInboundConnections: ulInt64(0)}, live: flvxGroupLive{current: 2}, conns: ulInt64(1)},
		{name: "server cap 0 idle", b: flvx.UserLimitBudget{ServerMaxInboundConnections: ulInt64(0)}, blockConns: true},
		{name: "IPs with reserve", b: flvx.UserLimitBudget{MaxInboundOnlineIPs: ulInt(3), RelayIPReserve: 1}, ips: ulInt(4)},
		{name: "IPs: seen relays beat the reserve", b: flvx.UserLimitBudget{MaxInboundOnlineIPs: ulInt(3), RelayIPReserve: 1}, live: flvxGroupLive{relayIPs: 2}, ips: ulInt(5)},
		{name: "IPs 0 nothing seen", b: flvx.UserLimitBudget{MaxInboundOnlineIPs: ulInt(0)}, blockIPs: true},
		{name: "IPs 0 with a seen IP gets 1", b: flvx.UserLimitBudget{MaxInboundOnlineIPs: ulInt(0)}, live: flvxGroupLive{seenIPs: 1}, ips: ulInt(1)},
		{name: "IPs 0 but a relay reserve", b: flvx.UserLimitBudget{MaxInboundOnlineIPs: ulInt(0), RelayIPReserve: 1}, ips: ulInt(1)},
	}
	for _, c := range cases {
		conns, blockConns := flvxGroupConnLimit(c.b, c.live)
		ips, blockIPs := flvxGroupIPLimit(c.b, c.live)
		if !reflect.DeepEqual(conns, c.conns) || blockConns != c.blockConns || !reflect.DeepEqual(ips, c.ips) || blockIPs != c.blockIPs {
			t.Errorf("%s: conns=%v/%v ips=%v/%v", c.name, deref64(conns), blockConns, derefInt(ips), blockIPs)
		}
	}
}

func deref64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func derefInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

// §5: one group per owner with budgets, mapped by identity; another owner's
// nodes stay out; the group limit uses the pass's snapshot (relay add-back);
// usage excludes relay sources and grace-only IPs, IPv4-mapped normalised.
func TestFLVXUserGroupsMappingAndUsage(t *testing.T) {
	m, f := fakeFLVXManager(t)
	now := time.Now()
	m.clock = func() time.Time { return now }
	m.state.Payload.Desired.Nodes = []flvx.Node{ownedNode("a1", 30001, "26"), ownedNode("a2", 30002, "26"), ownedNode("b1", 30004, "31")}
	if err := m.submitUserLimits(signedBudgets(m, now, 100, []string{"198.51.100.7", "relay.example"},
		flvx.UserLimitBudget{UserID: "26", MaxInboundConnections: ulInt64(2), MaxInboundOnlineIPs: ulInt(1), RelayIPReserve: 1})); err != nil {
		t.Fatal(err)
	}
	f.snap = coreSnapshotResponse{
		Users: []coreUserSnapshot{
			{Identity: coreIdentity{InboundTag: "flvxl-a1", User: "flvxl-a1"}, InboundCurrent: 3, CurrentTotal: 7},
			{Identity: coreIdentity{InboundTag: "flvxl-a2", User: "flvxl-a2"}, InboundCurrent: 2, CurrentTotal: 4},
			{Identity: coreIdentity{InboundTag: "flvxl-b1", User: "flvxl-b1"}, InboundCurrent: 1, CurrentTotal: 2},
		},
		ManagementGroups: []managementGroupSnapshot{{Username: "flvx-user-26", InboundCurrent: 5, InboundOnlineIPs: []onlineIP{
			{IP: "::ffff:198.51.100.7", Connections: 3}, {IP: "203.0.113.9", Connections: 2}, {IP: "203.0.113.10", Connections: 0},
		}}, {Username: "mmwx-user", InboundCurrent: 9}},
	}
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantMappings := []managementMapping{
		{Identity: coreIdentity{InboundTag: "flvxl-a1", User: "flvxl-a1"}, Group: "flvx-user-26"},
		{Identity: coreIdentity{InboundTag: "flvxl-a2", User: "flvxl-a2"}, Group: "flvx-user-26"},
	}
	if !reflect.DeepEqual(f.limits.ManagementMappings, wantMappings) {
		t.Fatalf("mappings %+v", f.limits.ManagementMappings)
	}
	g := groupLimit(f.limits, "flvx-user-26")
	// 2 + 3 relay connections; 1 IP + max(1 relay IP seen, reserve 1).
	if g == nil || deref64(g.MaxInboundConnections) != int64(5) || derefInt(g.MaxInboundOnlineIPs) != 2 || g.MaxTotalConnections != nil || len(f.limits.ManagementLimits) != 1 {
		t.Fatalf("group limits %+v", f.limits.ManagementLimits)
	}
	if n := len(*f.limits.BlockedIdentities); n != 0 {
		t.Fatalf("blocked %v", *f.limits.BlockedIdentities)
	}
	u := m.userLimitsUpload()
	want := &flvx.UserLimitsUsage{HeldSequence: 100, AppliedSequence: 100, CollectedAt: u.CollectedAt, Users: []flvx.UserLimitUsage{{UserID: "26", InboundConnections: 2, RelayConnections: 3, OnlineIPs: []string{"203.0.113.9"}}}}
	if !reflect.DeepEqual(u, want) || u.CollectedAt != now.UnixMilli() {
		t.Fatalf("usage %+v", u)
	}
	// The node status carries the 入站 count for every node.
	inbound := map[string]int64{}
	for _, n := range m.snapshotReport().Nodes {
		if n.InboundConnections == nil {
			t.Fatalf("node %s without inbound_connections", n.ID)
		}
		inbound[n.ID] = *n.InboundConnections
	}
	if !reflect.DeepEqual(inbound, map[string]int64{"a1": 3, "a2": 2, "b1": 1}) {
		t.Fatalf("inbound %v", inbound)
	}
}

// A full idle group is blocked by identity; once it has budget again the
// block goes away within the next pass.
func TestFLVXUserGroupBlockAndRelease(t *testing.T) {
	m, f := fakeFLVXManager(t)
	now := time.Now()
	m.clock = func() time.Time { return now }
	m.state.Payload.Desired.Nodes = []flvx.Node{ownedNode("a1", 30001, "26"), ownedNode("b1", 30004, "31")}
	m.submitUserLimits(signedBudgets(m, now, 1, nil, flvx.UserLimitBudget{UserID: "26", MaxInboundConnections: ulInt64(0)}, flvx.UserLimitBudget{UserID: "31", ServerMaxInboundConnections: ulInt64(3)}))
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*f.limits.BlockedIdentities, []coreIdentity{{InboundTag: "flvxl-a1", User: "flvxl-a1"}}) {
		t.Fatalf("blocked %+v", *f.limits.BlockedIdentities)
	}
	if g := groupLimit(f.limits, "flvx-user-26"); g == nil || g.MaxInboundConnections != nil {
		t.Fatalf("full group %+v", g)
	}
	if g := groupLimit(f.limits, "flvx-user-31"); g == nil || deref64(g.MaxInboundConnections) != int64(3) {
		t.Fatalf("other owner %+v", g)
	}
	now = now.Add(5 * time.Second)
	m.submitUserLimits(signedBudgets(m, now, 2, nil, flvx.UserLimitBudget{UserID: "26", MaxInboundConnections: ulInt64(1)}))
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*f.limits.BlockedIdentities) != 0 || groupLimit(f.limits, "flvx-user-31") != nil || deref64(groupLimit(f.limits, "flvx-user-26").MaxInboundConnections) != int64(1) {
		t.Fatalf("after release: blocked %+v limits %+v", *f.limits.BlockedIdentities, f.limits.ManagementLimits)
	}
}

// Budgets live in memory only: changing them every pass writes nothing to
// disk and makes no extra Core call; they expire 10 minutes after the last
// exchange (fail open) and an older sequence never replaces newer budgets.
func TestFLVXBudgetsMemoryOnlyAndExpiry(t *testing.T) {
	m, f := fakeFLVXManager(t)
	now := time.Now()
	m.clock = func() time.Time { return now }
	m.state.Payload.Desired.Nodes = []flvx.Node{ownedNode("a1", 30001, "26")}
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.writes, f.calls = 0, 0
	for i := int64(1); i <= 12; i++ {
		now = now.Add(5 * time.Second)
		m.submitUserLimits(signedBudgets(m, now, i, nil, flvx.UserLimitBudget{UserID: "26", MaxInboundConnections: ulInt64(i)}))
		f.snap.Users = []coreUserSnapshot{{Identity: coreIdentity{InboundTag: "flvxl-a1", User: "flvxl-a1"}, InboundCurrent: i % 3, CurrentTotal: i}}
		if err := m.reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		if deref64(groupLimit(f.limits, "flvx-user-26").MaxInboundConnections) != i {
			t.Fatalf("pass %d did not apply its budget", i)
		}
	}
	// Same as the steady-state heartbeat: one write per 30 s, one CLI call per pass.
	if f.writes > 2 || f.calls > 12 {
		t.Fatalf("budget changes wrote %d times, %d CLI calls", f.writes, f.calls)
	}
	raw, _ := json.Marshal(m.state)
	if strings.Contains(string(raw), "max_inbound_connections") || strings.Contains(string(raw), "flvx-user") {
		t.Fatal("budgets reached the persisted state")
	}
	// An older sequence is ignored while the newer budgets are in force.
	m.submitUserLimits(signedBudgets(m, now, 3, nil, flvx.UserLimitBudget{UserID: "26", MaxInboundConnections: ulInt64(99)}))
	if m.heldBudgetSequence() != 12 {
		t.Fatal("older sequence replaced newer budgets")
	}
	// No exchange for 10 minutes: no groups any more.
	now = now.Add(flvx.UserLimitsTTL)
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.limits.ManagementMappings) != 0 || len(f.limits.ManagementLimits) != 0 || m.heldBudgetSequence() != 0 {
		t.Fatalf("expired budgets still applied: %+v", f.limits.ManagementLimits)
	}
	// After expiry an older sequence is taken (FLVX restarted with an earlier clock).
	if err := m.submitUserLimits(signedBudgets(m, now, 5, nil, flvx.UserLimitBudget{UserID: "26", MaxInboundConnections: ulInt64(4)})); err != nil || m.heldBudgetSequence() != 5 {
		t.Fatalf("post-expiry budgets: %v %d", err, m.heldBudgetSequence())
	}
}

func TestFLVXSubmitUserLimitsRejectsForgeries(t *testing.T) {
	m, _ := fakeFLVXManager(t)
	now := time.Now()
	m.clock = func() time.Time { return now }
	good := signedBudgets(m, now, 4, nil, flvx.UserLimitBudget{UserID: "26"})
	forged := *good
	forged.Payload.Users = []flvx.UserLimitBudget{{UserID: "26", MaxInboundConnections: ulInt64(1000)}}
	other := *good
	other.Payload.ServerID = "8"
	other.Sign(m.config.Token)
	for name, e := range map[string]*flvx.UserLimitsEnvelope{"forged": &forged, "other server": &other} {
		if err := m.submitUserLimits(e); err == nil || m.heldBudgetSequence() != 0 {
			t.Fatalf("%s accepted", name)
		}
	}
	bad := signedBudgets(m, now, 4, nil, flvx.UserLimitBudget{UserID: "26", MaxInboundConnections: ulInt64(-1)})
	if err := m.submitUserLimits(bad); err == nil {
		t.Fatal("invalid budget accepted")
	}
	if err := m.submitUserLimits(nil); err != nil {
		t.Fatal(err)
	}
	var none *flvxManager
	if none.submitUserLimits(good) != nil || none.userLimitsUpload() != nil {
		t.Fatal("nil manager")
	}
	// Before any FLVX state is published there is nothing to upload.
	if m.userLimitsUpload() != nil {
		t.Fatal("upload part without a share")
	}
	if err := m.submitUserLimits(good); err != nil || m.heldBudgetSequence() != 4 {
		t.Fatal(err)
	}
}

// A share that is paused / without the owner's nodes maps nothing, and a
// failed pass clears the usage instead of reporting stale numbers.
func TestFLVXUserLimitsPausedAndFailure(t *testing.T) {
	m, f := fakeFLVXManager(t)
	now := time.Now()
	m.clock = func() time.Time { return now }
	m.state.Payload.Desired.Nodes = []flvx.Node{ownedNode("a1", 30001, "26")}
	m.submitUserLimits(signedBudgets(m, now, 1, nil, flvx.UserLimitBudget{UserID: "26", MaxInboundConnections: ulInt64(5)}))
	f.snap.ManagementGroups = []managementGroupSnapshot{{Username: "flvx-user-26", InboundCurrent: 1, InboundOnlineIPs: []onlineIP{{IP: "203.0.113.9", Connections: 1}}}}
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if u := m.userLimitsUpload(); len(u.Users) != 1 {
		t.Fatalf("usage %+v", u)
	}
	m.stop = func(context.Context) error { return nil }
	m.reportFailure(context.Background(), errors.New("boom"))
	if u := m.userLimitsUpload(); len(u.Users) != 0 {
		t.Fatalf("stale usage after failure %+v", u)
	}
	m.state.Payload.Status = "paused"
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.limits.ManagementMappings) != 0 {
		t.Fatalf("paused share mapped %+v", f.limits.ManagementMappings)
	}
}
