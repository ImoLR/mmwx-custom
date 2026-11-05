package main

import "github.com/ImoLR/mmwx-custom/internal/flvx"

// 节点总连接数 (contract landing-node-total-v1, FLVX plan 211): Custom
// forwards nodes[].max_total_connections to Helpers with flvx.node_total.v1
// and relays their per-node NodeStatus.TCP. Only the FLVX Core instance is
// involved; mmwx's own Core, users and settings never are.

// flvxNodeTotalCapability is the share list's node_total object: only while
// the share's Helper applies the field, so FLVX sends nothing new to an old
// Helper.
func flvxNodeTotalCapability(helper flvxHelper) map[string]any {
	if !helper.Ready || !helper.NodeTotal {
		return nil
	}
	return map[string]any{"versions": []int{flvx.NodeTotalVersion}}
}

// flvxHelperNodeTotal strips max_total_connections for a Helper without
// flvx.node_total.v1: its strict payload check would reject the signed
// desired state. Like FLVX itself for such a runtime, the total is kept where
// the old fields can carry it (never unlimited): with a 入站+出站 node limit
// the smaller of the two; as the node limit when there is no 入站 limit;
// next to a 入站 limit it waits for the upgrade (FLVX shows 「待升级」).
// Runs after flvxHelperConnMode, so a stripped conn_mode counts 入站+出站.
func flvxHelperNodeTotal(d flvx.Desired, supported bool) flvx.Desired {
	if supported {
		return d
	}
	var nodes []flvx.Node
	for i, n := range d.Nodes {
		if n.MaxTotalConnections == 0 {
			continue
		}
		if nodes == nil {
			nodes = append([]flvx.Node(nil), d.Nodes...)
		}
		total := n.MaxTotalConnections
		n.MaxTotalConnections = 0
		switch {
		case !n.InboundLimit():
			if n.MaxConnections <= 0 || total < n.MaxConnections {
				n.MaxConnections = total
			}
		case n.MaxConnections <= 0:
			n.MaxConnections, n.ConnMode = total, ""
		}
		nodes[i] = n
	}
	if nodes != nil {
		d.Nodes = nodes
	}
	return d
}
