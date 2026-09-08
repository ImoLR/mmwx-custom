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

func TestLegacyReporterMigrationAddsManagementKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper-state.json")
	state, err := openHelperState(path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	uuid := state.recordLegacyReporter("7", "legacy-token", "v0.1.0")
	if uuid == "" {
		t.Fatal("legacy reporter was not registered")
	}
	reloaded, err := openHelperState(path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	identity := reloaded.data.Servers["7"]
	if identity.CustomServerUUID != uuid || identity.HelperTokenHash != hashSecret("legacy-token") {
		t.Fatalf("legacy identity was not preserved with a management key: %#v", identity)
	}
}

func TestInstallTokenPreservesExistingIdentity(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	state.recordLegacyReporter("7", "existing-token", "v0.1.0")
	before := state.data.Servers["7"]
	record, rawToken, err := state.createInstallToken("7")
	if err != nil {
		t.Fatal(err)
	}
	if !record.PreserveExisting || record.HelperToken != "" || rawToken == "" {
		t.Fatalf("unexpected upgrade token: %#v", record)
	}
	if _, ok, err := state.consumeInstallToken(rawToken); err != nil || !ok {
		t.Fatalf("consume upgrade token: ok=%v err=%v", ok, err)
	}
	after := state.data.Servers["7"]
	if after.CustomServerUUID != before.CustomServerUUID || after.HelperTokenHash != before.HelperTokenHash {
		t.Fatalf("identity changed during upgrade: before=%#v after=%#v", before, after)
	}
}

func TestManagementCommandAndSignedResultRoundTrip(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	state.recordLegacyReporter("7", "helper-token", "v0.1.0")
	command, err := state.enqueueManagementCommand("7", "core.status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if command.Signature == "" {
		t.Fatal("command was not signed")
	}
	result := managementResult{
		CommandID:   command.ID,
		Action:      command.Action,
		Success:     true,
		Data:        json.RawMessage(`{"installed":true}`),
		CompletedAt: time.Now().UTC(),
	}
	result.Signature, err = signManagementResult(result, hashSecret("helper-token"))
	if err != nil {
		t.Fatal(err)
	}
	next, err := state.acceptManagementReport("7", &managementReport{Result: &result})
	if err != nil {
		t.Fatal(err)
	}
	if next != nil || len(state.data.ManagementCommands["7"]) != 0 || len(state.data.ManagementResults["7"]) != 1 {
		t.Fatalf("command was not completed: next=%#v state=%#v", next, state.data)
	}
}

func TestManagementRejectsUnsafeActionsAndArtifacts(t *testing.T) {
	for _, action := range []string{"exec", "shell", "systemd.restart", "file.upload"} {
		if _, ok := managementActions[action]; ok {
			t.Fatalf("unsafe action is allowlisted: %s", action)
		}
	}
	payload := json.RawMessage(`{"url":"https://example.com/core","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	if err := validateManagementPayload("core.update", payload); err == nil {
		t.Fatal("unapproved artifact host was accepted")
	}
	payload = json.RawMessage(`{"url":"https://github.com/another/project/releases/download/v1/core","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	if err := validateManagementPayload("core.update", payload); err == nil {
		t.Fatal("unapproved GitHub repository was accepted")
	}
}

func TestAgentManagementEndpointRequiresIndependentOperatorToken(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	state.recordLegacyReporter("7", "helper-token", "v0.1.0")
	app := &app{apiToken: "operator-token", helperState: state}
	body := []byte(`{"action":"core.status"}`)

	request := httptest.NewRequest(http.MethodPost, "/api/custom/servers/7/agent", bytes.NewReader(body))
	response := httptest.NewRecorder()
	app.agentManagementHandler(response, request, "7")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing token returned %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/custom/servers/7/agent", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer operator-token")
	response = httptest.NewRecorder()
	app.agentManagementHandler(response, request, "7")
	if response.Code != http.StatusAccepted {
		t.Fatalf("authorized request returned %d: %s", response.Code, response.Body.String())
	}
}

func TestInstallTokenCanUseIndependentOperatorAuthorization(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	app := &app{apiToken: "operator-token", helperState: state, publicURL: "https://controller.invalid"}
	request := httptest.NewRequest(http.MethodPost, "/api/custom/helper/install-token", bytes.NewReader([]byte(`{"server_id":7}`)))
	request.Header.Set("Authorization", "Bearer operator-token")
	response := httptest.NewRecorder()
	app.createHelperInstallTokenHandler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("independent operator request returned %d: %s", response.Code, response.Body.String())
	}
	var body helperInstallTokenResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ServerID != "7" || body.ServerUUID == "" || body.Command == "" {
		t.Fatalf("unexpected install response: %#v", body)
	}
}
