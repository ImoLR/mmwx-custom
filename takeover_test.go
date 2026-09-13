package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHelperTakeoverUpdatesOnlyAuthorizedMappedServer(t *testing.T) {
	heartbeat := time.Now().UTC()
	store := &fakeAdminSessionStore{runtime: remoteServerRuntime{XrayMode: "embedded", Status: "connected", LastHeartbeat: &heartbeat}}
	state, err := openHelperState(t.TempDir()+"/helper-state.json", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	uuid := state.recordLegacyReporter("12", "helper-secret", "v0.4.3")
	application := &app{adminStore: store, helperState: state}
	body := []byte(`{"server_id":"` + uuid + `","helper_version":"v0.4.3","xray_mode":"external"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/custom/agent/takeover", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer helper-secret")
	recorder := httptest.NewRecorder()
	application.helperTakeoverHandler(recorder, request)
	if recorder.Code != http.StatusOK || store.mode != "external" {
		t.Fatalf("takeover response=%d body=%s mode=%q", recorder.Code, recorder.Body.String(), store.mode)
	}
}

func TestHelperTakeoverRejectsWrongHelperIdentity(t *testing.T) {
	store := &fakeAdminSessionStore{runtime: remoteServerRuntime{XrayMode: "embedded", Status: "connected"}}
	state, err := openHelperState(t.TempDir()+"/helper-state.json", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	uuid := state.recordLegacyReporter("12", "helper-secret", "v0.4.3")
	application := &app{adminStore: store, helperState: state}
	request := httptest.NewRequest(http.MethodPost, "/api/custom/agent/takeover", bytes.NewReader([]byte(`{"server_id":"`+uuid+`","xray_mode":"external"}`)))
	request.Header.Set("Authorization", "Bearer wrong-secret")
	recorder := httptest.NewRecorder()
	application.helperTakeoverHandler(recorder, request)
	if recorder.Code != http.StatusUnauthorized || store.mode != "" {
		t.Fatalf("unauthorized takeover response=%d mode=%q", recorder.Code, store.mode)
	}
}
