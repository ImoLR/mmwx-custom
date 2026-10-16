package main

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const updateProgressRequestBytes = 4096

type updateProgressLine struct {
	At    time.Time `json:"at"`
	Phase string    `json:"phase"`
	Text  string    `json:"text"`
}

type componentUpdateProgress struct {
	Component     string               `json:"component"`
	Phase         string               `json:"phase"`
	TargetVersion string               `json:"target_version,omitempty"`
	Message       string               `json:"message,omitempty"`
	UpdatedAt     time.Time            `json:"updated_at"`
	StartedAt     time.Time            `json:"started_at"`
	Percent       *int                 `json:"percent,omitempty"`
	BytesDone     int64                `json:"bytes_done,omitempty"`
	BytesTotal    int64                `json:"bytes_total,omitempty"`
	Log           []updateProgressLine `json:"log,omitempty"`
}

type helperUpdateProgressRequest struct {
	ServerID      any    `json:"server_id"`
	HelperVersion string `json:"helper_version"`
	Component     string `json:"component"`
	Phase         string `json:"phase"`
	TargetVersion string `json:"target_version,omitempty"`
	Message       string `json:"message,omitempty"`
	Percent       *int   `json:"percent,omitempty"`
	BytesDone     int64  `json:"bytes_done,omitempty"`
	BytesTotal    int64  `json:"bytes_total,omitempty"`
	LogLine       string `json:"log_line,omitempty"`
}

var allowedUpdatePhases = map[string]struct{}{
	"dispatching": {}, "downloading": {}, "verifying": {}, "installing": {}, "restarting": {},
	"reconnecting": {}, "success": {}, "failed": {}, "rolled_back": {},
}

var updatePhaseText = map[string]string{
	"dispatching": "已下发升级任务", "downloading": "正在下载", "verifying": "正在校验",
	"installing": "正在安装", "restarting": "正在重启", "reconnecting": "等待重新连接",
	"success": "升级成功", "failed": "升级失败", "rolled_back": "升级失败，已回滚",
}

var updatePrivateText = regexp.MustCompile(`(?i)(?:https?|ftp)://\S+|\b(?:authorization|bearer|token|password|secret|api[_-]?key)\s*[:=]?\s*\S+|\b(?:[a-z0-9_-]+\.)+[a-z]{2,}\b|\b(?:\d{1,3}\.){3}\d{1,3}\b|[a-z0-9_+/=-]{32,}`)

func sanitizeUpdateText(value string) string {
	value = updatePrivateText.ReplaceAllString(value, "[已隐藏]")
	value = strings.Join(strings.Fields(value), " ")
	value = strings.ToValidUTF8(sanitizeCoreModeMessage(value), "")
	runes := []rune(value)
	if len(runes) > 200 {
		value = string(runes[:200])
	}
	return value
}

func terminalUpdatePhase(phase string) bool {
	return phase == "success" || phase == "failed" || phase == "rolled_back"
}

func (progress *componentUpdateProgress) appendLine(now time.Time, text string) {
	text = sanitizeUpdateText(text)
	if text == "" {
		return
	}
	lines := append([]updateProgressLine(nil), progress.Log...)
	lines = append(lines, updateProgressLine{At: now, Phase: progress.Phase, Text: text})
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	progress.Log = lines
}

func dispatchedUpdate(component, version string, now time.Time) *componentUpdateProgress {
	progress := &componentUpdateProgress{Component: component, Phase: "dispatching", TargetVersion: sanitizeUpdateText(version), StartedAt: now, UpdatedAt: now}
	progress.appendLine(now, "已下发升级任务 → "+progress.TargetVersion)
	return progress
}

func (s *helperState) recordUpdateProgress(serverID string, progress componentUpdateProgress, logLine string) error {
	return s.recordUpdateProgressAt(serverID, progress, logLine, time.Now().UTC())
}

func (s *helperState) recordUpdateProgressAt(serverID string, progress componentUpdateProgress, logLine string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.data.AgentStatuses[serverID]
	previous := status.Update
	progress.UpdatedAt = now
	progress.TargetVersion = sanitizeUpdateText(progress.TargetVersion)
	progress.Message = sanitizeUpdateText(progress.Message)
	phaseChanged := true
	if previous != nil && previous.Component == progress.Component && (progress.TargetVersion == "" || previous.TargetVersion == progress.TargetVersion) {
		// Late worker posts must not reopen a completed attempt. A real rollback
		// can still replace an optimistic version observation with failure.
		if terminalUpdatePhase(previous.Phase) && (!terminalUpdatePhase(progress.Phase) || (previous.Phase != "success" && progress.Phase == "success")) {
			return nil
		}
		progress.StartedAt = previous.StartedAt
		if progress.StartedAt.IsZero() {
			progress.StartedAt = previous.UpdatedAt
		}
		progress.Log = previous.Log
		if progress.TargetVersion == "" {
			progress.TargetVersion = previous.TargetVersion
		}
		phaseChanged = previous.Phase != progress.Phase
		if !phaseChanged && progress.Percent == nil {
			progress.Percent, progress.BytesDone, progress.BytesTotal = previous.Percent, previous.BytesDone, previous.BytesTotal
		}
	}
	if progress.StartedAt.IsZero() {
		progress.StartedAt = now
	}
	if phaseChanged {
		progress.appendLine(now, updatePhaseText[progress.Phase])
	}
	progress.appendLine(now, progress.Message)
	if logLine != progress.Message {
		progress.appendLine(now, logLine)
	}
	status.Update = &progress
	s.data.AgentStatuses[serverID] = status
	s.updateProgressDirty = true
	if now.Sub(s.updateProgressSavedAt) < 5*time.Second {
		return nil
	}
	if err := s.saveLocked(); err != nil {
		status.Update = previous
		s.data.AgentStatuses[serverID] = status
		return err
	}
	s.updateProgressSavedAt = now
	s.updateProgressDirty = false
	return nil
}

func (a *app) helperUpdateProgressHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	var request helperUpdateProgressRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, updateProgressRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid request"})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid request"})
		return
	}
	reporterID, ok := normalizeHelperServerID(request.ServerID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid server_id"})
		return
	}
	// Progress owns the persistence cadence; authentication must not save on
	// every download sample before recordUpdateProgress applies its throttle.
	officialID, _, authorized := a.authorizedHelper(r, reporterID, strings.TrimSpace(request.HelperVersion), false)
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
	if (request.Percent != nil && (*request.Percent < 0 || *request.Percent > 100)) || request.BytesDone < 0 || request.BytesTotal < 0 || request.BytesDone > 1<<53-1 || request.BytesTotal > 1<<53-1 || (request.BytesTotal > 0 && request.BytesDone > request.BytesTotal) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid download progress"})
		return
	}
	if token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); token != "" {
		request.Message = strings.ReplaceAll(request.Message, token, "[已隐藏]")
		request.LogLine = strings.ReplaceAll(request.LogLine, token, "[已隐藏]")
	}
	progress := componentUpdateProgress{Component: component, Phase: phase, TargetVersion: strings.TrimSpace(request.TargetVersion), Message: request.Message, Percent: request.Percent, BytesDone: request.BytesDone, BytesTotal: request.BytesTotal}
	if err := a.helperState.recordUpdateProgress(officialID, progress, request.LogLine); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to record update progress"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// The batch scheduler also flushes a final progress post when no further reports arrive.
func (s *helperState) flushUpdateProgressLocked(now time.Time) error {
	if !s.updateProgressDirty || now.Sub(s.updateProgressSavedAt) < 5*time.Second {
		return nil
	}
	if err := s.saveLocked(); err != nil {
		return err
	}
	s.updateProgressSavedAt, s.updateProgressDirty = now, false
	return nil
}
