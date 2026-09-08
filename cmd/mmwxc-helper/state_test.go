package main

import (
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
