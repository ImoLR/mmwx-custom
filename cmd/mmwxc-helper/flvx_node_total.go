package main

import (
	"strings"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// flvxTCPByNode is landing-node-total-v1's NodeStatus.TCP from the snapshot
// the Helper already read: per node, the sum over its Core identities, keyed
// like the node's connections so Inbound + Outbound matches them (the Core's
// current_total is inbound_tcp + outbound_tcp tcp_total). Every Core the FLVX
// runtime accepts (snapshot v7) reports these state counts.
func flvxTCPByNode(snapshot coreSnapshotResponse) map[string]flvx.NodeTCP {
	tcp := map[string]flvx.NodeTCP{}
	for _, u := range snapshot.Users {
		id := strings.TrimPrefix(u.Identity.InboundTag, "flvxl-")
		t := tcp[id]
		t.Inbound += u.InboundTCP.Total
		t.Outbound += u.OutboundTCP.Total
		t.Established += u.InboundTCP.Established + u.OutboundTCP.Established
		t.TimeWait += u.InboundTCP.TimeWait + u.OutboundTCP.TimeWait
		t.CloseWait += u.InboundTCP.CloseWait + u.OutboundTCP.CloseWait
		tcp[id] = t
	}
	return tcp
}
