package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reinstallTestState(t *testing.T) (*helperState, helperInstallToken, string) {
	t.Helper()
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	state.recordLegacyReporter("7", "old-helper-token", "v0.6.8")
	identity := state.data.Servers["7"]
	identity.MachineID = "old-machine-identity"
	state.data.Servers["7"] = identity
	record, raw, err := state.createInstallTokenForMode("7", "takeover")
	if err != nil {
		t.Fatal(err)
	}
	if record.HelperToken == "" || record.HelperTokenHash == identity.HelperTokenHash || !record.PreserveExisting {
		t.Fatal("reinstall did not receive a fresh credential")
	}
	if _, _, ok := state.authorizeReporter(record.CustomServerUUID, record.HelperToken, "v0.6.8"); ok {
		t.Fatal("unconsumed install credential was authorized")
	}
	if _, ok, err := state.consumeInstallToken(raw); err != nil || !ok {
		t.Fatalf("consume: %v, %v", ok, err)
	}
	return state, record, raw
}

func TestReinstallCredentialPromotesOnlyAfterValidReport(t *testing.T) {
	state, record, raw := reinstallTestState(t)
	if _, ok, err := state.consumeInstallToken(raw); err != nil || ok {
		t.Fatalf("install token was not one-time: %v, %v", ok, err)
	}
	state, err := openHelperState(state.path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	identity := state.data.Servers["7"]
	if !identity.PendingHelperTokenExpiresAt.Equal(record.ExpiresAt.Add(24 * time.Hour)) {
		t.Fatal("pending expiry was not persisted")
	}
	for _, token := range []string{"old-helper-token", record.HelperToken} {
		if id, _, ok := state.authorizeReporter(record.CustomServerUUID, token, "v0.6.8"); !ok || id != "7" {
			t.Fatal("current or pending credential rejected during installation")
		}
	}
	if state.data.Servers["7"].HelperTokenHash != hashSecret("old-helper-token") {
		t.Fatal("installation probe revoked the current credential")
	}
	script := renderHelperInstaller("https://controller.invalid", record.CustomServerUUID, record.HelperToken, record.InstallMode, raw, "")
	if !strings.Contains(script, `export MMWXC_HELPER_TOKEN="`+record.HelperToken+`"`) {
		t.Fatal("wiped machine installer has no fresh credential")
	}
	settings := defaultServerConnectionSettings()
	settings.MachineProtection = &serverMachineProtectionSettings{Enabled: true, MaxTotal: int64Pointer(10000)}
	if err := state.setConnectionSettings("7", settings); err != nil {
		t.Fatal(err)
	}
	command, err := state.enqueueManagementCommand("7", "core.status", nil)
	if err != nil {
		t.Fatal(err)
	}
	application := &app{helperState: state, helperTokens: map[string]string{"7": "old-helper-token"}, helperRate: make(map[string]time.Time), connectionMetrics: make(map[string]connectionMetrics), detailedConnections: make(map[string]serverDetailedConnectionRecord)}
	report := helperDetailedMetricsRequest{ServerID: record.CustomServerUUID, HelperVersion: "v0.6.8", Management: &managementReport{Status: agentStatus{MachineID: "reinstalled-machine-identity"}}}
	post := func() *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/custom/agent/connections", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+record.HelperToken)
		response := httptest.NewRecorder()
		application.helperDetailedConnectionsHandler(response, request)
		return response
	}
	report.Snapshot.System.Total = -1
	if response := post(); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid snapshot status=%d", response.Code)
	}
	report.Snapshot.System.Total = 0
	application.helperRate["7"] = time.Now()
	if response := post(); response.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limited report status=%d", response.Code)
	}
	delete(application.helperRate, "7")
	report.Management.Result = &managementResult{CommandID: command.ID, Action: command.Action, Signature: "invalid"}
	if response := post(); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid signature status=%d", response.Code)
	}
	if state.data.Servers["7"].HelperTokenHash != hashSecret("old-helper-token") {
		t.Fatal("rejected report promoted the pending credential")
	}
	delete(application.helperRate, "7")
	report.Management.Result = nil
	response := post()
	if response.Code != http.StatusOK {
		t.Fatalf("reinstalled machine report status=%d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Command  *managementCommand       `json:"command"`
		Settings serverConnectionSettings `json:"settings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Command == nil || result.Command.ID != command.ID {
		t.Fatal("pending command was lost")
	}
	wantSignature, err := signManagementCommand(*result.Command, record.HelperTokenHash)
	if err != nil || result.Command.Signature != wantSignature || result.Command.Signature == command.Signature {
		t.Fatal("pending command did not follow the promoted signing key")
	}
	if result.Settings.MachineProtection == nil || *result.Settings.MachineProtection.MaxTotal != 10000 {
		t.Fatal("reinstall lost machine protection settings")
	}
	state, err = openHelperState(state.path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	identity = state.data.Servers["7"]
	if identity.HelperTokenHash != record.HelperTokenHash || identity.PendingHelperTokenHash != "" || !identity.PendingHelperTokenExpiresAt.IsZero() || identity.MachineID != "reinstalled-machine-identity" || state.data.CoreModeIntents["7"].MachineID != identity.MachineID {
		t.Fatal("promoted credential or new machine identity was not persisted")
	}
	if _, _, ok := state.authorizeReporter(record.CustomServerUUID, "old-helper-token", "v0.6.8"); ok {
		t.Fatal("retired credential still authorizes reports")
	}
	application.helperState = state
	legacyRequest := httptest.NewRequest(http.MethodPost, "/api/custom/agent/connections", nil)
	legacyRequest.Header.Set("Authorization", "Bearer old-helper-token")
	if _, _, ok := application.authorizedHelper(legacyRequest, "7", "v0.6.8"); ok {
		t.Fatal("legacy token configuration revived the retired credential")
	}
	if _, _, err := state.rebindMachine(raw, identity.MachineID, record.HelperToken); err == nil {
		t.Fatal("successful reinstall left its rebind authorization reusable")
	}
	if _, err := state.acceptManagementReport("7", "old-helper-token", nil); err == nil {
		t.Fatal("old report authorized before promotion was accepted afterwards")
	}
	managementResult := managementResult{CommandID: command.ID, Action: command.Action, Success: true, CompletedAt: time.Now().UTC()}
	managementResult.Signature, _ = signManagementResult(managementResult, record.HelperTokenHash)
	if next, err := state.acceptManagementReport("7", record.HelperToken, &managementReport{Result: &managementResult}); err != nil || next != nil {
		t.Fatalf("promoted Helper result failed: %v", err)
	}
}

func TestReinstallPendingExpiryAndInPlaceRebind(t *testing.T) {
	for _, expire := range []bool{true, false} {
		t.Run(map[bool]string{true: "expiry", false: "existing-config"}[expire], func(t *testing.T) {
			state, record, raw := reinstallTestState(t)
			if expire {
				identity := state.data.Servers["7"]
				identity.PendingHelperTokenExpiresAt = time.Now().Add(-time.Second)
				state.data.Servers["7"] = identity
				if _, err := state.acceptManagementReport("7", record.HelperToken, nil); err == nil {
					t.Fatal("expired pending credential promoted")
				}
			} else if _, _, err := state.rebindMachine(raw, "old-machine-identity", "old-helper-token"); err != nil {
				t.Fatal(err)
			}
			if _, _, ok := state.authorizeReporter(record.CustomServerUUID, record.HelperToken, "v0.6.8"); ok {
				t.Fatal("unused reinstall credential remained authorized")
			}
			if _, _, ok := state.authorizeReporter(record.CustomServerUUID, "old-helper-token", "v0.6.8"); !ok {
				t.Fatal("current credential was lost")
			}
			state.pruneExpiredLocked(time.Now())
			if state.data.Servers["7"].PendingHelperTokenHash != "" {
				t.Fatal("unused pending credential was not removed")
			}
		})
	}
}

func TestReinstallPromotionPersistenceFailureKeepsCurrentCredential(t *testing.T) {
	state, record, _ := reinstallTestState(t)
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	state.path = filepath.Join(blocked, "state.json")
	if _, err := state.acceptManagementReport("7", record.HelperToken, &managementReport{Status: agentStatus{MachineID: "new-machine-identity"}}); err == nil {
		t.Fatal("state persistence unexpectedly succeeded")
	}
	identity := state.data.Servers["7"]
	if identity.HelperTokenHash != hashSecret("old-helper-token") || identity.PendingHelperTokenHash != record.HelperTokenHash || identity.MachineID != "old-machine-identity" {
		t.Fatal("failed persistence revoked the running machine")
	}
}
