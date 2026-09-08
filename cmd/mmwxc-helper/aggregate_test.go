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
