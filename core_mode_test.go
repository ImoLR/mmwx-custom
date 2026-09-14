package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func newCoreModeTestState(t *testing.T) *helperState {
	t.Helper()
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	state.recordLegacyReporter("12", "helper-token", "v0.5.0")
	return state
}

func TestCoreModeEndpointIsDedicatedAndPersistsExplicitChoice(t *testing.T) {
	state := newCoreModeTestState(t)
	store := &fakeAdminSessionStore{server: true, runtime: remoteServerRuntime{XrayMode: "embedded", Status: "connected"}}
	app := &app{apiToken: "operator-token", adminStore: store, helperState: state}
	request := httptest.NewRequest(http.MethodPost, "/api/custom/servers/12/core-mode", bytes.NewBufferString(`{"desired_mode":"external"}`))
	request.Header.Set("Authorization", "Bearer operator-token")
	response := httptest.NewRecorder()
	app.coreModeHandler(response, request, "12")
	if response.Code != http.StatusOK {
		t.Fatalf("core mode POST returned %d: %s", response.Code, response.Body.String())
	}
	intent, ok := state.coreModeIntent("12")
	if !ok || intent.DesiredMode != "external" || !intent.CustomCoreOwned {
		t.Fatalf("explicit core intent=%#v configured=%v", intent, ok)
	}
}

func healthyExternalStatus() agentStatus {
	return agentStatus{
		CoreMode: "external", ReportedAt: time.Now().UTC(), MachineID: "stable-machine-identity",
		Capabilities: []string{"core.mode.apply"},
		Ownership:    externalOwnershipStatus{Enabled: true, ServiceOwned: true, RuntimeOwned: true, SingleCore: true, ServiceActive: true, CoreReady: true},
	}
}

func TestCoreModeIntentDefaultsToExternalForNormalInstall(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.createInstallToken("12"); err != nil {
		t.Fatal(err)
	}
	intent, ok := state.coreModeIntent("12")
	if !ok || intent.DesiredMode != "external" || !intent.CustomCoreOwned {
		t.Fatalf("normal install intent=%#v configured=%v", intent, ok)
	}
}

func TestCoreModeHealthyDoesNotQueueRepair(t *testing.T) {
	state := newCoreModeTestState(t)
	if _, err := state.setCoreModeIntent("12", "external"); err != nil {
		t.Fatal(err)
	}
	command, err := state.reconcileCoreMode("12", healthyExternalStatus(), remoteServerRuntime{XrayMode: "external", Status: "connected"}, nil)
	if err != nil || command != nil {
		t.Fatalf("healthy reconcile command=%#v err=%v", command, err)
	}
	intent, _ := state.coreModeIntent("12")
	if intent.RepairStatus != "healthy" || intent.RepairAttempts != 0 {
		t.Fatalf("healthy intent=%#v", intent)
	}
}

func TestExternalIntentDetectsEveryRequiredDriftSignal(t *testing.T) {
	tests := []struct {
		name    string
		status  agentStatus
		runtime remoteServerRuntime
	}{
		{name: "controller mode", status: healthyExternalStatus(), runtime: remoteServerRuntime{XrayMode: "embedded", Status: "connected"}},
		{name: "agent mode", status: func() agentStatus { value := healthyExternalStatus(); value.CoreMode = "embedded"; return value }(), runtime: remoteServerRuntime{XrayMode: "external", Status: "connected"}},
		{name: "service inactive", status: func() agentStatus {
			value := healthyExternalStatus()
			value.Ownership.ServiceActive = false
			return value
		}(), runtime: remoteServerRuntime{XrayMode: "external", Status: "connected"}},
		{name: "runtime not owned", status: func() agentStatus {
			value := healthyExternalStatus()
			value.Ownership.RuntimeOwned = false
			return value
		}(), runtime: remoteServerRuntime{XrayMode: "external", Status: "connected"}},
		{name: "core not ready", status: func() agentStatus { value := healthyExternalStatus(); value.Ownership.CoreReady = false; return value }(), runtime: remoteServerRuntime{XrayMode: "external", Status: "connected"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := newCoreModeTestState(t)
			if _, err := state.setCoreModeIntent("12", "external"); err != nil {
				t.Fatal(err)
			}
			command, err := state.reconcileCoreMode("12", test.status, test.runtime, nil)
			if err != nil || command == nil || command.Action != "core.mode.apply" {
				t.Fatalf("drift command=%#v err=%v", command, err)
			}
		})
	}
}

func TestExternalIntentWithLegacyHelperDegradesWithoutUnsafeCommand(t *testing.T) {
	state := newCoreModeTestState(t)
	if _, err := state.setCoreModeIntent("12", "external"); err != nil {
		t.Fatal(err)
	}
	status := healthyExternalStatus()
	status.Capabilities = nil
	status.CoreMode = "embedded"
	status.Ownership = externalOwnershipStatus{}
	command, err := state.reconcileCoreMode("12", status, remoteServerRuntime{XrayMode: "embedded", Status: "connected"}, nil)
	if err != nil || command != nil {
		t.Fatalf("legacy helper command=%#v err=%v", command, err)
	}
	intent, _ := state.coreModeIntent("12")
	if intent.RepairStatus != "degraded" || intent.LastRepairError == "" {
		t.Fatalf("legacy helper intent=%#v", intent)
	}
}

func TestExistingHelperWithoutExplicitIntentNeverAutoRepairs(t *testing.T) {
	state := newCoreModeTestState(t)
	status := healthyExternalStatus()
	status.CoreMode = "embedded"
	status.Ownership = externalOwnershipStatus{}
	command, err := state.reconcileCoreMode("12", status, remoteServerRuntime{XrayMode: "embedded", Status: "connected"}, nil)
	if err != nil || command != nil {
		t.Fatalf("unconfigured existing helper command=%#v err=%v", command, err)
	}
	if _, configured := state.coreModeIntent("12"); configured {
		t.Fatal("reconcile created an implicit desired-mode intent for an existing helper")
	}
}

func TestCoreModeDriftQueuesOneRepairAndBacksOffAfterFailure(t *testing.T) {
	state := newCoreModeTestState(t)
	if _, err := state.setCoreModeIntent("12", "external"); err != nil {
		t.Fatal(err)
	}
	status := healthyExternalStatus()
	status.CoreMode = "embedded"
	status.Ownership.RuntimeOwned = false
	status.Ownership.CoreReady = false
	command, err := state.reconcileCoreMode("12", status, remoteServerRuntime{XrayMode: "embedded", Status: "connected"}, nil)
	if err != nil || command == nil || command.Action != "core.mode.apply" {
		t.Fatalf("drift command=%#v err=%v", command, err)
	}
	var payload map[string]string
	if err := json.Unmarshal(command.Payload, &payload); err != nil || payload["desired_mode"] != "external" {
		t.Fatalf("repair payload=%s err=%v", command.Payload, err)
	}
	state.mu.Lock()
	state.data.ManagementCommands["12"] = nil
	state.mu.Unlock()
	failed := &managementResult{Action: "core.mode.apply", Success: false, Message: "test failure", CompletedAt: time.Now().UTC()}
	next, err := state.reconcileCoreMode("12", status, remoteServerRuntime{XrayMode: "embedded", Status: "connected"}, failed)
	if err != nil || next != nil {
		t.Fatalf("failed reconcile command=%#v err=%v", next, err)
	}
	intent, _ := state.coreModeIntent("12")
	if intent.RepairAttempts != 1 || intent.NextRepairAt.Before(time.Now()) || intent.RepairStatus != "drift_detected" {
		t.Fatalf("backoff intent=%#v", intent)
	}
}

func TestExplicitEmbeddedIntentNeverQueuesExternalRepair(t *testing.T) {
	state := newCoreModeTestState(t)
	if _, err := state.setCoreModeIntent("12", "embedded"); err != nil {
		t.Fatal(err)
	}
	status := healthyExternalStatus()
	status.CoreMode = "embedded"
	status.Ownership = externalOwnershipStatus{}
	command, err := state.reconcileCoreMode("12", status, remoteServerRuntime{XrayMode: "embedded", Status: "connected"}, nil)
	if err != nil || command != nil {
		t.Fatalf("embedded reconcile command=%#v err=%v", command, err)
	}
	intent, _ := state.coreModeIntent("12")
	if intent.CustomCoreOwned || intent.RepairStatus != "healthy" {
		t.Fatalf("embedded intent=%#v", intent)
	}
}

func TestMachineRebindMovesOwnershipIntentToNewServerID(t *testing.T) {
	state := newCoreModeTestState(t)
	state.mu.Lock()
	identity := state.data.Servers["12"]
	identity.MachineID = "stable-machine-identity"
	state.data.Servers["12"] = identity
	state.mu.Unlock()
	if _, err := state.setCoreModeIntent("12", "external"); err != nil {
		t.Fatal(err)
	}
	record, installToken, err := state.createInstallToken("20")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := state.consumeInstallToken(installToken); err != nil || !ok {
		t.Fatalf("consume install token: ok=%v err=%v", ok, err)
	}
	serverID, machineID, err := state.rebindMachine(installToken, "stable-machine-identity", "helper-token")
	if err != nil {
		t.Fatal(err)
	}
	if serverID != "20" || machineID != identity.CustomServerUUID || record.OfficialServerID != "20" {
		t.Fatalf("rebind result server=%s machine=%s record=%#v", serverID, machineID, record)
	}
	if _, exists := state.data.Servers["12"]; exists {
		t.Fatal("old server identity remains after rebind")
	}
	intent, ok := state.coreModeIntent("20")
	if !ok || intent.DesiredMode != "external" || !intent.CustomCoreOwned {
		t.Fatalf("rebound intent=%#v configured=%v", intent, ok)
	}
}
