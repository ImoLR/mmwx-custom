package main

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxHelperMetricsBytes = 4096

type connectionMetrics struct {
	ServerID        string    `json:"server_id"`
	ServerUUID      string    `json:"custom_server_uuid,omitempty"`
	TCPCount        int64     `json:"tcp_count"`
	UDPCount        int64     `json:"udp_count"`
	ConnectionCount int64     `json:"connection_count"`
	SampledAt       time.Time `json:"sampled_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	HelperVersion   string    `json:"helper_version,omitempty"`
}

type helperMetricsRequest struct {
	ServerID        any    `json:"server_id"`
	TCPCount        int64  `json:"tcp_count"`
	UDPCount        int64  `json:"udp_count"`
	ConnectionCount int64  `json:"connection_count"`
	SampledAt       string `json:"sampled_at"`
	HelperVersion   string `json:"helper_version"`
}

type connectionMetricsView struct {
	ServerID        string `json:"server_id"`
	ServerUUID      string `json:"custom_server_uuid,omitempty"`
	TCPCount        int64  `json:"tcp_count"`
	UDPCount        int64  `json:"udp_count"`
	ConnectionCount int64  `json:"connection_count"`
	SampledAt       string `json:"sampled_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
	HelperVersion   string `json:"helper_version,omitempty"`
	Available       bool   `json:"available"`
}

func parseHelperTokens(raw string) map[string]string {
	out := make(map[string]string)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		serverID, token, ok := strings.Cut(part, ":")
		serverID = strings.TrimSpace(serverID)
		token = strings.TrimSpace(token)
		if !ok || serverID == "" || token == "" {
			continue
		}
		out[serverID] = token
	}
	return out
}

func (a *app) connectionMetricsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.listConnectionMetrics(w, r)
	case http.MethodPost:
		a.ingestConnectionMetrics(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
	}
}

// helperUserConnectionsHandler exposes the live, identity-attributed inbound
// connection counts reported by Custom Core. The formal controller already
// supplies counts for Embedded Xray, so this endpoint deliberately includes
// only fresh External-mode Helper snapshots; callers can safely add both maps
// without double counting an Embedded server that also has Helper installed.
func (a *app) helperUserConnectionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	if err := a.authorizeOperatorRequest(r); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	store, ok := a.adminStore.(remoteServerModeStore)
	if !ok || store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "remote server runtime store unavailable"})
		return
	}

	a.connectionMu.Lock()
	records := make(map[string]serverDetailedConnectionRecord, len(a.detailedConnections))
	for serverID, record := range a.detailedConnections {
		records[serverID] = record
	}
	a.connectionMu.Unlock()

	counterStore, countersAvailable := a.adminStore.(userTrafficCounterStore)
	counters := []userTrafficCounter{}
	if countersAvailable {
		if loaded, err := counterStore.UserTrafficCounters(r.Context()); err == nil {
			counters = loaded
		} else {
			countersAvailable = false
		}
	}
	serverIDs := make(map[string]struct{}, len(records)+len(counters))
	for serverID := range records {
		serverIDs[serverID] = struct{}{}
	}
	for _, counter := range counters {
		serverIDs[counter.ServerID] = struct{}{}
	}
	external := make(map[string]bool, len(serverIDs))
	modes := make(map[string]string, len(serverIDs))
	for serverID := range serverIDs {
		runtime, err := store.RemoteServerRuntime(r.Context(), serverID)
		if err != nil {
			continue
		}
		modes[serverID] = runtime.XrayMode
		external[serverID] = runtime.XrayMode == "external"
	}
	now := time.Now().UTC()
	connections, serverConnections, availableServerIDs := aggregateHelperUserConnections(now, records, external)
	userRates := map[string]normalizedUserRate{}
	if countersAvailable {
		userRates = a.normalizedUserRates(r.Context(), now, counters, records, modes)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "connections": connections, "server_connections": serverConnections,
		"available_server_ids": availableServerIDs, "stale_timeout_seconds": int(helperStaleTimeout.Seconds()),
		"user_rates": userRates, "rate_available": countersAvailable, "rate_stale_timeout_seconds": int(userRateStaleTimeout.Seconds()),
	})
}

func aggregateHelperUserConnections(now time.Time, records map[string]serverDetailedConnectionRecord, external map[string]bool) (map[string]int64, map[string]map[string]int64, []string) {
	connections := make(map[string]int64)
	serverConnections := make(map[string]map[string]int64)
	availableServerIDs := make([]string, 0, len(records))
	for serverID, record := range records {
		if !external[serverID] || record.UpdatedAt.IsZero() || now.Sub(record.UpdatedAt) > helperStaleTimeout {
			continue
		}
		perServer := make(map[string]int64)
		for _, group := range record.Snapshot.ManagementGroups {
			username := strings.TrimSpace(group.Username)
			if username == "" {
				continue
			}
			count := group.InboundCurrent
			if record.Snapshot.Core.Version < 4 {
				count = group.InboundActive
			}
			if count <= 0 {
				continue
			}
			perServer[username] += count
			connections[username] += count
		}
		serverConnections[serverID] = perServer
		availableServerIDs = append(availableServerIDs, serverID)
	}
	sort.Strings(availableServerIDs)
	return connections, serverConnections, availableServerIDs
}

func (a *app) listConnectionMetrics(w http.ResponseWriter, r *http.Request) {
	if err := a.authorizeOperatorRequest(r); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	now := time.Now()
	metrics := make(map[string]connectionMetricsView)
	metadata := a.helperState.metadataSnapshot()

	a.connectionMu.Lock()
	for serverID, item := range a.connectionMetrics {
		available := now.Sub(item.UpdatedAt) <= helperStaleTimeout
		view := connectionMetricsView{
			ServerID:        serverID,
			ServerUUID:      item.ServerUUID,
			TCPCount:        item.TCPCount,
			UDPCount:        item.UDPCount,
			ConnectionCount: item.ConnectionCount,
			HelperVersion:   item.HelperVersion,
			Available:       available,
		}
		if view.ServerUUID == "" {
			view.ServerUUID = metadata[serverID].CustomServerUUID
		}
		if view.HelperVersion == "" {
			view.HelperVersion = metadata[serverID].LastHelperVersion
		}
		if !item.SampledAt.IsZero() {
			view.SampledAt = item.SampledAt.UTC().Format(time.RFC3339)
		}
		if !item.UpdatedAt.IsZero() {
			view.UpdatedAt = item.UpdatedAt.UTC().Format(time.RFC3339)
		}
		metrics[serverID] = view
	}
	a.connectionMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"success":               true,
		"stale_timeout_seconds": int(helperStaleTimeout.Seconds()),
		"metrics":               metrics,
	})
}

func (a *app) ingestConnectionMetrics(w http.ResponseWriter, r *http.Request) {
	if len(a.helperTokens) == 0 && a.helperState == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "helper metrics are not configured"})
		return
	}

	var req helperMetricsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxHelperMetricsBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid json"})
		return
	}
	serverID, ok := normalizeHelperServerID(req.ServerID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid server_id"})
		return
	}
	officialServerID, customServerUUID, authorized := a.authorizedHelper(r, serverID, req.HelperVersion)
	if !authorized {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "unauthorized"})
		return
	}
	if req.TCPCount < 0 || req.UDPCount < 0 || req.ConnectionCount < 0 || req.TCPCount+req.UDPCount != req.ConnectionCount {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid metrics"})
		return
	}

	now := time.Now()
	sampledAt := now
	if req.SampledAt != "" {
		if parsed, err := time.Parse(time.RFC3339, req.SampledAt); err == nil {
			sampledAt = parsed
		}
	}

	a.connectionMu.Lock()
	if last, ok := a.helperRate[officialServerID]; ok && now.Sub(last) < minHelperReportGap {
		a.connectionMu.Unlock()
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"success": false, "message": "rate limited"})
		return
	}
	a.helperRate[officialServerID] = now
	a.connectionMetrics[officialServerID] = connectionMetrics{
		ServerID:        officialServerID,
		ServerUUID:      customServerUUID,
		TCPCount:        req.TCPCount,
		UDPCount:        req.UDPCount,
		ConnectionCount: req.ConnectionCount,
		SampledAt:       sampledAt,
		UpdatedAt:       now,
		HelperVersion:   strings.TrimSpace(req.HelperVersion),
	}
	a.connectionMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func normalizeHelperServerID(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		id := strings.TrimSpace(v)
		return id, id != ""
	case float64:
		if v < 0 || v != float64(int64(v)) {
			return "", false
		}
		return strconv.FormatInt(int64(v), 10), true
	case int:
		if v < 0 {
			return "", false
		}
		return strconv.Itoa(v), true
	default:
		return "", false
	}
}

func (a *app) authorizedHelper(r *http.Request, serverID, version string) (string, string, bool) {
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, prefix) {
		return "", "", false
	}
	got := strings.TrimSpace(strings.TrimPrefix(value, prefix))
	if a.helperState != nil {
		if officialID, customUUID, ok := a.helperState.authorizeReporter(serverID, got, version); ok {
			return officialID, customUUID, true
		}
	}
	want, ok := a.helperTokens[serverID]
	if !ok || want == "" {
		return "", "", false
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
		customUUID := ""
		if a.helperState != nil {
			customUUID = a.helperState.recordLegacyReporter(serverID, got, version)
		}
		return serverID, customUUID, true
	}
	return "", "", false
}
