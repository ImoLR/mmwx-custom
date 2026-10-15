package main

import (
	"context"
	"database/sql"
	"strings"

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
	application, _ := newOfficialCoreModeFixture(t, store, state)
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

type noCoreModeSessionStore struct{ fakeAdminSessionStore }

func (s *noCoreModeSessionStore) CoreModeAdminSession(context.Context) (string, error) {
	return "", sql.ErrNoRows
}

func TestAutomaticTakeoverWithoutAdminSessionFailsClosed(t *testing.T) {
	for _, mode := range []string{"external", "embedded"} {
		t.Run(mode, func(t *testing.T) {
			state := newCoreModeTestState(t)
			store := &noCoreModeSessionStore{fakeAdminSessionStore: fakeAdminSessionStore{runtime: remoteServerRuntime{XrayMode: "external", Status: "connected"}}}
			application := &app{adminStore: store, helperState: state}
			application.coreModeSessions.Store("12", "expired-session")
			body := []byte(`{"server_id":"` + state.data.Servers["12"].CustomServerUUID + `","helper_version":"v0.5.1","xray_mode":"` + mode + `"}`)
			request := httptest.NewRequest(http.MethodPost, "/api/custom/agent/takeover", bytes.NewReader(body))
			request.Header.Set("Authorization", "Bearer helper-token")
			response := httptest.NewRecorder()
			application.helperTakeoverHandler(response, request)
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), coreModeAdminSessionRequired) || store.mode != "" {
				t.Fatalf("unsafe sessionless handoff: %d %s", response.Code, response.Body.String())
			}
			if _, cached := application.coreModeSessions.Load("12"); cached {
				t.Fatal("expired admin session retained")
			}
			if err := application.ensureOfficialExternalConfig(context.Background(), "12", "prepare"); err == nil || !strings.Contains(err.Error(), coreModeAdminSessionRequired) {
				t.Fatalf("sessionless regeneration err=%v", err)
			}
		})
	}
}
