package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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
		payload := officialServerUpdatePayload(server)
		// The list redacts credentials. Updating the pull address/port also
		// replaces its token, even when the address and port are unchanged.
		var secrets struct {
			PullToken *string `json:"pull_token"`
		}
		if err := a.officialLifecycleJSON(ctx, token, http.MethodGet, "/api/admin/remote-servers/reveal-token?server_id="+url.QueryEscape(serverID), nil, &secrets); err != nil {
			return err
		}
		if secrets.PullToken == nil {
			return errors.New("official pull token is unavailable; server was not updated")
		}
		payload["pull_token"] = *secrets.PullToken
		payload["xray_mode"] = mode
		if err := a.officialLifecycleJSON(ctx, token, http.MethodPost, "/api/v3", map[string]any{"op": "2146f0b13a7196cb", "payload": payload}, nil); err != nil {
			// Official writes xray_mode before validating the rest of the update.
			// Even a timeout or success:false can therefore mean the mode changed.
			// Read its database independently of the failed request's deadline.
			verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			store, ok := a.adminStore.(remoteServerModeStore)
			if !ok {
				return err
			}
			runtime, readErr := store.RemoteServerRuntime(verifyCtx, serverID)
			if readErr != nil || (runtime.XrayMode != "embedded" && runtime.XrayMode != "external") {
				return errors.Join(err, errors.New("official mode could not be verified after the failed update"), readErr)
			}
			if saveErr := a.helperState.observeOfficialModeAfterWrite(serverID, runtime.XrayMode); saveErr != nil {
				return saveErr
			}
			if runtime.XrayMode != mode {
				return err
			}
		} else if err := a.helperState.observeOfficialModeAfterWrite(serverID, mode); err != nil {
			return err
		}
		if mode == "embedded" {
			return a.helperState.markExternalConfigRepair(serverID, "")
		}
		return nil
	}
	return errOperatorServerNotFound
}

func (s *helperState) observeOfficialModeAfterWrite(serverID, mode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	intent, ok := s.data.CoreModeIntents[serverID]
	if !ok {
		return nil
	}
	intent.LastObservedMode = mode
	// A rejected change or a completed rollback ends the previous request.
	// Requeue must repair the real mode, not repeat a failed user transition.
	if intent.PendingChange && intent.DesiredMode != mode {
		intent.DesiredMode = mode
		intent.PendingChange = false
		intent.LifecycleSource = "formal"
		intent.RepairStatus = "drift_detected"
		intent.RepairAttempts = 0
		intent.LastRepairError = ""
		intent.LastRepairReason = "official mode retained after handoff failure or rollback"
		intent.NextRepairAt = time.Time{}
	}
	intent.UpdatedAt = time.Now().UTC()
	s.data.CoreModeIntents[serverID] = intent
	return s.saveLocked()
}

// Keep this rule in sync with frontend/src/server-update.ts. Echo the latest
// readback, excluding redacted secrets and the computed traffic total; official
// treats traffic_used as an explicit counter adjustment, not a read-only field.
func officialServerUpdatePayload(server map[string]any) map[string]any {
	payload := make(map[string]any, len(server))
	for key, value := range server {
		switch key {
		case "token", "agent_token", "traffic_used":
			continue
		}
		payload[key] = value
	}
	if expiry, ok := payload["expires_at"].(string); ok {
		payload["expires_at"] = strings.SplitN(expiry, "T", 2)[0]
	}
	return payload
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
