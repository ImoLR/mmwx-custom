import React, { useCallback, useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  ArrowDown,
  ArrowUp,
  Boxes,
  ChevronDown,
  Clock3,
  Database,
  Edit3,
  Gauge,
  Globe2,
  Home,
  LayoutGrid,
  List,
  LogIn,
  LogOut,
  Menu,
  Moon,
  MoreHorizontal,
  Power,
  Plus,
  RefreshCw,
  Route,
  RotateCw,
  Search,
  Server,
  Share2,
  Settings,
  Sun,
  TerminalSquare,
  Tags,
  Trash2,
  Wrench,
  X,
  Users,
} from "lucide-react";
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import {
  clearSession,
  controlRemoteService,
  createHelperInstallToken,
  fetchConnectionMetrics,
  fetchAdminTraffic,
  fetchLocalSystemMetrics,
  fetchNodeTotals,
  fetchRemoteServers,
  fetchTrafficSummary,
  fetchUserConnections,
  fetchUserSpeeds,
  fetchUsers,
  loadSession,
  login,
  saveSession,
} from "./api";
import { formatBytes, formatDurationSince, formatGB, formatSpeed, todayUTC } from "./format";
import { loadingRegion, lookupServerRegion, serverRegionAddress, serverRegionFromFields, unknownRegion } from "./geo";
import type {
  AdminTrafficResponse,
  ConnectionMetric,
  HelperInstallTokenResponse,
  NodeTrafficItem,
  RealtimeSnapshot,
  RemoteServer,
  Session,
  SystemMetrics as SystemMetricsData,
  TrafficSummary,
  UserTrafficSummary,
} from "./types";
import "./styles.css";

type DashboardState = {
  summary: TrafficSummary | null;
  systemMetrics: SystemMetricsData | null;
  servers: RemoteServer[];
  nodes: NodeTrafficItem[];
  users: UserTrafficSummary[];
  userConnections: Record<string, number>;
  userSpeeds: Record<string, number>;
  adminTraffic: AdminTrafficResponse | null;
  connectionMetrics: Record<string, ConnectionMetric>;
};

const emptyState: DashboardState = {
  summary: null,
  systemMetrics: null,
  servers: [],
  nodes: [],
  users: [],
  userConnections: {},
  userSpeeds: {},
  adminTraffic: null,
  connectionMetrics: {},
};

const SYSTEM_METRICS_REFRESH_MS = 5000;

function App() {
  const [session, setSession] = useState<Session | null>(() => loadSession());
  const [dark, setDark] = useState(false);

  useEffect(() => {
    document.documentElement.dataset.theme = dark ? "dark" : "light";
  }, [dark]);

  if (!session) {
    return <LoginScreen onLogin={setSession} dark={dark} onToggleTheme={() => setDark((value) => !value)} />;
  }

  return (
    <Dashboard
      session={session}
      dark={dark}
      onToggleTheme={() => setDark((value) => !value)}
      onLogout={() => {
        clearSession();
        setSession(null);
      }}
    />
  );
}

function LoginScreen({
  onLogin,
  dark,
  onToggleTheme,
}: {
  onLogin: (session: Session) => void;
  dark: boolean;
  onToggleTheme: () => void;
}) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [rememberMe, setRememberMe] = useState(true);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setError("");
    setLoading(true);
    try {
      const response = await login(username, password, rememberMe);
      if (response.requires_2fa) {
        setError("当前账号启用了二步验证，第一阶段新版前端暂未接入 2FA。");
        return;
      }
      onLogin(saveSession(response));
    } catch (err) {
      setError(err instanceof Error ? err.message : "登录失败");
    } finally {
      setLoading(false);
    }
  }

  return (
    <main className="login-page">
      <div className="login-actions">
        <button className="round-button" type="button" onClick={onToggleTheme} aria-label="切换主题">
          {dark ? <Sun /> : <Moon />}
        </button>
      </div>
      <form className="login-card" onSubmit={submit}>
        <div className="brand-mark">妙</div>
        <h1>妙妙屋 X</h1>
        <p>欢迎回来</p>

        <label>
          <span>用户名</span>
          <input value={username} onChange={(event) => setUsername(event.target.value)} placeholder="用户名" autoComplete="username" />
        </label>

        <label>
          <span>密码</span>
          <input
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            placeholder="密码"
            type="password"
            autoComplete="current-password"
          />
        </label>

        <label className="remember">
          <input checked={rememberMe} onChange={(event) => setRememberMe(event.target.checked)} type="checkbox" />
          <span>记住我</span>
        </label>

        {error && <div className="error-text">{error}</div>}

        <button className="primary-button" disabled={loading} type="submit">
          {loading ? "登录中..." : "登录"}
        </button>
      </form>
    </main>
  );
}

function Dashboard({
  session,
  dark,
  onToggleTheme,
  onLogout,
}: {
  session: Session;
  dark: boolean;
  onToggleTheme: () => void;
  onLogout: () => void;
}) {
  const [state, setState] = useState<DashboardState>(emptyState);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [activeTab, setActiveTab] = useState("overview");
  const [menuOpen, setMenuOpen] = useState(false);
  const [selectedServerId, setSelectedServerId] = useState<number | null>(null);
  const [xrayActionBusy, setXrayActionBusy] = useState(false);
  const [serviceViewMode, setServiceViewMode] = useState<"grid" | "list">("grid");
  const [serviceMenuServer, setServiceMenuServer] = useState<RemoteServer | null>(null);
  const [serviceDialog, setServiceDialog] = useState<{ kind: "add" | "access" | "edit" | "xray" | "agent" | "helper"; server?: RemoteServer } | null>(null);

  const refreshUserSpeeds = useCallback(
    async (servers: RemoteServer[]) => {
      if (servers.length === 0) {
        setState((current) => ({ ...current, userSpeeds: {} }));
        return;
      }

      const results = await Promise.allSettled(servers.map((server) => fetchUserSpeeds(session.token, server.id)));
      const userSpeeds = aggregateUserSpeeds(results);
      setState((current) => ({ ...current, userSpeeds }));
    },
    [session.token],
  );

  const loadDashboard = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const date = todayUTC();
      const [summary, remoteServers, nodeTotals, users, connections, adminTraffic, helperConnections] = await Promise.all([
        fetchTrafficSummary(session.token),
        fetchRemoteServers(session.token),
        fetchNodeTotals(session.token, date),
        fetchUsers(session.token),
        fetchUserConnections(session.token),
        fetchAdminTraffic(session.token),
        fetchConnectionMetrics(),
      ]);

      const servers = remoteServers.servers ?? [];
      const speedResults = await Promise.allSettled(servers.map((server) => fetchUserSpeeds(session.token, server.id)));

      setState((current) => ({
        summary,
        systemMetrics: current.systemMetrics,
        servers,
        nodes: nodeTotals.items ?? [],
        users: users.users ?? [],
        userConnections: connections.connections ?? {},
        userSpeeds: aggregateUserSpeeds(speedResults),
        adminTraffic,
        connectionMetrics: helperConnections.metrics ?? {},
      }));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Dashboard 加载失败");
    } finally {
      setLoading(false);
    }
  }, [session.token]);

  useEffect(() => {
    void loadDashboard();
  }, [loadDashboard]);

  useEffect(() => {
    if (activeTab !== "overview") return;

    let stopped = false;
    let timer: number | undefined;
    let controller: AbortController | null = null;
    let inFlight = false;

    const clearTimer = () => {
      if (timer !== undefined) {
        window.clearTimeout(timer);
        timer = undefined;
      }
    };

    const scheduleNext = () => {
      clearTimer();
      if (stopped || document.visibilityState !== "visible") return;
      timer = window.setTimeout(() => {
        void refreshSystemMetrics();
      }, SYSTEM_METRICS_REFRESH_MS);
    };

    const refreshSystemMetrics = async () => {
      if (stopped || inFlight || document.visibilityState !== "visible") return;
      inFlight = true;
      controller?.abort();
      controller = new AbortController();
      try {
        const metrics = await fetchLocalSystemMetrics(session.token, controller.signal);
        if (!stopped) {
          setState((current) => ({ ...current, systemMetrics: metrics }));
        }
      } catch {
        // Keep the last successful system snapshot and retry on the next cycle.
      } finally {
        inFlight = false;
        scheduleNext();
      }
    };

    const handleVisibility = () => {
      if (document.visibilityState === "visible") {
        void refreshSystemMetrics();
      } else {
        clearTimer();
        controller?.abort();
      }
    };

    void refreshSystemMetrics();
    document.addEventListener("visibilitychange", handleVisibility);

    return () => {
      stopped = true;
      clearTimer();
      controller?.abort();
      document.removeEventListener("visibilitychange", handleVisibility);
    };
  }, [activeTab, session.token]);

  useEffect(() => {
    if (activeTab !== "services") return;

    let stopped = false;
    let timer: number | undefined;
    let controller: AbortController | null = null;
    let inFlight = false;

    const clearTimer = () => {
      if (timer !== undefined) {
        window.clearTimeout(timer);
        timer = undefined;
      }
    };

    const scheduleNext = () => {
      clearTimer();
      if (stopped || document.visibilityState !== "visible") return;
      timer = window.setTimeout(() => {
        void refreshConnections();
      }, SYSTEM_METRICS_REFRESH_MS);
    };

    const refreshConnections = async () => {
      if (stopped || inFlight || document.visibilityState !== "visible") return;
      inFlight = true;
      controller?.abort();
      controller = new AbortController();
      try {
        const response = await fetchConnectionMetrics(controller.signal);
        if (!stopped) {
          setState((current) => ({ ...current, connectionMetrics: response.metrics ?? {} }));
        }
      } catch {
        // Keep the last helper snapshot; stale entries render as unavailable on the next response.
      } finally {
        inFlight = false;
        scheduleNext();
      }
    };

    const handleVisibility = () => {
      if (document.visibilityState === "visible") {
        void refreshConnections();
      } else {
        clearTimer();
        controller?.abort();
      }
    };

    void refreshConnections();
    document.addEventListener("visibilitychange", handleVisibility);

    return () => {
      stopped = true;
      clearTimer();
      controller?.abort();
      document.removeEventListener("visibilitychange", handleVisibility);
    };
  }, [activeTab]);

  useEffect(() => {
    let ws: WebSocket | null = null;
    let reconnectTimer: number | undefined;
    let stopped = false;

    function clearReconnectTimer() {
      if (reconnectTimer !== undefined) {
        window.clearTimeout(reconnectTimer);
        reconnectTimer = undefined;
      }
    }

    function applySnapshot(event: MessageEvent) {
      try {
        const snapshot = JSON.parse(event.data as string) as RealtimeSnapshot;
        if (snapshot.type !== "realtime") return;
        if (snapshot.servers) void refreshUserSpeeds(snapshot.servers);
        setState((current) => ({
          ...current,
          servers: snapshot.servers ? mergeServerSnapshots(current.servers, snapshot.servers) : current.servers,
          summary: snapshot.trafficSummary ?? current.summary,
          adminTraffic: snapshot.adminTraffic ?? current.adminTraffic,
          nodes: snapshot.nodeTotals?.items ?? current.nodes,
          userConnections: snapshot.userConnections ?? current.userConnections,
        }));
      } catch {
        // Ignore malformed websocket frames; the dashboard will keep the last valid snapshot.
      }
    }

    function scheduleReconnect() {
      if (stopped || reconnectTimer !== undefined || document.visibilityState === "hidden") return;
      reconnectTimer = window.setTimeout(() => {
        reconnectTimer = undefined;
        openSocket();
      }, 2000);
    }

    function openSocket() {
      if (stopped || ws?.readyState === WebSocket.OPEN || ws?.readyState === WebSocket.CONNECTING) return;
      const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
      const socket = new WebSocket(`${protocol}//${window.location.host}/api/ws/dashboard?token=${encodeURIComponent(session.token)}`);
      ws = socket;
      socket.onmessage = applySnapshot;
      socket.onclose = () => {
        if (ws === socket) ws = null;
        scheduleReconnect();
      };
      socket.onerror = () => {
        socket.close();
      };
    }

    function reconnectNow() {
      clearReconnectTimer();
      if (ws) {
        const socket = ws;
        ws = null;
        socket.onclose = null;
        socket.onerror = null;
        socket.close();
      }
      openSocket();
    }

    function handleVisible() {
      if (document.visibilityState === "visible") reconnectNow();
    }

    openSocket();
    document.addEventListener("visibilitychange", handleVisible);
    window.addEventListener("focus", reconnectNow);

    return () => {
      stopped = true;
      clearReconnectTimer();
      document.removeEventListener("visibilitychange", handleVisible);
      window.removeEventListener("focus", reconnectNow);
      ws?.close();
    };
  }, [refreshUserSpeeds, session.token]);

  const totals = useMemo(() => calculateTotals(state.servers), [state.servers]);
  const topNodes = useMemo(() => [...state.nodes].sort(byTraffic).slice(0, 5), [state.nodes]);
  const topUsers = useMemo(() => [...state.users].sort(byUserTraffic).slice(0, 5), [state.users]);
  const selectedServer = useMemo(() => {
    if (state.servers.length === 0) return undefined;
    return state.servers.find((server) => server.id === selectedServerId) ?? state.servers[0];
  }, [selectedServerId, state.servers]);

  useEffect(() => {
    if (state.servers.length === 0) {
      setSelectedServerId(null);
      return;
    }
    if (selectedServerId == null || !state.servers.some((server) => server.id === selectedServerId)) {
      setSelectedServerId(state.servers[0].id);
    }
  }, [selectedServerId, state.servers]);

  const runXrayAction = useCallback(
    async (action: "start" | "stop" | "restart", targetServer = selectedServer) => {
      if (!targetServer || xrayActionBusy) return;
      const label = action === "restart" ? "重启" : action === "stop" ? "停止" : "启动";
      if (!window.confirm(`确认${label} ${targetServer.name} 的 Xray？`)) return;
      setXrayActionBusy(true);
      setError("");
      try {
        await controlRemoteService(session.token, targetServer.id, "xray", action);
        await loadDashboard();
      } catch (err) {
        setError(err instanceof Error ? err.message : `${label} Xray 失败`);
      } finally {
        setXrayActionBusy(false);
      }
    },
    [loadDashboard, selectedServer, session.token, xrayActionBusy],
  );

  return (
    <main className="app-shell">
      {activeTab !== "services" && (
        <header className="topbar compact">
          <button className="round-button" type="button" onClick={() => setMenuOpen(true)} aria-label="打开菜单">
            <Menu />
          </button>
        </header>
      )}

      {menuOpen && (
        <SideMenu
          session={session}
          dark={dark}
          activeTab={activeTab}
          onSelectTab={(tab) => {
            setActiveTab(tab);
            setMenuOpen(false);
          }}
          onToggleTheme={onToggleTheme}
          onClose={() => setMenuOpen(false)}
          onLogout={onLogout}
        />
      )}

      {error && (
        <section className="notice-card">
          <span>{error}</span>
          <button type="button" onClick={() => void loadDashboard()}>
            重试
          </button>
        </section>
      )}

      {activeTab === "services" ? (
        <ServiceManagementPage
          servers={state.servers}
          connectionMetrics={state.connectionMetrics}
          totals={totals}
          viewMode={serviceViewMode}
          onViewModeChange={setServiceViewMode}
          onOpenGlobalMenu={() => setMenuOpen(true)}
          onOpenMenu={setServiceMenuServer}
          onOpenDialog={(kind, server) => setServiceDialog({ kind, server })}
        />
      ) : activeTab !== "overview" ? (
        <Placeholder title={tabTitle(activeTab)} />
      ) : (
        <div className="dashboard-content" aria-busy={loading}>
          <section className="metric-grid">
            <SystemStatusCard metrics={state.systemMetrics} />
            <XrayStatusCard server={selectedServer} busy={xrayActionBusy} onAction={runXrayAction} />
          </section>

          <TrafficChart summary={state.summary} />
          <NodeView nodes={topNodes} />
          <UserView users={topUsers} connections={state.userConnections} speeds={state.userSpeeds} />
          <ServerOverview server={selectedServer} upload={totals.upload} download={totals.download} />
        </div>
      )}

      {serviceMenuServer && (
        <ServerActionsLayer
          server={serviceMenuServer}
          onClose={() => setServiceMenuServer(null)}
          onOpenDialog={(kind) => {
            setServiceDialog({ kind, server: serviceMenuServer });
            setServiceMenuServer(null);
          }}
          onXrayAction={(action) => {
            setServiceMenuServer(null);
            void runXrayAction(action, serviceMenuServer);
          }}
          connectionMetric={state.connectionMetrics[String(serviceMenuServer.id)]}
        />
      )}

      {serviceDialog && (
        <ServiceDialog
          dialog={serviceDialog}
          onClose={() => setServiceDialog(null)}
          onXrayAction={(action) => {
            void runXrayAction(action, serviceDialog.server);
          }}
          xrayActionBusy={xrayActionBusy}
          sessionToken={session.token}
          connectionMetric={serviceDialog.server ? state.connectionMetrics[String(serviceDialog.server.id)] : undefined}
        />
      )}
    </main>
  );
}

function SystemStatusCard({ metrics }: { metrics: SystemMetricsData | null }) {
  const cpuDetail = metrics?.cpu_cores ? `${metrics.cpu_cores} Core` : "-- Core";
  const cpu = metricFromPercent(metrics?.cpu_pct, "CPU", cpuDetail);
  const memory = metricFromUsage(metrics?.mem_used, metrics?.mem_total, "内存");
  const swap = metricFromUsage(metrics?.swap_used, metrics?.swap_total, "交换空间");
  const disk = metricFromUsage(metrics?.disk_used, metrics?.disk_total, "存储");

  return (
    <section className="panel-card system-status-card" aria-label="系统状态">
      <div className="status-card-source">
        <span>系统状态</span>
        <strong>主控本机</strong>
      </div>
      <div className="system-metric-grid">
        <SystemGauge metric={cpu} tone="blue" />
        <SystemGauge metric={memory} tone={memory.percent >= 80 ? "orange" : "blue"} />
        <SystemGauge metric={swap} tone="neutral" />
        <SystemGauge metric={disk} tone={disk.percent >= 80 ? "orange" : "blue"} />
      </div>
    </section>
  );
}

function XrayStatusCard({
  server,
  busy,
  onAction,
}: {
  server?: RemoteServer;
  busy: boolean;
  onAction: (action: "start" | "stop" | "restart") => void;
}) {
  const state = xrayState(server);
  const version = server?.xray_version ? `v${stripVersionPrefix(server.xray_version)}` : "版本未知";
  const serviceAction = server?.xray_running ? "stop" : "start";
  const controlDisabled = !server || busy;
  const settingsTitle = "当前新版前端还没有已迁移的 Xray 设置入口";

  return (
    <section className="panel-card xray-card" aria-label="Xray 状态">
      <div className="xray-card-main">
        <div className="xray-title-group">
          <h2>Xray</h2>
          <span className="xray-version">{version}</span>
        </div>
        <div className="xray-state">
          <span className={`xray-state-dot ${state.kind}`} />
          <span>{state.label}</span>
        </div>
      </div>
      <div className="xray-actions" aria-label="Xray 操作">
        <button type="button" disabled={controlDisabled} onClick={() => onAction(serviceAction)} aria-label={server?.xray_running ? "停止 Xray" : "启动 Xray"}>
          <Power />
        </button>
        <button type="button" disabled={controlDisabled} onClick={() => onAction("restart")} aria-label="重启 Xray">
          <RotateCw />
        </button>
        <button type="button" disabled title={settingsTitle} aria-label={settingsTitle}>
          <Wrench />
        </button>
      </div>
    </section>
  );
}

type SystemMetric = {
  label: string;
  percent: number;
  percentText: string;
  detail: string;
};

function SystemGauge({ metric, tone }: { metric: SystemMetric; tone: "blue" | "orange" | "neutral" }) {
  const radius = 42;
  const circumference = 2 * Math.PI * radius;
  const arc = circumference * 0.72;
  const progress = arc * clampPercent(metric.percent) / 100;

  return (
    <div className="system-metric">
      <svg className={`gauge-ring ${tone}`} viewBox="0 0 110 86" role="img" aria-label={`${metric.label} ${metric.percentText}`}>
        <circle className="gauge-rail" cx="55" cy="55" r={radius} pathLength={circumference} />
        <circle
          className="gauge-value"
          cx="55"
          cy="55"
          r={radius}
          pathLength={circumference}
          strokeDasharray={`${progress} ${circumference - progress}`}
        />
        <text x="55" y="58" textAnchor="middle">
          {metric.percentText}
        </text>
      </svg>
      <div className="system-metric-label">
        <strong>{metric.label}:</strong> <span>{metric.detail}</span>
      </div>
    </div>
  );
}

function TrafficChart({ summary }: { summary: TrafficSummary | null }) {
  const data = summary?.history?.map((item) => ({
    date: item.date.slice(5),
    used: item.used_gb,
  })) ?? [];

  return (
    <section className="panel-card chart-card">
      <div className="panel-header">
        <div>
          <h2>每日流量趋势</h2>
          <p>最近记录的日度流量趋势</p>
        </div>
        <div className="segmented">
          <button>今天</button>
          <button>本周</button>
          <button className="active">本月</button>
        </div>
      </div>
      <div className="chart-box">
        {data.length > 0 ? (
          <ResponsiveContainer width="100%" height="100%">
            <AreaChart data={data} margin={{ left: 0, right: 12, top: 18, bottom: 4 }}>
              <defs>
                <linearGradient id="trafficGradient" x1="0" x2="0" y1="0" y2="1">
                  <stop offset="5%" stopColor="#0958d9" stopOpacity={0.26} />
                  <stop offset="95%" stopColor="#0958d9" stopOpacity={0.03} />
                </linearGradient>
              </defs>
              <CartesianGrid stroke="#eef0f6" vertical={false} />
              <XAxis dataKey="date" tick={{ fontSize: 15, fill: "#8a8f9d" }} tickLine={false} axisLine={false} />
              <YAxis tick={{ fontSize: 15, fill: "#8a8f9d" }} tickLine={false} axisLine={false} width={42} />
              <Tooltip formatter={(value) => [`${value} GB`, "流量"]} />
              <Area type="monotone" dataKey="used" stroke="#0958d9" strokeWidth={2.5} fill="url(#trafficGradient)" connectNulls={false} />
            </AreaChart>
          </ResponsiveContainer>
        ) : (
          <EmptyText>暂无历史记录</EmptyText>
        )}
      </div>
    </section>
  );
}

function NodeView({ nodes }: { nodes: NodeTrafficItem[] }) {
  return (
    <section className="panel-card">
      <PanelTitle icon={<Server />} title="节点视图" subtitle="按流量排序" />
      <div className="list-stack">
        {nodes.length ? nodes.map((node) => <TrafficRow key={node.node_id} name={node.node_name} up={node.uplink} down={node.downlink} badge={node.server_name} />) : <EmptyText>暂无节点数据</EmptyText>}
      </div>
    </section>
  );
}

function UserView({
  users,
  connections,
  speeds,
}: {
  users: UserTrafficSummary[];
  connections: Record<string, number>;
  speeds: Record<string, number>;
}) {
  return (
    <section className="panel-card">
      <PanelTitle icon={<Users />} title="用户视图" subtitle="按流量排序" />
      <div className="list-stack">
        {users.length ? (
          users.map((user) => (
            <TrafficRow
              key={user.username}
              name={user.username}
              up={user.cycle_uplink}
              down={user.cycle_downlink}
              badge={formatUserRealtime(connections[user.username], speeds[user.username])}
            />
          ))
        ) : (
          <EmptyText>暂无用户数据</EmptyText>
        )}
      </div>
    </section>
  );
}

function ServerOverview({ server, upload, download }: { server?: RemoteServer; upload: number; download: number }) {
  const usedGB = server?.traffic_used ? server.traffic_used / 1024 / 1024 / 1024 : 0;
  const limitGB = server?.traffic_limit && server.traffic_limit > 0 ? server.traffic_limit / 1024 / 1024 / 1024 : null;
  const remainingGB = limitGB == null ? null : Math.max(0, limitGB - usedGB);
  const usage = limitGB ? (usedGB / limitGB) * 100 : null;

  return (
    <section className="panel-card server-card">
      <div className="panel-header compact">
        <h2>服务器概览</h2>
        <div className="speed-pair">
          <span>↑ {formatSpeed(upload)}</span>
          <span>↓ {formatSpeed(download)}</span>
        </div>
      </div>
      {server ? (
        <>
          <h3>{server.name}</h3>
          <div className="server-metrics">
            <InfoBlock label="已用" value={formatGB(usedGB)} />
            <InfoBlock label="总量" value={limitGB == null ? "无限" : formatGB(limitGB)} />
            <InfoBlock label="剩余" value={remainingGB == null ? "--" : formatGB(remainingGB)} />
            <InfoBlock label="使用率" value={usage == null ? "--" : formatPercent(usage)} />
          </div>
        </>
      ) : (
        <EmptyText>暂无服务器数据</EmptyText>
      )}
    </section>
  );
}

function ServiceManagementPage({
  servers,
  connectionMetrics,
  totals,
  viewMode,
  onViewModeChange,
  onOpenGlobalMenu,
  onOpenMenu,
  onOpenDialog,
}: {
  servers: RemoteServer[];
  connectionMetrics: Record<string, ConnectionMetric>;
  totals: { upload: number; download: number };
  viewMode: "grid" | "list";
  onViewModeChange: (mode: "grid" | "list") => void;
  onOpenGlobalMenu: () => void;
  onOpenMenu: (server: RemoteServer) => void;
  onOpenDialog: (kind: "add" | "access" | "edit" | "xray" | "agent" | "helper", server?: RemoteServer) => void;
}) {
  const online = servers.filter(isServerOnline).length;
  const offline = Math.max(0, servers.length - online);

  return (
    <div className="service-page">
      <header className="service-header">
        <button className="round-button service-menu-button" type="button" onClick={onOpenGlobalMenu} aria-label="打开菜单">
          <Menu />
        </button>
        <div className="service-title-block">
          <h1>服务管理</h1>
          <p>管理远程服务器、Agent 和 Xray 配置</p>
        </div>
        <div className="service-header-actions">
          <div className="service-view-toggle" aria-label="视图切换">
            <button className={viewMode === "grid" ? "active" : ""} type="button" onClick={() => onViewModeChange("grid")} aria-label="网格视图">
              <LayoutGrid />
            </button>
            <button className={viewMode === "list" ? "active" : ""} type="button" onClick={() => onViewModeChange("list")} aria-label="列表视图">
              <List />
            </button>
          </div>
          <button className="service-primary-button" type="button" onClick={() => onOpenDialog("add")}>
            <Plus />
            <span>添加</span>
          </button>
          <button className="service-icon-button service-page-more" type="button" onClick={() => onOpenDialog("access")} aria-label="页面更多">
            <MoreHorizontal />
          </button>
        </div>
      </header>

      <section className="service-summary-grid" aria-label="服务统计">
        <ServiceSummaryItem icon={<span aria-hidden="true">●</span>} value={String(online)} tone="success" label="在线" />
        <ServiceSummaryItem icon={<span aria-hidden="true">●</span>} value={String(offline)} tone="danger" label="离线" />
        <ServiceSummaryItem icon={<ArrowUp />} value={formatSpeed(totals.upload)} tone="upload" label="上传" />
        <ServiceSummaryItem icon={<ArrowDown />} value={formatSpeed(totals.download)} tone="download" label="下载" />
      </section>

      <section className={`service-server-list ${viewMode}`}>
        {servers.length ? (
          servers.map((server) => (
            <ServiceServerCard
              key={server.id}
              server={server}
              connectionMetric={connectionMetrics[String(server.id)]}
              viewMode={viewMode}
              onOpenMenu={() => onOpenMenu(server)}
              onOpenDialog={(kind) => onOpenDialog(kind, server)}
            />
          ))
        ) : (
          <section className="panel-card">
            <EmptyText>暂无服务器数据</EmptyText>
          </section>
        )}
      </section>
    </div>
  );
}

function ServiceSummaryItem({ icon, value, tone, label }: { icon: React.ReactNode; value: string; tone: "success" | "danger" | "upload" | "download"; label: string }) {
  const [mainValue, ...unitParts] = value.split(" ");
  const unit = unitParts.join(" ");

  return (
    <div className={`service-summary-item ${tone}`} aria-label={`${label} ${value}`} title={`${label} ${value}`}>
      <span className="service-summary-icon">{icon}</span>
      <span className="service-summary-value">
        <strong>{mainValue}</strong>
        {unit && <em>{unit}</em>}
      </span>
    </div>
  );
}

function ServiceServerCard({
  server,
  connectionMetric,
  viewMode,
  onOpenMenu,
  onOpenDialog,
}: {
  server: RemoteServer;
  connectionMetric?: ConnectionMetric;
  viewMode: "grid" | "list";
  onOpenMenu: () => void;
  onOpenDialog: (kind: "edit" | "xray" | "agent" | "helper") => void;
}) {
  const address = serverRegionAddress(server);
  const regionFieldKey = [server.country_code, server.country, server.region, server.region_name].join("|");
  const [region, setRegion] = useState(() => serverRegionFromFields(server) ?? (address ? loadingRegion() : unknownRegion()));

  useEffect(() => {
    const controller = new AbortController();
    let mounted = true;
    const fromFields = serverRegionFromFields(server);

    if (fromFields) {
      setRegion(fromFields);
      return () => {
        mounted = false;
        controller.abort();
      };
    }

    if (!address) {
      setRegion(unknownRegion());
      return () => {
        mounted = false;
        controller.abort();
      };
    }

    setRegion(loadingRegion());
    lookupServerRegion(server, controller.signal).then((nextRegion) => {
      if (mounted && !controller.signal.aborted) {
        setRegion(nextRegion);
      }
    });

    return () => {
      mounted = false;
      controller.abort();
    };
  }, [address, regionFieldKey]);

  return (
    <article className={`service-server-card ${viewMode}`}>
      <div className="service-server-main">
        <div className="service-server-head">
          <span className={`service-status-dot ${serverStatusKind(server)}`} aria-label={isServerOnline(server) ? "服务器在线" : "服务器离线"} />
          <div className="service-server-identity">
            <div className={`service-location ${region.known ? "known" : ""}`}>
              {region.flag && <span className="service-location-flag" aria-hidden="true">{region.flag}</span>}
              <span>{region.label}</span>
            </div>
            <h2>{server.name}</h2>
          </div>
          <button className="service-card-more" type="button" onClick={onOpenMenu} aria-label={`${server.name} 更多操作`}>
            <MoreHorizontal />
          </button>
        </div>

        <div className="service-badges">
          <span className="service-badge success">{formatXrayMode(server.xray_mode)}</span>
          <span className="service-badge success">Agent {agentVersion(server)}</span>
          <span className="service-badge success connection-tag" aria-label={`连接数 ${formatConnectionCount(connectionMetric)}`} title="连接数">
            <span className="connection-tag-icon" aria-hidden="true">🔌</span>
            <span className="connection-tag-value">{formatConnectionCount(connectionMetric)}</span>
          </span>
          {server.ddns_pending && <span className="service-badge warning">DDNS 待同步</span>}
        </div>

        <div className="service-v3-metrics">
          <div className="service-v3-row service-v3-row-stats">
            <div className="service-v3-column service-v3-column-speeds">
              <ServiceMetricLine icon={<ArrowUp />} label="上传速度" tone="upload">
                {formatSpeed(server.current_upload_speed ?? 0)}
              </ServiceMetricLine>
              <ServiceMetricLine icon={<ArrowDown />} label="下载速度" tone="download">
                {formatSpeed(server.current_download_speed ?? 0)}
              </ServiceMetricLine>
            </div>
            <div className="service-v3-column service-v3-column-traffic">
              <ServiceMetricLine label="已用流量 / 总流量" title={trafficResetText(server)}>
                <span>{formatBytes(server.traffic_used ?? 0)}</span>
                <span aria-hidden="true"> / </span>
                <span>{trafficLimitText(server)}</span>
              </ServiceMetricLine>
              <TrafficRemainingBar server={server} />
            </div>
          </div>
        </div>
      </div>

      <div className="service-card-actions">
        <button type="button" onClick={() => onOpenDialog("xray")}>
          <TerminalSquare />
          <span>Xray 配置</span>
        </button>
        <button type="button" onClick={() => onOpenDialog("agent")}>
          <Wrench />
          <span>Agent 管理</span>
        </button>
        <button type="button" onClick={onOpenMenu}>
          <MoreHorizontal />
          <span>更多操作</span>
        </button>
      </div>
    </article>
  );
}

function TrafficRemainingBar({ server }: { server: RemoteServer }) {
  const remainingPercent = trafficRemainingPercent(server);

  return (
    <div className="service-v3-traffic-bar" aria-hidden="true">
      <span style={remainingPercent == null ? undefined : { width: `${remainingPercent}%` }} />
    </div>
  );
}

function ServiceMetricLine({
  icon,
  label,
  title,
  tone,
  children,
}: {
  icon?: React.ReactNode;
  label: string;
  title?: string;
  tone?: "upload" | "download";
  children: React.ReactNode;
}) {
  return (
    <div className={`service-v3-metric-line${tone ? ` ${tone}` : ""}`} aria-label={label} title={title || label}>
      {icon && <span className="service-v3-icon">{icon}</span>}
      <span className="service-v3-inline-value">{children}</span>
    </div>
  );
}

function HelperInstallDialog({ server, sessionToken, connectionMetric }: { server: RemoteServer; sessionToken: string; connectionMetric?: ConnectionMetric }) {
  const [install, setInstall] = useState<HelperInstallTokenResponse | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const helper = helperStatus(connectionMetric);

  const generate = useCallback(async () => {
    setBusy(true);
    setError("");
    try {
      const response = await createHelperInstallToken(sessionToken, server.id);
      setInstall(response);
    } catch (err) {
      setError(err instanceof Error ? err.message : "生成安装命令失败");
    } finally {
      setBusy(false);
    }
  }, [server.id, sessionToken]);

  return (
    <div className="service-dialog-body">
      <div className="helper-status-grid">
        <InfoBlock label="状态" value={helper.label} />
        <InfoBlock label="版本" value={helper.version || "--"} />
        <InfoBlock label="最近上报" value={helper.updatedAt ? formatRelativeTime(helper.updatedAt) : "--"} />
      </div>
      <div className="service-dialog-section">
        <h4>安装 Connections Helper</h4>
        <p>该命令只绑定当前服务器：{server.name}。安装链接短期有效且只能使用一次，不会修改官方 mmw-agent。</p>
        <button className="helper-generate-button" type="button" disabled={busy} onClick={() => void generate()}>
          {busy ? "生成中..." : "生成安装命令"}
        </button>
        {error && <p className="helper-error">{error}</p>}
        {install && (
          <div className="helper-command-box">
            <p>过期时间：{formatDateTime(install.expires_at)}</p>
            <code>{install.command}</code>
            <button type="button" onClick={() => void copyText(install.command)}>复制命令</button>
          </div>
        )}
      </div>
    </div>
  );
}

function ServerActionsLayer({
  server,
  connectionMetric,
  onClose,
  onOpenDialog,
  onXrayAction,
}: {
  server: RemoteServer;
  connectionMetric?: ConnectionMetric;
  onClose: () => void;
  onOpenDialog: (kind: "edit" | "xray" | "agent" | "helper") => void;
  onXrayAction: (action: "start" | "stop" | "restart") => void;
}) {
  const helper = helperStatus(connectionMetric);

  return (
    <div className="service-action-layer" role="presentation" onClick={onClose}>
      <div className="service-action-sheet" role="dialog" aria-label={`${server.name} 更多操作`} onClick={(event) => event.stopPropagation()}>
        <div className="service-action-head">
          <div>
            <h3>{server.name}</h3>
            <p>更多操作</p>
          </div>
          <button className="service-icon-button" type="button" onClick={onClose} aria-label="关闭">
            <X />
          </button>
        </div>
        <ActionGroup title="服务器">
          <ActionButton icon={<Edit3 />} label="编辑服务器" onClick={() => onOpenDialog("edit")} />
          <ActionButton icon={<Share2 />} label="分享 / 接入服务器" badge="PRO" disabled />
          <ActionButton icon={<Search />} label="扫描远程服务" disabled />
        </ActionGroup>
        <ActionGroup title="节点同步">
          <ActionButton icon={<RefreshCw />} label="同步节点" disabled />
          <ActionButton icon={<Globe2 />} label="同步节点地址" disabled />
          <ActionButton icon={<Clock3 />} label="配置历史" disabled />
          <ActionButton icon={<Database />} label="下发默认配置" disabled />
          <ActionButton icon={<Plus />} label="添加网站" disabled />
        </ActionGroup>
        <ActionGroup title="Xray 管理">
          <ActionButton icon={<TerminalSquare />} label="配置 / 入站 / 出站 / 路由" onClick={() => onOpenDialog("xray")} />
          <ActionButton icon={<Gauge />} label="指标 / 流量统计 / gRPC" onClick={() => onOpenDialog("xray")} />
        </ActionGroup>
        <ActionGroup title="Agent 管理">
          <ActionButton icon={<Wrench />} label="Agent 管理" onClick={() => onOpenDialog("agent")} />
          <ActionButton icon={<RefreshCw />} label="升级 Agent" danger disabled />
        </ActionGroup>
        <ActionGroup title="Connections Helper">
          <ActionButton
            icon={<Gauge />}
            label={`Helper ${helper.label}${helper.version ? ` ${helper.version}` : ""}`}
            badge={helper.updatedAt ? formatRelativeTime(helper.updatedAt) : undefined}
            onClick={() => onOpenDialog("helper")}
          />
          <ActionButton icon={<TerminalSquare />} label="生成安装命令" onClick={() => onOpenDialog("helper")} />
        </ActionGroup>
        <ActionGroup title="危险操作">
          <ActionButton icon={<Power />} label={server.xray_running ? "停止 Xray" : "启动 Xray"} danger onClick={() => onXrayAction(server.xray_running ? "stop" : "start")} />
          <ActionButton icon={<RotateCw />} label="重启 Xray" danger onClick={() => onXrayAction("restart")} />
          <ActionButton icon={<Trash2 />} label="卸载 Agent" danger disabled />
          <ActionButton icon={<Trash2 />} label="删除服务器" danger disabled />
        </ActionGroup>
      </div>
    </div>
  );
}

function ActionGroup({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="service-action-group">
      <h4>{title}</h4>
      <div>{children}</div>
    </section>
  );
}

function ActionButton({ icon, label, badge, danger, disabled, onClick }: { icon: React.ReactNode; label: string; badge?: string; danger?: boolean; disabled?: boolean; onClick?: () => void }) {
  return (
    <button className={`service-action-row${danger ? " danger" : ""}`} type="button" disabled={disabled} onClick={onClick}>
      {icon}
      <span>{label}</span>
      {badge && <em>{badge}</em>}
      {disabled && <small>暂未迁移</small>}
    </button>
  );
}

function ServiceDialog({
  dialog,
  onClose,
  onXrayAction,
  xrayActionBusy,
  sessionToken,
  connectionMetric,
}: {
  dialog: { kind: "add" | "access" | "edit" | "xray" | "agent" | "helper"; server?: RemoteServer };
  onClose: () => void;
  onXrayAction: (action: "start" | "stop" | "restart") => void;
  xrayActionBusy: boolean;
  sessionToken: string;
  connectionMetric?: ConnectionMetric;
}) {
  const server = dialog.server;
  const title = {
    add: "添加服务器",
    access: "接入分享服务器",
    edit: "编辑远程服务器",
    xray: "Xray 管理",
    agent: "Agent 管理",
    helper: "Connections Helper",
  }[dialog.kind];

  return (
    <div className="service-dialog-layer" role="presentation" onClick={onClose}>
      <section className="service-dialog" role="dialog" aria-label={title} onClick={(event) => event.stopPropagation()}>
        <div className="service-dialog-head">
          <h3>{title}</h3>
          <button className="service-icon-button" type="button" onClick={onClose} aria-label="关闭">
            <X />
          </button>
        </div>

        {dialog.kind === "xray" && server ? (
          <div className="service-dialog-body">
            <div className="service-tabs">
              <button className="active">Config</button>
              <button>Inbounds</button>
              <button>Outbounds</button>
              <button>Routing</button>
            </div>
            <div className="service-dialog-section">
              <h4>Service Control</h4>
              <div className="service-inline-actions">
                <button type="button" disabled={xrayActionBusy} onClick={() => onXrayAction("start")}>Start</button>
                <button type="button" disabled={xrayActionBusy} onClick={() => onXrayAction("stop")}>Stop</button>
                <button type="button" disabled={xrayActionBusy} onClick={() => onXrayAction("restart")}>Restart</button>
              </div>
              <p>{xrayState(server).label}{server.xray_version ? ` (${server.xray_version})` : ""}</p>
            </div>
            <div className="service-dialog-section">
              <h4>Metrics / Traffic Stats / gRPC</h4>
              <p>入口已保留；具体配置编辑继续沿用官方接口迁移。</p>
            </div>
          </div>
        ) : dialog.kind === "agent" && server ? (
          <div className="service-dialog-body">
            <div className="service-form-grid">
              <InfoBlock label="Agent 版本" value={agentVersion(server)} />
            </div>
            <p className="service-dialog-note">Agent 升级、卸载等危险操作入口已在更多操作中保留，正式执行前必须继续走原确认流程。</p>
          </div>
        ) : dialog.kind === "helper" && server ? (
          <HelperInstallDialog server={server} sessionToken={sessionToken} connectionMetric={connectionMetric} />
        ) : (
          <div className="service-dialog-body">
            <div className="service-dialog-section">
              <h4>{dialog.kind === "edit" ? "基本信息" : "功能入口"}</h4>
              <p>本次重构仅迁移服务管理视觉和入口层级，不自动保存或执行配置变更。</p>
            </div>
            <div className="service-form-grid">
              <label>
                <span>服务器名称</span>
                <input value={server?.name ?? ""} readOnly placeholder="服务器名称" />
              </label>
              <label>
                <span>服务器地址</span>
                <input value={displayServerAddress(server)} readOnly placeholder="服务器地址" />
              </label>
              <label>
                <span>Agent 端口</span>
                <input value={server?.listen_port ?? server?.pull_port ?? ""} readOnly placeholder="Agent 端口" />
              </label>
              <label>
                <span>Xray Mode</span>
                <input value={formatXrayMode(server?.xray_mode)} readOnly placeholder="Xray Mode" />
              </label>
            </div>
          </div>
        )}

        <div className="service-dialog-actions">
          <button type="button" onClick={onClose}>关闭</button>
          {dialog.kind === "edit" && <button type="button" disabled>保存</button>}
        </div>
      </section>
    </div>
  );
}

function SideMenu({
  session,
  dark,
  activeTab,
  onSelectTab,
  onToggleTheme,
  onClose,
  onLogout,
}: {
  session: Session;
  dark: boolean;
  activeTab: string;
  onSelectTab: (tab: string) => void;
  onToggleTheme: () => void;
  onClose: () => void;
  onLogout: () => void;
}) {
  const menuItems = [
    { key: "overview", label: "概览", icon: Home },
    { key: "nodes", label: "节点", icon: Boxes },
    { key: "users", label: "用户", icon: Users },
    { key: "services", label: "服务管理", icon: Server },
    { key: "settings", label: "设置", icon: Settings },
    { key: "inbounds", label: "入站", icon: LogIn },
    { key: "outbounds", label: "出站", icon: LogOut },
    { key: "routing", label: "路由", icon: Route },
    { key: "xray", label: "Xray 配置", icon: Wrench, expandable: true },
  ];

  return (
    <div className="menu-layer" role="presentation" onClick={onClose}>
      <aside className="side-menu" role="dialog" aria-label="菜单" onClick={(event) => event.stopPropagation()}>
        <div className="menu-top">
          <h2>妙妙屋 X</h2>
          <div className="menu-top-actions">
            <button className="ghost-button" type="button" onClick={onToggleTheme} aria-label="切换主题">
              {dark ? <Sun /> : <Moon />}
            </button>
            <button className="avatar-button compact" type="button" onClick={onLogout} aria-label="退出登录">
              {session.avatarUrl ? <img src={session.avatarUrl} alt="" /> : <span>{session.nickname?.[0] || session.username[0]}</span>}
            </button>
          </div>
          <button className="ghost-button" onClick={onClose} type="button">
            <X />
          </button>
        </div>
        <div className="menu-profile">
          <div className="avatar-large">{session.avatarUrl ? <img src={session.avatarUrl} alt="" /> : session.username[0]}</div>
          <strong>{session.nickname || session.username}</strong>
          <span>{session.role}</span>
        </div>
        {menuItems.map(({ key, label, icon: Icon, expandable }) => (
          <button className={`menu-item${activeTab === key ? " active" : ""}`} key={key} type="button" onClick={() => onSelectTab(key)}>
            <Icon />
            <span>{label}</span>
            {expandable && <ChevronDown className="menu-item-chevron" />}
          </button>
        ))}
        <button className="menu-item danger" type="button" onClick={onLogout}>
          <LogOut />
          <span>退出登录</span>
        </button>
      </aside>
    </div>
  );
}

function PanelTitle({ icon, title, subtitle }: { icon: React.ReactNode; title: string; subtitle: string }) {
  return (
    <div className="panel-header">
      <div>
        <h2>
          {icon}
          {title}
        </h2>
        <p>{subtitle}</p>
      </div>
      <button className="more-button" type="button">
        更多
      </button>
    </div>
  );
}

function TrafficRow({ name, up, down, badge }: { name: string; up: number; down: number; badge?: string }) {
  return (
    <div className="traffic-row">
      <div>
        <strong>{name}</strong>
        {badge && <span>{badge}</span>}
      </div>
      <div className="traffic-values">
        <span>↑ {formatBytes(up)}</span>
        <span>↓ {formatBytes(down)}</span>
      </div>
    </div>
  );
}

function InfoBlock({ label, value }: { label: string; value: string }) {
  return (
    <div className="info-block">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function EmptyText({ children }: { children: React.ReactNode }) {
  return <div className="empty-text">{children}</div>;
}

function Placeholder({ title }: { title: string }) {
  return (
    <section className="panel-card placeholder-card">
      <h2>{title}</h2>
      <p>第一阶段先完成概览 Dashboard。此入口保留给后续页面迁移。</p>
    </section>
  );
}

function calculateTotals(servers: RemoteServer[]) {
  return servers.reduce(
    (acc, server) => ({
      upload: acc.upload + (server.current_upload_speed ?? 0),
      download: acc.download + (server.current_download_speed ?? 0),
    }),
    { upload: 0, download: 0 },
  );
}

function mergeServerSnapshots(current: RemoteServer[], snapshot: RemoteServer[]) {
  if (current.length === 0) return snapshot;
  const merged = new Map<number, RemoteServer>();
  current.forEach((server) => merged.set(server.id, server));
  snapshot.forEach((server) => {
    merged.set(server.id, { ...(merged.get(server.id) ?? {}), ...server });
  });
  return Array.from(merged.values());
}

function byTraffic(a: NodeTrafficItem, b: NodeTrafficItem) {
  return b.uplink + b.downlink - (a.uplink + a.downlink);
}

function byUserTraffic(a: UserTrafficSummary, b: UserTrafficSummary) {
  return b.cycle_uplink + b.cycle_downlink - (a.cycle_uplink + a.cycle_downlink);
}

function aggregateUserSpeeds(results: PromiseSettledResult<Awaited<ReturnType<typeof fetchUserSpeeds>>>[]) {
  const speeds: Record<string, number> = {};
  for (const result of results) {
    if (result.status !== "fulfilled") continue;
    for (const [username, speed] of Object.entries(result.value.user_speeds ?? {})) {
      speeds[username] = (speeds[username] ?? 0) + speed;
    }
  }
  return speeds;
}

function isServerOnline(server: RemoteServer) {
  return server.status === "connected" || server.ws_connected === true;
}

function isAgentOnline(server: RemoteServer) {
  return isServerOnline(server);
}

function serverStatusKind(server: RemoteServer) {
  if (isServerOnline(server)) return "online";
  if (server.status === "connecting") return "pending";
  return "offline";
}

function displayServerAddress(server?: RemoteServer) {
  if (!server) return "";
  return server.pull_address || server.domain || server.ip_address || server.ip_address_v6 || "--";
}

function formatXrayMode(mode?: string) {
  if (!mode) return "--";
  return mode === "embedded" ? "Embedded Xray" : mode === "external" ? "External Xray" : mode;
}

function formatXrayVersion(version: string) {
  return version.startsWith("v") ? version : `v${version}`;
}

function agentVersion(server: RemoteServer) {
  return server.agent_version ? formatXrayVersion(stripVersionPrefix(server.agent_version)) : "--";
}

function formatConnectionCount(metric?: ConnectionMetric) {
  return metric?.available && typeof metric.connection_count === "number" ? metric.connection_count.toLocaleString() : "--";
}

function helperStatus(metric?: ConnectionMetric) {
  if (!metric) return { label: "未安装", version: "", updatedAt: "" };
  if (!metric.available) {
    return {
      label: "离线 / 数据过期",
      version: metric.helper_version || "",
      updatedAt: metric.updated_at || "",
    };
  }
  return {
    label: "在线",
    version: metric.helper_version || "",
    updatedAt: metric.updated_at || "",
  };
}

async function copyText(value: string) {
  try {
    await navigator.clipboard?.writeText(value);
  } catch {
    const textarea = document.createElement("textarea");
    textarea.value = value;
    textarea.setAttribute("readonly", "true");
    textarea.style.position = "fixed";
    textarea.style.opacity = "0";
    document.body.appendChild(textarea);
    textarea.select();
    document.execCommand("copy");
    textarea.remove();
  }
}

function trafficLimitText(server: RemoteServer) {
  if (!server.traffic_limit || server.traffic_limit <= 0) return "无限";
  return formatBytes(server.traffic_limit);
}

function trafficRemainingPercent(server: RemoteServer) {
  const total = server.traffic_limit ?? 0;
  if (total <= 0) return null;
  const used = Math.max(server.traffic_used ?? 0, 0);
  const remaining = Math.max(total - used, 0);
  return Math.max(0, Math.min(100, (remaining / total) * 100));
}

function trafficResetText(server: RemoteServer) {
  const parts: string[] = [];
  if (server.traffic_reset_day) parts.push(`每月 ${server.traffic_reset_day} 日重置`);
  if (server.last_traffic_reset_at) parts.push(`上次重置 ${formatDateTime(server.last_traffic_reset_at)}`);
  return parts.join(" · ") || trafficLimitText(server);
}

function formatDateTime(value?: string | null) {
  if (!value) return "--";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function formatRelativeTime(value?: string | null) {
  if (!value) return "--";
  const date = new Date(value);
  const time = date.getTime();
  if (Number.isNaN(time)) return value;
  const seconds = Math.max(0, Math.floor((Date.now() - time) / 1000));
  if (seconds < 10) return "刚刚";
  if (seconds < 60) return `${seconds} 秒前`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.floor(hours / 24);
  return `${days} 天前`;
}

function formatUserRealtime(connections?: number, speed?: number) {
  if (!connections) return undefined;

  const parts: string[] = [];
  parts.push(`🔌 ${connections}`);
  if (speed) parts.push(`⚡ ${formatSpeed(speed)}`);
  return parts.join(" · ");
}

function formatPercent(value?: number | null) {
  if (value == null || Number.isNaN(value)) return "--";
  return `${value.toFixed(value >= 10 ? 1 : 2)}%`;
}

function metricFromPercent(value: number | undefined, label: string, detail: string): SystemMetric {
  const hasData = typeof value === "number" && Number.isFinite(value);
  const percent = hasData ? clampPercent(value) : 0;
  return {
    label,
    percent,
    percentText: hasData ? `${trimPercent(percent)}%` : "--",
    detail,
  };
}

function metricFromUsage(used: number | undefined, total: number | undefined, label: string): SystemMetric {
  const hasData = typeof used === "number" && typeof total === "number" && Number.isFinite(used) && Number.isFinite(total);
  const percent = hasData && total > 0 ? clampPercent((used / total) * 100) : 0;
  return {
    label,
    percent,
    percentText: hasData ? `${trimPercent(percent)}%` : "--",
    detail: hasData ? `${formatBytes(used)} / ${formatBytes(total)}` : "-- / --",
  };
}

function xrayState(server?: RemoteServer) {
  if (!server) return { kind: "unknown", label: "等待数据" };
  return server.xray_running ? { kind: "running", label: "运行中" } : { kind: "stopped", label: "已停止" };
}

function stripVersionPrefix(version: string) {
  return version.trim().replace(/^v/i, "");
}

function clampPercent(value: number) {
  return Math.max(0, Math.min(100, value));
}

function trimPercent(value: number) {
  return value.toFixed(2).replace(/\.?0+$/, "");
}

function tabTitle(tab: string) {
  const titles: Record<string, string> = {
    overview: "概览",
    nodes: "节点",
    users: "用户",
    subscriptions: "订阅",
    settings: "设置",
  };
  return titles[tab] ?? "概览";
}

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
