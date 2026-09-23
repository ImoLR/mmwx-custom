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
	Identity                       coreIdentity   `json:"identity"`
	InboundName                    string         `json:"inbound_name,omitempty"`
	InboundPort                    uint32         `json:"inbound_port,omitempty"`
	OutboundTag                    string         `json:"outbound_tag,omitempty"`
	Attributed                     bool           `json:"attributed"`
	InboundActive                  int64          `json:"inbound_active"`
	InboundCurrent                 int64          `json:"inbound_current"`
	InboundTotal                   uint64         `json:"inbound_total"`
	CurrentTotal                   int64          `json:"current_total"`
	InboundTCP                     tcpStateCounts `json:"inbound_tcp"`
	InboundOnlineIPs               []onlineIP     `json:"inbound_online_ips"`
	OutboundActive                 int64          `json:"outbound_active"`
	OutboundPending                int64          `json:"outbound_pending"`
	OutboundTCP                    tcpStateCounts `json:"outbound_tcp"`
	OutboundNewTotal               uint64         `json:"outbound_new_total"`
	OutboundNewRate                int            `json:"outbound_new_rate"`
	OutboundRejectedTotal          uint64         `json:"outbound_rejected_total"`
	RejectedActiveLimit            uint64         `json:"rejected_active_limit"`
	RejectedNewRateLimit           uint64         `json:"rejected_new_rate_limit"`
	RejectedUserTotalLimit         uint64         `json:"rejected_user_total_limit"`
	RejectedPortTotalLimit         uint64         `json:"rejected_port_total_limit"`
	RejectedUserNewRateLimit       uint64         `json:"rejected_user_new_rate_limit"`
	RejectedPortNewRateLimit       uint64         `json:"rejected_port_new_rate_limit"`
	RejectedOnlineIPLimit          uint64         `json:"rejected_online_ip_limit"`
	RejectedGlobalTotalLimit       uint64         `json:"rejected_global_total_limit"`
	RejectedUserInboundLimit       uint64         `json:"rejected_user_inbound_limit"`
	RejectedPortInboundLimit       uint64         `json:"rejected_port_inbound_limit"`
	RejectedUserOnlineIPLimit      uint64         `json:"rejected_user_online_ip_limit"`
	RejectedPortOnlineIPLimit      uint64         `json:"rejected_port_online_ip_limit"`
	RejectedGlobalInboundLimit     uint64         `json:"rejected_global_inbound_limit"`
	MaxInboundOnlineIPs            *int           `json:"max_inbound_online_ips"`
	MaxTotalConnections            *int64         `json:"max_total_connections"`
	MaxOutboundTCPActive           *int64         `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond     *int           `json:"max_outbound_tcp_new_per_second"`
	MaxPortOutboundTCPActive       *int64         `json:"max_port_outbound_tcp_active"`
	MaxPortOutboundTCPNewPerSecond *int           `json:"max_port_outbound_tcp_new_per_second"`
	MaxPortInboundConnections      *int64         `json:"max_port_inbound_connections"`
	MaxPortInboundOnlineIPs        *int           `json:"max_port_inbound_online_ips"`
	CloseWaitTimeoutSeconds        *int64         `json:"close_wait_timeout_seconds"`
	ManagementGroup                string         `json:"management_group,omitempty"`
}

type coreInboundSnapshot struct {
	InboundTag  string   `json:"inbound_tag"`
	InboundName string   `json:"inbound_name,omitempty"`
	InboundPort uint32   `json:"inbound_port"`
	Users       []string `json:"users"`
}

type coreSnapshotResponse struct {
	Version          int                       `json:"version"`
	StartedAt        time.Time                 `json:"started_at"`
	Global           coreGlobalSnapshot        `json:"global"`
	Inbounds         []coreInboundSnapshot     `json:"inbounds"`
	Users            []coreUserSnapshot        `json:"proxy_users"`
	ManagementGroups []managementGroupSnapshot `json:"management_groups"`
}

type coreGlobalSnapshot struct {
	CurrentTotal               int64  `json:"current_total"`
	MaxTotal                   *int64 `json:"max_total"`
	CurrentInbound             int64  `json:"current_inbound"`
	MaxInbound                 *int64 `json:"max_inbound"`
	RejectedGlobalTotalLimit   uint64 `json:"rejected_global_total_limit"`
	RejectedGlobalInboundLimit uint64 `json:"rejected_global_inbound_limit"`
}

type managementGroupSnapshot struct {
	Username                   string         `json:"group"`
	CurrentTotal               int64          `json:"current_total"`
	InboundActive              int64          `json:"inbound_active"`
	InboundCurrent             int64          `json:"inbound_current"`
	InboundTCP                 tcpStateCounts `json:"inbound_tcp"`
	InboundOnlineIPs           []onlineIP     `json:"inbound_online_ips"`
	OutboundActive             int64          `json:"outbound_active"`
	OutboundPending            int64          `json:"outbound_pending"`
	OutboundTCP                tcpStateCounts `json:"outbound_tcp"`
	OutboundNewRate            int            `json:"outbound_new_rate"`
	OutboundNewTotal           uint64         `json:"outbound_new_total"`
	OutboundRejectedTotal      uint64         `json:"outbound_rejected_total"`
	RejectedUserTotalLimit     uint64         `json:"rejected_user_total_limit"`
	RejectedUserNewRateLimit   uint64         `json:"rejected_user_new_rate_limit"`
	RejectedPortTotalLimit     uint64         `json:"rejected_port_total_limit"`
	RejectedPortNewRateLimit   uint64         `json:"rejected_port_new_rate_limit"`
	RejectedOnlineIPLimit      uint64         `json:"rejected_online_ip_limit"`
	RejectedGlobalTotalLimit   uint64         `json:"rejected_global_total_limit"`
	RejectedUserInboundLimit   uint64         `json:"rejected_user_inbound_limit"`
	RejectedPortInboundLimit   uint64         `json:"rejected_port_inbound_limit"`
	RejectedUserOnlineIPLimit  uint64         `json:"rejected_user_online_ip_limit"`
	RejectedPortOnlineIPLimit  uint64         `json:"rejected_port_online_ip_limit"`
	RejectedGlobalInboundLimit uint64         `json:"rejected_global_inbound_limit"`
	MaxInboundConnections      *int64         `json:"max_inbound_connections"`
	MaxInboundOnlineIPs        *int           `json:"max_inbound_online_ips"`
	MaxOutboundTCPActive       *int64         `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int           `json:"max_outbound_tcp_new_per_second"`
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
	MaxGlobalInboundConnections    *int64                   `json:"max_global_inbound_connections"`
	Users                          []userConnectionSettings `json:"users"`
	Ports                          []portConnectionSettings `json:"ports"`
	ManagementUsers                []managementUserSettings `json:"management_users"`
	ManagementMappings             []managementMapping      `json:"management_mappings"`
}

type portConnectionSettings struct {
	InboundTag                 string `json:"inbound_tag"`
	MaxInboundConnections      *int64 `json:"max_inbound_connections"`
	MaxInboundOnlineIPs        *int   `json:"max_inbound_online_ips"`
	MaxOutboundTCPActive       *int64 `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int   `json:"max_outbound_tcp_new_per_second"`
}

type managementUserSettings struct {
	Username                   string `json:"username"`
	MaxInboundConnections      *int64 `json:"max_inbound_connections"`
	MaxInboundOnlineIPs        *int   `json:"max_inbound_online_ips"`
	MaxOutboundTCPActive       *int64 `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int   `json:"max_outbound_tcp_new_per_second"`
}

type managementMapping struct {
	Identity coreIdentity `json:"identity"`
	Group    string       `json:"group"`
}

type coreConfig struct {
	DefaultCloseWaitTimeoutSeconds *int64                   `json:"default_close_wait_timeout_seconds"`
	OnlineIPGracePeriodSeconds     int64                    `json:"online_ip_grace_period_seconds"`
	MaxGlobalTotalConnections      *int64                   `json:"max_global_total_connections"`
	MaxGlobalInboundConnections    *int64                   `json:"max_global_inbound_connections"`
	Limits                         []coreLimit              `json:"limits"`
	PortLimits                     []portConnectionSettings `json:"port_limits"`
	ManagementMappings             []managementMapping      `json:"management_mappings"`
	ManagementLimits               []coreManagementLimit    `json:"management_limits"`
}

type legacyCoreConfig struct {
	DefaultCloseWaitTimeoutSeconds *int64      `json:"default_close_wait_timeout_seconds"`
	OnlineIPGracePeriodSeconds     int64       `json:"online_ip_grace_period_seconds"`
	MaxGlobalTotalConnections      *int64      `json:"max_global_total_connections"`
	Limits                         []coreLimit `json:"limits"`
}

type coreManagementLimit struct {
	Group                      string `json:"group"`
	MaxInboundConnections      *int64 `json:"max_inbound_connections"`
	MaxInboundOnlineIPs        *int   `json:"max_inbound_online_ips"`
	MaxOutboundTCPActive       *int64 `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int   `json:"max_outbound_tcp_new_per_second"`
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
	Identity                       coreIdentity   `json:"identity"`
	InboundTag                     string         `json:"inbound_tag"`
	User                           string         `json:"user"`
	InboundPort                    uint32         `json:"inbound_port,omitempty"`
	InboundActive                  int64          `json:"inbound_active"`
	InboundCurrent                 int64          `json:"inbound_current"`
	CurrentTotal                   int64          `json:"current_total"`
	InboundTCP                     tcpStateCounts `json:"inbound_tcp"`
	InboundOnlineIPs               []onlineIP     `json:"inbound_online_ips"`
	OutboundActive                 int64          `json:"outbound_active"`
	OutboundPending                int64          `json:"outbound_pending"`
	OutboundTCP                    tcpStateCounts `json:"outbound_tcp"`
	OutboundNewRate                int            `json:"outbound_new_rate"`
	OutboundNewTotal               uint64         `json:"outbound_new_total"`
	OutboundRejectedTotal          uint64         `json:"outbound_rejected_total"`
	RejectedActiveLimit            uint64         `json:"rejected_active_limit"`
	RejectedNewRateLimit           uint64         `json:"rejected_new_rate_limit"`
	RejectedUserTotalLimit         uint64         `json:"rejected_user_total_limit"`
	RejectedPortTotalLimit         uint64         `json:"rejected_port_total_limit"`
	RejectedUserNewRateLimit       uint64         `json:"rejected_user_new_rate_limit"`
	RejectedPortNewRateLimit       uint64         `json:"rejected_port_new_rate_limit"`
	RejectedOnlineIPLimit          uint64         `json:"rejected_online_ip_limit"`
	RejectedGlobalTotalLimit       uint64         `json:"rejected_global_total_limit"`
	RejectedUserInboundLimit       uint64         `json:"rejected_user_inbound_limit"`
	RejectedPortInboundLimit       uint64         `json:"rejected_port_inbound_limit"`
	RejectedUserOnlineIPLimit      uint64         `json:"rejected_user_online_ip_limit"`
	RejectedPortOnlineIPLimit      uint64         `json:"rejected_port_online_ip_limit"`
	RejectedGlobalInboundLimit     uint64         `json:"rejected_global_inbound_limit"`
	MaxInboundOnlineIPs            *int           `json:"max_inbound_online_ips"`
	MaxTotalConnections            *int64         `json:"max_total_connections"`
	MaxOutboundTCPActive           *int64         `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond     *int           `json:"max_outbound_tcp_new_per_second"`
	MaxPortOutboundTCPActive       *int64         `json:"max_port_outbound_tcp_active"`
	MaxPortOutboundTCPNewPerSecond *int           `json:"max_port_outbound_tcp_new_per_second"`
	MaxPortInboundConnections      *int64         `json:"max_port_inbound_connections"`
	MaxPortInboundOnlineIPs        *int           `json:"max_port_inbound_online_ips"`
	CloseWaitTimeoutSeconds        *int64         `json:"close_wait_timeout_seconds"`
	Source                         string         `json:"source"`
	ManagementGroup                string         `json:"management_group,omitempty"`
}

type coreStatus struct {
	Available bool      `json:"available"`
	Version   int       `json:"interface_version,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type detailedConnectionSnapshot struct {
	System           tcpStateCounts            `json:"system"`
	Inbounds         []inboundSnapshot         `json:"inbounds"`
	ProxyUsers       []proxyUserSnapshot       `json:"proxy_users"`
	ManagementGroups []managementGroupSnapshot `json:"management_groups"`
	Global           coreGlobalSnapshot        `json:"global"`
	Core             coreStatus                `json:"core"`
	SampledAt        time.Time                 `json:"sampled_at"`
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
	return connectionSettings{OnlineIPGracePeriodSeconds: 30, Users: []userConnectionSettings{}, Ports: []portConnectionSettings{}, ManagementUsers: []managementUserSettings{}, ManagementMappings: []managementMapping{}}
}

func (settings connectionSettings) coreConfig() coreConfig {
	config := coreConfig{
		DefaultCloseWaitTimeoutSeconds: settings.DefaultCloseWaitTimeoutSeconds,
		OnlineIPGracePeriodSeconds:     settings.OnlineIPGracePeriodSeconds,
		MaxGlobalInboundConnections:    settings.MaxGlobalInboundConnections,
		PortLimits:                     append([]portConnectionSettings(nil), settings.Ports...),
		ManagementMappings:             append([]managementMapping(nil), settings.ManagementMappings...),
	}
	for _, user := range settings.ManagementUsers {
		config.ManagementLimits = append(config.ManagementLimits, coreManagementLimit{
			Group: user.Username, MaxInboundConnections: user.MaxInboundConnections, MaxInboundOnlineIPs: user.MaxInboundOnlineIPs,
			MaxOutboundTCPActive: user.MaxOutboundTCPActive, MaxOutboundTCPNewPerSecond: user.MaxOutboundTCPNewPerSecond,
		})
	}
	if settings.GlobalTotalLimitEnabled {
		config.MaxGlobalTotalConnections = settings.MaxGlobalTotalConnections
	}
	for _, user := range settings.Users {
		config.Limits = append(config.Limits, coreLimit{Identity: user.Identity, CloseWaitTimeoutSeconds: user.CloseWaitTimeoutSeconds})
	}
	return config
}

func (settings connectionSettings) legacyCoreConfig() legacyCoreConfig {
	current := settings.coreConfig()
	return legacyCoreConfig{
		DefaultCloseWaitTimeoutSeconds: current.DefaultCloseWaitTimeoutSeconds,
		OnlineIPGracePeriodSeconds:     current.OnlineIPGracePeriodSeconds,
		MaxGlobalTotalConnections:      current.MaxGlobalTotalConnections,
		Limits:                         current.Limits,
	}
}
