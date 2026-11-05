package flvx

import (
	"encoding/json"
	"strings"
	"testing"
)

// landing-node-total-v1 wire shape: nodes[].max_total_connections (omitted
// when 0, so a node without it encodes exactly as before) and the status
// nodes[].tcp object with FLVX's keys.
func TestNodeTotalWireShape(t *testing.T) {
	n := testNode()
	n.MaxConnections, n.ConnMode, n.MaxTotalConnections = 100, ConnModeInbound, 400
	raw, _ := json.Marshal(n)
	if !strings.Contains(string(raw), `"max_connections":100,"max_online_ips":0,"expires_at":null,"conn_mode":"inbound","max_total_connections":400,"ss2022"`) {
		t.Fatalf("node encoding %s", raw)
	}
	var back Node
	if err := json.Unmarshal([]byte(`{"id":"108","max_connections":100,"conn_mode":"inbound","max_total_connections":400}`), &back); err != nil || back.MaxTotalConnections != 400 || !back.InboundLimit() {
		t.Fatalf("decode %v %+v", err, back)
	}
	n.MaxTotalConnections = 0
	if raw, _ = json.Marshal(n); strings.Contains(string(raw), "max_total_connections") {
		t.Fatalf("zero total serialised: %s", raw)
	}

	conns := int64(87)
	s := NodeStatus{ID: "108", State: "active", Connections: conns, TCP: &NodeTCP{Inbound: 14, Outbound: 73, Established: 25, TimeWait: 55, CloseWait: 7}}
	raw, _ = json.Marshal(s)
	if !strings.HasSuffix(string(raw), `"connections":87,"tcp":{"inbound":14,"outbound":73,"established":25,"time_wait":55,"close_wait":7}}`) {
		t.Fatalf("status encoding %s", raw)
	}
	if raw, _ = json.Marshal(NodeStatus{ID: "1", State: "active"}); strings.Contains(string(raw), "tcp") {
		t.Fatalf("absent tcp serialised: %s", raw)
	}
}

func TestNodeTotalValidation(t *testing.T) {
	cfg := Config{PortMin: 30000, PortMax: 31000, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}
	for total, valid := range map[int64]bool{0: true, 1: true, 400: true, MaxNodeTotalConnections: true, -1: false, MaxNodeTotalConnections + 1: false} {
		n := testNode()
		n.MaxConnections, n.ConnMode, n.MaxTotalConnections = 3, ConnModeInbound, total
		problems := ValidateDesired(Desired{Revision: 1, Nodes: []Node{n}}, cfg, nil)
		if (len(problems) == 0) != valid || !valid && problems[0].Field != "max_total_connections" {
			t.Errorf("max_total_connections %d: %+v", total, problems)
		}
	}
}

func TestNodeTCPValidFor(t *testing.T) {
	ok := NodeTCP{Inbound: 14, Outbound: 73, Established: 25, TimeWait: 55, CloseWait: 7}
	if !ok.ValidFor(87) {
		t.Fatal("contract example rejected")
	}
	if !(&NodeTCP{}).ValidFor(0) {
		t.Fatal("idle node rejected")
	}
	var missing *NodeTCP
	for name, c := range map[string]struct {
		tcp   *NodeTCP
		conns int64
	}{
		"nil":            {missing, 0},
		"sum mismatch":   {&ok, 88},
		"negative":       {&NodeTCP{Inbound: -1, Outbound: 1}, 0},
		"states too big": {&NodeTCP{Inbound: 1, Outbound: 1, Established: 2, TimeWait: 1}, 2},
		"negative state": {&NodeTCP{Inbound: 1, CloseWait: -1}, 1},
	} {
		if c.tcp.ValidFor(c.conns) {
			t.Errorf("%s accepted", name)
		}
	}
}
