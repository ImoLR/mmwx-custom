package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func serveUnixHTTP(t *testing.T, handler http.Handler) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "core.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = os.Remove(path)
	})
	return path
}

func TestCoreClientMalformedResponse(t *testing.T) {
	path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"wrong"}`))
	}))
	if _, err := newCoreClient(path).snapshot(context.Background()); err == nil {
		t.Fatal("malformed Core response was accepted")
	}
}

func TestCoreClientAcceptsCurrentRejectionCounters(t *testing.T) {
	path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":1,"started_at":"2026-09-08T00:00:00Z","proxy_users":[{"identity":{"inbound_tag":"in-a","user":"user-a"},"attributed":true,"inbound_active":1,"inbound_total":2,"outbound_active":1,"outbound_pending":0,"outbound_new_total":3,"outbound_new_rate":1,"outbound_rejected_total":2,"rejected_active_limit":1,"rejected_new_rate_limit":1,"max_outbound_tcp_active":null,"max_outbound_tcp_new_per_second":null,"close_wait_timeout_seconds":null}]}`))
	}))
	snapshot, err := newCoreClient(path).snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Users) != 1 || snapshot.Users[0].RejectedActiveLimit != 1 || snapshot.Users[0].RejectedNewRateLimit != 1 {
		t.Fatalf("unexpected rejection counters: %#v", snapshot.Users)
	}
}

func TestCoreClientAcceptsV2TupleStatesAndGlobalLimit(t *testing.T) {
	path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":2,"started_at":"2026-09-13T00:00:00Z","global":{"current_total":4,"max_total":20,"rejected_global_total_limit":1},"inbounds":[{"inbound_tag":"in-a","inbound_name":"shadowsocks-2022-multi","inbound_port":10015,"users":["user-a"]}],"proxy_users":[{"identity":{"inbound_tag":"in-a","user":"user-a"},"attributed":true,"current_total":4,"inbound_active":2,"inbound_tcp":{"tcp_total":3,"established":2,"time_wait":1},"inbound_online_ips":[{"ip":"198.51.100.1","connections":2}],"outbound_active":2,"outbound_pending":0,"outbound_tcp":{"tcp_total":3,"established":2,"time_wait":1},"rejected_user_total_limit":1,"rejected_online_ip_limit":2,"rejected_global_total_limit":1}]}`))
	}))
	snapshot, err := newCoreClient(path).snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Global.CurrentTotal != 4 || snapshot.Global.MaxTotal == nil || *snapshot.Global.MaxTotal != 20 || len(snapshot.Inbounds) != 1 || snapshot.Inbounds[0].InboundPort != 10015 || len(snapshot.Users) != 1 || snapshot.Users[0].InboundTCP.TimeWait != 1 || snapshot.Users[0].OutboundTCP.TimeWait != 1 {
		t.Fatalf("v2 snapshot was not decoded: %#v", snapshot)
	}
}

func TestCoreClientAcceptsV3ManagementGroups(t *testing.T) {
	path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":3,"started_at":"2026-09-15T00:00:00Z","management_groups":[{"group":"ken","current_total":9,"outbound_active":6,"outbound_pending":1,"outbound_new_rate":4,"rejected_user_total_limit":2,"rejected_user_new_rate_limit":3}],"proxy_users":[{"identity":{"inbound_tag":"in-a","user":"proto-a"},"attributed":true,"management_group":"ken","rejected_port_total_limit":1,"rejected_port_new_rate_limit":2}]}`))
	}))
	snapshot, err := newCoreClient(path).snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ManagementGroups) != 1 || snapshot.ManagementGroups[0].Username != "ken" || snapshot.ManagementGroups[0].RejectedUserNewRateLimit != 3 || len(snapshot.Users) != 1 || snapshot.Users[0].ManagementGroup != "ken" || snapshot.Users[0].RejectedPortTotalLimit != 1 {
		t.Fatalf("v3 management data was not decoded: %#v", snapshot)
	}
}

func TestCoreClientAcceptsV4InboundLimits(t *testing.T) {
	path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":4,"started_at":"2026-09-24T00:00:00Z","global":{"current_inbound":7,"max_inbound":1000,"rejected_global_inbound_limit":2},"management_groups":[{"group":"ken","inbound_current":7,"inbound_online_ips":[{"ip":"198.51.100.1","connections":4}],"max_inbound_connections":100,"max_inbound_online_ips":3,"rejected_user_inbound_limit":1,"rejected_user_online_ip_limit":2}],"proxy_users":[{"identity":{"inbound_tag":"in-a","user":"proto-a"},"attributed":true,"inbound_current":4,"management_group":"ken","max_port_inbound_connections":60,"max_port_inbound_online_ips":2,"rejected_port_inbound_limit":3,"rejected_port_online_ip_limit":4}]}`))
	}))
	snapshot, err := newCoreClient(path).snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Global.CurrentInbound != 7 || snapshot.Global.MaxInbound == nil || *snapshot.Global.MaxInbound != 1000 || len(snapshot.ManagementGroups) != 1 || snapshot.ManagementGroups[0].InboundCurrent != 7 || snapshot.ManagementGroups[0].MaxInboundOnlineIPs == nil || *snapshot.ManagementGroups[0].MaxInboundOnlineIPs != 3 || len(snapshot.Users) != 1 || snapshot.Users[0].MaxPortInboundConnections == nil || *snapshot.Users[0].MaxPortInboundConnections != 60 {
		t.Fatalf("v4 inbound data was not decoded: %#v", snapshot)
	}
}

func TestCoreClientUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sock")
	if _, err := newCoreClient(path).snapshot(context.Background()); err == nil {
		t.Fatal("missing Core socket was accepted")
	}
}

func TestCoreClientAppliesNullUnlimited(t *testing.T) {
	var received coreConfig
	path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/config" || r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	settings := defaultConnectionSettings()
	legacyIdentityLimit := int64(9)
	legacyIdentityIPLimit := 4
	settings.Users = []userConnectionSettings{{
		Identity: coreIdentity{InboundTag: "in-a", User: "user-a"}, MaxInboundOnlineIPs: &legacyIdentityIPLimit,
		MaxTotalConnections: &legacyIdentityLimit, MaxOutboundTCPActive: &legacyIdentityLimit,
	}}
	portLimit := int64(10)
	portInboundLimit := int64(30)
	portIPLimit := 2
	settings.Ports = []portConnectionSettings{{InboundTag: "in-a", MaxInboundConnections: &portInboundLimit, MaxInboundOnlineIPs: &portIPLimit, MaxOutboundTCPActive: &portLimit}}
	groupLimit := int64(20)
	groupInboundLimit := int64(100)
	groupIPLimit := 3
	settings.ManagementUsers = []managementUserSettings{{Username: "ken", MaxInboundConnections: &groupInboundLimit, MaxInboundOnlineIPs: &groupIPLimit, MaxOutboundTCPActive: &groupLimit}}
	settings.ManagementMappings = []managementMapping{{Identity: coreIdentity{InboundTag: "in-a", User: "user-a"}, Group: "ken"}}
	globalInboundLimit := int64(1000)
	settings.MaxGlobalInboundConnections = &globalInboundLimit
	if err := newCoreClient(path).apply(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if len(received.Limits) != 1 || received.Limits[0].MaxInboundOnlineIPs != nil || received.Limits[0].MaxTotalConnections != nil || received.Limits[0].MaxOutboundTCPActive != nil || received.MaxGlobalInboundConnections == nil || *received.MaxGlobalInboundConnections != 1000 || len(received.PortLimits) != 1 || received.PortLimits[0].MaxInboundConnections == nil || *received.PortLimits[0].MaxInboundConnections != 30 || received.PortLimits[0].MaxOutboundTCPActive == nil || *received.PortLimits[0].MaxOutboundTCPActive != 10 || len(received.ManagementMappings) != 1 || received.ManagementMappings[0].Group != "ken" || len(received.ManagementLimits) != 1 || received.ManagementLimits[0].MaxInboundConnections == nil || *received.ManagementLimits[0].MaxInboundConnections != 100 || received.ManagementLimits[0].MaxOutboundTCPActive == nil || *received.ManagementLimits[0].MaxOutboundTCPActive != 20 {
		t.Fatalf("null unlimited changed: %#v", received)
	}
}

func TestCoreClientFallsBackToV2ConfigDuringRollingUpgrade(t *testing.T) {
	requests := 0
	path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if requests == 1 {
			if _, exists := body["management_mappings"]; !exists {
				t.Error("v3 config was not attempted first")
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"json: unknown field management_mappings"}`))
			return
		}
		if _, exists := body["management_mappings"]; exists {
			t.Error("legacy retry still contained v3 fields")
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	settings := defaultConnectionSettings()
	settings.ManagementMappings = []managementMapping{{Identity: coreIdentity{InboundTag: "in-a", User: "proto-a"}, Group: "ken"}}
	if err := newCoreClient(path).apply(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d, want 2", requests)
	}
}

func TestCoreClientReconnectsAfterSocketAppears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.sock")
	client := newCoreClient(path)
	if _, err := client.snapshot(context.Background()); err == nil {
		t.Fatal("missing socket was accepted")
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/snapshot" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":1,"started_at":"2026-09-08T00:00:00Z","proxy_users":[]}`))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	if _, err := client.snapshot(context.Background()); err != nil {
		t.Fatalf("client did not reconnect: %v", err)
	}
}

func TestCoreClientRejectsMalformedApplyResponse(t *testing.T) {
	path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":"yes"}`))
	}))
	if err := newCoreClient(path).apply(context.Background(), defaultConnectionSettings()); err == nil {
		t.Fatal("malformed Core apply response was accepted")
	}
}
