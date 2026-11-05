package main

import (
	"context"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// landing-node-total-v1 §2 / §4: max_total_connections becomes the port's
// max_total_connections next to an inbound node limit; next to a 入站+出站
// node limit the smaller one wins; nodes without it map exactly as before.
func TestFLVXNodeTotalMapsPortLimits(t *testing.T) {
	m, f := fakeFLVXManager(t)
	node := func(id string, port int, mode string, conns, total int64) flvx.Node {
		n := helperFLVXNode(id, port)
		n.ConnMode, n.MaxConnections, n.MaxTotalConnections = mode, conns, total
		return n
	}
	m.state.Payload.Desired.Nodes = append(m.state.Payload.Desired.Nodes,
		node("in-total", 30002, flvx.ConnModeInbound, 100, 400),
		node("min-total", 30004, "", 4, 3),
		node("min-conns", 30005, "", 4, 400),
		node("total-only", 30006, "", 0, 400),
		node("in-zero", 30007, flvx.ConnModeInbound, 0, 400),
		node("mode-total", 30008, flvx.ConnModeTotal, 10, 5),
		node("in-only", 30009, flvx.ConnModeInbound, 7, 0),
	)
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	str := func(v *int64) any {
		if v == nil {
			return nil
		}
		return *v
	}
	for tag, want := range map[string][2]any{
		"flvxl-one":        {nil, int64(4)}, // unchanged: no total
		"flvxl-in-total":   {int64(100), int64(400)},
		"flvxl-min-total":  {nil, int64(3)},
		"flvxl-min-conns":  {nil, int64(4)},
		"flvxl-total-only": {nil, int64(400)},
		"flvxl-in-zero":    {nil, int64(400)},
		"flvxl-mode-total": {nil, int64(5)},
		"flvxl-in-only":    {int64(7), nil},
	} {
		p := portLimit(t, f.limits, tag)
		if got := [2]any{str(p.MaxInboundConnections), str(p.MaxTotalConnections)}; got != want {
			t.Errorf("%s: inbound/total = %v, want %v", tag, got, want)
		}
		if p.MaxInboundOnlineIPs == nil || *p.MaxInboundOnlineIPs != 2 {
			t.Errorf("%s: online IPs %v", tag, p.MaxInboundOnlineIPs)
		}
	}
	// The share cap stays the global total, untouched by node totals.
	if f.limits.MaxGlobalTotalConnections == nil || *f.limits.MaxGlobalTotalConnections != 6 {
		t.Fatalf("global total %v", f.limits.MaxGlobalTotalConnections)
	}
}

func flvxTCPUser(tag, user string, in, out tcpStateCounts) coreUserSnapshot {
	return coreUserSnapshot{Identity: coreIdentity{InboundTag: tag, User: user}, CurrentTotal: in.Total + out.Total, InboundCurrent: in.Established, InboundTCP: in, OutboundTCP: out}
}

// §3: nodes[].tcp is summed over the node's Core identities from the snapshot
// already read; inbound + outbound == connections, otherwise no tcp.
func TestFLVXNodeTCPReport(t *testing.T) {
	m, f := fakeFLVXManager(t)
	m.state.Payload.Desired.Nodes = append(m.state.Payload.Desired.Nodes, helperFLVXNode("two", 30002), helperFLVXNode("idle", 30004))
	f.snap.Users = []coreUserSnapshot{
		flvxTCPUser("flvxl-one", "flvxl-one", tcpStateCounts{Total: 10, Established: 6, TimeWait: 3, SynRecv: 1}, tcpStateCounts{Total: 60, Established: 15, TimeWait: 40, CloseWait: 5}),
		flvxTCPUser("flvxl-one", "", tcpStateCounts{Total: 4, Established: 1, CloseWait: 2, FinWait2: 1}, tcpStateCounts{Total: 13, Established: 3, TimeWait: 10}),
		// A Core whose current_total disagrees with its state counts.
		{Identity: coreIdentity{InboundTag: "flvxl-two", User: "flvxl-two"}, CurrentTotal: 5, InboundTCP: tcpStateCounts{Total: 2, Established: 2}, OutboundTCP: tcpStateCounts{Total: 2, Established: 2}},
		// mmwx's own tags never reach FLVX.
		flvxTCPUser("vless-in", "alice", tcpStateCounts{Total: 9, Established: 9}, tcpStateCounts{}),
	}
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := map[string]flvx.NodeStatus{}
	for _, n := range m.snapshotReport().Nodes {
		got[n.ID] = n
	}
	one := got["one"]
	if one.State != "active" || one.Connections != 87 || one.TCP == nil || *one.TCP != (flvx.NodeTCP{Inbound: 14, Outbound: 73, Established: 25, TimeWait: 53, CloseWait: 7}) {
		t.Fatalf("one: %+v tcp %+v", one, one.TCP)
	}
	if two := got["two"]; two.Connections != 5 || two.TCP != nil {
		t.Fatalf("mismatched counts still reported: %+v tcp %+v", two, two.TCP)
	}
	if idle := got["idle"]; idle.Connections != 0 || idle.TCP == nil || *idle.TCP != (flvx.NodeTCP{}) {
		t.Fatalf("idle: %+v tcp %+v", idle, idle.TCP)
	}
	if _, ok := got["vless-in"]; ok {
		t.Fatal("mmwx tag reported")
	}

	// A removed node with sockets left still reports them, then disappears.
	m.state.Payload.Desired.Nodes = m.state.Payload.Desired.Nodes[1:]
	m.state.Payload.Desired.Revision++
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, n := range m.snapshotReport().Nodes {
		if n.ID == "one" && (n.State != "removed" || n.TCP == nil || n.TCP.Inbound+n.TCP.Outbound != n.Connections || n.Connections != 87) {
			t.Fatalf("removed one: %+v tcp %+v", n, n.TCP)
		}
	}
}

// The breakdown changes every 5 s like the connection counts: it must not
// make the Helper rewrite state.json more often (same bound as the steady
// state test).
func TestFLVXNodeTCPNoExtraStateWrites(t *testing.T) {
	m, f := fakeFLVXManager(t)
	now := time.Now()
	m.clock = func() time.Time { return now }
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.calls, f.writes = 0, 0
	ticks := int(time.Minute / (5 * time.Second))
	for i := 1; i <= ticks; i++ {
		now = now.Add(5 * time.Second)
		f.snap.Users = []coreUserSnapshot{flvxTCPUser("flvxl-one", "flvxl-one", tcpStateCounts{Total: int64(i), Established: 1}, tcpStateCounts{Total: int64(2 * i), TimeWait: int64(i)})}
		if err := m.reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		if tcp := m.state.Report.Nodes[0].TCP; tcp == nil || tcp.Inbound != int64(i) {
			t.Fatalf("tick %d: tcp %+v", i, tcp)
		}
		// Published with the connections of the same snapshot (the report
		// goes out on a material change or every 30 s, like connections).
		if n := m.snapshotReport().Nodes[0]; n.TCP == nil || n.TCP.Inbound+n.TCP.Outbound != n.Connections {
			t.Fatalf("tick %d: published %+v tcp %+v", i, n, n.TCP)
		}
	}
	t.Logf("steady minute with changing tcp: ticks=%d writes=%d CLI=%d", ticks, f.writes, f.calls)
	if f.writes > 3 || f.calls > ticks {
		t.Fatal("tcp breakdown raised the persistence or Core CLI rate")
	}
}
