package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
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
	valid.MachineProtection = &serverMachineProtectionSettings{Enabled: true, MaxActive: int64Pointer(10000), MaxTotal: int64Pointer(20000)}
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
	invalid.MachineProtection = &serverMachineProtectionSettings{Enabled: true}
	if err := validateServerConnectionSettings(invalid); err == nil {
		t.Fatal("enabled machine protection without thresholds was accepted")
	}
	invalid.MachineProtection = valid.MachineProtection
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
	settings.MachineProtection = &serverMachineProtectionSettings{Enabled: true, MaxActive: int64Pointer(10000)}
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
	if len(got.Users) != 1 || got.Users[0].MaxOutboundTCPActive == nil || *got.Users[0].MaxOutboundTCPActive != 10 || got.Users[0].MaxTotalConnections == nil || *got.Users[0].MaxTotalConnections != 20 || len(got.ManagementUsers) != 1 || got.ManagementUsers[0].MaxInboundConnections == nil || *got.ManagementUsers[0].MaxInboundConnections != 100 || len(got.Ports) != 1 || got.Ports[0].MaxInboundOnlineIPs == nil || *got.Ports[0].MaxInboundOnlineIPs != 2 || len(got.ManagementMappings) != 1 || !got.GlobalTotalLimitEnabled || got.MaxGlobalTotalConnections == nil || *got.MaxGlobalTotalConnections != 200 || got.MaxGlobalInboundConnections == nil || *got.MaxGlobalInboundConnections != 1000 || got.MachineProtection == nil || got.MachineProtection.MaxActive == nil || *got.MachineProtection.MaxActive != 10000 {
		t.Fatalf("settings did not persist: %#v", got)
	}
}

func TestCoreAccountingExplainsAttributedAndUnattributed(t *testing.T) {
	snapshot := serverDetailedConnectionSnapshot{
		Global: serverGlobalConnections{CurrentTotal: 38, CurrentInbound: 14},
		ProxyUsers: []serverProxyUserConnections{
			{CurrentTotal: 28, OutboundActive: 14, ManagementGroup: "wings"},
			{CurrentTotal: 10, OutboundPending: 10},
		},
	}
	got := coreAccounting(snapshot)
	if got.Total != 38 || got.Attributed != 28 || got.Unattributed != 10 || got.TrackerTotal != 38 || got.OutboundActive != 14 || got.OutboundPending != 10 || !got.Balanced || got.ReconciliationDelta != 0 {
		t.Fatalf("unexpected core accounting: %#v", got)
	}
}

func TestCoreAccountingReportsRealReconciliationDelta(t *testing.T) {
	snapshot := serverDetailedConnectionSnapshot{Global: serverGlobalConnections{CurrentTotal: 9}, ProxyUsers: []serverProxyUserConnections{{CurrentTotal: 7, ManagementGroup: "ken"}}}
	got := coreAccounting(snapshot)
	if got.Unattributed != 0 || got.ReconciliationDelta != 2 || got.Balanced {
		t.Fatalf("tracker gap was hidden as unattributed: %#v", got)
	}
}

func TestMachineProtectionCompatibilityByHelperVersion(t *testing.T) {
	settings := defaultServerConnectionSettings()
	settings.MachineProtection = &serverMachineProtectionSettings{Enabled: true, MaxActive: int64Pointer(1000)}
	if got := settingsForHelper(settings, "v0.6.5"); got.MachineProtection != nil {
		t.Fatalf("old Helper received an unknown machine field: %#v", got.MachineProtection)
	}
	if got := settingsForHelper(settings, "v0.6.6"); got.MachineProtection == nil || !got.MachineProtection.Enabled {
		t.Fatalf("new Helper did not receive machine settings: %#v", got.MachineProtection)
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

func TestInboundSettingsPUTPersistsAndReachesHelper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper-state.json")
	state, err := openHelperState(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{apiToken: "operator-token", helperState: state, helperTokens: map[string]string{"7": "helper-token"},
		adminStore: &fakePersistentConnectionStore{fakeAdminSessionStore: fakeAdminSessionStore{authorized: true, server: true}},
		helperRate: make(map[string]time.Time), connectionMetrics: make(map[string]connectionMetrics), detailedConnections: make(map[string]serverDetailedConnectionRecord)}
	settings := defaultServerConnectionSettings()
	settings.GlobalTotalLimitEnabled = true
	settings.MaxGlobalTotalConnections = int64Pointer(2000)
	settings.MaxGlobalInboundConnections = int64Pointer(1000)
	settings.ManagementUsers = []serverManagementUserSettings{{Username: "ken", MaxInboundConnections: int64Pointer(100), MaxTotalConnections: int64Pointer(40)}}
	settings.Ports = []serverPortConnectionSettings{{InboundTag: "in-a", MaxInboundConnections: int64Pointer(30), MaxTotalConnections: int64Pointer(25)}}
	put := func(want serverConnectionSettings, status int) {
		t.Helper()
		body, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPut, "/api/custom/servers/7/connections", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer operator-token")
		w := httptest.NewRecorder()
		a.serverConnectionsHandler(w, r)
		if w.Code != status {
			t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
		}
	}
	for _, field := range []string{"global", "user", "port"} {
		invalid := cloneServerConnectionSettings(settings)
		switch field {
		case "global":
			invalid.MaxGlobalInboundConnections = int64Pointer(-1)
		case "user":
			invalid.ManagementUsers[0].MaxInboundConnections = int64Pointer(-1)
		case "port":
			invalid.Ports[0].MaxInboundConnections = int64Pointer(-1)
		}
		put(invalid, http.StatusBadRequest)
	}
	for _, value := range []*int64{int64Pointer(1000), int64Pointer(0), nil} {
		settings.MaxGlobalInboundConnections = value
		if value == nil || *value == 0 {
			settings.ManagementUsers[0].MaxInboundConnections = value
			settings.Ports[0].MaxInboundConnections = value
		}
		put(settings, http.StatusOK)
		a.helperState, err = openHelperState(path, 0)
		if err != nil {
			t.Fatal(err)
		}
		want := cloneServerConnectionSettings(settings)
		if got := a.helperState.connectionSettings("7"); !reflect.DeepEqual(got, want) {
			t.Fatalf("settings lost on reload: got %#v want %#v", got, want)
		}
		a.helperRate = make(map[string]time.Time)
		r := httptest.NewRequest(http.MethodPost, "/api/custom/agent/connections", bytes.NewBufferString(`{"server_id":"7","helper_version":"v0.7.4","snapshot":{}}`))
		r.Header.Set("Authorization", "Bearer helper-token")
		w := httptest.NewRecorder()
		a.helperDetailedConnectionsHandler(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("Helper report: %d %s", w.Code, w.Body.String())
		}
		var reply struct {
			Settings serverConnectionSettings `json:"settings"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		reply.Settings = cloneServerConnectionSettings(reply.Settings)
		if !reflect.DeepEqual(reply.Settings, want) {
			t.Fatalf("Helper settings changed inbound or legacy values: got %#v want %#v", reply.Settings, want)
		}
	}
}
