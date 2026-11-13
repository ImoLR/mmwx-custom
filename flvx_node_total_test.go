package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// flvxTestNodeTotalHelper marks the server's Helper as v0.7.19 (scan guard,
// user limits), plus flvx.node_total.v1 (v0.7.20) when nodeTotal is set.
func flvxTestNodeTotalHelper(a *app, id int64, nodeTotal bool) {
	key := flvxID(id)
	status := agentStatus{Helper: componentStatus{Installed: true, Version: "v0.7.19"}, Architecture: "amd64", Capabilities: []string{"helper.update", "flvx.share", "flvx.egress.v1", "flvx.protocols.v1", "flvx.reality_guard.v1", flvx.UserLimitsCapability, flvx.ScanGuardCapability}}
	if nodeTotal {
		status.Helper.Version = "v0.7.20"
		status.Capabilities = append(status.Capabilities, flvx.NodeTotalCapability)
	}
	a.helperState.mu.Lock()
	a.helperState.data.Servers[key] = helperServerIdentity{OfficialServerID: key, HelperTokenHash: strings.Repeat("a", 64)}
	a.helperState.data.AgentStatuses[key] = status
	a.helperState.mu.Unlock()
}

// A Helper without flvx.node_total.v1 never sees max_total_connections; the
// total travels in the old fields where they can carry it, exactly as FLVX
// sends it to such a runtime (LandingSendLimits), so it is never unlimited.
func TestFLVXHelperNodeTotalStrip(t *testing.T) {
	node := func(mode string, conns, total int64) flvx.Node {
		return flvx.Node{ID: "1", ConnMode: mode, MaxConnections: conns, MaxTotalConnections: total}
	}
	for name, c := range map[string]struct{ in, want flvx.Node }{
		"inbound + total keeps inbound": {node("inbound", 100, 400), node("inbound", 100, 0)},
		"inbound 0 + total → old total": {node("inbound", 0, 400), node("", 400, 0)},
		"total-mode limit smaller":      {node("", 4, 400), node("", 4, 0)},
		"total smaller":                 {node("", 500, 400), node("", 400, 0)},
		"no limit + total":              {node("", 0, 400), node("", 400, 0)},
		"explicit total mode":           {node("total", 10, 5), node("total", 5, 0)},
		"no total untouched":            {node("inbound", 7, 0), node("inbound", 7, 0)},
	} {
		d := flvx.Desired{Revision: 1, Nodes: []flvx.Node{c.in}}
		got := flvxHelperNodeTotal(d, false).Nodes[0]
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", name, got, c.want)
		}
		if d.Nodes[0] != c.in {
			t.Errorf("%s: stored desired modified: %+v", name, d.Nodes[0])
		}
		if kept := flvxHelperNodeTotal(d, true).Nodes[0]; kept != c.in {
			t.Errorf("%s: capable Helper got %+v", name, kept)
		}
	}
	// A Helper without user limits too: conn_mode is stripped first, the
	// node limit counts 入站+出站, so the smaller of the two.
	d := flvx.Desired{Revision: 1, Nodes: []flvx.Node{node("inbound", 100, 40)}}
	if got := flvxHelperNodeTotal(flvxHelperConnMode(d, false), false).Nodes[0]; got != node("", 40, 0) {
		t.Fatalf("old Helper without user limits: %+v", got)
	}
	// Nothing to strip: the very same slice goes out (byte-identical payload).
	plain := flvx.Desired{Revision: 1, Nodes: []flvx.Node{node("inbound", 7, 0)}}
	if out := flvxHelperNodeTotal(plain, false); &out.Nodes[0] != &plain.Nodes[0] {
		t.Fatal("unchanged desired copied")
	}
}

// Contract landing-node-total-v1 on Custom: node_total in the share list only
// with a v0.7.20 Helper; max_total_connections validated, stored, forwarded
// to such a Helper and folded away for older ones (which verify the signed
// payload); the Helper's nodes[].tcp relayed in the status, bad ones dropped.
func TestFLVXNodeTotalPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	db := a.flvxDB()
	auditDeleteExec(t, db, `UPDATE mmwxc_flvx_integration SET token_hash=$1`, flvxTokenHash("flvx_fixture"))
	c := flvx.Config{PortMin: 30000, PortMax: 30999, ResetDay: 1, MemoryMB: 128, CPUPercent: 50}
	flvxTestNodeTotalHelper(a, 7, false)
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal(err)
	}
	list := func(path string) map[string]any {
		t.Helper()
		token := "flvx_fixture"
		if strings.Contains(path, "/admin") {
			token = "operator-token"
		}
		w := flvxTestRequest(a, "GET", path, token, nil)
		var body struct {
			Shares []map[string]any `json:"shares"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Shares) != 1 {
			t.Fatalf("list %s: %d %s", path, w.Code, w.Body.String())
		}
		return body.Shares[0]
	}
	put := func(body any) (int, string) {
		w := flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", "flvx_fixture", body)
		return w.Code, w.Body.String()
	}
	helperPayload := func(nodeTotal bool, r *flvx.Report) *flvx.Envelope {
		t.Helper()
		e, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, true, true, nodeTotal, false, false, false, r)
		if err != nil || e == nil || !e.Verify("helper", "7", time.Now()) {
			t.Fatalf("helper payload: %v %+v", err, e)
		}
		return e
	}
	base := flvxTestDesired().Nodes[0] // flvxl-11
	plain := base
	plain.MaxConnections, plain.ConnMode = 100, flvx.ConnModeInbound

	// Without max_total_connections the old Helper's payload is today's.
	if code, body := put(flvx.Desired{Revision: 1, Nodes: []flvx.Node{plain}}); code != 200 {
		t.Fatalf("plain desired: %d %s", code, body)
	}
	before, _ := json.Marshal(helperPayload(false, nil).Payload.Desired)
	if after, _ := json.Marshal(helperPayload(true, nil).Payload.Desired); string(before) != string(after) || strings.Contains(string(before), "max_total_connections") {
		t.Fatalf("plain payload differs per Helper:\n%s\n%s", before, after)
	}

	// v0.7.19 Helper: no capability in either list.
	for _, path := range []string{"/api/custom/flvx/v1/shares", "/api/custom/flvx/admin"} {
		if s := list(path); s["node_total"] != nil {
			t.Fatalf("%s: capability without a v0.7.20 Helper: %v", path, s["node_total"])
		}
	}
	// FLVX's field is accepted (strict decoding knows it) and validated.
	total := plain
	total.MaxTotalConnections = 400
	open := base
	open.ID, open.Port, open.MaxTotalConnections = "12", 30004, 400
	if code, body := put(flvx.Desired{Revision: 2, Nodes: []flvx.Node{total, open}}); code != 200 {
		t.Fatalf("desired with total: %d %s", code, body)
	}
	for _, bad := range []int64{-1, flvx.MaxNodeTotalConnections + 1} {
		n := total
		n.MaxTotalConnections = bad
		if code, body := put(flvx.Desired{Revision: 3, Nodes: []flvx.Node{n}}); code != 422 || !strings.Contains(body, "max_total_connections") {
			t.Fatalf("total %d accepted: %d %s", bad, code, body)
		}
	}
	old := helperPayload(false, nil)
	raw, _ := json.Marshal(old.Payload.Desired)
	if strings.Contains(string(raw), "max_total_connections") || len(old.Payload.Desired.Nodes) != 2 {
		t.Fatalf("old Helper got the field: %s", raw)
	}
	if n := old.Payload.Desired.Nodes; n[0].MaxConnections != 100 || n[0].ConnMode != "inbound" || n[1].MaxConnections != 400 || n[1].ConnMode != "" {
		t.Fatalf("old Helper fold: %+v", n)
	}

	// v0.7.20 Helper: capability, field forwarded as stored.
	flvxTestNodeTotalHelper(a, 7, true)
	for _, path := range []string{"/api/custom/flvx/v1/shares", "/api/custom/flvx/admin"} {
		if cap, _ := json.Marshal(list(path)["node_total"]); string(cap) != `{"versions":[1]}` {
			t.Fatalf("%s: capability %s", path, cap)
		}
	}
	if helper, _ := list("/api/custom/flvx/admin")["helper"].(map[string]any); helper["flvx_node_total"] != true {
		t.Fatalf("admin helper summary: %v", helper)
	}
	payload := helperPayload(true, nil)
	if n := payload.Payload.Desired.Nodes; n[0].MaxTotalConnections != 400 || n[0].MaxConnections != 100 || n[0].ConnMode != "inbound" || n[1].MaxTotalConnections != 400 || n[1].MaxConnections != 0 {
		t.Fatalf("forwarded %+v", n)
	}

	// Status: tcp relayed as reported; an inconsistent one dropped; a node
	// that disappeared from the report loses it with its connections.
	status := func() map[string]any {
		t.Helper()
		w := flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares/7/status", "flvx_fixture", nil)
		var body map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("status %d %s", w.Code, w.Body.String())
		}
		nodes := map[string]any{}
		for _, n := range body["nodes"].([]any) {
			nodes[n.(map[string]any)["id"].(string)] = n
		}
		return nodes
	}
	in := int64(14)
	r := flvxProtocolsReport(1, nil, nil, 2)
	r.Nodes = []flvx.NodeStatus{
		{ID: "11", State: "active", Connections: 87, InboundConnections: &in, TCP: &flvx.NodeTCP{Inbound: 14, Outbound: 73, Established: 25, TimeWait: 55, CloseWait: 7}},
		{ID: "12", State: "active", Connections: 5, TCP: &flvx.NodeTCP{Inbound: 1, Outbound: 1}},
	}
	helperPayload(true, r)
	nodes := status()
	if got, _ := json.Marshal(nodes["11"].(map[string]any)["tcp"]); string(got) != `{"close_wait":7,"established":25,"inbound":14,"outbound":73,"time_wait":55}` {
		t.Fatalf("tcp relayed as %s", got)
	}
	if n := nodes["12"].(map[string]any); n["tcp"] != nil || n["connections"] != float64(5) {
		t.Fatalf("inconsistent tcp kept: %v", n)
	}
	r = flvxProtocolsReport(2, nil, nil, 2)
	r.Nodes = []flvx.NodeStatus{{ID: "12", State: "active", Connections: 2, TCP: &flvx.NodeTCP{Inbound: 1, Outbound: 1, TimeWait: 1}}}
	helperPayload(true, r)
	nodes = status()
	if n := nodes["11"].(map[string]any); n["state"] != "removed" || n["connections"] != float64(0) || n["tcp"] != nil {
		t.Fatalf("removed node: %v", n)
	}
	if got, _ := json.Marshal(nodes["12"].(map[string]any)["tcp"]); string(got) != `{"close_wait":0,"established":0,"inbound":1,"outbound":1,"time_wait":1}` {
		t.Fatalf("tcp relayed as %s", got)
	}
}
