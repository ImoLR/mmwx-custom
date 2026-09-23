package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func intPointer(value int) *int       { return &value }
func int64Pointer(value int64) *int64 { return &value }

func TestValidateServerConnectionSettings(t *testing.T) {
	valid := defaultServerConnectionSettings()
	valid.Users = []serverUserConnectionSettings{{
		Identity:                   serverConnectionIdentity{InboundTag: "in-a", User: "user-a"},
		MaxInboundOnlineIPs:        intPointer(3),
		MaxTotalConnections:        int64Pointer(400),
		MaxOutboundTCPActive:       int64Pointer(200),
		MaxOutboundTCPNewPerSecond: intPointer(30),
	}}
	valid.MaxGlobalInboundConnections = int64Pointer(1000)
	valid.ManagementUsers = []serverManagementUserSettings{{Username: "ken", MaxInboundConnections: int64Pointer(100), MaxInboundOnlineIPs: intPointer(3), MaxOutboundTCPActive: int64Pointer(100)}}
	valid.Ports = []serverPortConnectionSettings{{InboundTag: "in-a", MaxInboundConnections: int64Pointer(60), MaxInboundOnlineIPs: intPointer(2), MaxOutboundTCPActive: int64Pointer(30)}}
	if err := validateServerConnectionSettings(valid); err != nil {
		t.Fatal(err)
	}
	invalid := cloneServerConnectionSettings(valid)
	invalid.Users = append(invalid.Users, invalid.Users[0])
	if err := validateServerConnectionSettings(invalid); err == nil {
		t.Fatal("duplicate identity was accepted")
	}
	negative := -1
	invalid = cloneServerConnectionSettings(valid)
	invalid.ManagementUsers[0].MaxInboundOnlineIPs = &negative
	if err := validateServerConnectionSettings(invalid); err == nil {
		t.Fatal("negative management inbound limit was accepted")
	}
	zero := 0
	valid.ManagementUsers[0].MaxInboundOnlineIPs = &zero
	valid.Ports[0].MaxInboundConnections = int64Pointer(0)
	if err := validateServerConnectionSettings(valid); err != nil {
		t.Fatalf("zero unlimited inbound value was rejected: %v", err)
	}
	invalid = cloneServerConnectionSettings(valid)
	invalid.GlobalTotalLimitEnabled = true
	if err := validateServerConnectionSettings(invalid); err == nil {
		t.Fatal("enabled global limit without a value was accepted")
	}
	invalid.MaxGlobalTotalConnections = int64Pointer(1000)
	if err := validateServerConnectionSettings(invalid); err != nil {
		t.Fatalf("valid global limit was rejected: %v", err)
	}
}

func TestHelperStatePersistsConnectionSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper-state.json")
	state, err := openHelperState(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	settings := defaultServerConnectionSettings()
	settings.Users = []serverUserConnectionSettings{{
		Identity:             serverConnectionIdentity{InboundTag: "in-a", User: "user-a"},
		MaxTotalConnections:  int64Pointer(20),
		MaxOutboundTCPActive: int64Pointer(10),
	}}
	settings.GlobalTotalLimitEnabled = true
	settings.MaxGlobalTotalConnections = int64Pointer(200)
	settings.MaxGlobalInboundConnections = int64Pointer(1000)
	settings.ManagementUsers = []serverManagementUserSettings{{Username: "ken", MaxInboundConnections: int64Pointer(100), MaxInboundOnlineIPs: intPointer(3), MaxOutboundTCPActive: int64Pointer(100)}}
	settings.Ports = []serverPortConnectionSettings{{InboundTag: "in-a", MaxInboundConnections: int64Pointer(60), MaxInboundOnlineIPs: intPointer(2), MaxOutboundTCPActive: int64Pointer(30)}}
	settings.ManagementMappings = []serverManagementMapping{{Identity: serverConnectionIdentity{InboundTag: "in-a", User: "user-a"}, Group: "ken"}}
	if err := state.setConnectionSettings("7", settings); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openHelperState(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.connectionSettings("7")
	if len(got.Users) != 1 || got.Users[0].MaxOutboundTCPActive == nil || *got.Users[0].MaxOutboundTCPActive != 10 || got.Users[0].MaxTotalConnections == nil || *got.Users[0].MaxTotalConnections != 20 || len(got.ManagementUsers) != 1 || got.ManagementUsers[0].MaxInboundConnections == nil || *got.ManagementUsers[0].MaxInboundConnections != 100 || len(got.Ports) != 1 || got.Ports[0].MaxInboundOnlineIPs == nil || *got.Ports[0].MaxInboundOnlineIPs != 2 || len(got.ManagementMappings) != 1 || !got.GlobalTotalLimitEnabled || got.MaxGlobalTotalConnections == nil || *got.MaxGlobalTotalConnections != 200 || got.MaxGlobalInboundConnections == nil || *got.MaxGlobalInboundConnections != 1000 {
		t.Fatalf("settings did not persist: %#v", got)
	}
}

func TestValidateDetailedSnapshotFullTCPStates(t *testing.T) {
	snapshot := serverDetailedConnectionSnapshot{
		System:     serverTCPStateCounts{Total: 4, Established: 1, FinWait1: 1, LastAck: 1, Unknown: 1},
		Inbounds:   []serverInboundConnections{{TCP: serverTCPStateCounts{Total: 1, TimeWait: 1}, OnlineIPCount: 1, OnlineIPs: []serverOnlineIP{{IP: "198.51.100.1", Connections: 0}}}},
		ProxyUsers: []serverProxyUserConnections{{CurrentTotal: 1, InboundActive: 1, InboundTCP: serverTCPStateCounts{Total: 1, Established: 1}}},
		Global:     serverGlobalConnections{CurrentTotal: 1},
	}
	if err := validateDetailedSnapshot(snapshot); err != nil {
		t.Fatalf("valid full snapshot rejected: %v", err)
	}
	snapshot.ProxyUsers[0].OutboundTCP.TimeWait = -1
	if err := validateDetailedSnapshot(snapshot); err == nil {
		t.Fatal("negative nested TCP state was accepted")
	}
}

func TestParseServerConnectionsPath(t *testing.T) {
	if id, ok := parseServerConnectionsPath("/api/custom/servers/42/connections"); !ok || id != "42" {
		t.Fatalf("valid path rejected: id=%q ok=%v", id, ok)
	}
	for _, path := range []string{"/api/custom/servers/x/connections", "/api/custom/servers/0/connections", "/api/custom/servers/-1/connections", "/api/custom/servers/42/other", "/api/custom/servers/42/a/connections"} {
		if _, ok := parseServerConnectionsPath(path); ok {
			t.Fatalf("invalid path accepted: %s", path)
		}
	}
}

func TestWriteServerConnectionsUsesEmptyArrays(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	app := &app{helperState: state, detailedConnections: make(map[string]serverDetailedConnectionRecord)}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/custom/servers/6/connections", nil)
	app.writeServerConnections(response, request, "6")
	if response.Code != 200 {
		t.Fatalf("unexpected status %d", response.Code)
	}
	for _, expected := range [][]byte{[]byte(`"inbounds":[]`), []byte(`"proxy_users":[]`)} {
		if !bytes.Contains(response.Body.Bytes(), expected) {
			t.Fatalf("response does not contain %s: %s", expected, response.Body.String())
		}
	}
}
