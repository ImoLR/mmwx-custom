package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateMachineIDPersistsFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mmwxc", "machine-id")
	first, err := loadOrCreateMachineID(path, "stable-machine-identity")
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateMachineID(path, "different-machine-identity")
	if err != nil {
		t.Fatal(err)
	}
	if first != "stable-machine-identity" || second != first {
		t.Fatalf("machine identity changed: first=%q second=%q", first, second)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("machine identity permissions=%v", info.Mode().Perm())
	}
}

func TestLoadOrCreateMachineIDGeneratesForLegacyNumericID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machine-id")
	value, err := loadOrCreateMachineID(path, "1")
	if err != nil || !validMachineID(value) || value == "1" {
		t.Fatalf("generated machine identity=%q err=%v", value, err)
	}
}
