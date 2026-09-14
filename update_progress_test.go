package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestHelperUpdateProgressIsAuthenticatedAndSurvivesHeartbeat(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	customID := state.recordLegacyReporter("12", "helper-token", "v0.5.1")
	app := &app{helperState: state, helperTokens: map[string]string{}}
	request := httptest.NewRequest(http.MethodPost, "/api/custom/agent/update-progress", bytes.NewBufferString(`{"server_id":"`+customID+`","helper_version":"v0.5.1","component":"helper","phase":"verifying","target_version":"v0.5.2"}`))
	request.Header.Set("Authorization", "Bearer helper-token")
	response := httptest.NewRecorder()
	app.helperUpdateProgressHandler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("progress POST returned %d: %s", response.Code, response.Body.String())
	}
	state.mu.Lock()
	progress := state.data.AgentStatuses["12"].Update
	state.mu.Unlock()
	if progress == nil || progress.Component != "helper" || progress.Phase != "verifying" || progress.TargetVersion != "v0.5.2" {
		t.Fatalf("progress=%#v", progress)
	}
	if _, err := state.acceptManagementReport("12", &managementReport{Status: agentStatus{Helper: componentStatus{Version: "v0.5.1"}}}); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	preserved := state.data.AgentStatuses["12"].Update
	state.mu.Unlock()
	if preserved == nil || preserved.Phase != "verifying" {
		t.Fatalf("heartbeat erased progress: %#v", preserved)
	}
}

func TestHelperUpdateProgressRejectsInvalidPhase(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	customID := state.recordLegacyReporter("12", "helper-token", "v0.5.1")
	app := &app{helperState: state, helperTokens: map[string]string{}}
	request := httptest.NewRequest(http.MethodPost, "/api/custom/agent/update-progress", bytes.NewBufferString(`{"server_id":"`+customID+`","helper_version":"v0.5.1","component":"helper","phase":"executing_shell"}`))
	request.Header.Set("Authorization", "Bearer helper-token")
	response := httptest.NewRecorder()
	app.helperUpdateProgressHandler(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid phase returned %d", response.Code)
	}
}

func TestObservedTargetVersionCompletesLegacyScheduledUpdate(t *testing.T) {
	progress := &componentUpdateProgress{Component: "helper", Phase: "dispatching", TargetVersion: "v0.5.1", UpdatedAt: time.Now().Add(-time.Minute)}
	completed := completeObservedUpdate(progress, agentStatus{Helper: componentStatus{Version: "v0.5.1"}}, time.Now())
	if completed == nil || completed.Phase != "success" {
		t.Fatalf("completed progress=%#v", completed)
	}
	if progress.Phase != "dispatching" {
		t.Fatal("source progress was mutated")
	}
}
