package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeUIMetadataStore struct {
	fakeAdminSessionStore
	username   string
	preference uiPreferenceRecord
	presets    []routingRulePreset
	nextID     int64
}

func (s *fakeUIMetadataStore) AdminUsername(context.Context, string) (string, error) {
	return s.username, s.err
}

func (s *fakeUIMetadataStore) GetUIPreference(context.Context, string, string) (uiPreferenceRecord, error) {
	return s.preference, s.err
}

func (s *fakeUIMetadataStore) PutUIPreference(_ context.Context, _, _ string, data json.RawMessage, onlyIfEmpty bool) (uiPreferenceRecord, error) {
	if onlyIfEmpty && s.preference.Exists {
		return uiPreferenceRecord{}, errUIMetadataConflict
	}
	s.preference = uiPreferenceRecord{Exists: true, Data: append(json.RawMessage(nil), data...), Revision: s.preference.Revision + 1, UpdatedAt: time.Now().UTC()}
	return s.preference, nil
}

func (s *fakeUIMetadataStore) ListRoutingRulePresets(context.Context, string) ([]routingRulePreset, error) {
	return append([]routingRulePreset(nil), s.presets...), s.err
}

func (s *fakeUIMetadataStore) UpsertRoutingRulePreset(_ context.Context, _ string, name string, rule map[string]any) (routingRulePreset, error) {
	canonical, _, err := canonicalRoutingRule(rule)
	if err != nil {
		return routingRulePreset{}, err
	}
	for index := range s.presets {
		existing, _, _ := canonicalRoutingRule(s.presets[index].Rule)
		if bytes.Equal(existing, canonical) {
			s.presets[index].Name = name
			s.presets[index].UpdatedAt = time.Now().UTC()
			return s.presets[index], nil
		}
	}
	s.nextID++
	preset := routingRulePreset{ID: s.nextID, Name: name, Rule: rule, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	s.presets = append([]routingRulePreset{preset}, s.presets...)
	if len(s.presets) > maxRoutingRulePresets {
		s.presets = s.presets[:maxRoutingRulePresets]
	}
	return preset, nil
}

func (s *fakeUIMetadataStore) DeleteRoutingRulePreset(_ context.Context, _ string, id int64) error {
	for index := range s.presets {
		if s.presets[index].ID == id {
			s.presets = append(s.presets[:index], s.presets[index+1:]...)
			return nil
		}
	}
	return errors.New("not found")
}

func TestUIMetadataSchemaIsIndependentAndAccountScoped(t *testing.T) {
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS mmwxc_ui_preferences",
		"CREATE TABLE IF NOT EXISTS mmwxc_routing_rule_presets",
		"PRIMARY KEY (username, preference_key)",
		"UNIQUE (username, rule_hash)",
		"REFERENCES users(username)",
	} {
		if !strings.Contains(uiMetadataSchema, fragment) {
			t.Fatalf("UI metadata schema missing %q", fragment)
		}
	}
	if strings.Contains(uiMetadataSchema, "ALTER TABLE remote_servers") || strings.Contains(uiMetadataSchema, "ALTER TABLE nodes") {
		t.Fatal("UI metadata must not alter formal server/node tables")
	}
}

func TestServiceGroupsRoundTripPreservesGroupAndServerOrder(t *testing.T) {
	store := &fakeUIMetadataStore{username: "admin", fakeAdminSessionStore: fakeAdminSessionStore{authorized: true}}
	app := &app{adminStore: store}
	body := `{"groups":[{"id":"tokyo","name":"Tokyo","server_ids":[14,5]},{"id":"hk","name":"Hong Kong","server_ids":[12]}],"only_if_empty":true}`
	request := httptest.NewRequest(http.MethodPut, "/api/custom/ui/service-groups", strings.NewReader(body))
	request.Header.Set("MM-Authorization", "admin-session")
	recorder := httptest.NewRecorder()
	app.uiServiceGroupsHandler(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/custom/ui/service-groups", nil)
	request.Header.Set("MM-Authorization", "admin-session")
	recorder = httptest.NewRecorder()
	app.uiServiceGroupsHandler(recorder, request)
	var response struct {
		Exists bool           `json:"exists"`
		Groups []serviceGroup `json:"groups"`
	}
	if json.Unmarshal(recorder.Body.Bytes(), &response) != nil || !response.Exists || len(response.Groups) != 2 {
		t.Fatalf("unexpected response: %s", recorder.Body.String())
	}
	if response.Groups[0].ID != "tokyo" || response.Groups[0].ServerIDs[0] != 14 || response.Groups[0].ServerIDs[1] != 5 {
		t.Fatalf("order was not preserved: %#v", response.Groups)
	}
}

func TestUIMetadataFrontendUsesPlainCustomTransport(t *testing.T) {
	source, err := os.ReadFile("frontend/src/api.ts")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "export function fetchCustomServiceGroups")
	if start < 0 {
		t.Fatal("Custom UI metadata API functions were not found")
	}
	end := strings.Index(text[start:], "export function deleteRoutingRulePreset(")
	if end < 0 {
		t.Fatal("Custom UI metadata delete API function was not found")
	}
	end += start
	close := strings.Index(text[end:], "\n}")
	if close < 0 {
		t.Fatal("Custom UI metadata delete API function was not closed")
	}
	metadataAPI := text[start : end+close+2]
	if got := strings.Count(metadataAPI, "return requestCustomApi<"); got != 5 {
		t.Fatalf("Custom UI metadata endpoints using plain Custom transport=%d, want 5", got)
	}
	if strings.Contains(metadataAPI, "return request<") {
		t.Fatal("Custom UI metadata must not use the formal secure-channel request transport")
	}
	if got := strings.Count(metadataAPI, `headers: { "MM-Authorization": token`); got != 5 {
		t.Fatalf("Custom UI metadata endpoints carrying admin session=%d, want 5", got)
	}
	if got := strings.Count(metadataAPI, `"Content-Type": "application/json"`); got != 2 {
		t.Fatalf("Custom UI metadata JSON mutations with explicit content type=%d, want 2", got)
	}
}

func TestServiceGroupLegacyMigrationCannotOverwriteExistingServerState(t *testing.T) {
	store := &fakeUIMetadataStore{username: "admin", preference: uiPreferenceRecord{Exists: true, Revision: 3}, fakeAdminSessionStore: fakeAdminSessionStore{authorized: true}}
	app := &app{adminStore: store}
	request := httptest.NewRequest(http.MethodPut, "/api/custom/ui/service-groups", strings.NewReader(`{"groups":[],"only_if_empty":true}`))
	request.Header.Set("MM-Authorization", "admin-session")
	recorder := httptest.NewRecorder()
	app.uiServiceGroupsHandler(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRoutingPresetPreservesAdvancedFieldsDeduplicatesAndDeletes(t *testing.T) {
	store := &fakeUIMetadataStore{username: "admin", fakeAdminSessionStore: fakeAdminSessionStore{authorized: true}}
	app := &app{adminStore: store}
	rule := `{"type":"field","domain":["example.com"],"ip":["1.1.1.1"],"protocol":["tls"],"inboundTag":["in-a"],"user":["alice__in-a"],"outboundTag":"direct","network":"tcp","port":"443","sourcePort":"1000-2000","attrs":"attrs[':method']=='GET'","custom":{"keep":true}}`
	for _, name := range []string{"first", "renamed"} {
		request := httptest.NewRequest(http.MethodPost, "/api/custom/ui/routing-presets", strings.NewReader(`{"name":"`+name+`","rule":`+rule+`}`))
		request.Header.Set("MM-Authorization", "admin-session")
		recorder := httptest.NewRecorder()
		app.uiRoutingPresetsHandler(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("save status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	if len(store.presets) != 1 || store.presets[0].Name != "renamed" {
		t.Fatalf("preset was not deduplicated: %#v", store.presets)
	}
	if custom, ok := store.presets[0].Rule["custom"].(map[string]any); !ok || custom["keep"] != true {
		t.Fatalf("advanced rule field was lost: %#v", store.presets[0].Rule)
	}

	request := httptest.NewRequest(http.MethodDelete, "/api/custom/ui/routing-presets?id=1", nil)
	request.Header.Set("MM-Authorization", "admin-session")
	recorder := httptest.NewRecorder()
	app.uiRoutingPresetsHandler(recorder, request)
	if recorder.Code != http.StatusOK || len(store.presets) != 0 {
		t.Fatalf("delete failed: status=%d presets=%d", recorder.Code, len(store.presets))
	}
}

func TestUIEndpointsRequireNamedAdminSession(t *testing.T) {
	store := &fakeUIMetadataStore{fakeAdminSessionStore: fakeAdminSessionStore{authorized: true}}
	app := &app{apiToken: "operator-token", adminStore: store}
	request := httptest.NewRequest(http.MethodGet, "/api/custom/ui/service-groups", nil)
	request.Header.Set("Authorization", "Bearer operator-token")
	recorder := httptest.NewRecorder()
	app.uiServiceGroupsHandler(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unnamed operator access status=%d", recorder.Code)
	}
}
