package main

import "time"

type tcpStateCounts struct {
	Total       int64 `json:"tcp_total"`
	Established int64 `json:"established"`
	TimeWait    int64 `json:"time_wait"`
	CloseWait   int64 `json:"close_wait"`
	SynSent     int64 `json:"syn_sent"`
	SynRecv     int64 `json:"syn_recv"`
}

type coreIdentity struct {
	InboundTag string `json:"inbound_tag"`
	User       string `json:"user"`
}

type coreUserSnapshot struct {
	Identity                   coreIdentity `json:"identity"`
	InboundName                string       `json:"inbound_name,omitempty"`
	InboundPort                uint32       `json:"inbound_port,omitempty"`
	OutboundTag                string       `json:"outbound_tag,omitempty"`
	Attributed                 bool         `json:"attributed"`
	InboundActive              int64        `json:"inbound_active"`
	InboundTotal               uint64       `json:"inbound_total"`
	OutboundActive             int64        `json:"outbound_active"`
	OutboundPending            int64        `json:"outbound_pending"`
	OutboundNewTotal           uint64       `json:"outbound_new_total"`
	OutboundNewRate            int          `json:"outbound_new_rate"`
	OutboundRejectedTotal      uint64       `json:"outbound_rejected_total"`
	RejectedActiveLimit        uint64       `json:"rejected_active_limit"`
	RejectedNewRateLimit       uint64       `json:"rejected_new_rate_limit"`
	MaxOutboundTCPActive       *int64       `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int         `json:"max_outbound_tcp_new_per_second"`
	CloseWaitTimeoutSeconds    *int64       `json:"close_wait_timeout_seconds"`
}

type coreSnapshotResponse struct {
	Version   int                `json:"version"`
	StartedAt time.Time          `json:"started_at"`
	Users     []coreUserSnapshot `json:"proxy_users"`
}

type userConnectionSettings struct {
	Identity                   coreIdentity `json:"identity"`
	MaxInboundOnlineIPs        *int         `json:"max_inbound_online_ips"`
	MaxOutboundTCPActive       *int64       `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int         `json:"max_outbound_tcp_new_per_second"`
	CloseWaitTimeoutSeconds    *int64       `json:"close_wait_timeout_seconds"`
}

type connectionSettings struct {
	DefaultCloseWaitTimeoutSeconds *int64                   `json:"default_close_wait_timeout_seconds"`
	OnlineIPGracePeriodSeconds     int64                    `json:"online_ip_grace_period_seconds"`
	Users                          []userConnectionSettings `json:"users"`
}

type coreConfig struct {
	DefaultCloseWaitTimeoutSeconds *int64      `json:"default_close_wait_timeout_seconds"`
	Limits                         []coreLimit `json:"limits"`
}

type coreLimit struct {
	Identity                   coreIdentity `json:"identity"`
	MaxOutboundTCPActive       *int64       `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int         `json:"max_outbound_tcp_new_per_second"`
	CloseWaitTimeoutSeconds    *int64       `json:"close_wait_timeout_seconds"`
}

type onlineIP struct {
	IP          string `json:"ip"`
	Connections int64  `json:"connections"`
}

type inboundSnapshot struct {
	Port          uint32     `json:"port"`
	InboundTag    string     `json:"inbound_tag"`
	Protocol      string     `json:"protocol,omitempty"`
	User          string     `json:"user,omitempty"`
	Attribution   string     `json:"attribution"`
	Established   int64      `json:"established"`
	TimeWait      int64      `json:"time_wait"`
	CloseWait     int64      `json:"close_wait"`
	OnlineIPCount int        `json:"online_ip_count"`
	OnlineIPs     []onlineIP `json:"online_ips"`
	MaxOnlineIPs  *int       `json:"max_online_ips"`
}

type proxyUserSnapshot struct {
	Identity                   coreIdentity `json:"identity"`
	InboundTag                 string       `json:"inbound_tag"`
	User                       string       `json:"user"`
	InboundPort                uint32       `json:"inbound_port,omitempty"`
	InboundActive              int64        `json:"inbound_active"`
	OutboundActive             int64        `json:"outbound_active"`
	OutboundNewRate            int          `json:"outbound_new_rate"`
	OutboundNewTotal           uint64       `json:"outbound_new_total"`
	OutboundRejectedTotal      uint64       `json:"outbound_rejected_total"`
	MaxInboundOnlineIPs        *int         `json:"max_inbound_online_ips"`
	MaxOutboundTCPActive       *int64       `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int         `json:"max_outbound_tcp_new_per_second"`
	CloseWaitTimeoutSeconds    *int64       `json:"close_wait_timeout_seconds"`
	Source                     string       `json:"source"`
}

type coreStatus struct {
	Available bool      `json:"available"`
	Version   int       `json:"interface_version,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type detailedConnectionSnapshot struct {
	System     tcpStateCounts      `json:"system"`
	Inbounds   []inboundSnapshot   `json:"inbounds"`
	ProxyUsers []proxyUserSnapshot `json:"proxy_users"`
	Core       coreStatus          `json:"core"`
	SampledAt  time.Time           `json:"sampled_at"`
}

type detailedMetricsPayload struct {
	ServerID        string                     `json:"server_id"`
	HelperVersion   string                     `json:"helper_version"`
	TCPCount        int64                      `json:"tcp_count"`
	UDPCount        int64                      `json:"udp_count"`
	ConnectionCount int64                      `json:"connection_count"`
	Snapshot        detailedConnectionSnapshot `json:"snapshot"`
	Management      *managementReport          `json:"management,omitempty"`
}

type detailedMetricsResponse struct {
	Success  bool               `json:"success"`
	Settings connectionSettings `json:"settings"`
	Command  *managementCommand `json:"command,omitempty"`
}

func defaultConnectionSettings() connectionSettings {
	return connectionSettings{OnlineIPGracePeriodSeconds: 30, Users: []userConnectionSettings{}}
}

func (settings connectionSettings) coreConfig() coreConfig {
	config := coreConfig{DefaultCloseWaitTimeoutSeconds: settings.DefaultCloseWaitTimeoutSeconds}
	for _, user := range settings.Users {
		config.Limits = append(config.Limits, coreLimit{
			Identity:                   user.Identity,
			MaxOutboundTCPActive:       user.MaxOutboundTCPActive,
			MaxOutboundTCPNewPerSecond: user.MaxOutboundTCPNewPerSecond,
			CloseWaitTimeoutSeconds:    user.CloseWaitTimeoutSeconds,
		})
	}
	return config
}
