package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplaceAgentXrayModePreservesUnrelatedConfig(t *testing.T) {
	original := []byte("mode: remote\ntoken: secret-value\n  xray_mode: embedded\nlisten_port: \"23889\"\n")
	current, updated, err := replaceAgentXrayMode(original, "external")
	if err != nil {
		t.Fatal(err)
	}
	if current != "embedded" || string(updated) != "mode: remote\ntoken: secret-value\n  xray_mode: external\nlisten_port: \"23889\"\n" {
		t.Fatalf("unexpected mode rewrite: current=%q config=%q", current, updated)
	}
}

func TestReplaceAgentXrayModeFailsClosedOnDuplicate(t *testing.T) {
	_, _, err := replaceAgentXrayMode([]byte("xray_mode: embedded\nxray_mode: external\n"), "external")
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate mode was accepted: %v", err)
	}
}

func TestOwnershipBackupIncludesAndRestoresAgentConfig(t *testing.T) {
	// The path-level primitives used by the ownership manifest must preserve
	// exact Agent config bytes and mode. This avoids exercising systemd in a
	// unit test while covering the rollback artifact itself.
	directory := t.TempDir()
	source := filepath.Join(directory, "config.yaml")
	backupDirectory := filepath.Join(directory, "backup")
	if err := os.MkdirAll(backupDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("token: do-not-change\nxray_mode: embedded\n")
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	backup, err := backupOwnershipPath(source, backupDirectory, "agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("xray_mode: external\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restoreOwnershipPath(source, backupDirectory, backup); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(source)
	if err != nil || string(restored) != string(original) {
		t.Fatalf("Agent config was not restored byte-for-byte: %q err=%v", restored, err)
	}
}
