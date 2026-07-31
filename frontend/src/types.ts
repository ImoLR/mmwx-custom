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
  status?: string;
  traffic_limit?: number;
  traffic_used?: number;
  current_upload_speed?: number;
  current_download_speed?: number;
  boot_time?: string | null;
  xray_boot_time?: string | null;
  xray_running?: boolean;
  xray_version?: string;
  agent_version?: string;
  ip_address?: string;
  ip_address_v6?: string;
  domain?: string;
  pull_address?: string;
  pull_port?: number;
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
  ddns_pending?: boolean;
  inbounds?: Array<{
    tag?: string;
    protocol?: string;
    port?: number;
    uplink?: number;
    downlink?: number;
  }>;
  traffic_source?: string;
  sysmetrics?: SystemMetrics | null;
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

export type NodeTrafficItem = {
  node_id: number;
  node_name: string;
  server_name: string;
  node_type?: string;
  uplink: number;
  downlink: number;
  last_uplink: number;
  last_downlink: number;
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
