package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const maxTakeoverRequestBytes = 4096

type helperTakeoverRequest struct {
	ServerID            any    `json:"server_id"`
	HelperVersion       string `json:"helper_version,omitempty"`
	XrayMode            string `json:"xray_mode,omitempty"`
	ExternalConfigPhase string `json:"external_config_phase,omitempty"`
}

func (a *app) helperTakeoverHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	var request helperTakeoverRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxTakeoverRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid request"})
		return
	}
	reportedID, ok := normalizeHelperServerID(request.ServerID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid server_id"})
		return
	}
	officialID, _, authorized := a.authorizedHelper(r, reportedID, request.HelperVersion)
	if !authorized {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "unauthorized"})
		return
	}
	store, ok := a.adminStore.(remoteServerModeStore)
	if !ok || store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "remote server mode store unavailable"})
		return
	}
	mode := strings.TrimSpace(request.XrayMode)
	if mode != "" {
		if mode != "embedded" && mode != "external" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid xray_mode"})
			return
		}
		if err := a.setOfficialCoreMode(r.Context(), officialID, mode); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": sanitizeCoreModeMessage(err.Error())})
			return
		}
	}
	runtime, err := store.RemoteServerRuntime(r.Context(), officialID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "remote server status unavailable"})
		return
	}
	if request.ExternalConfigPhase != "" {
		if request.ExternalConfigPhase != "prepare" && request.ExternalConfigPhase != "confirm" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid External config phase"})
			return
		}
		intent, configured := a.helperState.coreModeIntent(officialID)
		if runtime.XrayMode != "external" || (configured && intent.PendingChange && intent.DesiredMode != "external") {
			writeJSON(w, http.StatusConflict, map[string]any{"success": false, "message": "External config repair requires External mode"})
			return
		}
		if err := a.ensureOfficialExternalConfig(r.Context(), officialID, request.ExternalConfigPhase); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": sanitizeCoreModeMessage(err.Error())})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "server_id": officialID, "runtime": runtime})
}
