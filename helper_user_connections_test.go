package main

import (
	"testing"
	"time"
)

func TestAggregateHelperUserConnectionsUsesFreshExternalSnapshots(t *testing.T) {
	now := time.Now().UTC()
	records := map[string]serverDetailedConnectionRecord{
		"5": {
			UpdatedAt: now,
			Snapshot: serverDetailedConnectionSnapshot{
				Core: serverCoreConnectionStatus{Version: 5},
				ManagementGroups: []serverManagementGroupConnections{
					{Username: "ken", InboundCurrent: 4, CurrentTotal: 8},
					{Username: "ken", InboundCurrent: 2, CurrentTotal: 4},
				},
			},
		},
		"12": {
			UpdatedAt: now.Add(-helperStaleTimeout - time.Second),
			Snapshot:  serverDetailedConnectionSnapshot{Core: serverCoreConnectionStatus{Version: 5}, ManagementGroups: []serverManagementGroupConnections{{Username: "stale", InboundCurrent: 9}}},
		},
		"15": {
			UpdatedAt: now,
			Snapshot:  serverDetailedConnectionSnapshot{Core: serverCoreConnectionStatus{Version: 5}, ManagementGroups: []serverManagementGroupConnections{{Username: "embedded", InboundCurrent: 7}}},
		},
		"14": {
			UpdatedAt: now,
			Snapshot:  serverDetailedConnectionSnapshot{Core: serverCoreConnectionStatus{Version: 3}, ManagementGroups: []serverManagementGroupConnections{{Username: "legacy", InboundActive: 3, InboundCurrent: 0}}},
		},
	}

	connections, byServer, available := aggregateHelperUserConnections(now, records, map[string]bool{"5": true, "12": true, "14": true})
	if connections["ken"] != 6 || connections["legacy"] != 3 || len(connections) != 2 {
		t.Fatalf("unexpected aggregate: %#v", connections)
	}
	if byServer["5"]["ken"] != 6 || byServer["14"]["legacy"] != 3 || len(byServer) != 2 {
		t.Fatalf("unexpected per-server aggregate: %#v", byServer)
	}
	if len(available) != 2 || available[0] != "14" || available[1] != "5" {
		t.Fatalf("unexpected available server ids: %#v", available)
	}
}
