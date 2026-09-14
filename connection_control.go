package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxDetailedMetricsBytes = 4 << 20

type serverConnectionIdentity struct {
	InboundTag string `json:"inbound_tag"`
	User       string `json:"user"`
}

type serverUserConnectionSettings struct {
	Identity                   serverConnectionIdentity `json:"identity"`
	MaxInboundOnlineIPs        *int                     `json:"max_inbound_online_ips"`
	MaxTotalConnections        *int64                   `json:"max_total_connections"`
	MaxOutboundTCPActive       *int64                   `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int                     `json:"max_outbound_tcp_new_per_second"`
	CloseWaitTimeoutSeconds    *int64                   `json:"close_wait_timeout_seconds"`
}

type serverConnectionSettings struct {
	DefaultCloseWaitTimeoutSeconds *int64                         `json:"default_close_wait_timeout_seconds"`
	OnlineIPGracePeriodSeconds     int64                          `json:"online_ip_grace_period_seconds"`
	GlobalTotalLimitEnabled        bool                           `json:"global_total_limit_enabled"`
	MaxGlobalTotalConnections      *int64                         `json:"max_global_total_connections"`
	Users                          []serverUserConnectionSettings `json:"users"`
}

type serverTCPStateCounts struct {
	Total       int64 `json:"tcp_total"`
	Established int64 `json:"established"`
	SynSent     int64 `json:"syn_sent"`
	SynRecv     int64 `json:"syn_recv"`
	FinWait1    int64 `json:"fin_wait_1"`
	FinWait2    int64 `json:"fin_wait_2"`
	TimeWait    int64 `json:"time_wait"`
	CloseWait   int64 `json:"close_wait"`
	LastAck     int64 `json:"last_ack"`
	Closing     int64 `json:"closing"`
	Close       int64 `json:"close"`
	Unknown     int64 `json:"unknown"`
}

type serverOnlineIP struct {
	IP          string `json:"ip"`
	Connections int64  `json:"connections"`
}

type serverInboundConnections struct {
	Port          uint32               `json:"port"`
	InboundTag    string               `json:"inbound_tag"`
	Protocol      string               `json:"protocol,omitempty"`
	User          string               `json:"user,omitempty"`
	Attribution   string               `json:"attribution"`
	TCP           serverTCPStateCounts `json:"tcp"`
	Established   int64                `json:"established"`
	SynRecv       int64                `json:"syn_recv"`
	FinWait1      int64                `json:"fin_wait_1"`
	FinWait2      int64                `json:"fin_wait_2"`
	TimeWait      int64                `json:"time_wait"`
	CloseWait     int64                `json:"close_wait"`
	LastAck       int64                `json:"last_ack"`
	Closing       int64                `json:"closing"`
	OnlineIPCount int                  `json:"online_ip_count"`
	OnlineIPs     []serverOnlineIP     `json:"online_ips"`
	MaxOnlineIPs  *int                 `json:"max_online_ips"`
}

type serverProxyUserConnections struct {
	Identity                   serverConnectionIdentity `json:"identity"`
	InboundTag                 string                   `json:"inbound_tag"`
	User                       string                   `json:"user"`
	InboundPort                uint32                   `json:"inbound_port,omitempty"`
	CurrentTotal               int64                    `json:"current_total"`
	InboundActive              int64                    `json:"inbound_active"`
	InboundTCP                 serverTCPStateCounts     `json:"inbound_tcp"`
	InboundOnlineIPs           []serverOnlineIP         `json:"inbound_online_ips"`
	OutboundActive             int64                    `json:"outbound_active"`
	OutboundPending            int64                    `json:"outbound_pending"`
	OutboundTCP                serverTCPStateCounts     `json:"outbound_tcp"`
	OutboundNewRate            int                      `json:"outbound_new_rate"`
	OutboundNewTotal           uint64                   `json:"outbound_new_total"`
	OutboundRejectedTotal      uint64                   `json:"outbound_rejected_total"`
	RejectedActiveLimit        uint64                   `json:"rejected_active_limit"`
	RejectedNewRateLimit       uint64                   `json:"rejected_new_rate_limit"`
	RejectedUserTotalLimit     uint64                   `json:"rejected_user_total_limit"`
	RejectedOnlineIPLimit      uint64                   `json:"rejected_online_ip_limit"`
	RejectedGlobalTotalLimit   uint64                   `json:"rejected_global_total_limit"`
	MaxInboundOnlineIPs        *int                     `json:"max_inbound_online_ips"`
	MaxTotalConnections        *int64                   `json:"max_total_connections"`
	MaxOutboundTCPActive       *int64                   `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int                     `json:"max_outbound_tcp_new_per_second"`
	CloseWaitTimeoutSeconds    *int64                   `json:"close_wait_timeout_seconds"`
	Source                     string                   `json:"source"`
}

type serverGlobalConnections struct {
	CurrentTotal             int64  `json:"current_total"`
	RejectedGlobalTotalLimit uint64 `json:"rejected_global_total_limit"`
	MaxTotal                 *int64 `json:"max_total"`
}

type serverCoreConnectionStatus struct {
	Available bool      `json:"available"`
	Version   int       `json:"interface_version,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type serverDetailedConnectionSnapshot struct {
	System     serverTCPStateCounts         `json:"system"`
	Inbounds   []serverInboundConnections   `json:"inbounds"`
	ProxyUsers []serverProxyUserConnections `json:"proxy_users"`
	Global     serverGlobalConnections      `json:"global"`
	Core       serverCoreConnectionStatus   `json:"core"`
	SampledAt  time.Time                    `json:"sampled_at"`
}

type helperDetailedMetricsRequest struct {
	ServerID        any                              `json:"server_id"`
	HelperVersion   string                           `json:"helper_version"`
	TCPCount        int64                            `json:"tcp_count"`
	UDPCount        int64                            `json:"udp_count"`
	ConnectionCount int64                            `json:"connection_count"`
	Snapshot        serverDetailedConnectionSnapshot `json:"snapshot"`
	Management      *managementReport                `json:"management,omitempty"`
}

type serverDetailedConnectionRecord struct {
	ServerID      string                           `json:"server_id"`
	ServerUUID    string                           `json:"custom_server_uuid,omitempty"`
	HelperVersion string                           `json:"helper_version"`
	Snapshot      serverDetailedConnectionSnapshot `json:"snapshot"`
	UpdatedAt     time.Time                        `json:"updated_at"`
}

func defaultServerConnectionSettings() serverConnectionSettings {
	return serverConnectionSettings{OnlineIPGracePeriodSeconds: 30, Users: []serverUserConnectionSettings{}}
}

func cloneServerConnectionSettings(settings serverConnectionSettings) serverConnectionSettings {
	data, _ := json.Marshal(settings)
	var clone serverConnectionSettings
	_ = json.Unmarshal(data, &clone)
	if clone.OnlineIPGracePeriodSeconds <= 0 {
		clone.OnlineIPGracePeriodSeconds = 30
	}
	if clone.Users == nil {
		clone.Users = []serverUserConnectionSettings{}
	}
	return clone
}

func validateServerConnectionSettings(settings serverConnectionSettings) error {
	if settings.OnlineIPGracePeriodSeconds < 1 || settings.OnlineIPGracePeriodSeconds > 3600 {
		return errors.New("online_ip_grace_period_seconds must be between 1 and 3600")
	}
	if settings.DefaultCloseWaitTimeoutSeconds != nil && *settings.DefaultCloseWaitTimeoutSeconds < 0 {
		return errors.New("default_close_wait_timeout_seconds must be non-negative")
	}
	if settings.MaxGlobalTotalConnections != nil && *settings.MaxGlobalTotalConnections <= 0 {
		return errors.New("max_global_total_connections must be positive when set")
	}
	if settings.GlobalTotalLimitEnabled && settings.MaxGlobalTotalConnections == nil {
		return errors.New("max_global_total_connections is required when the global limit is enabled")
	}
	seen := make(map[serverConnectionIdentity]struct{}, len(settings.Users))
	for _, user := range settings.Users {
		if strings.TrimSpace(user.Identity.InboundTag) == "" || strings.TrimSpace(user.Identity.User) == "" {
			return errors.New("every user setting requires inbound_tag and user")
		}
		if _, exists := seen[user.Identity]; exists {
			return fmt.Errorf("duplicate connection settings for %s/%s", user.Identity.InboundTag, user.Identity.User)
		}
		seen[user.Identity] = struct{}{}
		if user.MaxInboundOnlineIPs != nil && *user.MaxInboundOnlineIPs <= 0 {
			return errors.New("max_inbound_online_ips must be positive when set")
		}
		if user.MaxTotalConnections != nil && *user.MaxTotalConnections <= 0 {
			return errors.New("max_total_connections must be positive when set")
		}
		if user.MaxOutboundTCPActive != nil && *user.MaxOutboundTCPActive <= 0 {
			return errors.New("max_outbound_tcp_active must be positive when set")
		}
		if user.MaxOutboundTCPNewPerSecond != nil && *user.MaxOutboundTCPNewPerSecond <= 0 {
			return errors.New("max_outbound_tcp_new_per_second must be positive when set")
		}
		if user.CloseWaitTimeoutSeconds != nil && *user.CloseWaitTimeoutSeconds < 0 {
			return errors.New("close_wait_timeout_seconds must be non-negative")
		}
	}
	return nil
}

func (a *app) helperDetailedConnectionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	var request helperDetailedMetricsRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxDetailedMetricsBytes))
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid json"})
		return
	}
	serverID, ok := normalizeHelperServerID(request.ServerID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid server_id"})
		return
	}
	officialID, customUUID, authorized := a.authorizedHelper(r, serverID, request.HelperVersion)
	if !authorized {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "unauthorized"})
		return
	}
	if err := validateDetailedSnapshot(request.Snapshot); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": err.Error()})
		return
	}
	request.Snapshot = normalizeDetailedSnapshot(request.Snapshot)
	if request.TCPCount < 0 || request.UDPCount < 0 || request.ConnectionCount < 0 || request.TCPCount+request.UDPCount != request.ConnectionCount {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid aggregate metrics"})
		return
	}
	now := time.Now().UTC()
	a.connectionMu.Lock()
	if last, exists := a.helperRate[officialID]; exists && now.Sub(last) < minHelperReportGap {
		a.connectionMu.Unlock()
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"success": false, "message": "rate limited"})
		return
	}
	a.helperRate[officialID] = now
	a.detailedConnections[officialID] = serverDetailedConnectionRecord{
		ServerID: officialID, ServerUUID: customUUID, HelperVersion: strings.TrimSpace(request.HelperVersion), Snapshot: request.Snapshot, UpdatedAt: now,
	}
	a.connectionMetrics[officialID] = connectionMetrics{
		ServerID: officialID, ServerUUID: customUUID, TCPCount: request.TCPCount, UDPCount: request.UDPCount,
		ConnectionCount: request.ConnectionCount, SampledAt: request.Snapshot.SampledAt, UpdatedAt: now, HelperVersion: strings.TrimSpace(request.HelperVersion),
	}
	a.connectionMu.Unlock()
	command, err := a.helperState.acceptManagementReport(officialID, request.Management)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": err.Error()})
		return
	}
	if request.Management != nil {
		if store, ok := a.adminStore.(remoteServerModeStore); ok && store != nil {
			if runtime, runtimeErr := store.RemoteServerRuntime(r.Context(), officialID); runtimeErr == nil {
				autoCommand, reconcileErr := a.helperState.reconcileCoreMode(officialID, request.Management.Status, runtime, request.Management.Result)
				if reconcileErr != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "desired core mode reconciliation failed"})
					return
				}
				if command == nil && autoCommand != nil {
					command = autoCommand
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "settings": sortedConnectionSettings(a.helperState.connectionSettings(officialID)), "command": command})
}

func validateDetailedSnapshot(snapshot serverDetailedConnectionSnapshot) error {
	counts := tcpStateValues(snapshot.System)
	for _, count := range counts {
		if count < 0 {
			return errors.New("negative system TCP count")
		}
	}
	if len(snapshot.Inbounds) > 10000 || len(snapshot.ProxyUsers) > 10000 {
		return errors.New("too many connection records")
	}
	if snapshot.Global.CurrentTotal < 0 {
		return errors.New("negative global connection count")
	}
	for _, inbound := range snapshot.Inbounds {
		if err := validateTCPStateCounts(inbound.TCP, "inbound"); err != nil {
			return err
		}
		if inbound.OnlineIPCount < 0 {
			return errors.New("invalid inbound online IP count")
		}
		for _, item := range inbound.OnlineIPs {
			if strings.TrimSpace(item.IP) == "" || item.Connections < 0 {
				return errors.New("invalid inbound online IP record")
			}
		}
	}
	for _, user := range snapshot.ProxyUsers {
		if user.CurrentTotal < 0 || user.InboundActive < 0 || user.OutboundActive < 0 || user.OutboundPending < 0 || user.OutboundNewRate < 0 {
			return errors.New("negative proxy user connection count")
		}
		if err := validateTCPStateCounts(user.InboundTCP, "proxy user inbound"); err != nil {
			return err
		}
		if err := validateTCPStateCounts(user.OutboundTCP, "proxy user outbound"); err != nil {
			return err
		}
	}
	return nil
}

func tcpStateValues(counts serverTCPStateCounts) []int64 {
	return []int64{
		counts.Total, counts.Established, counts.SynSent, counts.SynRecv,
		counts.FinWait1, counts.FinWait2, counts.TimeWait, counts.CloseWait,
		counts.LastAck, counts.Closing, counts.Close, counts.Unknown,
	}
}

func validateTCPStateCounts(counts serverTCPStateCounts, label string) error {
	for _, count := range tcpStateValues(counts) {
		if count < 0 {
			return fmt.Errorf("negative %s TCP count", label)
		}
	}
	return nil
}

func normalizeDetailedSnapshot(snapshot serverDetailedConnectionSnapshot) serverDetailedConnectionSnapshot {
	if snapshot.Inbounds == nil {
		snapshot.Inbounds = []serverInboundConnections{}
	}
	if snapshot.ProxyUsers == nil {
		snapshot.ProxyUsers = []serverProxyUserConnections{}
	}
	for index := range snapshot.Inbounds {
		if snapshot.Inbounds[index].OnlineIPs == nil {
			snapshot.Inbounds[index].OnlineIPs = []serverOnlineIP{}
		}
	}
	for index := range snapshot.ProxyUsers {
		if snapshot.ProxyUsers[index].InboundOnlineIPs == nil {
			snapshot.ProxyUsers[index].InboundOnlineIPs = []serverOnlineIP{}
		}
	}
	return snapshot
}

func (a *app) serverConnectionsHandler(w http.ResponseWriter, r *http.Request) {
	if serverID, ok := parseCoreModePath(r.URL.Path); ok {
		a.coreModeHandler(w, r, serverID)
		return
	}
	if serverID, ok := parseAgentManagementPath(r.URL.Path); ok {
		a.agentManagementHandler(w, r, serverID)
		return
	}
	serverID, ok := parseServerConnectionsPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := a.authorizeOperatorServerRequest(r, serverID); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.writeServerConnections(w, serverID)
	case http.MethodPut:
		var settings serverConnectionSettings
		decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&settings); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid settings"})
			return
		}
		if err := validateServerConnectionSettings(settings); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": err.Error()})
			return
		}
		if err := a.helperState.setConnectionSettings(serverID, settings); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to persist settings"})
			return
		}
		a.writeServerConnections(w, serverID)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
	}
}

func (a *app) writeServerConnections(w http.ResponseWriter, serverID string) {
	a.connectionMu.Lock()
	record, exists := a.detailedConnections[serverID]
	a.connectionMu.Unlock()
	record.Snapshot = normalizeDetailedSnapshot(record.Snapshot)
	available := exists && time.Since(record.UpdatedAt) <= helperStaleTimeout
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "available": available, "stale_timeout_seconds": int(helperStaleTimeout.Seconds()),
		"record": record, "settings": sortedConnectionSettings(a.helperState.connectionSettings(serverID)),
	})
}

func parseServerConnectionsPath(value string) (string, bool) {
	const prefix = "/api/custom/servers/"
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, "/connections") {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(value, prefix), "/connections")
	if id == "" || strings.Contains(id, "/") {
		return "", false
	}
	parsed, err := strconv.ParseInt(id, 10, 64)
	if err != nil || parsed <= 0 {
		return "", false
	}
	return id, true
}

func sortedConnectionSettings(settings serverConnectionSettings) serverConnectionSettings {
	settings = cloneServerConnectionSettings(settings)
	sort.Slice(settings.Users, func(i, j int) bool {
		if settings.Users[i].Identity.InboundTag != settings.Users[j].Identity.InboundTag {
			return settings.Users[i].Identity.InboundTag < settings.Users[j].Identity.InboundTag
		}
		return settings.Users[i].Identity.User < settings.Users[j].Identity.User
	})
	return settings
}
