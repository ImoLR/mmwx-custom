export type LoginResponse = {
  token: string;
  expires_at: string;
  username: string;
  email?: string;
  nickname?: string;
  avatar_url?: string;
  role: string;
  is_admin: boolean;
  requires_2fa?: boolean;
  two_factor_token?: string;
};

export type TrafficSummary = {
  metrics: {
    total_limit_gb: number;
    total_used_gb: number;
    total_remaining_gb: number;
    usage_percentage: number;
    unlimited_used_gb: number;
  };
  history: Array<{
    date: string;
    used_gb: number | null;
  }>;
};

export type RemoteServer = {
  id: number;
  name: string;
  token?: string;
  agent_token?: string;
  status?: string;
  traffic_limit?: number;
  traffic_used?: number;
  traffic_used_offset?: number;
  current_upload_speed?: number;
  current_download_speed?: number;
  boot_time?: string | null;
  xray_boot_time?: string | null;
  xray_running?: boolean;
  xray_version?: string;
  agent_version?: string;
  country_code?: string;
  country?: string;
  region?: string;
  region_country?: string;
  region_name?: string;
  region_city?: string;
  location?: string;
  geo_country?: string;
  geo_country_code?: string;
  service_location?: string;
  displayLocation?: string;
  display_location?: string;
  countryName?: string;
  flag?: string;
  ip_address?: string;
  ip_address_v6?: string;
  domain?: string;
  pull_address?: string;
  pull_address_v6?: string;
  pull_port?: number;
  pull_token?: string;
  listen_port?: number;
  connection_mode?: string;
  xray_mode?: string;
  last_heartbeat?: string;
  speed_updated_at?: string;
  traffic_reset_day?: number;
  last_traffic_reset_at?: string;
  ipv6_enabled?: boolean;
  ws_connected?: boolean;
  fallback_to_pull?: boolean;
  ddns_enabled?: boolean;
  ddns_provider_id?: number;
  ddns_pending?: boolean;
  ddns_last_error?: string;
  is_federated?: boolean;
  same_host_as_master?: boolean;
  warp_installed?: boolean;
  steal_mode?: string;
  domain_v6?: string;
  traffic_stats_mode?: string;
  use_443?: boolean;
  inbounds?: Array<{
    tag?: string;
    protocol?: string;
    port?: number;
    uplink?: number;
    downlink?: number;
  }>;
  sysmetrics?: SystemMetrics | null;
};

export type RemoteServerCreateRequest = {
  name: string;
  traffic_limit?: number;
  traffic_used_offset?: number;
  traffic_reset_day?: number;
  ip_address?: string;
  connection_mode?: string;
  listen_port?: number;
  pull_address?: string;
  pull_address_v6?: string;
  pull_port?: number;
  pull_token?: string;
  steal_self?: boolean;
  front_service?: "xray" | "nginx" | string;
  domain?: string;
  domain_v6?: string;
  use_443?: boolean;
  steal_mode?: "tunnel" | "fallback" | "default" | string;
  site_type?: "static" | "proxy" | string;
  site_value?: string;
  xray_mode?: "external" | "embedded" | string;
  traffic_stats_mode?: "both" | "upload" | "download" | "max" | string;
  traffic_source?: "xray" | "system" | string;
  ipv6_enabled?: boolean;
  ddns_enabled?: boolean;
  ddns_provider_id?: number;
};

export type RemoteServerMutationResponse = {
  success: boolean;
  message?: string;
  server?: RemoteServer;
  install_command?: string;
  is_local?: boolean;
};

export type SharedServerAddRequest = {
  owner_url: string;
  share_token: string;
  name?: string;
  prefix?: string;
};

export type SharedServerAddResponse = {
  id: number;
  name: string;
  status?: string;
  success?: boolean;
  message?: string;
};

export type MasterUrlResponse = {
  master_url?: string;
  success?: boolean;
};

export type DNSProvider = {
  id?: number;
  ID?: number;
  name?: string;
  Name?: string;
  provider_type?: string;
  ProviderType?: string;
};

export type DNSProvidersResponse = {
  success?: boolean;
  providers?: DNSProvider[];
};

export type ValidCertificate = {
  id?: number;
  domain?: string;
  dns_provider_id?: number;
  [key: string]: unknown;
};

export type ConnectionMetric = {
  server_id: string;
  custom_server_uuid?: string;
  tcp_count: number;
  udp_count: number;
  connection_count: number;
  sampled_at?: string;
  updated_at?: string;
  helper_version?: string;
  available: boolean;
};

export type ConnectionMetricsResponse = {
  success: boolean;
  stale_timeout_seconds?: number;
  metrics?: Record<string, ConnectionMetric>;
};

export type ConnectionIdentity = {
  inbound_tag: string;
  user: string;
};

export type UserConnectionSettings = {
  identity: ConnectionIdentity;
  max_inbound_online_ips: number | null;
  max_outbound_tcp_active: number | null;
  max_outbound_tcp_new_per_second: number | null;
  close_wait_timeout_seconds: number | null;
};

export type ServerConnectionSettings = {
  default_close_wait_timeout_seconds: number | null;
  online_ip_grace_period_seconds: number;
  users: UserConnectionSettings[];
};

export type DetailedConnectionResponse = {
  success: boolean;
  available: boolean;
  stale_timeout_seconds: number;
  settings: ServerConnectionSettings;
  record?: {
    server_id: string;
    custom_server_uuid?: string;
    helper_version?: string;
    updated_at?: string;
    snapshot: {
      system: {
        tcp_total: number;
        established: number;
        time_wait: number;
        close_wait: number;
        syn_sent: number;
        syn_recv: number;
      };
      inbounds: Array<{
        port: number;
        inbound_tag: string;
        protocol?: string;
        user?: string;
        attribution: "single_user_inbound" | "inbound_port" | string;
        established: number;
        time_wait: number;
        close_wait: number;
        online_ip_count: number;
        online_ips: Array<{ ip: string; connections: number }>;
        max_online_ips: number | null;
      }>;
      proxy_users: Array<{
        identity: ConnectionIdentity;
        inbound_tag: string;
        user: string;
        inbound_port?: number;
        inbound_active: number;
        outbound_active: number;
        outbound_new_rate: number;
        outbound_new_total: number;
        outbound_rejected_total: number;
        max_inbound_online_ips: number | null;
        max_outbound_tcp_active: number | null;
        max_outbound_tcp_new_per_second: number | null;
        close_wait_timeout_seconds: number | null;
        source: string;
      }>;
      core: {
        available: boolean;
        interface_version?: number;
        started_at?: string;
        error?: string;
      };
      sampled_at: string;
    };
  };
};

export type HelperInstallTokenResponse = {
  success: boolean;
  server_id: string;
  custom_server_uuid: string;
  install_url: string;
  expires_at: string;
  command: string;
};

export type GeoLookupResponse = {
  success?: boolean;
  country_code?: string;
  country?: string;
  flag?: string;
  message?: string;
  cached?: boolean;
};

export type SystemMetrics = {
  cpu_pct?: number;
  cpu_cores?: number;
  loadavg?: string;
  mem_used?: number;
  mem_total?: number;
  swap_used?: number;
  swap_total?: number;
  disk_used?: number;
  disk_total?: number;
  has_cpu?: boolean;
  has_mem?: boolean;
  has_disk?: boolean;
  HasCPU?: boolean;
  HasMem?: boolean;
  HasDisk?: boolean;
};

export type RemoteServersResponse = {
  success: boolean;
  message?: string;
  servers?: RemoteServer[];
};

export type XrayObject = Record<string, unknown>;

export type XrayServiceStatusResponse = {
  success?: boolean;
  xray?: {
    installed?: boolean;
    running?: boolean;
    version?: string;
  };
  nginx?: {
    installed?: boolean;
    running?: boolean;
    version?: string;
  };
};

export type XrayConfigResponse = {
  success?: boolean;
  path?: string;
  config?: string;
};

export type XraySystemConfig = {
  metrics_enabled: boolean;
  metrics_listen: string;
  stats_enabled: boolean;
  grpc_enabled: boolean;
  grpc_port: number;
};

export type XraySystemConfigResponse = Partial<XraySystemConfig> & {
  success?: boolean;
  message?: string;
  config?: Partial<XraySystemConfig>;
};

export type XrayInboundsResponse = {
  success?: boolean;
  inbounds?: XrayObject[];
};

export type XrayOutboundsResponse = {
  success?: boolean;
  outbounds?: XrayObject[];
};

export type XrayServerNIC = {
  name: string;
  addrs: Array<{
    ip: string;
    family?: "v4" | "v6" | string;
    scope?: string;
  }>;
};

export type XrayServerNICsResponse = {
  success?: boolean;
  reason?: string;
  message?: string;
  nics?: XrayServerNIC[];
};

export type XrayWarpStatus = {
  success?: boolean;
  message?: string;
  installed?: boolean;
  license_active?: boolean;
  addr_v4?: string;
  addr_v6?: string;
};

export type AgentVersionInfo = {
  server_id?: number;
  current?: string;
  latest?: string;
  upgrade_available?: boolean;
  current_error?: string;
  latest_error?: string;
};

export type RemoteSystemInfo = {
  success?: boolean;
  agent_version?: string;
  hostname?: string;
  uptime?: string;
  loadavg?: string;
  tcp_fast_open_server?: boolean;
  memory?: Record<string, string>;
};

export type AgentSyncNodesResponse = {
  success?: boolean;
  message?: string;
  synced_count?: number;
  skipped_count?: number;
  total_inbounds?: number;
  synced_tags?: string[];
  errors?: string[];
};

export type XraySnapshotItem = {
  id: number;
  config_hash: string;
  source?: string;
  status?: string;
  created_at?: string;
  size_bytes?: number;
  config_json?: string;
};

export type XraySnapshotsResponse = {
  items?: XraySnapshotItem[];
  total?: number;
};

export type XrayRecoveryStatusResponse = {
  has_pending?: boolean;
  has_current?: boolean;
  pending?: XraySnapshotItem;
  current?: XraySnapshotItem;
};

export type RemoteWebsiteItem = {
  domain?: string;
  path?: string;
  type?: string;
  value?: string;
  managed?: boolean;
  legacy?: boolean;
  protected?: boolean;
  reason?: string;
};

export type RemoteWebsitesResponse = {
  success?: boolean;
  message?: string;
  nginx?: {
    installed?: boolean;
    running?: boolean;
    manager?: string;
    can_manage?: boolean;
    reason?: string;
    binary?: string;
  };
  ports?: Record<string, string>;
  websites?: RemoteWebsiteItem[];
};

export type WebsiteMutationResponse = {
  success?: boolean;
  message?: string;
  entry_mode?: string;
};

export type XrayRoutingResponse = {
  success?: boolean;
  routing?: XrayObject;
};

export type XrayNode = {
  id: number;
  raw_url?: string;
  node_name: string;
  server?: string;
  port?: number;
  protocol?: string;
  parsed_config?: string;
  clash_config?: string;
  enabled?: boolean;
  tag?: string;
  tags?: string[];
  original_server?: string;
  original_domain?: string;
  inbound_tag?: string;
  chain_proxy_node_id?: number | null;
  relay_group_name?: string;
  relay_group_node_ids?: number[];
  node_type?: string;
  parent_node_id?: number | null;
  routed_outbound_tag?: string;
  routed_owner?: string;
  created_by?: string;
  multiplier?: number;
  relay_orig_server?: string;
  relay_orig_port?: number;
  created_at?: string;
  updated_at?: string;
};

export type XrayNodesResponse = {
  success?: boolean;
  nodes?: XrayNode[];
};

export type PackageTemplate = {
  name: string;
  filename: string;
  type?: "clash" | "surge" | string;
};

export type ManagedPackage = {
  id: number;
  name: string;
  description?: string;
  traffic_limit_gb: number;
  cycle_days: number;
  is_reset: boolean;
  reset_day: number;
  nodes: number[];
  nodes_configured?: boolean;
  node_multipliers?: Record<number, number> | null;
  node_name_overrides?: Record<number, string> | null;
  node_name_override_enabled: boolean;
  node_speed_limits?: Record<number, number> | null;
  node_device_limits?: Record<number, number> | null;
  node_traffic_limits?: Record<number, number> | null;
  speed_limit_mbps: number;
  device_limit: number;
  forward_rule_limit: number;
  forward_port_limit: number;
  forward_speed_mbps: number;
  forward_conn_limit: number;
  forward_chains?: number[] | null;
  short_code?: string;
  traffic_mode: "oneway" | "twoway" | string;
  template_filename?: string;
  surge_template_filename?: string;
  created_at?: string;
  updated_at?: string;
};

export type PackagePayload = Omit<ManagedPackage, "id" | "nodes_configured" | "short_code" | "created_at" | "updated_at"> & {
  id?: number;
  description: string;
  node_multipliers: Record<number, number>;
  node_name_overrides: Record<number, string>;
  node_speed_limits: Record<number, number>;
  node_device_limits: Record<number, number>;
  node_traffic_limits: Record<number, number>;
  forward_chains: number[];
  template_filename: string;
  surge_template_filename: string;
};

export type PackagesResponse = { packages?: ManagedPackage[] };
export type PackageTemplatesResponse = { templates?: PackageTemplate[] };

export type PackageForwardChain = {
  id: number;
  name: string;
  hops?: unknown[];
  port_range_start?: number;
  port_range_end?: number;
};

export type PackageForwardChainsResponse = {
  success?: boolean;
  chains?: PackageForwardChain[] | null;
  issues?: Record<string, unknown>;
};

export type ForwardBalanceStrategy =
  | "round_robin"
  | "least_conn"
  | "percentage"
  | "cycle"
  | "sticky"
  | string;

export type ForwardGroupMember = {
  server_id: number;
  weight?: number;
};

export type ForwardGroup = {
  id: number;
  name: string;
  description?: string;
  balance_strategy?: ForwardBalanceStrategy;
  members?: ForwardGroupMember[];
};

export type ForwardChainHop = {
  group_id: number;
  group_name?: string;
  order: number;
};

export type ForwardBoundNode = {
  node_id: number;
  node_name?: string;
  port?: number;
  terminus_addr?: string;
};

export type ForwardChain = {
  id: number;
  name: string;
  hops?: ForwardChainHop[];
  port_range_start?: number;
  port_range_end?: number;
  dns_domain?: string;
  dns_domain_v6?: string;
  dns_provider_id?: number;
  bound_nodes?: ForwardBoundNode[];
};

export type ForwardChainsResponse = {
  success?: boolean;
  chains?: ForwardChain[] | null;
  issues?: Record<string, string[] | string | unknown>;
};

export type ForwardGroupsResponse = { success?: boolean; groups?: ForwardGroup[] | null };
export type ForwardServersResponse = { success?: boolean; servers?: RemoteServer[] | null };
export type ForwardCertificatesResponse = { success?: boolean; certificates?: ValidCertificate[] | null };
export type ForwardNodesResponse = { success?: boolean; nodes?: XrayNode[] | null };
export type ForwardMutationResponse = {
  success?: boolean;
  message?: string;
  id?: number;
  chain_id?: number;
  group_id?: number;
  group?: ForwardGroup;
  chain?: ForwardChain;
  warning?: string;
  warnings?: string[];
  count?: number;
};
export type ForwardProbeResponse = {
  success?: boolean;
  latency_ms?: number;
  method?: string;
  error?: string;
  results?: Array<{
    target?: string;
    success?: boolean;
    latency_ms?: number;
    method?: string;
    error?: string;
  }>;
};

export type PackageMutationResponse = {
  id?: number;
  success?: boolean;
  message?: string;
};

export type CarpoolPublishRequest = {
  package_id: number;
  price_minor: number;
  currency: "CNY" | "USDT";
  billing_period: "monthly" | "quarterly" | "yearly" | "one_time" | "custom";
  slots_total: number;
  slots_available: number;
  description: string;
};

export type NodeMutationRequest = {
  raw_url?: string;
  node_name: string;
  protocol?: string;
  parsed_config?: string;
  clash_config?: string;
  enabled?: boolean;
  tag?: string;
  tags?: string[];
  inbound_tag?: string;
  chain_proxy_node_id?: number | null;
  relay_group_name?: string;
  relay_group_node_ids?: number[] | null;
  relay_server?: string;
  relay_port?: number;
};

export type NodeMutationResponse = {
  success?: boolean;
  message?: string;
  status?: string;
  node?: XrayNode;
  nodes?: XrayNode[];
  deleted?: number;
  total?: number;
  failed?: number;
  skipped?: number;
};

export type NodeParseResponse = {
  success?: boolean;
  proxies?: Array<Record<string, unknown>>;
  count?: number;
  suggested_tag?: string;
};

export type NodeTagsResponse = {
  success?: boolean;
  tags?: string[];
};

export type NodeURIResponse = {
  uri?: string;
};

export type NodeRelatedInboundsResponse = {
  node_name?: string;
  inbound_tag?: string;
  inbounds?: Array<Record<string, unknown>>;
  count?: number;
};

export type NodeTCPingResponse = {
  success?: boolean;
  latency?: number;
  error?: string;
};

export type NodeTempSubscriptionResponse = {
  success?: boolean;
  url?: string;
  token?: string;
  expire_at?: string;
  expires_at?: string;
};

export type SpeedTester = {
  id: number;
  name: string;
  created_by?: string;
  last_seen?: string | null;
  created_at?: string;
  online?: boolean;
  caps?: string[];
  version?: string;
};

export type SpeedTestResult = {
  id?: number;
  node_id?: number;
  node_name?: string;
  source?: string;
  down_mbps?: number;
  latency_ms?: number;
  bytes?: number;
  status?: string;
  error?: string;
  egress_ip?: string;
  created_at?: string;
  updated_at?: string;
};

export type SpeedTestResultsResponse = {
  success?: boolean;
  results?: SpeedTestResult[];
};

export type SpeedTestRunResponse = {
  success?: boolean;
  result?: SpeedTestResult;
};

export type SpeedTestersResponse = {
  success?: boolean;
  testers?: SpeedTester[];
};

export type NodeURIItem = {
  username: string;
  node_id: number;
  node_name: string;
  server_name?: string;
  protocol?: string;
  node_type?: string;
  uri: string;
};

export type NodeTunnel = {
  kind: "inbound" | "routed";
  server_id: number;
  server_name: string;
  is_federated?: boolean;
  tag: string;
  listen_port?: number;
  target_address?: string;
  target_port?: number;
  network?: string;
  inbound_tag?: string;
  match_domain?: string[];
  match_ip?: string[];
  rule_index?: number;
};

export type NodeTunnelChain = {
  label: string;
  hops: Array<{
    server_id: number;
    server_name: string;
    tag: string;
    listen_port?: number;
    target_address?: string;
    target_port?: number;
  }>;
  entry_server?: number;
  entry_port?: number;
  final_target?: string;
};

export type ExternalSyncCandidate = {
  id: string;
  subscription_name: string;
  name: string;
  protocol: string;
  server: string;
  port?: string | number;
};

export type ExternalSyncResponse = {
  message?: string;
  updated_count?: number;
  session_id?: string;
  new_nodes?: ExternalSyncCandidate[];
};

export type UserConfigResponse = Record<string, unknown> & {
  node_order?: number[];
};

export type NodeTrafficItem = {
  node_id: number;
  node_name: string;
  server_name: string;
  node_type?: string;
  uplink: number;
  downlink: number;
  used?: number;
  last_uplink?: number;
  last_downlink?: number;
};

export type NodeTotalsResponse = {
  success: boolean;
  items?: NodeTrafficItem[];
};

export type UserTrafficSummary = {
  username: string;
  total_uplink: number;
  total_downlink: number;
  cycle_uplink: number;
  cycle_downlink: number;
};

export type UsersTrafficResponse = {
  success: boolean;
  users?: UserTrafficSummary[];
};

export type TrafficRange = "today" | "week" | "month";

export type PeriodUserTrafficItem = {
  username: string;
  uplink: number;
  downlink: number;
  used: number;
};

export type TrafficPeriodResponse<T> = {
  success: boolean;
  range: TrafficRange;
  range_start: string;
  range_end: string;
  timezone: string;
  complete: boolean;
  items?: T[];
};

export type NodeConnectionsResponse = {
  success: boolean;
  connections?: Record<string, number>;
  users?: Record<string, Record<string, number>>;
};

export type UserConnectionsResponse = {
  success: boolean;
  connections?: Record<string, number>;
};

export type UserSpeedsResponse = {
  success: boolean;
  user_speeds?: Record<string, number>;
};

export type AdminTrafficResponse = {
  success: boolean;
  servers?: Array<{
    server_id: number;
    server_name: string;
    inbounds?: NodeTrafficItem[];
    outbounds?: NodeTrafficItem[];
    users?: UserTrafficSummary[];
  }>;
};

export type RealtimeSnapshot = {
  type: "realtime";
  servers?: RemoteServer[];
  userConnections?: Record<string, number>;
  trafficSummary?: TrafficSummary;
  adminTraffic?: AdminTrafficResponse;
  nodeTotals?: NodeTotalsResponse;
  nodeTotalsDate?: string;
};

export type Session = {
  token: string;
  username: string;
  nickname?: string;
  avatarUrl?: string;
  role: string;
  isAdmin: boolean;
  expiresAt: string;
};
