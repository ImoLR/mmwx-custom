package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultHelperStatePath       = "/etc/mmwx-custom/helper-state.json"
	defaultHelperInstallTokenTTL = 30 * time.Minute
	maxInstallTokenRequestBytes  = 4096
)

type helperState struct {
	path string
	ttl  time.Duration

	mu   sync.Mutex
	data helperStateData
}

type helperStateData struct {
	Servers            map[string]helperServerIdentity     `json:"servers"`
	InstallTokens      map[string]helperInstallToken       `json:"install_tokens"`
	ConnectionSettings map[string]serverConnectionSettings `json:"connection_settings,omitempty"`
	ManagementCommands map[string][]managementCommand      `json:"management_commands,omitempty"`
	ManagementResults  map[string][]managementResult       `json:"management_results,omitempty"`
	AgentStatuses      map[string]agentStatus              `json:"agent_statuses,omitempty"`
	CoreModeIntents    map[string]coreModeIntent           `json:"core_mode_intents,omitempty"`
	RebindTokens       map[string]helperRebindToken        `json:"rebind_tokens,omitempty"`
}

type helperServerIdentity struct {
	OfficialServerID  string    `json:"official_remote_server_id"`
	CustomServerUUID  string    `json:"custom_server_uuid"`
	HelperTokenHash   string    `json:"helper_token_hash"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	LastSeenAt        time.Time `json:"last_seen_at,omitempty"`
	LastHelperVersion string    `json:"last_helper_version,omitempty"`
	MachineID         string    `json:"machine_id,omitempty"`
}

type helperInstallToken struct {
	TokenHash        string    `json:"token_hash"`
	OfficialServerID string    `json:"official_remote_server_id"`
	CustomServerUUID string    `json:"custom_server_uuid"`
	HelperToken      string    `json:"helper_token,omitempty"`
	HelperTokenHash  string    `json:"helper_token_hash"`
	CreatedAt        time.Time `json:"created_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	PreserveExisting bool      `json:"preserve_existing,omitempty"`
	InstallMode      string    `json:"install_mode"`
}

type helperRebindToken struct {
	OfficialServerID string    `json:"official_remote_server_id"`
	ExpiresAt        time.Time `json:"expires_at"`
}

type helperRebindRequest struct {
	InstallToken string `json:"install_token"`
	MachineID    string `json:"machine_id"`
	HelperToken  string `json:"helper_token"`
}

type helperInstallTokenRequest struct {
	ServerID any    `json:"server_id"`
	Mode     string `json:"mode,omitempty"`
}

type helperInstallTokenResponse struct {
	Success    bool   `json:"success"`
	ServerID   string `json:"server_id"`
	ServerUUID string `json:"custom_server_uuid"`
	InstallURL string `json:"install_url"`
	ExpiresAt  string `json:"expires_at"`
	Command    string `json:"command"`
}

func openHelperState(statePath string, ttl time.Duration) (*helperState, error) {
	statePath = strings.TrimSpace(statePath)
	if statePath == "" {
		statePath = defaultHelperStatePath
	}
	if ttl <= 0 {
		ttl = defaultHelperInstallTokenTTL
	}
	store := &helperState{path: statePath, ttl: ttl}
	store.data.Servers = make(map[string]helperServerIdentity)
	store.data.InstallTokens = make(map[string]helperInstallToken)
	store.data.ConnectionSettings = make(map[string]serverConnectionSettings)
	store.data.ManagementCommands = make(map[string][]managementCommand)
	store.data.ManagementResults = make(map[string][]managementResult)
	store.data.AgentStatuses = make(map[string]agentStatus)
	store.data.CoreModeIntents = make(map[string]coreModeIntent)
	store.data.RebindTokens = make(map[string]helperRebindToken)
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *helperState) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, &s.data); err != nil {
		return err
	}
	if s.data.Servers == nil {
		s.data.Servers = make(map[string]helperServerIdentity)
	}
	if s.data.InstallTokens == nil {
		s.data.InstallTokens = make(map[string]helperInstallToken)
	}
	if s.data.ConnectionSettings == nil {
		s.data.ConnectionSettings = make(map[string]serverConnectionSettings)
	}
	if s.data.ManagementCommands == nil {
		s.data.ManagementCommands = make(map[string][]managementCommand)
	}
	if s.data.ManagementResults == nil {
		s.data.ManagementResults = make(map[string][]managementResult)
	}
	if s.data.AgentStatuses == nil {
		s.data.AgentStatuses = make(map[string]agentStatus)
	}
	if s.data.CoreModeIntents == nil {
		s.data.CoreModeIntents = make(map[string]coreModeIntent)
	}
	if s.data.RebindTokens == nil {
		s.data.RebindTokens = make(map[string]helperRebindToken)
	}
	return nil
}

func (s *helperState) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *helperState) createInstallToken(officialServerID string) (helperInstallToken, string, error) {
	return s.createInstallTokenForMode(officialServerID, "takeover")
}

func (s *helperState) createInstallTokenForMode(officialServerID, installMode string) (helperInstallToken, string, error) {
	if installMode == "" {
		installMode = "takeover"
	}
	if installMode != "takeover" && installMode != "helper-only" {
		return helperInstallToken{}, "", errors.New("invalid install mode")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)

	identity, ok := s.data.Servers[officialServerID]
	if !ok {
		uuid, err := randomUUID()
		if err != nil {
			return helperInstallToken{}, "", err
		}
		identity = helperServerIdentity{
			OfficialServerID: officialServerID,
			CustomServerUUID: uuid,
			MachineID:        uuid,
			CreatedAt:        now,
		}
	}

	helperToken := ""
	preserveExisting := ok && identity.HelperTokenHash != ""
	if !preserveExisting {
		var err error
		helperToken, err = randomSecret(32)
		if err != nil {
			return helperInstallToken{}, "", err
		}
	}
	identity.UpdatedAt = now
	s.data.Servers[officialServerID] = identity
	if _, exists := s.data.CoreModeIntents[officialServerID]; !exists {
		if installMode == "takeover" {
			s.data.CoreModeIntents[officialServerID] = newCoreModeIntent("external", now)
		} else {
			s.data.CoreModeIntents[officialServerID] = newCoreModeIntent("embedded", now)
		}
	}

	installToken, err := randomSecret(32)
	if err != nil {
		return helperInstallToken{}, "", err
	}
	record := helperInstallToken{
		TokenHash:        hashSecret(installToken),
		OfficialServerID: officialServerID,
		CustomServerUUID: identity.CustomServerUUID,
		HelperToken:      helperToken,
		HelperTokenHash:  identity.HelperTokenHash,
		CreatedAt:        now,
		ExpiresAt:        now.Add(s.ttl),
		PreserveExisting: preserveExisting,
		InstallMode:      installMode,
	}
	if !preserveExisting {
		record.HelperTokenHash = hashSecret(helperToken)
	}
	s.data.InstallTokens[record.TokenHash] = record
	if err := s.saveLocked(); err != nil {
		return helperInstallToken{}, "", err
	}
	return record, installToken, nil
}

func (s *helperState) consumeInstallToken(rawToken string) (helperInstallToken, bool, error) {
	now := time.Now().UTC()
	tokenHash := hashSecret(rawToken)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	record, ok := s.data.InstallTokens[tokenHash]
	if !ok || now.After(record.ExpiresAt) || (!record.PreserveExisting && record.HelperToken == "") {
		if ok {
			delete(s.data.InstallTokens, tokenHash)
			_ = s.saveLocked()
		}
		return helperInstallToken{}, false, nil
	}
	identity := s.data.Servers[record.OfficialServerID]
	identity.OfficialServerID = record.OfficialServerID
	identity.CustomServerUUID = record.CustomServerUUID
	if identity.CreatedAt.IsZero() {
		identity.CreatedAt = record.CreatedAt
	}
	if !record.PreserveExisting {
		identity.HelperTokenHash = record.HelperTokenHash
	}
	identity.UpdatedAt = now
	s.data.Servers[record.OfficialServerID] = identity
	s.data.RebindTokens[tokenHash] = helperRebindToken{OfficialServerID: record.OfficialServerID, ExpiresAt: record.ExpiresAt}
	delete(s.data.InstallTokens, tokenHash)
	if err := s.saveLocked(); err != nil {
		return helperInstallToken{}, false, err
	}
	return record, true, nil
}

func (s *helperState) authorizeReporter(reportedID, token, version string) (string, string, bool) {
	if reportedID == "" || token == "" {
		return "", "", false
	}
	tokenHash := hashSecret(token)
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for officialID, identity := range s.data.Servers {
		if identity.CustomServerUUID == reportedID && identity.HelperTokenHash == tokenHash {
			identity.LastSeenAt = now
			identity.LastHelperVersion = strings.TrimSpace(version)
			identity.UpdatedAt = now
			s.data.Servers[officialID] = identity
			_ = s.saveLocked()
			return officialID, identity.CustomServerUUID, true
		}
	}
	return "", "", false
}

func (s *helperState) recordLegacyReporter(officialServerID, token, version string) string {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, ok := s.data.Servers[officialServerID]
	if !ok {
		uuid, err := randomUUID()
		if err != nil {
			return ""
		}
		identity = helperServerIdentity{
			OfficialServerID: officialServerID,
			CustomServerUUID: uuid,
			CreatedAt:        now,
		}
	}
	identity.LastSeenAt = now
	identity.LastHelperVersion = strings.TrimSpace(version)
	identity.HelperTokenHash = hashSecret(token)
	identity.UpdatedAt = now
	s.data.Servers[officialServerID] = identity
	_ = s.saveLocked()
	return identity.CustomServerUUID
}

func (s *helperState) metadataSnapshot() map[string]helperServerIdentity {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]helperServerIdentity, len(s.data.Servers))
	for k, v := range s.data.Servers {
		out[k] = v
	}
	return out
}

func (s *helperState) connectionSettings(officialServerID string) serverConnectionSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings, ok := s.data.ConnectionSettings[officialServerID]
	if !ok {
		return defaultServerConnectionSettings()
	}
	return cloneServerConnectionSettings(settings)
}

func (s *helperState) setConnectionSettings(officialServerID string, settings serverConnectionSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.ConnectionSettings[officialServerID] = cloneServerConnectionSettings(settings)
	return s.saveLocked()
}

func (s *helperState) pruneExpiredLocked(now time.Time) {
	for key, record := range s.data.InstallTokens {
		if now.After(record.ExpiresAt) {
			delete(s.data.InstallTokens, key)
		}
	}
	for key, record := range s.data.RebindTokens {
		if now.After(record.ExpiresAt) {
			delete(s.data.RebindTokens, key)
		}
	}
}

func (s *helperState) rebindMachine(rawInstallToken, machineID, helperToken string) (string, string, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	key := hashSecret(strings.TrimSpace(rawInstallToken))
	rebind, ok := s.data.RebindTokens[key]
	if !ok || now.After(rebind.ExpiresAt) {
		return "", "", errors.New("invalid or expired rebind token")
	}
	machineID = strings.TrimSpace(machineID)
	helperHash := hashSecret(strings.TrimSpace(helperToken))
	sourceID := ""
	var identity helperServerIdentity
	for officialID, candidate := range s.data.Servers {
		if (candidate.MachineID == machineID || candidate.CustomServerUUID == machineID) && candidate.HelperTokenHash == helperHash {
			sourceID, identity = officialID, candidate
			break
		}
	}
	if sourceID == "" {
		return "", "", errors.New("existing machine identity is not authorized")
	}
	targetID := rebind.OfficialServerID
	identity.OfficialServerID = targetID
	identity.MachineID = machineID
	identity.UpdatedAt = now
	s.data.Servers[targetID] = identity
	if sourceID != targetID {
		delete(s.data.Servers, sourceID)
		if settings, exists := s.data.ConnectionSettings[sourceID]; exists {
			s.data.ConnectionSettings[targetID] = settings
			delete(s.data.ConnectionSettings, sourceID)
		}
		if status, exists := s.data.AgentStatuses[sourceID]; exists {
			s.data.AgentStatuses[targetID] = status
			delete(s.data.AgentStatuses, sourceID)
		}
		if results, exists := s.data.ManagementResults[sourceID]; exists {
			s.data.ManagementResults[targetID] = results
			delete(s.data.ManagementResults, sourceID)
		}
		delete(s.data.ManagementCommands, sourceID)
		if intent, exists := s.data.CoreModeIntents[sourceID]; exists {
			intent.MachineID = machineID
			intent.UpdatedAt = now
			s.data.CoreModeIntents[targetID] = intent
			delete(s.data.CoreModeIntents, sourceID)
		}
	}
	if intent, exists := s.data.CoreModeIntents[targetID]; exists {
		intent.MachineID = machineID
		intent.UpdatedAt = now
		s.data.CoreModeIntents[targetID] = intent
	}
	delete(s.data.RebindTokens, key)
	if err := s.saveLocked(); err != nil {
		return "", "", err
	}
	return targetID, identity.CustomServerUUID, nil
}

func (a *app) createHelperInstallTokenHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	var req helperInstallTokenRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxInstallTokenRequestBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid json"})
		return
	}
	serverID, ok := normalizeHelperServerID(req.ServerID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid server_id"})
		return
	}
	installMode := strings.TrimSpace(req.Mode)
	if installMode == "" {
		installMode = "takeover"
	}
	if installMode != "takeover" && installMode != "helper-only" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid install mode"})
		return
	}
	if err := a.authorizeOperatorServerRequest(r, serverID); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	record, installToken, err := a.helperState.createInstallTokenForMode(serverID, installMode)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to create install token"})
		return
	}
	installURL := a.externalBaseURL(r) + "/api/custom/helper/install/" + url.PathEscape(installToken)
	quotedInstallURL := shellSingleQuote(installURL)
	writeJSON(w, http.StatusOK, helperInstallTokenResponse{
		Success:    true,
		ServerID:   serverID,
		ServerUUID: record.CustomServerUUID,
		InstallURL: installURL,
		ExpiresAt:  record.ExpiresAt.Format(time.RFC3339),
		Command:    `(install_script="$(mktemp)" && trap 'rm -f "$install_script"' EXIT && curl --fail --show-error --silent --location --retry 3 --output "$install_script" ` + quotedInstallURL + ` && test -s "$install_script" && bash "$install_script")`,
	})
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func (a *app) helperInstallScriptHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	rawToken := strings.TrimPrefix(r.URL.Path, "/api/custom/helper/install/")
	rawToken, _ = url.PathUnescape(strings.TrimSpace(rawToken))
	if rawToken == "" || strings.Contains(rawToken, "/") {
		http.NotFound(w, r)
		return
	}
	record, ok, err := a.helperState.consumeInstallToken(rawToken)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to consume install token"})
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, renderHelperInstaller(a.externalBaseURL(r), record.CustomServerUUID, record.HelperToken, record.InstallMode, rawToken))
}

func (a *app) helperRebindHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	var request helperRebindRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxInstallTokenRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid request"})
		return
	}
	officialID, machineID, err := a.helperState.rebindMachine(request.InstallToken, request.MachineID, request.HelperToken)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "server_id": officialID, "machine_id": machineID})
}

func (a *app) externalBaseURL(r *http.Request) string {
	if a.publicURL != "" {
		return a.publicURL
	}
	proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	return strings.TrimRight(proto+"://"+host, "/")
}

//go:embed scripts/install-helper.sh
var helperInstallerScript string

func renderHelperInstaller(apiURL, serverUUID, helperToken, installMode, rebindToken string) string {
	prefix := fmt.Sprintf("#!/usr/bin/env bash\nexport MMWXC_HELPER_API_URL=%q\nexport MMWXC_HELPER_SERVER_ID=%q\nexport MMWXC_HELPER_TOKEN=%q\nexport MMWXC_INSTALL_MODE=%q\nexport MMWXC_REBIND_TOKEN=%q\n", apiURL, serverUUID, helperToken, installMode, rebindToken)
	return prefix + strings.TrimPrefix(helperInstallerScript, "#!/usr/bin/env bash\n")
}

func randomSecret(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func randomUUID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

func hashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func helperInstallTokenTTLFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("MMWXC_HELPER_INSTALL_TOKEN_TTL"))
	if raw == "" {
		return defaultHelperInstallTokenTTL
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return defaultHelperInstallTokenTTL
}
