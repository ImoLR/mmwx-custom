package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const updateProgressRequestBytes = 4096

type componentUpdateProgress struct {
	Component     string    `json:"component"`
	Phase         string    `json:"phase"`
	TargetVersion string    `json:"target_version,omitempty"`
	Message       string    `json:"message,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type helperUpdateProgressRequest struct {
	ServerID      any    `json:"server_id"`
	HelperVersion string `json:"helper_version"`
	Component     string `json:"component"`
	Phase         string `json:"phase"`
	TargetVersion string `json:"target_version,omitempty"`
	Message       string `json:"message,omitempty"`
}

var allowedUpdatePhases = map[string]struct{}{
	"dispatching": {}, "downloading": {}, "verifying": {}, "installing": {}, "restarting": {},
	"reconnecting": {}, "success": {}, "failed": {}, "rolled_back": {},
}

func (s *helperState) recordUpdateProgress(serverID string, progress componentUpdateProgress) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.data.AgentStatuses[serverID]
	progress.UpdatedAt = time.Now().UTC()
	progress.Message = sanitizeCoreModeMessage(progress.Message)
	status.Update = &progress
	s.data.AgentStatuses[serverID] = status
	return s.saveLocked()
}

func (a *app) helperUpdateProgressHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	var request helperUpdateProgressRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, updateProgressRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid request"})
		return
	}
	reporterID, ok := normalizeHelperServerID(request.ServerID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid server_id"})
		return
	}
	officialID, _, authorized := a.authorizedHelper(r, reporterID, strings.TrimSpace(request.HelperVersion))
	if !authorized {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "unauthorized"})
		return
	}
	component := strings.TrimSpace(request.Component)
	phase := strings.TrimSpace(request.Phase)
	if (component != "helper" && component != "core") || len(request.TargetVersion) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid component"})
		return
	}
	if _, ok := allowedUpdatePhases[phase]; !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid phase"})
		return
	}
	progress := componentUpdateProgress{Component: component, Phase: phase, TargetVersion: strings.TrimSpace(request.TargetVersion), Message: request.Message}
	if err := a.helperState.recordUpdateProgress(officialID, progress); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to record update progress"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
