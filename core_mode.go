package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const coreModeRequestBytes = 4096

var coreRepairBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

type coreModeIntent struct {
	DesiredMode      string    `json:"desired_core_mode"`
	CustomCoreOwned  bool      `json:"custom_core_owned"`
	PendingChange    bool      `json:"pending_lifecycle_change,omitempty"`
	LifecycleSource  string    `json:"lifecycle_source,omitempty"`
	MachineID        string    `json:"machine_id,omitempty"`
	RepairStatus     string    `json:"repair_status"`
	RepairAttempts   int       `json:"repair_attempts"`
	LastRepairAt     time.Time `json:"last_repair_at,omitempty"`
	LastRepairReason string    `json:"last_repair_reason,omitempty"`
	LastRepairError  string    `json:"last_repair_error,omitempty"`
	NextRepairAt     time.Time `json:"next_repair_at,omitempty"`
	LastObservedMode string    `json:"last_observed_mode,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type coreModeView struct {
	Success          bool           `json:"success"`
	Configured       bool           `json:"configured"`
	Intent           coreModeIntent `json:"intent"`
	CurrentMode      string         `json:"current_mode,omitempty"`
	ControllerMode   string         `json:"controller_mode,omitempty"`
	ControllerStatus string         `json:"controller_status,omitempty"`
	AgentStatus      agentStatus    `json:"agent_status"`
}

func newCoreModeIntent(mode string, now time.Time) coreModeIntent {
	return coreModeIntent{
		DesiredMode: mode, CustomCoreOwned: true, LifecycleSource: "formal", RepairStatus: "drift_detected", UpdatedAt: now.UTC(),
	}
}

func (s *helperState) setCoreModeIntent(serverID, mode string) (coreModeIntent, error) {
	if mode != "external" && mode != "embedded" {
		return coreModeIntent{}, errors.New("invalid desired core mode")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, ok := s.data.Servers[serverID]
	if !ok {
		return coreModeIntent{}, errors.New("helper is not registered")
	}
	now := time.Now().UTC()
	intent := newCoreModeIntent(mode, now)
	intent.PendingChange = true
	intent.LifecycleSource = "custom-explicit"
	intent.MachineID = identity.CustomServerUUID
	s.data.CoreModeIntents[serverID] = intent
	if err := s.saveLocked(); err != nil {
		return coreModeIntent{}, err
	}
	return intent, nil
}

func (s *helperState) coreModeIntent(serverID string) (coreModeIntent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	intent, ok := s.data.CoreModeIntents[serverID]
	return intent, ok
}

func coreModeHealthy(intent coreModeIntent, status agentStatus, runtime remoteServerRuntime) bool {
	if runtime.Status != "connected" || status.ReportedAt.IsZero() || time.Since(status.ReportedAt) > helperStaleTimeout {
		return false
	}
	if intent.DesiredMode == "embedded" {
		return runtime.XrayMode == "embedded" && status.CoreMode == "embedded" && !status.Ownership.Enabled
	}
	ownership := status.Ownership
	return runtime.XrayMode == "external" && status.CoreMode == "external" && ownership.Enabled && ownership.ServiceOwned && ownership.RuntimeOwned && ownership.SingleCore && ownership.ServiceActive && ownership.CoreReady
}

func hasCapability(status agentStatus, capability string) bool {
	for _, current := range status.Capabilities {
		if current == capability {
			return true
		}
	}
	return false
}

func nextCoreRepairDelay(attempts int) time.Duration {
	if attempts <= 0 {
		return 0
	}
	index := attempts - 1
	if index >= len(coreRepairBackoff) {
		index = len(coreRepairBackoff) - 1
	}
	return coreRepairBackoff[index]
}

// reconcileCoreMode records one fresh observation and queues at most one
// signed, bounded repair command. It never executes lifecycle work in the
// controller process.
func (s *helperState) reconcileCoreMode(serverID string, status agentStatus, runtime remoteServerRuntime, result *managementResult) (*managementCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	intent, configured := s.data.CoreModeIntents[serverID]
	if !configured {
		return nil, nil
	}
	now := time.Now().UTC()
	// The formal controller owns the lifecycle choice. A completed Custom
	// transition must never turn into a permanent policy that pulls an
	// explicitly changed formal xray_mode back again. Legacy intents did not
	// carry PendingChange, so they safely adopt the currently recorded formal
	// mode on their first observation.
	if !intent.PendingChange && (runtime.XrayMode == "embedded" || runtime.XrayMode == "external") && intent.DesiredMode != runtime.XrayMode {
		intent.DesiredMode = runtime.XrayMode
		intent.CustomCoreOwned = true
		intent.LifecycleSource = "formal"
		intent.RepairAttempts = 0
		intent.LastRepairError = ""
		intent.NextRepairAt = time.Time{}
		intent.LastRepairReason = "formal lifecycle mode observed"
	}
	intent.LastObservedMode = runtime.XrayMode
	if status.MachineID != "" {
		intent.MachineID = status.MachineID
	}
	if result != nil && result.Action == "core.mode.apply" {
		intent.LastRepairAt = result.CompletedAt
		if result.Success {
			intent.RepairAttempts = 0
			intent.LastRepairError = ""
			intent.LastRepairReason = "desired core mode restored"
			intent.NextRepairAt = time.Time{}
		} else {
			intent.RepairAttempts++
			intent.LastRepairError = sanitizeCoreModeMessage(result.Message)
			intent.LastRepairReason = "automatic desired-state repair failed"
			intent.NextRepairAt = now.Add(nextCoreRepairDelay(intent.RepairAttempts))
		}
	}
	if coreModeHealthy(intent, status, runtime) {
		intent.PendingChange = false
		intent.LifecycleSource = "formal"
		intent.RepairStatus = "healthy"
		intent.RepairAttempts = 0
		intent.LastRepairError = ""
		intent.LastRepairReason = "formal lifecycle mode healthy"
		intent.NextRepairAt = time.Time{}
		intent.UpdatedAt = now
		s.data.CoreModeIntents[serverID] = intent
		return nil, s.saveLocked()
	}
	for _, command := range s.data.ManagementCommands[serverID] {
		if command.Action == "core.mode.apply" && now.Before(command.ExpiresAt) {
			intent.RepairStatus = "repairing"
			intent.UpdatedAt = now
			s.data.CoreModeIntents[serverID] = intent
			return &command, s.saveLocked()
		}
	}
	if !hasCapability(status, "core.mode.apply") {
		intent.RepairStatus = "degraded"
		intent.LastRepairError = "Helper upgrade required for desired-state repair"
		intent.UpdatedAt = now
		s.data.CoreModeIntents[serverID] = intent
		return nil, s.saveLocked()
	}
	if !intent.NextRepairAt.IsZero() && now.Before(intent.NextRepairAt) {
		if intent.RepairAttempts >= len(coreRepairBackoff) {
			intent.RepairStatus = "degraded"
		} else {
			intent.RepairStatus = "drift_detected"
		}
		intent.UpdatedAt = now
		s.data.CoreModeIntents[serverID] = intent
		return nil, s.saveLocked()
	}
	payload, _ := json.Marshal(map[string]string{"desired_mode": intent.DesiredMode})
	id, err := randomUUID()
	if err != nil {
		return nil, err
	}
	identity := s.data.Servers[serverID]
	command := managementCommand{ID: id, Action: "core.mode.apply", Payload: payload, CreatedAt: now, ExpiresAt: now.Add(managementCommandTTL)}
	command.Signature, err = signManagementCommand(command, identity.HelperTokenHash)
	if err != nil {
		return nil, err
	}
	s.data.ManagementCommands[serverID] = append(s.data.ManagementCommands[serverID], command)
	intent.RepairStatus = "repairing"
	intent.LastRepairReason = "desired/current core mode drift detected"
	intent.UpdatedAt = now
	s.data.CoreModeIntents[serverID] = intent
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return &command, nil
}

func sanitizeCoreModeMessage(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}

func parseCoreModePath(value string) (string, bool) {
	const prefix, suffix = "/api/custom/servers/", "/core-mode"
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(value, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return "", false
	}
	for _, char := range id {
		if char < '0' || char > '9' {
			return "", false
		}
	}
	return id, id != "0"
}

func (a *app) coreModeHandler(w http.ResponseWriter, r *http.Request, serverID string) {
	if err := a.authorizeOperatorServerRequest(r, serverID); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	store, ok := a.adminStore.(remoteServerModeStore)
	if !ok || store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "remote server mode store unavailable"})
		return
	}
	if r.Method == http.MethodPost {
		var request struct {
			DesiredMode string `json:"desired_mode"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, coreModeRequestBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid request"})
			return
		}
		if _, err := a.helperState.setCoreModeIntent(serverID, strings.TrimSpace(request.DesiredMode)); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": err.Error()})
			return
		}
	} else if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	runtime, err := store.RemoteServerRuntime(r.Context(), serverID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "remote server status unavailable"})
		return
	}
	a.helperState.mu.Lock()
	status := a.helperState.data.AgentStatuses[serverID]
	a.helperState.mu.Unlock()
	intent, configured := a.helperState.coreModeIntent(serverID)
	writeJSON(w, http.StatusOK, coreModeView{Success: true, Configured: configured, Intent: intent, CurrentMode: status.CoreMode, ControllerMode: runtime.XrayMode, ControllerStatus: runtime.Status, AgentStatus: status})
}
