package main

import (
	"net/netip"
	"testing"
	"time"
)

func TestAggregateInboundPortOnlineIPsAndGrace(t *testing.T) {
	tracker := newOnlineIPTracker()
	now := time.Unix(100, 0)
	tracker.now = func() time.Time { return now }
	identity := coreIdentity{InboundTag: "in-a", User: "user-a"}
	core := coreSnapshotResponse{Version: 1, Users: []coreUserSnapshot{{
		Identity: identity, Attributed: true, InboundName: "shadowsocks", InboundPort: 12968,
		InboundActive: 2, OutboundActive: 4, OutboundNewRate: 3,
	}}}
	limit := 3
	settings := connectionSettings{OnlineIPGracePeriodSeconds: 30, Users: []userConnectionSettings{{Identity: identity, MaxInboundOnlineIPs: &limit}}}
	entries := []socketEntry{
		{LocalPort: 12968, RemoteIP: netip.MustParseAddr("198.51.100.1"), State: tcpEstablished},
		{LocalPort: 12968, RemoteIP: netip.MustParseAddr("198.51.100.1"), State: tcpCloseWait},
		{LocalPort: 12968, RemoteIP: netip.MustParseAddr("198.51.100.2"), State: tcpTimeWait},
	}
	inbounds, users := tracker.aggregate(entries, core, settings)
	if len(inbounds) != 1 || inbounds[0].Established != 1 || inbounds[0].CloseWait != 1 || inbounds[0].TimeWait != 1 || inbounds[0].OnlineIPCount != 1 {
		t.Fatalf("unexpected inbound: %#v", inbounds)
	}
	if len(users) != 1 || users[0].OutboundActive != 4 || users[0].Source != "xray_core_runtime" {
		t.Fatalf("unexpected users: %#v", users)
	}
	now = now.Add(20 * time.Second)
	inbounds, _ = tracker.aggregate(nil, core, settings)
	if inbounds[0].OnlineIPCount != 1 || inbounds[0].OnlineIPs[0].Connections != 0 {
		t.Fatalf("grace slot was not retained: %#v", inbounds[0])
	}
	now = now.Add(11 * time.Second)
	inbounds, _ = tracker.aggregate(nil, core, settings)
	if inbounds[0].OnlineIPCount != 0 {
		t.Fatalf("expired slot was retained: %#v", inbounds[0])
	}
}

func TestMultipleUsersOnOnePortAreNotFalselyAttributed(t *testing.T) {
	tracker := newOnlineIPTracker()
	core := coreSnapshotResponse{Users: []coreUserSnapshot{
		{Identity: coreIdentity{InboundTag: "shared", User: "a"}, Attributed: true, InboundPort: 443},
		{Identity: coreIdentity{InboundTag: "shared", User: "b"}, Attributed: true, InboundPort: 443},
	}}
	inbounds, _ := tracker.aggregate(nil, core, defaultConnectionSettings())
	if len(inbounds) != 1 || inbounds[0].Attribution != "inbound_port" || inbounds[0].User != "" {
		t.Fatalf("multi-user port was falsely attributed: %#v", inbounds)
	}
}

func TestAggregateV2UsesCoreIdentityTupleStatesAndLimits(t *testing.T) {
	tracker := newOnlineIPTracker()
	identity := coreIdentity{InboundTag: "in-v2", User: "user-v2"}
	totalLimit := int64(50)
	globalLimit := int64(500)
	core := coreSnapshotResponse{
		Version: 2,
		Global:  coreGlobalSnapshot{CurrentTotal: 7, MaxTotal: &globalLimit, RejectedGlobalTotalLimit: 2},
		Users: []coreUserSnapshot{{
			Identity: identity, Attributed: true, InboundName: "shadowsocks", InboundPort: 10022,
			CurrentTotal: 7, InboundActive: 3, InboundTCP: tcpStateCounts{Total: 4, Established: 2, TimeWait: 1, CloseWait: 1},
			InboundOnlineIPs: []onlineIP{{IP: "198.51.100.10", Connections: 2}},
			OutboundActive:   4, OutboundTCP: tcpStateCounts{Total: 5, Established: 3, SynSent: 1, TimeWait: 1},
			RejectedUserTotalLimit: 1, RejectedGlobalTotalLimit: 2,
		}},
	}
	settings := connectionSettings{OnlineIPGracePeriodSeconds: 30, Users: []userConnectionSettings{{Identity: identity, MaxTotalConnections: &totalLimit}}}
	// This unrelated kernel entry must not be guessed into a v2 identity.
	entries := []socketEntry{{LocalPort: 10022, RemoteIP: netip.MustParseAddr("203.0.113.1"), State: tcpEstablished}}
	inbounds, users := tracker.aggregate(entries, core, settings)
	if len(inbounds) != 1 || inbounds[0].Attribution != "core_identity_tuple" || inbounds[0].TCP.TimeWait != 1 || inbounds[0].OnlineIPCount != 1 {
		t.Fatalf("v2 inbound was not sourced from Core: %#v", inbounds)
	}
	if len(users) != 1 || users[0].CurrentTotal != 7 || users[0].OutboundTCP.SynSent != 1 || users[0].MaxTotalConnections == nil || *users[0].MaxTotalConnections != 50 {
		t.Fatalf("v2 user snapshot mismatch: %#v", users)
	}
	config := connectionSettings{GlobalTotalLimitEnabled: true, MaxGlobalTotalConnections: &globalLimit, Users: settings.Users}.coreConfig()
	if config.MaxGlobalTotalConnections == nil || *config.MaxGlobalTotalConnections != 500 || config.Limits[0].MaxTotalConnections == nil || *config.Limits[0].MaxTotalConnections != 50 {
		t.Fatalf("limits were not propagated to Core config: %#v", config)
	}
}

func TestAggregateV2IncludesConfiguredInboundsAndUsersBeforeTraffic(t *testing.T) {
	tracker := newOnlineIPTracker()
	core := coreSnapshotResponse{
		Version: 2,
		Inbounds: []coreInboundSnapshot{
			{InboundTag: "ss-12311", InboundName: "shadowsocks-2022", InboundPort: 12311},
			{InboundTag: "ss-10015", InboundName: "shadowsocks-2022-multi", InboundPort: 10015, Users: []string{"user-a", "user-b"}},
		},
	}
	inbounds, users := tracker.aggregate(nil, core, defaultConnectionSettings())
	if len(inbounds) != 2 || inbounds[0].Port != 10015 || inbounds[1].Port != 12311 {
		t.Fatalf("configured inbounds = %#v", inbounds)
	}
	if inbounds[0].Attribution != "core_identity_tuple" || inbounds[1].Attribution != "core_inbound" {
		t.Fatalf("configured attribution = %#v", inbounds)
	}
	if len(users) != 2 || users[0].InboundTag != "ss-10015" || users[0].CurrentTotal != 0 {
		t.Fatalf("configured users = %#v", users)
	}
}
