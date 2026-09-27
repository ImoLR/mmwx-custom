package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	serviceGroupsPreferenceKey = "service_groups"
	maxServiceGroups           = 64
	maxServiceGroupServers     = 512
	maxRoutingRulePresets      = 20
	maxUIMetadataBytes         = 256 << 10
)

var errUIMetadataConflict = errors.New("UI metadata already exists")

type serviceGroup struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	ServerIDs []int64 `json:"server_ids"`
}

type serviceGroupsDocument struct {
	Groups []serviceGroup `json:"groups"`
}

type uiPreferenceRecord struct {
	Exists    bool            `json:"exists"`
	Data      json.RawMessage `json:"data"`
	Revision  int64           `json:"revision"`
	UpdatedAt time.Time       `json:"updated_at,omitempty"`
}

type routingRulePreset struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name"`
	Rule      map[string]any `json:"rule"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type uiMetadataStore interface {
	GetUIPreference(context.Context, string, string) (uiPreferenceRecord, error)
	PutUIPreference(context.Context, string, string, json.RawMessage, bool) (uiPreferenceRecord, error)
	ListRoutingRulePresets(context.Context, string) ([]routingRulePreset, error)
	UpsertRoutingRulePreset(context.Context, string, string, map[string]any) (routingRulePreset, error)
	DeleteRoutingRulePreset(context.Context, string, int64) error
}

type adminIdentityStore interface {
	AdminUsername(context.Context, string) (string, error)
}

func (a *app) authorizeUIRequest(r *http.Request) (string, uiMetadataStore, error) {
	token := strings.TrimSpace(r.Header.Get("MM-Authorization"))
	if token == "" {
		return "", nil, errOperatorAuthorizationMissing
	}
	identityStore, ok := a.adminStore.(adminIdentityStore)
	if !ok || identityStore == nil {
		return "", nil, errOperatorAuthorizationUnavailable
	}
	username, err := identityStore.AdminUsername(r.Context(), token)
	if err != nil {
		return "", nil, errOperatorAuthorizationUnavailable
	}
	if username == "" {
		return "", nil, errOperatorAuthorizationInvalid
	}
	metadataStore, ok := a.adminStore.(uiMetadataStore)
	if !ok || metadataStore == nil {
		return "", nil, errOperatorAuthorizationUnavailable
	}
	return username, metadataStore, nil
}

func validateServiceGroups(groups []serviceGroup) error {
	if len(groups) > maxServiceGroups {
		return errors.New("too many service groups")
	}
	ids := make(map[string]struct{}, len(groups))
	names := make(map[string]struct{}, len(groups))
	for index := range groups {
		groups[index].ID = strings.TrimSpace(groups[index].ID)
		groups[index].Name = strings.TrimSpace(groups[index].Name)
		if groups[index].ID == "" || len(groups[index].ID) > 80 || groups[index].Name == "" || len([]rune(groups[index].Name)) > 28 {
			return errors.New("invalid service group")
		}
		for _, ch := range groups[index].ID {
			if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '-' && ch != '_' {
				return errors.New("invalid service group id")
			}
		}
		nameKey := strings.ToLower(groups[index].Name)
		if _, exists := ids[groups[index].ID]; exists {
			return errors.New("duplicate service group id")
		}
		if _, exists := names[nameKey]; exists {
			return errors.New("duplicate service group name")
		}
		ids[groups[index].ID] = struct{}{}
		names[nameKey] = struct{}{}
		if len(groups[index].ServerIDs) > maxServiceGroupServers {
			return errors.New("too many servers in service group")
		}
		seenServers := make(map[int64]struct{}, len(groups[index].ServerIDs))
		for _, serverID := range groups[index].ServerIDs {
			if serverID <= 0 {
				return errors.New("invalid server id in service group")
			}
			if _, exists := seenServers[serverID]; exists {
				return errors.New("duplicate server id in service group")
			}
			seenServers[serverID] = struct{}{}
		}
	}
	return nil
}

func canonicalRoutingRule(rule map[string]any) ([]byte, string, error) {
	if len(rule) == 0 {
		return nil, "", errors.New("routing rule is required")
	}
	data, err := json.Marshal(rule)
	if err != nil || len(data) > 64<<10 {
		return nil, "", errors.New("invalid routing rule")
	}
	hash := sha256.Sum256(data)
	return data, hex.EncodeToString(hash[:]), nil
}

func routingRulePresetName(rule map[string]any) string {
	if value, ok := rule["marktag"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	for _, field := range []string{"domain", "ip", "protocol", "inboundTag", "user"} {
		values, ok := rule[field].([]any)
		if ok && len(values) > 0 {
			if value, ok := values[0].(string); ok && strings.TrimSpace(value) != "" {
				return field + ": " + strings.TrimSpace(value)
			}
		}
	}
	return "自定义规则"
}

func (a *app) uiServiceGroupsHandler(w http.ResponseWriter, r *http.Request) {
	username, store, err := a.authorizeUIRequest(r)
	if err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		record, err := store.GetUIPreference(r.Context(), username, serviceGroupsPreferenceKey)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to load service groups"})
			return
		}
		groups := []serviceGroup{}
		if record.Exists && len(record.Data) > 0 {
			var document serviceGroupsDocument
			if json.Unmarshal(record.Data, &document) == nil && document.Groups != nil {
				groups = document.Groups
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "exists": record.Exists, "groups": groups, "revision": record.Revision, "updated_at": record.UpdatedAt})
	case http.MethodPut:
		var request struct {
			Groups      []serviceGroup `json:"groups"`
			OnlyIfEmpty bool           `json:"only_if_empty,omitempty"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, maxUIMetadataBytes+1))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.Groups == nil || validateServiceGroups(request.Groups) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid service groups"})
			return
		}
		data, _ := json.Marshal(serviceGroupsDocument{Groups: request.Groups})
		record, err := store.PutUIPreference(r.Context(), username, serviceGroupsPreferenceKey, data, request.OnlyIfEmpty)
		if errors.Is(err, errUIMetadataConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"success": false, "message": "service groups already exist"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to save service groups"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "exists": true, "groups": request.Groups, "revision": record.Revision, "updated_at": record.UpdatedAt})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
	}
}

func (a *app) uiRoutingPresetsHandler(w http.ResponseWriter, r *http.Request) {
	username, store, err := a.authorizeUIRequest(r)
	if err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		presets, err := store.ListRoutingRulePresets(r.Context(), username)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to load routing presets"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "presets": presets})
	case http.MethodPost:
		var request struct {
			Name string         `json:"name"`
			Rule map[string]any `json:"rule"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, (64<<10)+1))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid routing preset"})
			return
		}
		request.Name = strings.TrimSpace(request.Name)
		if request.Name == "" {
			request.Name = routingRulePresetName(request.Rule)
		}
		if len([]rune(request.Name)) > 120 {
			request.Name = string([]rune(request.Name)[:120])
		}
		if _, _, err := canonicalRoutingRule(request.Rule); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": err.Error()})
			return
		}
		preset, err := store.UpsertRoutingRulePreset(r.Context(), username, request.Name, request.Rule)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to save routing preset"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "preset": preset})
	case http.MethodDelete:
		id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err != nil || id <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid routing preset id"})
			return
		}
		if err := store.DeleteRoutingRulePreset(r.Context(), username, id); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "message": "routing preset not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
	}
}
