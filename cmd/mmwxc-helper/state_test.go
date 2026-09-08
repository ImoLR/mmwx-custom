package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalStatePersistenceAndUnlimitedNull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := loadLocalState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.OnlineIPGracePeriodSeconds != 30 || state.Settings.DefaultCloseWaitTimeoutSeconds != nil {
		t.Fatalf("unexpected defaults: %#v", state)
	}
	value := int64(20)
	state.Settings.DefaultCloseWaitTimeoutSeconds = &value
	if err := saveLocalState(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadLocalState(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Settings.DefaultCloseWaitTimeoutSeconds == nil || *loaded.Settings.DefaultCloseWaitTimeoutSeconds != 20 {
		t.Fatalf("state did not round-trip: %#v", loaded)
	}
}

func TestLoadV010StatePreservesConnectionSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{"settings":{"default_close_wait_timeout_seconds":12,"online_ip_grace_period_seconds":45,"users":[{"identity":{"inbound_tag":"in-a","user":"user-a"},"max_inbound_online_ips":2,"max_outbound_tcp_active":10,"max_outbound_tcp_new_per_second":3,"close_wait_timeout_seconds":8}]}}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := loadLocalState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.OnlineIPGracePeriodSeconds != 45 || len(state.Settings.Users) != 1 || state.Settings.Users[0].Identity.InboundTag != "in-a" {
		t.Fatalf("v0.1.0 settings were not preserved: %#v", state.Settings)
	}
	if !state.HeartbeatAt.IsZero() || state.HelperVersion != "" {
		t.Fatalf("new metadata should be additive: %#v", state)
	}
}
