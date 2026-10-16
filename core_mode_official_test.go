package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/ImoLR/mmwx-custom/internal/externalconfig"
)

type officialCoreModeFixture struct {
	agent            *lifecycleAgentFixture
	current, pending string
	writes, accepts  int
	modeWrites       int
	settings         map[string]any
}

func newOfficialCoreModeFixture(t *testing.T, store *fakeAdminSessionStore, state *helperState) (*app, *officialCoreModeFixture) {
	t.Helper()
	config := map[string]any{"log": map[string]any{"loglevel": "error"}, "inbounds": []any{map[string]any{"tag": "snell-user", "protocol": "snell", "port": 10021}}, "outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}}}
	agent, server := newLifecycleAgentFixture(map[int64]map[string]any{12: config})
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	raw, _ := json.Marshal(config)
	f := &officialCoreModeFixture{agent: agent, current: string(raw), pending: string(raw)}
	agent.extraHandler = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("MM-Authorization") != "admin-session" {
			t.Error("missing admin session")
		}
		if r.URL.Path == "/api/admin/remote/xray/system-config" {
			if r.URL.Query().Get("server_id") != "12" {
				t.Fatal("wrong server")
			}
			if r.Method == http.MethodGet {
				writeJSON(w, 200, map[string]any{"success": true, "config": map[string]any{"grpc_port": 46736, "metrics_listen": "127.0.0.1:38889"}})
			} else {
				_ = json.NewDecoder(r.Body).Decode(&f.settings)
				f.writes++
				config["api"] = map[string]any{"tag": "api", "services": []any{"HandlerService", "LoggerService", "StatsService", "RoutingService"}}
				config["metrics"] = map[string]any{"tag": "Metrics", "listen": f.settings["metrics_listen"]}
				config["inbounds"] = append(config["inbounds"].([]any), map[string]any{"tag": "api", "listen": "127.0.0.1", "protocol": "dokodemo-door", "port": f.settings["grpc_port"]})
				config["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []any{"api"}, "outboundTag": "api"}}}
				writeJSON(w, 200, map[string]any{"success": true})
			}
			return true
		}
		if r.URL.Path != "/api/v3" {
			return false
		}
		var request struct {
			Op, Q   string
			Payload map[string]any
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Op {
		case "034e094d05aa3f83":
			writeJSON(w, 200, map[string]any{"servers": []any{map[string]any{"id": 12, "name": "test-server", "xray_mode": store.runtime.XrayMode, "traffic_limit": 123, "traffic_reset_day": 19, "domain": "keep.example", "expires_at": "2027-03-18T00:00:00Z", "notify_muted": true, "ddns_enabled": true, "ddns_provider_id": 1, "ipv6_enabled": false, "lock_entry_ip": true}}})
		case "2146f0b13a7196cb":
			if len(request.Payload) != 9 || request.Payload["id"] != float64(12) || request.Payload["name"] != "test-server" || request.Payload["traffic_limit"] != float64(123) || request.Payload["traffic_reset_day"] != float64(19) {
				t.Fatalf("lost server fields: %v", request.Payload)
			}
			if request.Payload["ddns_enabled"] != true || request.Payload["ddns_provider_id"] != float64(1) || request.Payload["ipv6_enabled"] != false || request.Payload["lock_entry_ip"] != true {
				t.Fatalf("mode switch would reset DDNS/entry switches: %v", request.Payload)
			}
			f.modeWrites++
			store.mode = request.Payload["xray_mode"].(string)
			store.runtime.XrayMode = store.mode
			writeJSON(w, 200, map[string]any{"success": true})
		case "0f1dfff4d60aad8f":
			if request.Q != "server_id=12&with_config=true" {
				t.Fatal("incorrect recovery query")
			}
			writeJSON(w, 200, map[string]any{"has_pending": f.pending != "", "current": map[string]any{"config_json": f.current}, "pending": map[string]any{"config_json": f.pending}})
		case "438a645265d45957":
			if request.Q != "server_id=12" {
				t.Fatal("incorrect accept query")
			}
			f.accepts++
			f.current, f.pending = f.pending, ""
			writeJSON(w, 200, map[string]any{"success": true})
		default:
			t.Fatalf("unexpected op: %s", request.Op)
		}
		return true
	}
	return &app{adminStore: store, helperState: state, officialInternalTarget: target}, f
}

func TestOfficialExternalRepairWaitsForExactAgentReport(t *testing.T) {
	state := newCoreModeTestState(t)
	a, f := newOfficialCoreModeFixture(t, &fakeAdminSessionStore{runtime: remoteServerRuntime{XrayMode: "external"}}, state)
	if err := a.ensureOfficialExternalConfig(context.Background(), "12", "prepare"); err != nil {
		t.Fatal(err)
	}
	if f.writes != 1 || f.accepts != 0 || state.data.ExternalConfigRepairs["12"] == "" {
		t.Fatal("repair not persisted before confirmation")
	}
	if err := a.ensureOfficialExternalConfig(context.Background(), "12", "confirm"); err == nil {
		t.Fatal("stale pending snapshot was accepted")
	}
	if f.accepts != 0 {
		t.Fatal("stale pending accepted")
	}
	raw, _ := json.Marshal(f.agent.configs[12])
	f.pending = string(raw)
	if complete, err := externalconfig.Complete(raw); err != nil || !complete {
		t.Fatal("fixture incomplete")
	}
	if err := a.ensureOfficialExternalConfig(context.Background(), "12", "confirm"); err != nil {
		t.Fatal(err)
	}
	if f.accepts != 1 || f.pending != "" || state.data.ExternalConfigRepairs["12"] != "" {
		t.Fatal("recovery not settled")
	}
	if err := a.ensureOfficialExternalConfig(context.Background(), "12", "prepare"); err != nil {
		t.Fatal(err)
	}
	if f.writes != 1 {
		t.Fatal("healthy config rewritten")
	}
}

func TestOfficialCoreModeSwitchKeepsDDNS(t *testing.T) {
	state := newCoreModeTestState(t)
	store := &fakeAdminSessionStore{runtime: remoteServerRuntime{XrayMode: "external"}}
	a, f := newOfficialCoreModeFixture(t, store, state)
	if err := a.setOfficialCoreMode(context.Background(), "12", "embedded"); err != nil {
		t.Fatal(err)
	}
	if f.modeWrites != 1 || store.mode != "embedded" {
		t.Fatal("mode switch not written")
	}
}

func TestOfficialExternalRepairRejectsUnrelatedChanges(t *testing.T) {
	state := newCoreModeTestState(t)
	a, f := newOfficialCoreModeFixture(t, &fakeAdminSessionStore{}, state)
	if err := a.ensureOfficialExternalConfig(context.Background(), "12", "prepare"); err != nil {
		t.Fatal(err)
	}
	f.agent.configs[12]["log"] = map[string]any{"access": "/tmp/unrelated"}
	raw, _ := json.Marshal(f.agent.configs[12])
	f.pending = string(raw)
	if err := a.ensureOfficialExternalConfig(context.Background(), "12", "confirm"); err == nil || f.accepts != 0 {
		t.Fatal("unrelated config accepted")
	}
}

func TestOfficialExternalRepairRequiresItsOwnMarker(t *testing.T) {
	state := newCoreModeTestState(t)
	a, f := newOfficialCoreModeFixture(t, &fakeAdminSessionStore{}, state)
	if err := a.ensureOfficialExternalConfig(context.Background(), "12", "prepare"); err != nil {
		t.Fatal(err)
	}
	delete(state.data.ExternalConfigRepairs, "12")
	raw, _ := json.Marshal(f.agent.configs[12])
	f.pending = string(raw)
	if err := a.ensureOfficialExternalConfig(context.Background(), "12", "confirm"); err == nil || f.accepts != 0 {
		t.Fatal("unowned pending recovery accepted")
	}
}

func TestOfficialExternalRepairDoesNotAcceptPreexistingUserDrift(t *testing.T) {
	state := newCoreModeTestState(t)
	a, f := newOfficialCoreModeFixture(t, &fakeAdminSessionStore{}, state)
	f.agent.configs[12]["log"] = map[string]any{"access": "/tmp/manual-change"}
	if err := a.ensureOfficialExternalConfig(context.Background(), "12", "prepare"); err == nil || f.writes != 0 || f.accepts != 0 {
		t.Fatal("unrelated existing recovery was modified")
	}
}
