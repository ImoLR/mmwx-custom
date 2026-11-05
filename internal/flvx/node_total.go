package flvx

// 节点总连接数 for shared landing nodes (contract landing-node-total-v1, FLVX
// plan 211): Node.MaxTotalConnections is the FLVX Core port
// max_total_connections (every TCP socket of the node's port in any state,
// TIME_WAIT / CLOSE_WAIT included), applied next to an inbound
// max_connections. NodeStatus.TCP is the port's socket breakdown from the same
// Core snapshot. Only the FLVX Core instance is involved; mmwx's own Core,
// users and settings never are.
const (
	NodeTotalVersion = 1
	// NodeTotalCapability: the Helper applies Node.MaxTotalConnections and
	// reports NodeStatus.TCP.
	NodeTotalCapability = "flvx.node_total.v1"

	MaxNodeTotalConnections = 10000000
)

// NodeTCP is a node's sockets by direction (any state) and how many of all of
// them are ESTABLISHED / TIME_WAIT / CLOSE_WAIT. Inbound + Outbound always
// equals the NodeStatus' Connections.
type NodeTCP struct {
	Inbound     int64 `json:"inbound"`
	Outbound    int64 `json:"outbound"`
	Established int64 `json:"established"`
	TimeWait    int64 `json:"time_wait"`
	CloseWait   int64 `json:"close_wait"`
}

// ValidFor reports whether t is a breakdown of connections sockets: no
// negative counts, the states within the sockets counted (FLVX drops anything
// else).
func (t *NodeTCP) ValidFor(connections int64) bool {
	return t != nil && t.Inbound >= 0 && t.Outbound >= 0 && t.Established >= 0 && t.TimeWait >= 0 && t.CloseWait >= 0 &&
		t.Inbound+t.Outbound == connections && t.Established+t.TimeWait+t.CloseWait <= connections
}
