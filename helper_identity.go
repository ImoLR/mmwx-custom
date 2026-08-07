package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
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
	Servers       map[string]helperServerIdentity `json:"servers"`
	InstallTokens map[string]helperInstallToken   `json:"install_tokens"`
}

type helperServerIdentity struct {
	OfficialServerID  string    `json:"official_remote_server_id"`
	CustomServerUUID  string    `json:"custom_server_uuid"`
	HelperTokenHash   string    `json:"helper_token_hash"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	LastSeenAt        time.Time `json:"last_seen_at,omitempty"`
	LastHelperVersion string    `json:"last_helper_version,omitempty"`
}

type helperInstallToken struct {
	TokenHash        string    `json:"token_hash"`
	OfficialServerID string    `json:"official_remote_server_id"`
	CustomServerUUID string    `json:"custom_server_uuid"`
	HelperToken      string    `json:"helper_token,omitempty"`
	HelperTokenHash  string    `json:"helper_token_hash"`
	CreatedAt        time.Time `json:"created_at"`
	ExpiresAt        time.Time `json:"expires_at"`
}

type helperInstallTokenRequest struct {
	ServerID any `json:"server_id"`
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
			CreatedAt:        now,
		}
	}

	helperToken, err := randomSecret(32)
	if err != nil {
		return helperInstallToken{}, "", err
	}
	identity.UpdatedAt = now
	s.data.Servers[officialServerID] = identity

	installToken, err := randomSecret(32)
	if err != nil {
		return helperInstallToken{}, "", err
	}
	record := helperInstallToken{
		TokenHash:        hashSecret(installToken),
		OfficialServerID: officialServerID,
		CustomServerUUID: identity.CustomServerUUID,
		HelperToken:      helperToken,
		HelperTokenHash:  hashSecret(helperToken),
		CreatedAt:        now,
		ExpiresAt:        now.Add(s.ttl),
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
	if !ok || now.After(record.ExpiresAt) || record.HelperToken == "" {
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
	identity.HelperTokenHash = record.HelperTokenHash
	identity.UpdatedAt = now
	s.data.Servers[record.OfficialServerID] = identity
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

func (s *helperState) recordLegacyReporter(officialServerID, version string) string {
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

func (s *helperState) pruneExpiredLocked(now time.Time) {
	for key, record := range s.data.InstallTokens {
		if now.After(record.ExpiresAt) {
			delete(s.data.InstallTokens, key)
		}
	}
}

func (a *app) createHelperInstallTokenHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	adminToken := strings.TrimSpace(r.Header.Get("MM-Authorization"))
	if adminToken == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "missing admin token"})
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
	if err := a.validateRemoteServer(r.Context(), adminToken, serverID); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errRemoteServerUnauthorized) {
			status = http.StatusUnauthorized
		} else if errors.Is(err, errRemoteServerNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"success": false, "message": err.Error()})
		return
	}
	record, installToken, err := a.helperState.createInstallToken(serverID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to create install token"})
		return
	}
	installURL := a.externalBaseURL(r) + "/api/custom/helper/install/" + url.PathEscape(installToken)
	writeJSON(w, http.StatusOK, helperInstallTokenResponse{
		Success:    true,
		ServerID:   serverID,
		ServerUUID: record.CustomServerUUID,
		InstallURL: installURL,
		ExpiresAt:  record.ExpiresAt.Format(time.RFC3339),
		Command:    "curl -fsSL '" + installURL + "' | bash",
	})
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
	_, _ = io.WriteString(w, renderHelperInstaller(a.externalBaseURL(r), record.CustomServerUUID, record.HelperToken))
}

var (
	errRemoteServerUnauthorized = errors.New("admin authorization failed")
	errRemoteServerNotFound     = errors.New("remote server not found")
)

func (a *app) validateRemoteServer(ctx context.Context, adminToken, serverID string) error {
	reqURL := *a.mmwxAPITarget
	reqURL.Path = path.Join(strings.TrimRight(reqURL.Path, "/"), "/api/admin/remote-servers")
	reqURL.RawQuery = ""
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("MM-Authorization", adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errRemoteServerUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("remote server validation failed: %d", resp.StatusCode)
	}
	var body struct {
		Servers []struct {
			ID int64 `json:"id"`
		} `json:"servers"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return err
	}
	for _, server := range body.Servers {
		if strconv.FormatInt(server.ID, 10) == serverID {
			return nil
		}
	}
	return errRemoteServerNotFound
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

func renderHelperInstaller(apiURL, serverUUID, helperToken string) string {
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

REPO="${MMWXC_HELPER_REPO:-ImoLR/mmwx-custom}"
API_URL=%q
SERVER_ID=%q
TOKEN=%q
INTERVAL="${MMWXC_HELPER_INTERVAL:-5s}"

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "mmwxc-helper only supports Linux" >&2
  exit 1
fi
if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  echo "please run as root" >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
url="https://github.com/${REPO}/releases/latest/download/mmwxc-helper-linux-${ARCH}"
echo "Downloading mmwxc-helper (${ARCH})..."
if command -v curl >/dev/null 2>&1; then
  curl -fsSL --connect-timeout 10 --max-time 180 -o "$tmp/mmwxc-helper" "$url"
elif command -v wget >/dev/null 2>&1; then
  wget -q --connect-timeout=10 --read-timeout=180 -O "$tmp/mmwxc-helper" "$url"
else
  echo "curl or wget is required" >&2
  exit 1
fi
chmod 755 "$tmp/mmwxc-helper"
"$tmp/mmwxc-helper" --version >/dev/null
install -m 0755 "$tmp/mmwxc-helper" /usr/local/bin/mmwxc-helper

umask 077
cat >/etc/mmwxc-helper.env <<EOF
MMWXC_HELPER_API_URL=${API_URL}
MMWXC_HELPER_SERVER_ID=${SERVER_ID}
MMWXC_HELPER_TOKEN=${TOKEN}
MMWXC_HELPER_INTERVAL=${INTERVAL}
EOF
chmod 600 /etc/mmwxc-helper.env

cat >/etc/systemd/system/mmwxc-helper.service <<'EOF'
[Unit]
Description=MMWXC Helper
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/mmwxc-helper.env
ExecStart=/usr/local/bin/mmwxc-helper
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now mmwxc-helper.service
systemctl is-active --quiet mmwxc-helper.service

echo "MMWXC Helper installed successfully"
echo "Service: $(systemctl is-active mmwxc-helper.service)"
echo "API: ${API_URL}"
echo "Version: $(/usr/local/bin/mmwxc-helper --version | awk '{print $2}')"
`, apiURL, serverUUID, helperToken)
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
