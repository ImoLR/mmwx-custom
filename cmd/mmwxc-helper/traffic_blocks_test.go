package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCoreClientTrafficBlocksV7AndClear(t *testing.T) {
	var requests [][]coreIdentity
	path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/snapshot" {
			_, _ = w.Write([]byte(`{"version":7,"proxy_users":[{"identity":{"inbound_tag":"in-a","user":"proto-a"},"attributed":true,"blocked":true,"rejected_blocked":3}]}`))
			return
		}
		var config coreConfig
		if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
			t.Error(err)
		}
		if config.BlockedIdentities == nil || *config.BlockedIdentities == nil {
			t.Error("config must include an explicit blocked_identities array")
		} else {
			requests = append(requests, *config.BlockedIdentities)
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	client := newCoreClient(path)
	settings := defaultConnectionSettings()
	settings.BlockedIdentities = []coreIdentity{{InboundTag: "in-a", User: "proto-a"}}
	before := collectDetailedSnapshot(context.Background(), client, newOnlineIPTracker(), settings)
	if before.Core.TrafficBlockSupported {
		t.Fatal("capability reported before Core accepted configuration")
	}
	if err := client.apply(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	snapshot := collectDetailedSnapshot(context.Background(), client, newOnlineIPTracker(), settings)
	if !snapshot.Core.TrafficBlockSupported || snapshot.Core.Version != 7 || len(snapshot.ProxyUsers) != 1 || !snapshot.ProxyUsers[0].Blocked || snapshot.ProxyUsers[0].RejectedBlocked != 3 {
		t.Fatalf("v7 traffic block status was not forwarded: %#v", snapshot)
	}
	settings.BlockedIdentities = []coreIdentity{}
	if err := client.apply(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || len(requests[0]) != 1 || requests[0][0].User != "proto-a" || len(requests[1]) != 0 || !client.trafficBlockSupported {
		t.Fatalf("blocked list was not replaced and cleared: %#v", requests)
	}
}

func TestCoreClientTrafficBlocksFallbackPreservesV6Limits(t *testing.T) {
	for _, block := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "blocked"}[block], func(t *testing.T) {
			requests := 0
			limit := int64(40)
			path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/snapshot" {
					_, _ = w.Write([]byte(`{"version":6}`))
					return
				}
				requests++
				var config coreConfig
				if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
					t.Error(err)
				}
				if config.BlockedIdentities != nil {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"json: unknown field blocked_identities"}`))
					return
				}
				if len(config.PortLimits) != 1 || config.PortLimits[0].MaxTotalConnections == nil || *config.PortLimits[0].MaxTotalConnections != limit || len(config.ManagementLimits) != 1 || config.ManagementLimits[0].MaxTotalConnections == nil || *config.ManagementLimits[0].MaxTotalConnections != limit {
					t.Errorf("fallback lost supported v6 limits: %#v", config)
				}
				_, _ = w.Write([]byte(`{"success":true}`))
			}))
			client := newCoreClient(path)
			settings := defaultConnectionSettings()
			if block {
				settings.BlockedIdentities = []coreIdentity{{InboundTag: "in-a", User: "proto-a"}}
			}
			settings.Ports = []portConnectionSettings{{InboundTag: "in-a", MaxTotalConnections: &limit}}
			settings.ManagementUsers = []managementUserSettings{{Username: "alice", MaxTotalConnections: &limit}}
			if err := client.apply(context.Background(), settings); err != nil {
				t.Fatal(err)
			}
			snapshot := collectDetailedSnapshot(context.Background(), client, newOnlineIPTracker(), settings)
			if requests != 2 || snapshot.Core.TrafficBlockSupported || !snapshot.Core.Available {
				t.Fatalf("v6 fallback falsely reported block support: requests=%d, core=%#v", requests, snapshot.Core)
			}
		})
	}
}

func TestCoreClientTrafficBlockSupportRequiresSuccessfulApply(t *testing.T) {
	failed := false
	version := 7
	path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/snapshot" {
			_ = json.NewEncoder(w).Encode(coreSnapshotResponse{Version: version})
			return
		}
		if failed {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	client := newCoreClient(path)
	settings := defaultConnectionSettings()
	if err := client.apply(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	version = 6
	if snapshot := collectDetailedSnapshot(context.Background(), client, newOnlineIPTracker(), settings); snapshot.Core.TrafficBlockSupported {
		t.Fatal("old version reported support despite accepting unknown fields")
	}
	version = 7
	failed = true
	if err := client.apply(context.Background(), settings); err == nil {
		t.Fatal("failed apply accepted")
	}
	if snapshot := collectDetailedSnapshot(context.Background(), client, newOnlineIPTracker(), settings); snapshot.Core.TrafficBlockSupported {
		t.Fatal("failed configuration did not revoke capability")
	}
}

func TestTrafficBlocksPersistAndAbsentControllerSettingsPreserve(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := localState{Settings: defaultConnectionSettings()}
	identity := coreIdentity{InboundTag: "in-a", User: "proto-a"}
	state.Settings.BlockedIdentities = []coreIdentity{identity}
	if err := saveLocalState(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadLocalState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Settings.BlockedIdentities, state.Settings.BlockedIdentities) || !reflect.DeepEqual(*loaded.Settings.coreConfig().BlockedIdentities, state.Settings.BlockedIdentities) {
		t.Fatalf("restart lost persisted Core blocks: %#v", loaded.Settings)
	}
	for _, body := range []string{`{"settings":{"online_ip_grace_period_seconds":40}}`, `{"settings":{"blocked_identities":null}}`} {
		var response detailedMetricsResponse
		if err := json.Unmarshal([]byte(body), &response); err != nil {
			t.Fatal(err)
		}
		loaded.updateSettings(response.Settings)
		if !reflect.DeepEqual(loaded.Settings.BlockedIdentities, []coreIdentity{identity}) {
			t.Fatal("absent traffic decision cleared persisted blocks")
		}
	}
	var response detailedMetricsResponse
	if err := json.Unmarshal([]byte(`{"settings":{"blocked_identities":[]}}`), &response); err != nil {
		t.Fatal(err)
	}
	loaded.updateSettings(response.Settings)
	if loaded.Settings.BlockedIdentities == nil || len(loaded.Settings.BlockedIdentities) != 0 {
		t.Fatal("explicit empty traffic decision did not clear persisted blocks")
	}
	if err := saveLocalState(path, loaded); err != nil {
		t.Fatal(err)
	}
	loaded, err = loadLocalState(path)
	if err != nil || len(loaded.Settings.BlockedIdentities) != 0 {
		t.Fatalf("cleared decision did not survive restart: %#v, %v", loaded.Settings, err)
	}
}
