package main

import "time"

type tcpStateCounts struct {
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

type coreIdentity struct {
	InboundTag string `json:"inbound_tag"`
	User       string `json:"user"`
}

type coreUserSnapshot struct {
	Identity                   coreIdentity   `json:"identity"`
	InboundName                string         `json:"inbound_name,omitempty"`
	InboundPort                uint32         `json:"inbound_port,omitempty"`
	OutboundTag                string         `json:"outbound_tag,omitempty"`
	Attributed                 bool           `json:"attributed"`
	InboundActive              int64          `json:"inbound_active"`
	InboundTotal               uint64         `json:"inbound_total"`
	CurrentTotal               int64          `json:"current_total"`
	InboundTCP                 tcpStateCounts `json:"inbound_tcp"`
	InboundOnlineIPs           []onlineIP     `json:"inbound_online_ips"`
	OutboundActive             int64          `json:"outbound_active"`
	OutboundPending            int64          `json:"outbound_pending"`
	OutboundTCP                tcpStateCounts `json:"outbound_tcp"`
	OutboundNewTotal           uint64         `json:"outbound_new_total"`
	OutboundNewRate            int            `json:"outbound_new_rate"`
	OutboundRejectedTotal      uint64         `json:"outbound_rejected_total"`
	RejectedActiveLimit        uint64         `json:"rejected_active_limit"`
	RejectedNewRateLimit       uint64         `json:"rejected_new_rate_limit"`
	RejectedUserTotalLimit     uint64         `json:"rejected_user_total_limit"`
	RejectedOnlineIPLimit      uint64         `json:"rejected_online_ip_limit"`
	RejectedGlobalTotalLimit   uint64         `json:"rejected_global_total_limit"`
	MaxInboundOnlineIPs        *int           `json:"max_inbound_online_ips"`
	MaxTotalConnections        *int64         `json:"max_total_connections"`
	MaxOutboundTCPActive       *int64         `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int           `json:"max_outbound_tcp_new_per_second"`
	CloseWaitTimeoutSeconds    *int64         `json:"close_wait_timeout_seconds"`
}

type coreInboundSnapshot struct {
	InboundTag  string   `json:"inbound_tag"`
	InboundName string   `json:"inbound_name,omitempty"`
	InboundPort uint32   `json:"inbound_port"`
	Users       []string `json:"users"`
}

type coreSnapshotResponse struct {
	Version   int                   `json:"version"`
	StartedAt time.Time             `json:"started_at"`
	Global    coreGlobalSnapshot    `json:"global"`
	Inbounds  []coreInboundSnapshot `json:"inbounds"`
	Users     []coreUserSnapshot    `json:"proxy_users"`
}

type coreGlobalSnapshot struct {
	CurrentTotal             int64  `json:"current_total"`
	MaxTotal                 *int64 `json:"max_total"`
	RejectedGlobalTotalLimit uint64 `json:"rejected_global_total_limit"`
}

type userConnectionSettings struct {
	Identity                   coreIdentity `json:"identity"`
	MaxInboundOnlineIPs        *int         `json:"max_inbound_online_ips"`
	MaxTotalConnections        *int64       `json:"max_total_connections"`
	MaxOutboundTCPActive       *int64       `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int         `json:"max_outbound_tcp_new_per_second"`
	CloseWaitTimeoutSeconds    *int64       `json:"close_wait_timeout_seconds"`
}

type connectionSettings struct {
	DefaultCloseWaitTimeoutSeconds *int64                   `json:"default_close_wait_timeout_seconds"`
	OnlineIPGracePeriodSeconds     int64                    `json:"online_ip_grace_period_seconds"`
	GlobalTotalLimitEnabled        bool                     `json:"global_total_limit_enabled"`
	MaxGlobalTotalConnections      *int64                   `json:"max_global_total_connections"`
	Users                          []userConnectionSettings `json:"users"`
}

type coreConfig struct {
	DefaultCloseWaitTimeoutSeconds *int64      `json:"default_close_wait_timeout_seconds"`
	OnlineIPGracePeriodSeconds     int64       `json:"online_ip_grace_period_seconds"`
	MaxGlobalTotalConnections      *int64      `json:"max_global_total_connections"`
	Limits                         []coreLimit `json:"limits"`
}

type coreLimit struct {
	Identity                   coreIdentity `json:"identity"`
	MaxInboundOnlineIPs        *int         `json:"max_inbound_online_ips"`
	MaxTotalConnections        *int64       `json:"max_total_connections"`
	MaxOutboundTCPActive       *int64       `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int         `json:"max_outbound_tcp_new_per_second"`
	CloseWaitTimeoutSeconds    *int64       `json:"close_wait_timeout_seconds"`
}

type onlineIP struct {
	IP          string `json:"ip"`
	Connections int64  `json:"connections"`
}

type inboundSnapshot struct {
	Port          uint32         `json:"port"`
	InboundTag    string         `json:"inbound_tag"`
	Protocol      string         `json:"protocol,omitempty"`
	User          string         `json:"user,omitempty"`
	Attribution   string         `json:"attribution"`
	TCP           tcpStateCounts `json:"tcp"`
	Established   int64          `json:"established"`
	SynRecv       int64          `json:"syn_recv"`
	FinWait1      int64          `json:"fin_wait_1"`
	FinWait2      int64          `json:"fin_wait_2"`
	TimeWait      int64          `json:"time_wait"`
	CloseWait     int64          `json:"close_wait"`
	LastAck       int64          `json:"last_ack"`
	Closing       int64          `json:"closing"`
	OnlineIPCount int            `json:"online_ip_count"`
	OnlineIPs     []onlineIP     `json:"online_ips"`
	MaxOnlineIPs  *int           `json:"max_online_ips"`
}

type proxyUserSnapshot struct {
	Identity                   coreIdentity   `json:"identity"`
	InboundTag                 string         `json:"inbound_tag"`
	User                       string         `json:"user"`
	InboundPort                uint32         `json:"inbound_port,omitempty"`
	InboundActive              int64          `json:"inbound_active"`
	CurrentTotal               int64          `json:"current_total"`
	InboundTCP                 tcpStateCounts `json:"inbound_tcp"`
	InboundOnlineIPs           []onlineIP     `json:"inbound_online_ips"`
	OutboundActive             int64          `json:"outbound_active"`
	OutboundPending            int64          `json:"outbound_pending"`
	OutboundTCP                tcpStateCounts `json:"outbound_tcp"`
	OutboundNewRate            int            `json:"outbound_new_rate"`
	OutboundNewTotal           uint64         `json:"outbound_new_total"`
	OutboundRejectedTotal      uint64         `json:"outbound_rejected_total"`
	RejectedActiveLimit        uint64         `json:"rejected_active_limit"`
	RejectedNewRateLimit       uint64         `json:"rejected_new_rate_limit"`
	RejectedUserTotalLimit     uint64         `json:"rejected_user_total_limit"`
	RejectedOnlineIPLimit      uint64         `json:"rejected_online_ip_limit"`
	RejectedGlobalTotalLimit   uint64         `json:"rejected_global_total_limit"`
	MaxInboundOnlineIPs        *int           `json:"max_inbound_online_ips"`
	MaxTotalConnections        *int64         `json:"max_total_connections"`
	MaxOutboundTCPActive       *int64         `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int           `json:"max_outbound_tcp_new_per_second"`
	CloseWaitTimeoutSeconds    *int64         `json:"close_wait_timeout_seconds"`
	Source                     string         `json:"source"`
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
	Global     coreGlobalSnapshot  `json:"global"`
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
	config := coreConfig{
		DefaultCloseWaitTimeoutSeconds: settings.DefaultCloseWaitTimeoutSeconds,
		OnlineIPGracePeriodSeconds:     settings.OnlineIPGracePeriodSeconds,
	}
	if settings.GlobalTotalLimitEnabled {
		config.MaxGlobalTotalConnections = settings.MaxGlobalTotalConnections
	}
	for _, user := range settings.Users {
		config.Limits = append(config.Limits, coreLimit{
			Identity:                   user.Identity,
			MaxInboundOnlineIPs:        user.MaxInboundOnlineIPs,
			MaxTotalConnections:        user.MaxTotalConnections,
			MaxOutboundTCPActive:       user.MaxOutboundTCPActive,
			MaxOutboundTCPNewPerSecond: user.MaxOutboundTCPNewPerSecond,
			CloseWaitTimeoutSeconds:    user.CloseWaitTimeoutSeconds,
		})
	}
	return config
}
