package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/ImoLR/mmwx-custom/internal/externalconfig"
)

type coreModeSessionStore interface {
	CoreModeAdminSession(context.Context) (string, error)
}

const coreModeAdminSessionRequired = "an active official administrator session is required for Core handoff"

func (s *postgresAdminSessionStore) CoreModeAdminSession(ctx context.Context) (string, error) {
	var token string
	err := s.db.QueryRowContext(ctx, `SELECT s.token FROM sessions s JOIN users u ON u.username=s.username WHERE s.expires_at>CURRENT_TIMESTAMP AND u.role='admin' AND u.is_active=1 ORDER BY s.expires_at DESC LIMIT 1`).Scan(&token)
	return token, err
}

func (a *app) coreModeAdminSession(ctx context.Context, serverID string) (string, error) {
	if token, ok := a.coreModeSessions.Load(serverID); ok {
		if valid, err := a.adminStore.AuthorizeAdmin(ctx, token.(string)); err == nil && valid {
			return token.(string), nil
		}
		a.coreModeSessions.Delete(serverID)
	}
	if store, ok := a.adminStore.(coreModeSessionStore); ok {
		if token, err := store.CoreModeAdminSession(ctx); err == nil && token != "" {
			return token, nil
		}
	}
	return "", errors.New(coreModeAdminSessionRequired)
}

func (a *app) setOfficialCoreMode(ctx context.Context, serverID, mode string) error {
	token, err := a.coreModeAdminSession(ctx, serverID)
	if err != nil {
		return err
	}
	var response struct {
		Servers []map[string]any `json:"servers"`
	}
	if err := a.officialLifecycleJSON(ctx, token, http.MethodPost, "/api/v3", map[string]any{"op": "034e094d05aa3f83"}, &response); err != nil {
		return err
	}
	for _, server := range response.Servers {
		id, _ := server["id"].(float64)
		if strconv.FormatInt(int64(id), 10) != serverID {
			continue
		}
		if server["xray_mode"] == mode {
			if mode == "embedded" {
				return a.helperState.markExternalConfigRepair(serverID, "")
			}
			return nil
		}
		// Official requires name/traffic fields and preserves most omitted
		// fields, but an omitted ddns_enabled/ddns_provider_id is saved as off,
		// which silently stops DDNS for dynamic-IP servers. Echo the switches
		// back; sending the full readback would reject RFC3339 expiry dates.
		payload := map[string]any{"id": id, "name": server["name"], "traffic_limit": server["traffic_limit"], "traffic_reset_day": server["traffic_reset_day"], "xray_mode": mode}
		for _, key := range []string{"ddns_enabled", "ddns_provider_id", "ipv6_enabled", "lock_entry_ip"} {
			if value, ok := server[key]; ok {
				payload[key] = value
			}
		}
		if err := a.officialLifecycleJSON(ctx, token, http.MethodPost, "/api/v3", map[string]any{"op": "2146f0b13a7196cb", "payload": payload}, nil); err != nil {
			return err
		}
		if mode == "embedded" {
			return a.helperState.markExternalConfigRepair(serverID, "")
		}
		return nil
	}
	return errOperatorServerNotFound
}

func (a *app) ensureOfficialExternalConfig(ctx context.Context, serverID, phase string) error {
	token, err := a.coreModeAdminSession(ctx, serverID)
	if err != nil {
		return err
	}
	query := "?server_id=" + url.QueryEscape(serverID)
	readConfig := func() (string, error) {
		var response struct {
			Config string `json:"config"`
		}
		err := a.officialLifecycleJSON(ctx, token, http.MethodGet, "/api/admin/remote/xray/config"+query, nil, &response)
		return response.Config, err
	}
	raw, err := readConfig()
	if err != nil {
		return err
	}
	complete, err := externalconfig.Complete([]byte(raw))
	if err != nil {
		return err
	}
	fingerprint, err := externalconfig.UserConfigHash([]byte(raw))
	if err != nil {
		return err
	}
	a.helperState.mu.Lock()
	expected := a.helperState.data.ExternalConfigRepairs[serverID]
	a.helperState.mu.Unlock()
	if expected != "" && expected != fingerprint {
		return errors.New("user configuration changed during External control repair")
	}
	if phase == "prepare" {
		if complete {
			return nil
		}
		var recovery struct {
			HasPending bool `json:"has_pending"`
			Current    struct {
				Config string `json:"config_json"`
			} `json:"current"`
		}
		if err := a.officialLifecycleJSON(ctx, token, http.MethodPost, "/api/v3", map[string]any{"op": "0f1dfff4d60aad8f", "q": "server_id=" + serverID + "&with_config=true"}, &recovery); err != nil {
			return err
		}
		if recovery.HasPending {
			master, err := externalconfig.UserConfigHash([]byte(recovery.Current.Config))
			if err != nil || master != fingerprint {
				return errors.New("unrelated pending recovery requires an administrator decision")
			}
		}
		// The marker is saved before calling official, so a lost HTTP response is
		// retryable. Only API/metrics/stats/policy differences can later be accepted.
		if err := a.helperState.markExternalConfigRepair(serverID, fingerprint); err != nil {
			return err
		}
		var current struct {
			Config map[string]any `json:"config"`
		}
		if err := a.officialLifecycleJSON(ctx, token, http.MethodGet, "/api/admin/remote/xray/system-config"+query, nil, &current); err != nil {
			return err
		}
		if err := a.officialLifecycleJSON(ctx, token, http.MethodPost, "/api/admin/remote/xray/system-config"+query, externalconfig.Settings(current.Config), nil); err != nil {
			return err
		}
		raw, err = readConfig()
		if err != nil {
			return err
		}
		complete, err = externalconfig.Complete([]byte(raw))
		after, hashErr := externalconfig.UserConfigHash([]byte(raw))
		if err != nil || !complete || hashErr != nil || after != fingerprint {
			return errors.New("official regeneration did not safely restore External API/metrics")
		}
		return nil
	}
	if !complete {
		return errors.New("External configuration is incomplete")
	}
	// The Helper restarted the Agent after regeneration. Accept only a fresh
	// pending report exactly matching the repaired live config and our marker.
	var recovery struct {
		HasPending bool `json:"has_pending"`
		Current    struct {
			Config string `json:"config_json"`
		} `json:"current"`
		Pending struct {
			Config string `json:"config_json"`
		} `json:"pending"`
	}
	readRecovery := func() error {
		recovery.HasPending = false
		return a.officialLifecycleJSON(ctx, token, http.MethodPost, "/api/v3", map[string]any{"op": "0f1dfff4d60aad8f", "q": "server_id=" + serverID + "&with_config=true"}, &recovery)
	}
	if err := readRecovery(); err != nil {
		return err
	}
	if recovery.HasPending {
		master, err := externalconfig.UserConfigHash([]byte(recovery.Current.Config))
		if err != nil || master != fingerprint {
			return errors.New("master configuration changed during External control repair")
		}
		if expected == "" || !equivalentCoreConfig(raw, recovery.Pending.Config) {
			return errors.New("waiting for the repaired Agent configuration report; unrelated recovery was not accepted")
		}
		if err := a.officialLifecycleJSON(ctx, token, http.MethodPost, "/api/v3", map[string]any{"op": "438a645265d45957", "q": "server_id=" + serverID}, nil); err != nil {
			return err
		}
		if err := readRecovery(); err != nil {
			return err
		}
	}
	if recovery.HasPending || !equivalentCoreConfig(raw, recovery.Current.Config) {
		return errors.New("official External config snapshot has not converged")
	}
	return a.helperState.markExternalConfigRepair(serverID, "")
}

func (s *helperState) markExternalConfigRepair(serverID, fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.ExternalConfigRepairs == nil {
		s.data.ExternalConfigRepairs = map[string]string{}
	}
	if fingerprint == "" {
		delete(s.data.ExternalConfigRepairs, serverID)
	} else {
		s.data.ExternalConfigRepairs[serverID] = fingerprint
	}
	return s.saveLocked()
}

func equivalentCoreConfig(a, b string) bool {
	var left, right any
	if json.Unmarshal([]byte(a), &left) != nil || json.Unmarshal([]byte(b), &right) != nil {
		return false
	}
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}
