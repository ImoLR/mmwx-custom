import React, { useCallback, useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  BarChart3,
  Boxes,
  ChevronDown,
  Gauge,
  Globe2,
  Home,
  LogIn,
  LogOut,
  Menu,
  Moon,
  Power,
  RefreshCw,
  Route,
  RotateCw,
  Server,
  Settings,
  Sun,
  Tags,
  Wrench,
  X,
  UserRound,
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
import type {
  AdminTrafficResponse,
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
      const [summary, remoteServers, nodeTotals, users, connections, adminTraffic] = await Promise.all([
        fetchTrafficSummary(session.token),
        fetchRemoteServers(session.token),
        fetchNodeTotals(session.token, date),
        fetchUsers(session.token),
        fetchUserConnections(session.token),
        fetchAdminTraffic(session.token),
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
          servers: snapshot.servers ?? current.servers,
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
    async (action: "start" | "stop" | "restart") => {
      if (!selectedServer || xrayActionBusy) return;
      const label = action === "restart" ? "重启" : action === "stop" ? "停止" : "启动";
      if (!window.confirm(`确认${label} ${selectedServer.name} 的 Xray？`)) return;
      setXrayActionBusy(true);
      setError("");
      try {
        await controlRemoteService(session.token, selectedServer.id, "xray", action);
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
      <header className="topbar compact">
        <button className="round-button" type="button" onClick={() => setMenuOpen(true)} aria-label="打开菜单">
          <Menu />
        </button>
      </header>

      {menuOpen && <SideMenu session={session} dark={dark} onToggleTheme={onToggleTheme} onClose={() => setMenuOpen(false)} onLogout={onLogout} />}

      {error && (
        <section className="notice-card">
          <span>{error}</span>
          <button type="button" onClick={() => void loadDashboard()}>
            重试
          </button>
        </section>
      )}

      {activeTab !== "overview" ? (
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

      <nav className="bottom-nav" aria-label="主导航">
        {[
          ["overview", Home, "概览"],
          ["nodes", Boxes, "节点"],
          ["users", Users, "用户"],
          ["subscriptions", BarChart3, "订阅"],
          ["settings", Settings, "设置"],
        ].map(([key, Icon, label]) => (
          <button className={activeTab === key ? "active" : ""} key={String(key)} type="button" onClick={() => setActiveTab(String(key))}>
            <Icon />
            <span>{label as string}</span>
          </button>
        ))}
      </nav>
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

function SideMenu({
  session,
  dark,
  onToggleTheme,
  onClose,
  onLogout,
}: {
  session: Session;
  dark: boolean;
  onToggleTheme: () => void;
  onClose: () => void;
  onLogout: () => void;
}) {
  const menuItems = [
    { label: "系统状态", icon: Gauge, active: true },
    { label: "入站", icon: LogIn },
    { label: "客户端", icon: Users },
    { label: "分组", icon: Tags },
    { label: "节点", icon: Server },
    { label: "主机", icon: Globe2 },
    { label: "出站", icon: LogOut },
    { label: "路由", icon: Route },
    { label: "面板设置", icon: Settings, expandable: true },
    { label: "Xray 配置", icon: Wrench, expandable: true },
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
        {menuItems.map(({ label, icon: Icon, active, expandable }) => (
          <button className={`menu-item${active ? " active" : ""}`} key={label} type="button">
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
