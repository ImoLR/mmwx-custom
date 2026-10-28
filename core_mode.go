package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	coreModeRequestBytes = 4096
	coreDriftGrace       = 20 * time.Second
)

var coreRepairBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

type coreModeIntent struct {
	DesiredMode          string    `json:"desired_core_mode"`
	CustomCoreOwned      bool      `json:"custom_core_owned"`
	PendingChange        bool      `json:"pending_lifecycle_change,omitempty"`
	LifecycleSource      string    `json:"lifecycle_source,omitempty"`
	MachineID            string    `json:"machine_id,omitempty"`
	RepairStatus         string    `json:"repair_status"`
	WaitingOfficialSince time.Time `json:"waiting_official_since,omitempty"`
	RepairAttempts       int       `json:"repair_attempts"`
	LastRepairAt         time.Time `json:"last_repair_at,omitempty"`
	LastRepairReason     string    `json:"last_repair_reason,omitempty"`
	LastRepairError      string    `json:"last_repair_error,omitempty"`
	NextRepairAt         time.Time `json:"next_repair_at,omitempty"`
	LastObservedMode     string    `json:"last_observed_mode,omitempty"`
	DriftDetectedAt      time.Time `json:"drift_detected_at,omitempty"`
	// A recorded xray_mode change that the Custom UI did not request. A switch
	// to Embedded is kept (it may be a manual edit in the official UI) but is
	// flagged until an administrator confirms or reverts it.
	FormalChangedFrom       string    `json:"formal_changed_from,omitempty"`
	FormalChangedAt         time.Time `json:"formal_changed_at,omitempty"`
	FormalChangeUnconfirmed bool      `json:"formal_change_unconfirmed,omitempty"`
	UpdatedAt               time.Time `json:"updated_at"`
}

type coreModeView struct {
	Success               bool           `json:"success"`
	Configured            bool           `json:"configured"`
	Intent                coreModeIntent `json:"intent"`
	CurrentMode           string         `json:"current_mode,omitempty"`
	ControllerMode        string         `json:"controller_mode,omitempty"`
	ControllerStatus      string         `json:"controller_status,omitempty"`
	LastOfficialHeartbeat *time.Time     `json:"last_official_heartbeat,omitempty"`
	AgentStatus           agentStatus    `json:"agent_status"`
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
	intent.LastObservedMode = s.data.CoreModeIntents[serverID].LastObservedMode
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
	return runtime.Status == "connected" && (runtime.LastHeartbeat == nil || time.Since(*runtime.LastHeartbeat) <= 90*time.Second) && runtime.XrayMode == intent.DesiredMode && coreModeLocalHealthy(intent, status)
}

func coreModeLocalHealthy(intent coreModeIntent, status agentStatus) bool {
	if status.ReportedAt.IsZero() || time.Since(status.ReportedAt) > helperStaleTimeout {
		return false
	}
	if intent.DesiredMode == "embedded" {
		return status.CoreMode == "embedded" && !status.Ownership.Enabled
	}
	ownership := status.Ownership
	return status.CoreMode == "external" && ownership.Enabled && ownership.ServiceOwned && ownership.RuntimeOwned && ownership.SingleCore && ownership.ServiceActive && ownership.CoreReady
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
	previous := intent
	now := time.Now().UTC()
	if coreModeHealthy(intent, status, runtime) || runtime.XrayMode != intent.DesiredMode || !coreModeLocalHealthy(intent, status) {
		intent.WaitingOfficialSince = time.Time{}
	}
	formalKnown := runtime.XrayMode == "embedded" || runtime.XrayMode == "external"
	formalChanged := formalKnown && ((intent.LastObservedMode != "" && intent.LastObservedMode != runtime.XrayMode) || (!intent.PendingChange && intent.DesiredMode != runtime.XrayMode))
	if formalKnown && intent.LastObservedMode != "" && intent.LastObservedMode != runtime.XrayMode {
		if intent.PendingChange && intent.LifecycleSource == "custom-explicit" && intent.DesiredMode == runtime.XrayMode {
			intent.FormalChangeUnconfirmed = false
		} else {
			intent.FormalChangedFrom = intent.LastObservedMode
			intent.FormalChangedAt = now
			intent.FormalChangeUnconfirmed = runtime.XrayMode == "embedded"
		}
	}
	// The recorded xray_mode is changed only by an administrator (official UI
	// or the Custom core-mode switch), so it is the desired lifecycle mode.
	// Local drift from it, such as an Agent upgrade restoring the embedded
	// Core, is repaired below. Legacy intents did not carry PendingChange, so
	// they safely adopt the currently recorded mode on their first observation.
	if !intent.PendingChange && (runtime.XrayMode == "embedded" || runtime.XrayMode == "external") && intent.DesiredMode != runtime.XrayMode {
		intent.DesiredMode = runtime.XrayMode
		intent.CustomCoreOwned = true
		intent.LifecycleSource = "formal"
		intent.RepairAttempts = 0
		intent.LastRepairError = ""
		intent.NextRepairAt = time.Time{}
		intent.LastRepairReason = "formal lifecycle mode observed"
	}
	if formalKnown {
		intent.LastObservedMode = runtime.XrayMode
	}
	if status.MachineID != "" {
		intent.MachineID = status.MachineID
	}
	if result != nil && result.Action == "core.mode.apply" {
		intent.LastRepairAt = result.CompletedAt
		intent.RepairAttempts++
		intent.NextRepairAt = now.Add(nextCoreRepairDelay(intent.RepairAttempts))
		if result.Success {
			intent.LastRepairError = ""
			intent.LastRepairReason = "apply completed; waiting for observed recovery"
		} else {
			intent.LastRepairError = sanitizeCoreModeMessage(result.Message)
			intent.LastRepairReason = "automatic desired-state repair failed"
		}
	}
	if coreModeHealthy(intent, status, runtime) {
		intent.DriftDetectedAt = time.Time{}
		intent.PendingChange = false
		intent.LifecycleSource = "formal"
		intent.RepairStatus = "healthy"
		intent.RepairAttempts = 0
		intent.LastRepairError = ""
		intent.LastRepairReason = "formal lifecycle mode healthy"
		intent.NextRepairAt = time.Time{}
		intent.UpdatedAt = now
		s.data.CoreModeIntents[serverID] = intent
		return nil, s.commitIntentLocked(previous, intent)
	}
	for _, command := range s.data.ManagementCommands[serverID] {
		if command.Action == "core.mode.apply" && now.Before(command.ExpiresAt) {
			intent.RepairStatus = "repairing"
			intent.UpdatedAt = now
			s.data.CoreModeIntents[serverID] = intent
			return &command, s.commitIntentLocked(previous, intent)
		}
	}
	// A healthy local Core cannot repair an offline/stale official runtime.
	// Preserve reconnect debounce, then wait without issuing lifecycle work.
	if runtime.XrayMode == intent.DesiredMode && coreModeLocalHealthy(intent, status) {
		if intent.DriftDetectedAt.IsZero() && intent.RepairStatus != "waiting_official" {
			intent.DriftDetectedAt = now
		}
		if intent.PendingChange || formalChanged || intent.RepairStatus == "waiting_official" || now.Sub(intent.DriftDetectedAt) > coreDriftGrace {
			if intent.WaitingOfficialSince.IsZero() {
				intent.WaitingOfficialSince = now
			}
			intent.RepairStatus = "waiting_official"
			intent.LastRepairReason = "local Core healthy; waiting for official runtime"
			intent.LastRepairError = ""
			intent.DriftDetectedAt = time.Time{}
		}
		intent.UpdatedAt = now
		s.data.CoreModeIntents[serverID] = intent
		return nil, s.commitIntentLocked(previous, intent)
	}
	// A reconnect can temporarily make the official runtime look unhealthy.
	// Keep the last settled UI state and wait for sustained drift. Explicit
	// lifecycle requests and recorded controller-mode changes remain immediate.
	if !intent.PendingChange && !formalChanged {
		if intent.DriftDetectedAt.IsZero() {
			intent.DriftDetectedAt = now
		}
		if now.Sub(intent.DriftDetectedAt) <= coreDriftGrace {
			intent.UpdatedAt = now
			s.data.CoreModeIntents[serverID] = intent
			return nil, s.commitIntentLocked(previous, intent)
		}
	}
	if !hasCapability(status, "core.mode.apply") {
		intent.RepairStatus = "degraded"
		intent.LastRepairError = "Helper upgrade required for desired-state repair"
		intent.UpdatedAt = now
		s.data.CoreModeIntents[serverID] = intent
		return nil, s.commitIntentLocked(previous, intent)
	}
	if !intent.NextRepairAt.IsZero() && now.Before(intent.NextRepairAt) {
		if intent.RepairAttempts >= len(coreRepairBackoff) || strings.Contains(intent.LastRepairError, coreModeAdminSessionRequired) {
			intent.RepairStatus = "degraded"
		} else {
			intent.RepairStatus = "drift_detected"
		}
		intent.UpdatedAt = now
		s.data.CoreModeIntents[serverID] = intent
		return nil, s.commitIntentLocked(previous, intent)
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

// commitIntentLocked saves a reconciled intent at once when anything but its
// UpdatedAt changed. A pass that only refreshes UpdatedAt keeps it in memory
// for the lazy flush instead of rewriting the file on every report.
func (s *helperState) commitIntentLocked(previous, next coreModeIntent) error {
	previous.UpdatedAt, next.UpdatedAt = time.Time{}, time.Time{}
	before, _ := json.Marshal(previous)
	after, _ := json.Marshal(next)
	if bytes.Equal(before, after) {
		s.markVolatileLocked()
		return nil
	}
	return s.saveLocked()
}

// coreModeSummary is the per-server lifecycle state shown on service cards.
type coreModeSummary struct {
	Configured              bool      `json:"configured"`
	DesiredMode             string    `json:"desired_mode,omitempty"`
	CurrentMode             string    `json:"current_mode,omitempty"`
	RepairStatus            string    `json:"repair_status,omitempty"`
	WaitingOfficialSince    time.Time `json:"waiting_official_since,omitempty"`
	PendingChange           bool      `json:"pending_change,omitempty"`
	LastRepairError         string    `json:"last_repair_error,omitempty"`
	FormalChangedFrom       string    `json:"formal_changed_from,omitempty"`
	FormalChangedAt         time.Time `json:"formal_changed_at,omitempty"`
	FormalChangeUnconfirmed bool      `json:"formal_change_unconfirmed,omitempty"`
	Fresh                   bool      `json:"fresh"`
	CanSwitch               bool      `json:"can_switch"`
}

func (s *helperState) coreModeSummaries() map[string]coreModeSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]coreModeSummary, len(s.data.Servers))
	for serverID := range s.data.Servers {
		status := s.data.AgentStatuses[serverID]
		intent, configured := s.data.CoreModeIntents[serverID]
		out[serverID] = coreModeSummary{
			Configured: configured, DesiredMode: intent.DesiredMode, CurrentMode: status.CoreMode,
			RepairStatus: intent.RepairStatus, WaitingOfficialSince: intent.WaitingOfficialSince, PendingChange: intent.PendingChange, LastRepairError: intent.LastRepairError,
			FormalChangedFrom: intent.FormalChangedFrom, FormalChangedAt: intent.FormalChangedAt, FormalChangeUnconfirmed: intent.FormalChangeUnconfirmed,
			Fresh:     !status.ReportedAt.IsZero() && time.Since(status.ReportedAt) <= helperStaleTimeout,
			CanSwitch: hasCapability(status, "core.mode.apply"),
		}
	}
	return out
}

func sanitizeCoreModeMessage(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		// Keep the leading 「端口 N 被 … 占用」 readable: never split a rune.
		value = strings.ToValidUTF8(value[:512], "")
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
		if token := strings.TrimSpace(r.Header.Get("MM-Authorization")); token != "" {
			a.coreModeSessions.Store(serverID, token)
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
	writeJSON(w, http.StatusOK, coreModeView{Success: true, Configured: configured, Intent: intent, CurrentMode: status.CoreMode, ControllerMode: runtime.XrayMode, ControllerStatus: runtime.Status, LastOfficialHeartbeat: runtime.LastHeartbeat, AgentStatus: status})
}
