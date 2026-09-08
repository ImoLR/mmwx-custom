package main

import (
	"path/filepath"
	"testing"
)

func intPointer(value int) *int       { return &value }
func int64Pointer(value int64) *int64 { return &value }

func TestValidateServerConnectionSettings(t *testing.T) {
	valid := defaultServerConnectionSettings()
	valid.Users = []serverUserConnectionSettings{{
		Identity:                   serverConnectionIdentity{InboundTag: "in-a", User: "user-a"},
		MaxInboundOnlineIPs:        intPointer(3),
		MaxOutboundTCPActive:       int64Pointer(200),
		MaxOutboundTCPNewPerSecond: intPointer(30),
	}}
	if err := validateServerConnectionSettings(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.Users = append(invalid.Users, invalid.Users[0])
	if err := validateServerConnectionSettings(invalid); err == nil {
		t.Fatal("duplicate identity was accepted")
	}
	zero := 0
	invalid = valid
	invalid.Users[0].MaxInboundOnlineIPs = &zero
	if err := validateServerConnectionSettings(invalid); err == nil {
		t.Fatal("zero non-null limit was accepted")
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
		MaxOutboundTCPActive: int64Pointer(10),
	}}
	if err := state.setConnectionSettings("7", settings); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openHelperState(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.connectionSettings("7")
	if len(got.Users) != 1 || got.Users[0].MaxOutboundTCPActive == nil || *got.Users[0].MaxOutboundTCPActive != 10 {
		t.Fatalf("settings did not persist: %#v", got)
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
