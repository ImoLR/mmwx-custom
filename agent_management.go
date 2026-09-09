package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	managementCommandTTL = 10 * time.Minute
	managementHistoryMax = 20
	managementQueueMax   = 16
	managementPayloadMax = 3 << 20
)

var managementActions = map[string]struct{}{
	"helper.status": {}, "helper.version": {}, "helper.update": {},
	"core.status": {}, "core.version": {}, "core.install": {}, "core.update": {},
	"core.restart": {}, "core.stop": {}, "core.rollback": {}, "core.config.apply": {},
	"official.xray.stop": {}, "official.xray.start": {},
	"connection.status": {}, "connection.settings": {},
}

type managementArtifact struct {
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Version  string `json:"version,omitempty"`
	Activate bool   `json:"activate,omitempty"`
}

type managementCommand struct {
	ID        string          `json:"id"`
	Action    string          `json:"action"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt time.Time       `json:"expires_at"`
	Signature string          `json:"signature"`
}

type managementResult struct {
	CommandID   string          `json:"command_id"`
	Action      string          `json:"action"`
	Success     bool            `json:"success"`
	Message     string          `json:"message,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	CompletedAt time.Time       `json:"completed_at"`
	Signature   string          `json:"signature"`
}

type componentStatus struct {
	Installed  bool   `json:"installed"`
	Prepared   bool   `json:"prepared"`
	Version    string `json:"version,omitempty"`
	BinaryPath string `json:"binary_path,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	Service    string `json:"service,omitempty"`
	Active     bool   `json:"active"`
	Ready      bool   `json:"ready"`
	Error      string `json:"error,omitempty"`
}

type agentStatus struct {
	Helper        componentStatus   `json:"helper"`
	Core          componentStatus   `json:"core"`
	RunUser       string            `json:"run_user,omitempty"`
	Architecture  string            `json:"architecture,omitempty"`
	Capabilities  []string          `json:"capabilities"`
	LastOperation *managementResult `json:"last_operation,omitempty"`
	ReportedAt    time.Time         `json:"reported_at"`
}

type managementReport struct {
	Status agentStatus       `json:"status"`
	Result *managementResult `json:"result,omitempty"`
}

type managementResponse struct {
	Command *managementCommand `json:"command,omitempty"`
}

type enqueueManagementRequest struct {
	Action  string          `json:"action"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func managementSigningKey(tokenHash string) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(tokenHash))
	if err != nil || len(key) != sha256.Size {
		return nil, errors.New("invalid helper signing key")
	}
	return key, nil
}

func signManagementCommand(command managementCommand, tokenHash string) (string, error) {
	key, err := managementSigningKey(tokenHash)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(commandSigningBytes(command))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func signManagementResult(result managementResult, tokenHash string) (string, error) {
	key, err := managementSigningKey(tokenHash)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(resultSigningBytes(result))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func commandSigningBytes(command managementCommand) []byte {
	payloadHash := sha256.Sum256(command.Payload)
	return []byte(fmt.Sprintf("%s\n%s\n%s\n%s\n%x", command.ID, command.Action, command.CreatedAt.UTC().Format(time.RFC3339Nano), command.ExpiresAt.UTC().Format(time.RFC3339Nano), payloadHash))
}

func resultSigningBytes(result managementResult) []byte {
	dataHash := sha256.Sum256(result.Data)
	return []byte(fmt.Sprintf("%s\n%s\n%t\n%s\n%s\n%x", result.CommandID, result.Action, result.Success, result.Message, result.CompletedAt.UTC().Format(time.RFC3339Nano), dataHash))
}

func (s *helperState) enqueueManagementCommand(serverID, action string, payload json.RawMessage) (managementCommand, error) {
	if _, ok := managementActions[action]; !ok {
		return managementCommand{}, errors.New("unsupported management action")
	}
	if err := validateManagementPayload(action, payload); err != nil {
		return managementCommand{}, err
	}
	now := time.Now().UTC()
	id, err := randomUUID()
	if err != nil {
		return managementCommand{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, ok := s.data.Servers[serverID]
	if !ok || identity.HelperTokenHash == "" {
		return managementCommand{}, errors.New("helper is not registered")
	}
	if len(s.data.ManagementCommands[serverID]) >= managementQueueMax {
		return managementCommand{}, errors.New("management queue is full")
	}
	command := managementCommand{ID: id, Action: action, Payload: append(json.RawMessage(nil), payload...), CreatedAt: now, ExpiresAt: now.Add(managementCommandTTL)}
	command.Signature, err = signManagementCommand(command, identity.HelperTokenHash)
	if err != nil {
		return managementCommand{}, err
	}
	s.data.ManagementCommands[serverID] = append(s.data.ManagementCommands[serverID], command)
	if err := s.saveLocked(); err != nil {
		return managementCommand{}, err
	}
	return command, nil
}

func (s *helperState) acceptManagementReport(serverID string, report *managementReport) (*managementCommand, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, ok := s.data.Servers[serverID]
	if !ok {
		return nil, errors.New("helper is not registered")
	}
	if report != nil {
		report.Status.ReportedAt = now
		report.Status.Capabilities = append([]string(nil), report.Status.Capabilities...)
		sort.Strings(report.Status.Capabilities)
		s.data.AgentStatuses[serverID] = report.Status
		if report.Result != nil {
			expected, err := signManagementResult(*report.Result, identity.HelperTokenHash)
			if err != nil || subtle.ConstantTimeCompare([]byte(expected), []byte(report.Result.Signature)) != 1 {
				queued := false
				for _, command := range s.data.ManagementCommands[serverID] {
					if command.ID == report.Result.CommandID {
						queued = true
						break
					}
				}
				if queued {
					return nil, errors.New("invalid management result signature")
				}
				report.Result = nil
			}
		}
		if report.Result != nil {
			commands := s.data.ManagementCommands[serverID]
			if len(commands) > 0 && commands[0].ID == report.Result.CommandID && commands[0].Action == report.Result.Action {
				s.data.ManagementCommands[serverID] = commands[1:]
			}
			history := append(s.data.ManagementResults[serverID], *report.Result)
			if len(history) > managementHistoryMax {
				history = history[len(history)-managementHistoryMax:]
			}
			s.data.ManagementResults[serverID] = history
		}
	}
	commands := s.data.ManagementCommands[serverID]
	for len(commands) > 0 && now.After(commands[0].ExpiresAt) {
		commands = commands[1:]
	}
	s.data.ManagementCommands[serverID] = commands
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	if len(commands) == 0 {
		return nil, nil
	}
	command := commands[0]
	return &command, nil
}

func validateManagementPayload(action string, payload json.RawMessage) error {
	if len(payload) > managementPayloadMax {
		return errors.New("management payload too large")
	}
	switch action {
	case "helper.status", "helper.version", "core.status", "core.version", "core.restart", "core.stop", "core.rollback", "official.xray.stop", "official.xray.start", "connection.status", "connection.settings":
		if len(strings.TrimSpace(string(payload))) > 0 && string(payload) != "{}" && string(payload) != "null" {
			return errors.New("action does not accept payload")
		}
	case "helper.update", "core.install", "core.update":
		var artifact managementArtifact
		if err := json.Unmarshal(payload, &artifact); err != nil {
			return errors.New("invalid artifact payload")
		}
		parsed, err := url.Parse(artifact.URL)
		if err != nil || !allowedArtifactSource(parsed) {
			return errors.New("artifact URL is not allowed")
		}
		if len(artifact.SHA256) != 64 {
			return errors.New("artifact sha256 is required")
		}
		if _, err := hex.DecodeString(artifact.SHA256); err != nil {
			return errors.New("artifact sha256 is invalid")
		}
	case "core.config.apply":
		var body struct {
			Config   json.RawMessage `json:"config"`
			Activate bool            `json:"activate,omitempty"`
		}
		if err := json.Unmarshal(payload, &body); err != nil || len(body.Config) == 0 || !json.Valid(body.Config) {
			return errors.New("valid Xray config is required")
		}
	}
	return nil
}

func allowedArtifactSource(parsed *url.URL) bool {
	return parsed.Scheme == "https" && strings.EqualFold(parsed.Hostname(), "github.com") && strings.HasPrefix(parsed.EscapedPath(), "/ImoLR/mmwx-custom/releases/")
}

func (a *app) agentManagementHandler(w http.ResponseWriter, r *http.Request, serverID string) {
	if err := a.authorizeOperatorServerRequest(r, serverID); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.helperState.mu.Lock()
		status := a.helperState.data.AgentStatuses[serverID]
		commands := append([]managementCommand(nil), a.helperState.data.ManagementCommands[serverID]...)
		results := append([]managementResult(nil), a.helperState.data.ManagementResults[serverID]...)
		a.helperState.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": status, "pending": commands, "results": results})
	case http.MethodPost:
		var request enqueueManagementRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, managementPayloadMax+4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid request"})
			return
		}
		command, err := a.helperState.enqueueManagementCommand(serverID, strings.TrimSpace(request.Action), request.Payload)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"success": true, "command": command})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
	}
}

func parseAgentManagementPath(value string) (string, bool) {
	const prefix, suffix = "/api/custom/servers/", "/agent"
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(value, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return "", false
	}
	for _, ch := range id {
		if ch < '0' || ch > '9' {
			return "", false
		}
	}
	return id, id != "0"
}
