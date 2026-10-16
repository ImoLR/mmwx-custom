package main

import (
	"net/http"
	"net/http/httptest"

	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedHealthUsesControllerRuntimeInsteadOfTCPOnlyProbe(t *testing.T) {
	var runtime takeoverRuntime
	if err := json.Unmarshal([]byte(`{"xray_mode":"embedded","xray_running":true,"status":"connected"}`), &runtime); err != nil {
		t.Fatal(err)
	}
	if !runtime.XrayRunning || runtime.XrayMode != "embedded" {
		t.Fatalf("runtime=%#v", runtime)
	}
	source, err := os.ReadFile("takeover.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "func waitEmbeddedTakeoverHealthy")
	if start < 0 {
		t.Fatal("embedded health function not found")
	}
	end := strings.Index(text[start:], "func readAgentXrayMode")
	if end < 0 {
		t.Fatal("embedded health function not found")
	}
	body := text[start : start+end]
	if !strings.Contains(body, "runtime.XrayRunning") || strings.Contains(body, "officialInboundPorts") {
		t.Fatalf("embedded health still depends on TCP-only probing: %s", body)
	}
}

func TestBootstrapExternalOwnershipStartsOnceWithoutRestartLoop(t *testing.T) {
	var calls []string
	run := func(_ context.Context, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	if err := bootstrapExternalOwnershipService(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "|")
	if joined != "unmask xray.service|enable xray.service|start xray.service" {
		t.Fatalf("unexpected bootstrap sequence: %s", joined)
	}
	if strings.Contains(joined, "restart") {
		t.Fatalf("handoff bootstrap must not create a restart loop: %s", joined)
	}
}

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

func TestOwnershipRollbackOnlyRestoresAgentMode(t *testing.T) {
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
	if err := os.WriteFile(source, []byte("token: rotated-token\nlisten_port: 12345\nmaster_url: https://new.example\nxray_mode: external\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restoreAgentModeFromBackup(source, backupDirectory, backup); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(source)
	if err != nil || string(restored) != "token: rotated-token\nlisten_port: 12345\nmaster_url: https://new.example\nxray_mode: embedded\n" {
		t.Fatalf("rollback lost current Agent settings: %q err=%v", restored, err)
	}
}

func TestTakeoverWithoutAdminSessionPreservesRuntime(t *testing.T) {
	for _, mode := range []string{"embedded", "external"} {
		t.Run(mode, func(t *testing.T) {
			calls, _ := countingSystemctl(t)
			requests := 0
			controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var request struct {
					XrayMode string `json:"xray_mode"`
				}
				_ = json.NewDecoder(r.Body).Decode(&request)
				if request.XrayMode == "" {
					_ = json.NewEncoder(w).Encode(takeoverResponse{Success: true, Runtime: takeoverRuntime{XrayMode: mode, Status: "connected"}})
					return
				}
				http.Error(w, "an active official administrator session is required for Core handoff", http.StatusServiceUnavailable)
			}))
			defer controller.Close()
			cfg := config{CustomAPIURL: controller.URL, ServerID: "2", Token: "helper-test"}
			state := localState{ExternalOwnership: externalOwnershipState{Enabled: true, BackupDir: t.TempDir()}}
			if err := os.WriteFile(filepath.Join(state.ExternalOwnership.BackupDir, "manifest.json"), []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			before := state.ExternalOwnership
			err := ensureExternalDesiredState(context.Background(), controller.Client(), cfg, nil, &state)
			if err == nil || !strings.Contains(err.Error(), "administrator session") || requests != 2 {
				t.Fatalf("preflight requests=%d err=%v", requests, err)
			}
			// The refused rollback must return before restoring its backup or
			// stopping the working Core/restarting Agent, without waiting 60s.
			err = restoreEmbeddedTakeover(context.Background(), controller.Client(), cfg, nil, &state)
			if err == nil || !strings.Contains(err.Error(), "current Core retained") || requests != 3 {
				t.Fatalf("rollback requests=%d err=%v", requests, err)
			}
			if state.ExternalOwnership != before {
				t.Fatal("ownership changed despite refused controller operation")
			}
			if data, _ := os.ReadFile(calls); len(data) != 0 {
				t.Fatalf("services changed without an admin session: %s", data)
			}
		})
	}
}

func TestMissingRollbackFileDoesNotStopServingCore(t *testing.T) {
	calls, active := countingSystemctl(t)
	_ = os.WriteFile(active, nil, 0600)
	directory := t.TempDir()
	manifest := ownershipBackupManifest{OfficialUnit: ownershipPathBackup{Exists: true, BackupName: "missing.service"}}
	raw, _ := json.Marshal(manifest)
	_ = os.WriteFile(filepath.Join(directory, "manifest.json"), raw, 0600)
	if err := restoreOwnershipBackup(context.Background(), directory, true); err == nil {
		t.Fatal("missing snapshot accepted")
	}
	state := localState{ExternalOwnership: externalOwnershipState{Enabled: true, BackupDir: directory}}
	if err := restoreEmbeddedTakeover(context.Background(), nil, config{}, nil, &state); err == nil {
		t.Fatal("handoff ignored missing snapshot")
	}
	if data, _ := os.ReadFile(calls); len(data) != 0 {
		t.Fatalf("Core stopped before rollback files were verified: %s", data)
	}
	if _, err := os.Stat(active); err != nil || !state.ExternalOwnership.Enabled {
		t.Fatal("working runtime was lost")
	}
}

func TestFailedAgentRestartStillResumesExternalCore(t *testing.T) {
	var calls []string
	err := resumeExternalServices(context.Background(), func(_ context.Context, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "restart" {
			return errors.New("Agent restart failed")
		}
		return nil
	})
	if err == nil || strings.Join(calls, "|") != "restart mmw-agent.service|unmask xray.service|enable xray.service|start xray.service" {
		t.Fatalf("Agent failure prevented Core recovery: %v %v", calls, err)
	}
}

func TestEmbeddedHandoffStopsBothExternalServices(t *testing.T) {
	for _, stopFails := range []bool{false, true} {
		var stopped []string
		err := stopExternalServices(context.Background(), func(_ context.Context, args ...string) error {
			stopped = append(stopped, strings.Join(args, " "))
			if stopFails {
				return errors.New("service still running")
			}
			return nil
		}, func(context.Context, string) bool { return stopFails })
		if stopFails {
			if err == nil {
				t.Fatal("Embedded activation would proceed with the port still occupied")
			}
		} else if err != nil || strings.Join(stopped, "|") != "disable --now xray.service|disable --now mmwxc-core.service" {
			t.Fatalf("External service could restart on reboot: %v %v", stopped, err)
		}
	}
}
