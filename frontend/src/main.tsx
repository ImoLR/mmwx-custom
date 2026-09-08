import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  Boxes,
  ChevronDown,
  Clock3,
  CheckCircle2,
  Copy,
  Database,
  Edit3,
  Gauge,
  Globe2,
  Home,
  KeyRound,
  LayoutGrid,
  List,
  LogIn,
  LogOut,
  Menu,
  Moon,
  MoreHorizontal,
  Package as PackageIcon,
  Power,
  Plus,
  RefreshCw,
  Route,
  RotateCw,
  Search,
  Server,
  Share2,
  Settings,
  ShieldCheck,
  Sun,
  TerminalSquare,
  Tags,
  Trash2,
  UploadCloud,
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
  acceptXrayRecovery,
  addRemoteWebsite,
  addSharedRemoteServer,
  applyXrayRecovery,
  controlRemoteService,
  createRemoteServer,
  createHelperInstallToken,
  deleteRemoteWebsite,
  deployRemoteDefaultConfig,
  expectXrayRecovery,
  fetchAgentVersionInfo,
  fetchConnectionMetrics,
  fetchAdminTraffic,
  fetchDNSProviders,
  fetchMasterUrl,
  fetchLocalSystemMetrics,
  fetchNodeConnections,
  fetchRemoteServers,
  fetchRemoteSystemInfo,
  fetchRemoteWebsites,
  fetchTrafficPeriod,
  fetchTrafficSummary,
  fetchUserConnections,
  fetchUserSpeeds,
  fetchXrayRecoveryStatus,
  fetchXrayServiceStatus,
  fetchXraySnapshots,
  fetchValidCertificates,
  installRemoteNginx,
  loadSession,
  login,
  revealRemoteServerToken,
  restoreXraySnapshot,
  saveSession,
  streamAgentAction,
  syncRemoteNodeAddress,
  syncRemoteNodes,
  validateRemoteWebsite,
} from "./api";
import { formatBytes, formatDurationSince, formatSpeed } from "./format";
import { ConnectionsManager } from "./connections-manager";
import { loadingRegion, lookupServerRegion, serverRegionAddress, serverRegionFromFields, unknownRegion } from "./geo";
import type {
  AdminTrafficResponse,
  ConnectionMetric,
  AgentVersionInfo,
  HelperInstallTokenResponse,
  DNSProvider,
  NodeTrafficItem,
  PeriodUserTrafficItem,
  RealtimeSnapshot,
  RemoteServer,
  RemoteServerCreateRequest,
  RemoteSystemInfo,
  RemoteWebsitesResponse,
  Session,
  SystemMetrics as SystemMetricsData,
  TrafficRange,
  TrafficSummary,
  ValidCertificate,
  XrayRecoveryStatusResponse,
  XrayServiceStatusResponse,
  XraySnapshotItem,
} from "./types";
import { NodeManagementPage } from "./node-manager";
import { PackageManagementPage } from "./package-manager";
import { ForwardManagementPage } from "./forward-manager";
import { XrayManager } from "./xray-manager";
import "./styles.css";

type DashboardState = {
  summary: TrafficSummary | null;
  systemMetrics: SystemMetricsData | null;
  servers: RemoteServer[];
  nodes: NodeTrafficItem[];
  users: PeriodUserTrafficItem[];
  nodeConnections: Record<string, number>;
  userConnections: Record<string, number>;
  userSpeeds: Record<string, number>;
  adminTraffic: AdminTrafficResponse | null;
  connectionMetrics: Record<string, ConnectionMetric>;
  period: PeriodMeta | null;
};

type PeriodMeta = {
  range: TrafficRange;
  start: string;
  end: string;
  timezone: string;
  complete: boolean;
};

type ServiceGroup = {
  id: string;
  name: string;
  serverIds: number[];
};

const emptyState: DashboardState = {
  summary: null,
  systemMetrics: null,
  servers: [],
  nodes: [],
  users: [],
  nodeConnections: {},
  userConnections: {},
  userSpeeds: {},
  adminTraffic: null,
  connectionMetrics: {},
  period: null,
};

const ALL_SERVICE_GROUP_ID = "all";
const SERVICE_GROUPS_STORAGE_PREFIX = "mmwxc-service-groups:";
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
  const [serviceGroups, setServiceGroups] = useState<ServiceGroup[]>(() => loadServiceGroups(session.username));
  const [activeServiceGroupId, setActiveServiceGroupId] = useState(ALL_SERVICE_GROUP_ID);
  const [serviceGroupDialogOpen, setServiceGroupDialogOpen] = useState(false);
  const [serviceMenuServer, setServiceMenuServer] = useState<RemoteServer | null>(null);
  const [serviceDialog, setServiceDialog] = useState<{ kind: "add" | "access" | "edit" | "xray" | "agent" | "helper" | "connections" | "batch-agent"; server?: RemoteServer } | null>(null);
  const [trafficRange, setTrafficRange] = useState<TrafficRange>("today");
  const [periodLoading, setPeriodLoading] = useState(true);
  const [trafficDialog, setTrafficDialog] = useState<"nodes" | "users" | null>(null);
  const periodRequestId = useRef(0);

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
      const [summary, remoteServers, connections, adminTraffic, helperConnections] = await Promise.all([
        fetchTrafficSummary(session.token),
        fetchRemoteServers(session.token),
        fetchUserConnections(session.token),
        fetchAdminTraffic(session.token),
        fetchConnectionMetrics(),
      ]);

      const servers = remoteServers.servers ?? [];
      const [speedResults, agentVersionResults] = await Promise.all([
        Promise.allSettled(servers.map((server) => fetchUserSpeeds(session.token, server.id))),
        Promise.allSettled(servers.map((server) => fetchAgentVersionInfo(session.token, server.id))),
      ]);
      const serversWithAgentVersions = servers.map((server, index) => {
        const result = agentVersionResults[index];
        if (result?.status !== "fulfilled" || !result.value.current) return server;
        return { ...server, agent_version: result.value.current };
      });

      setState((current) => ({
        ...current,
        summary,
        systemMetrics: current.systemMetrics,
        servers: serversWithAgentVersions,
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

  const loadPeriodData = useCallback(async (range: TrafficRange, showLoading = true) => {
    const requestId = ++periodRequestId.current;
    if (showLoading) {
      setPeriodLoading(true);
      setError("");
    }
    try {
      const [nodes, users, nodeConnections] = await Promise.all([
        fetchTrafficPeriod(session.token, range, "nodes"),
        fetchTrafficPeriod(session.token, range, "users"),
        fetchNodeConnections(session.token),
      ]);
      if (requestId !== periodRequestId.current) return;
      setState((current) => ({
        ...current,
        nodes: nodes.items ?? [],
        users: users.items ?? [],
        nodeConnections: nodeConnections.connections ?? {},
        period: {
          range: nodes.range,
          start: nodes.range_start,
          end: nodes.range_end,
          timezone: nodes.timezone,
          complete: nodes.complete && users.complete,
        },
      }));
    } catch (err) {
      if (requestId === periodRequestId.current) {
        setError(err instanceof Error ? err.message : "周期统计加载失败");
      }
    } finally {
      if (requestId === periodRequestId.current) setPeriodLoading(false);
    }
  }, [session.token]);

  const refreshNodeConnections = useCallback(async () => {
    try {
      const response = await fetchNodeConnections(session.token);
      setState((current) => ({ ...current, nodeConnections: response.connections ?? {} }));
    } catch {
      // Preserve the last valid in-memory connection snapshot.
    }
  }, [session.token]);

  useEffect(() => {
    void loadDashboard();
  }, [loadDashboard]);

  useEffect(() => {
    void loadPeriodData(trafficRange);
  }, [loadPeriodData, trafficRange]);

  useEffect(() => {
    if (activeTab !== "overview") return;
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void loadPeriodData(trafficRange, false);
    }, 30_000);
    return () => window.clearInterval(timer);
  }, [activeTab, loadPeriodData, trafficRange]);

  useEffect(() => {
    if (activeTab !== "overview") return;
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void refreshNodeConnections();
    }, SYSTEM_METRICS_REFRESH_MS);
    return () => window.clearInterval(timer);
  }, [activeTab, refreshNodeConnections]);

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
  const periodNodes = state.period?.range === trafficRange ? state.nodes : [];
  const periodUsers = state.period?.range === trafficRange ? state.users : [];
  const selectedServer = useMemo(() => {
    if (state.servers.length === 0) return undefined;
    return state.servers.find((server) => server.id === selectedServerId) ?? state.servers[0];
  }, [selectedServerId, state.servers]);
  const visibleServiceServers = useMemo(() => {
    if (activeServiceGroupId === ALL_SERVICE_GROUP_ID) return state.servers;
    const group = serviceGroups.find((item) => item.id === activeServiceGroupId);
    if (!group) return state.servers;
    const ids = new Set(group.serverIds);
    return state.servers.filter((server) => ids.has(server.id));
  }, [activeServiceGroupId, serviceGroups, state.servers]);
  const visibleServiceTotals = useMemo(() => calculateTotals(visibleServiceServers), [visibleServiceServers]);

  useEffect(() => {
    if (state.servers.length === 0) {
      setSelectedServerId(null);
      return;
    }
    if (selectedServerId == null || !state.servers.some((server) => server.id === selectedServerId)) {
      setSelectedServerId(state.servers[0].id);
    }
  }, [selectedServerId, state.servers]);

  useEffect(() => {
    setServiceGroups(loadServiceGroups(session.username));
    setActiveServiceGroupId(ALL_SERVICE_GROUP_ID);
  }, [session.username]);

  useEffect(() => {
    setServiceGroups((current) => sanitizeServiceGroups(current, state.servers));
  }, [state.servers]);

  useEffect(() => {
    saveServiceGroups(session.username, serviceGroups);
  }, [session.username, serviceGroups]);

  useEffect(() => {
    if (activeServiceGroupId === ALL_SERVICE_GROUP_ID) return;
    if (!serviceGroups.some((group) => group.id === activeServiceGroupId)) {
      setActiveServiceGroupId(ALL_SERVICE_GROUP_ID);
    }
  }, [activeServiceGroupId, serviceGroups]);

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
          <button type="button" onClick={() => {
            void loadDashboard();
            void loadPeriodData(trafficRange);
          }}>
            重试
          </button>
        </section>
      )}

      {activeTab === "services" ? (
        <ServiceManagementPage
          servers={visibleServiceServers}
          allServers={state.servers}
          serviceGroups={serviceGroups}
          activeServiceGroupId={activeServiceGroupId}
          connectionMetrics={state.connectionMetrics}
          totals={visibleServiceTotals}
          viewMode={serviceViewMode}
          onViewModeChange={setServiceViewMode}
          onSelectServiceGroup={setActiveServiceGroupId}
          onOpenGroupManager={() => setServiceGroupDialogOpen(true)}
          onOpenGlobalMenu={() => setMenuOpen(true)}
          onOpenMenu={setServiceMenuServer}
          onOpenDialog={(kind, server) => setServiceDialog({ kind, server })}
        />
      ) : activeTab === "nodes" ? (
        <NodeManagementPage token={session.token} servers={state.servers} username={session.username} />
      ) : activeTab === "packages" ? (
        <PackageManagementPage token={session.token} />
      ) : activeTab === "forward" ? (
        <ForwardManagementPage token={session.token} />
      ) : activeTab !== "overview" ? (
        <Placeholder title={tabTitle(activeTab)} />
      ) : (
        <div className="dashboard-content" aria-busy={loading}>
          <section className="metric-grid">
            <SystemStatusCard metrics={state.systemMetrics} />
            <XrayStatusCard server={selectedServer} busy={xrayActionBusy} onAction={runXrayAction} />
          </section>

          <TrafficChart summary={state.summary} />
          <PeriodControl
            range={trafficRange}
            period={state.period}
            loading={periodLoading}
            onChange={setTrafficRange}
            onRefresh={() => void loadPeriodData(trafficRange)}
          />
          <NodeView
            nodes={periodNodes}
            servers={state.servers}
            connections={state.nodeConnections}
            range={trafficRange}
            onViewAll={() => setTrafficDialog("nodes")}
          />
          <UserView
            users={periodUsers}
            connections={state.userConnections}
            speeds={state.userSpeeds}
            range={trafficRange}
            onViewAll={() => setTrafficDialog("users")}
          />
          <ServerOverview servers={state.servers} />
        </div>
      )}

      {trafficDialog && (
        <TrafficListDialog
          kind={trafficDialog}
          range={trafficRange}
          nodes={periodNodes}
          users={periodUsers}
          servers={state.servers}
          nodeConnections={state.nodeConnections}
          userConnections={state.userConnections}
          userSpeeds={state.userSpeeds}
          onClose={() => setTrafficDialog(null)}
        />
      )}

      {serviceGroupDialogOpen && (
        <ServiceGroupDialog
          servers={state.servers}
          groups={serviceGroups}
          activeGroupId={activeServiceGroupId}
          onGroupsChange={setServiceGroups}
          onActiveGroupChange={setActiveServiceGroupId}
          onClose={() => setServiceGroupDialogOpen(false)}
        />
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
          sessionUsername={session.username}
          servers={state.servers}
          connectionMetric={serviceDialog.server ? state.connectionMetrics[String(serviceDialog.server.id)] : undefined}
          onChanged={loadDashboard}
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

function PeriodControl({
  range,
  period,
  loading,
  onChange,
  onRefresh,
}: {
  range: TrafficRange;
  period: PeriodMeta | null;
  loading: boolean;
  onChange: (range: TrafficRange) => void;
  onRefresh: () => void;
}) {
  return (
    <section className="panel-card period-control" aria-label="周期统计">
      <div className="period-control-head">
        <div className="period-control-title">
          <Clock3 />
          <div>
            <h2>周期统计</h2>
            <p>{formatPeriodStart(range, period?.start)}</p>
          </div>
        </div>
        <button className="period-refresh" type="button" onClick={onRefresh} aria-label="刷新周期统计" title="刷新周期统计">
          <RefreshCw className={loading ? "spinning" : ""} />
        </button>
      </div>
      <div className="segmented period-segmented" aria-label="统计周期">
        {(["today", "week", "month"] as const).map((value) => (
          <button className={range === value ? "active" : ""} key={value} type="button" onClick={() => onChange(value)}>
            {rangeLabel(value)}
          </button>
        ))}
      </div>
    </section>
  );
}

function NodeView({
  nodes,
  servers,
  connections,
  range,
  onViewAll,
}: {
  nodes: NodeTrafficItem[];
  servers: RemoteServer[];
  connections: Record<string, number>;
  range: TrafficRange;
  onViewAll: () => void;
}) {
  const serverByName = useMemo(() => serversByName(servers), [servers]);
  const visible = nodes.slice(0, 5);
  return (
    <section className="panel-card">
      <PanelTitle icon={<Server />} title="节点视图" subtitle={`${rangeLabel(range)}流量排行`} />
      <div className="list-stack">
        {visible.length ? visible.map((node) => (
          <NodeTrafficRow
            key={node.node_id}
            node={node}
            server={serverByName.get(node.server_name.toLocaleLowerCase())}
            connections={connections[String(node.node_id)]}
          />
        )) : <EmptyText>暂无节点数据</EmptyText>}
      </div>
      <TrafficListFooter count={nodes.length} unit="节点" onViewAll={onViewAll} />
    </section>
  );
}

function UserView({
  users,
  connections,
  speeds,
  range,
  onViewAll,
}: {
  users: PeriodUserTrafficItem[];
  connections: Record<string, number>;
  speeds: Record<string, number>;
  range: TrafficRange;
  onViewAll: () => void;
}) {
  const visible = users.slice(0, 5);
  return (
    <section className="panel-card">
      <PanelTitle icon={<Users />} title="用户视图" subtitle={`${rangeLabel(range)}流量排行`} />
      <div className="list-stack">
        {visible.length ? (
          visible.map((user) => (
            <TrafficRow
              key={user.username}
              name={user.username}
              up={user.uplink}
              down={user.downlink}
              badge={formatUserRealtime(connections[user.username], speeds[user.username])}
            />
          ))
        ) : (
          <EmptyText>暂无用户数据</EmptyText>
        )}
      </div>
      <TrafficListFooter count={users.length} unit="用户" onViewAll={onViewAll} />
    </section>
  );
}

function ServerOverview({ servers }: { servers: RemoteServer[] }) {
  return (
    <section className="panel-card dashboard-server-overview">
      <PanelTitle icon={<Server />} title="服务器概览" subtitle={`共 ${servers.length} 台 Remote Server`} />
      <div className="dashboard-server-list">
        {servers.length ? servers.map((server) => <DashboardServerRow key={server.id} server={server} />) : <EmptyText>暂无服务器数据</EmptyText>}
      </div>
    </section>
  );
}

function DashboardServerRow({ server }: { server: RemoteServer }) {
  const region = useServerRegion(server);
  const usage = trafficUsagePercent(server);
  const remaining = trafficRemaining(server);

  return (
    <article className="dashboard-server-row">
      <div className="dashboard-server-head">
        <span className={`service-status-dot ${serverStatusKind(server)}`} aria-label={isServerOnline(server) ? "服务器在线" : "服务器离线"} />
        <div className="dashboard-server-identity">
          <span className="dashboard-server-region">
            {region.flag && <span aria-hidden="true">{region.flag}</span>}
            {region.label}
          </span>
          <h3>{server.name}</h3>
        </div>
        <span className="dashboard-server-cycle">{trafficCycleText(server)}</span>
      </div>

      <div className="dashboard-server-speeds">
        <span><ArrowUp />{formatSpeed(server.current_upload_speed ?? 0)}</span>
        <span><ArrowDown />{formatSpeed(server.current_download_speed ?? 0)}</span>
      </div>

      <div className="dashboard-server-traffic">
        <DashboardServerMetric label="已用" value={formatBytes(server.traffic_used ?? 0)} />
        <DashboardServerMetric label="总量" value={trafficLimitText(server)} />
        {remaining != null && <DashboardServerMetric label="剩余" value={formatBytes(remaining)} />}
        {usage != null && <DashboardServerMetric label="使用率" value={formatPercent(usage)} />}
      </div>

      {usage != null && (
        <div className="dashboard-server-progress" role="progressbar" aria-label={`${server.name} 流量使用率`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(usage)}>
          <span style={{ width: `${usage}%` }} />
        </div>
      )}
    </article>
  );
}

function DashboardServerMetric({ label, value }: { label: string; value: string }) {
  return (
    <div className="dashboard-server-metric">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function NodeTrafficRow({ node, server, connections }: { node: NodeTrafficItem; server?: RemoteServer; connections?: number }) {
  const region = useServerRegion(server);
  const name = region.flag ? stripLeadingCountryFlag(node.node_name) : node.node_name;

  return (
    <TrafficRow
      name={name}
      up={node.uplink}
      down={node.downlink}
      badge={(
        <>
          <span>{node.server_name}</span>
          {connections ? <span>🔌 {connections.toLocaleString()}</span> : null}
        </>
      )}
      prefix={region.flag ? <span className="traffic-row-flag" aria-label={region.label}>{region.flag}</span> : undefined}
    />
  );
}

function TrafficListFooter({ count, unit, onViewAll }: { count: number; unit: "节点" | "用户"; onViewAll: () => void }) {
  return (
    <div className="traffic-list-footer">
      <span>共 {count} 个{unit}</span>
      <button type="button" onClick={onViewAll} disabled={count === 0}>查看全部</button>
    </div>
  );
}

function TrafficListDialog({
  kind,
  range,
  nodes,
  users,
  servers,
  nodeConnections,
  userConnections,
  userSpeeds,
  onClose,
}: {
  kind: "nodes" | "users";
  range: TrafficRange;
  nodes: NodeTrafficItem[];
  users: PeriodUserTrafficItem[];
  servers: RemoteServer[];
  nodeConnections: Record<string, number>;
  userConnections: Record<string, number>;
  userSpeeds: Record<string, number>;
  onClose: () => void;
}) {
  const serverByName = useMemo(() => serversByName(servers), [servers]);

  useEffect(() => {
    const previousOverflow = document.body.style.overflow;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.body.style.overflow = "hidden";
    window.addEventListener("keydown", closeOnEscape);
    return () => {
      document.body.style.overflow = previousOverflow;
      window.removeEventListener("keydown", closeOnEscape);
    };
  }, [onClose]);

  return (
    <div className="traffic-list-layer" role="presentation" onClick={onClose}>
      <section className="traffic-list-sheet" role="dialog" aria-modal="true" aria-label={`全部${kind === "nodes" ? "节点" : "用户"}`} onClick={(event) => event.stopPropagation()}>
        <header className="traffic-list-dialog-head">
          <div>
            <h2>{kind === "nodes" ? "全部节点" : "全部用户"}</h2>
            <p>{rangeLabel(range)}流量排行 · 共 {kind === "nodes" ? nodes.length : users.length} 个</p>
          </div>
          <button type="button" onClick={onClose} aria-label="关闭">
            <X />
          </button>
        </header>
        <div className="traffic-list-dialog-body list-stack">
          {kind === "nodes" ? nodes.map((node) => (
            <NodeTrafficRow
              key={node.node_id}
              node={node}
              server={serverByName.get(node.server_name.toLocaleLowerCase())}
              connections={nodeConnections[String(node.node_id)]}
            />
          )) : users.map((user) => (
            <TrafficRow
              key={user.username}
              name={user.username}
              up={user.uplink}
              down={user.downlink}
              badge={formatUserRealtime(userConnections[user.username], userSpeeds[user.username])}
            />
          ))}
        </div>
      </section>
    </div>
  );
}

function ServiceManagementPage({
  servers,
  allServers,
  serviceGroups,
  activeServiceGroupId,
  connectionMetrics,
  totals,
  viewMode,
  onViewModeChange,
  onSelectServiceGroup,
  onOpenGroupManager,
  onOpenGlobalMenu,
  onOpenMenu,
  onOpenDialog,
}: {
  servers: RemoteServer[];
  allServers: RemoteServer[];
  serviceGroups: ServiceGroup[];
  activeServiceGroupId: string;
  connectionMetrics: Record<string, ConnectionMetric>;
  totals: { upload: number; download: number };
  viewMode: "grid" | "list";
  onViewModeChange: (mode: "grid" | "list") => void;
  onSelectServiceGroup: (groupId: string) => void;
  onOpenGroupManager: () => void;
  onOpenGlobalMenu: () => void;
  onOpenMenu: (server: RemoteServer) => void;
  onOpenDialog: (kind: "add" | "access" | "edit" | "xray" | "agent" | "helper" | "connections" | "batch-agent", server?: RemoteServer) => void;
}) {
  const online = servers.filter(isServerOnline).length;
  const offline = Math.max(0, servers.length - online);
  const activeGroupName = activeServiceGroupId === ALL_SERVICE_GROUP_ID
    ? "全部"
    : serviceGroups.find((group) => group.id === activeServiceGroupId)?.name ?? "全部";

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
          <button className="service-icon-button service-page-more" type="button" onClick={() => onOpenDialog("batch-agent")} aria-label="批量升级 Agent">
            <RefreshCw />
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

      <section className="service-group-panel" aria-label="服务器分组">
        <div className="service-group-scroll" role="list" aria-label={`当前分组 ${activeGroupName}`}>
          <button
            className={activeServiceGroupId === ALL_SERVICE_GROUP_ID ? "active" : ""}
            type="button"
            onClick={() => onSelectServiceGroup(ALL_SERVICE_GROUP_ID)}
          >
            <span>全部</span>
            <em>{allServers.length}</em>
          </button>
          {serviceGroups.map((group) => {
            const visibleCount = group.serverIds.filter((id) => allServers.some((server) => server.id === id)).length;
            return (
              <button
                key={group.id}
                className={activeServiceGroupId === group.id ? "active" : ""}
                type="button"
                onClick={() => onSelectServiceGroup(group.id)}
                title={group.name}
              >
                <span>{group.name}</span>
                <em>{visibleCount}</em>
              </button>
            );
          })}
        </div>
        <button className="service-group-manage-button" type="button" onClick={onOpenGroupManager}>
          <Tags />
          <span>分组</span>
        </button>
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
            <EmptyText>{activeServiceGroupId === ALL_SERVICE_GROUP_ID ? "暂无服务器数据" : "这个分组里还没有服务器"}</EmptyText>
          </section>
        )}
      </section>
    </div>
  );
}

function ServiceGroupDialog({
  servers,
  groups,
  activeGroupId,
  onGroupsChange,
  onActiveGroupChange,
  onClose,
}: {
  servers: RemoteServer[];
  groups: ServiceGroup[];
  activeGroupId: string;
  onGroupsChange: (groups: ServiceGroup[]) => void;
  onActiveGroupChange: (groupId: string) => void;
  onClose: () => void;
}) {
  const [newGroupName, setNewGroupName] = useState("");
  const [selectedGroupId, setSelectedGroupId] = useState(() => activeGroupId === ALL_SERVICE_GROUP_ID ? groups[0]?.id ?? "" : activeGroupId);
  const [renameValue, setRenameValue] = useState("");
  const selectedGroup = groups.find((group) => group.id === selectedGroupId) ?? null;

  useEffect(() => {
    if (!selectedGroupId && groups[0]) setSelectedGroupId(groups[0].id);
    if (selectedGroupId && !groups.some((group) => group.id === selectedGroupId)) {
      setSelectedGroupId(groups[0]?.id ?? "");
    }
  }, [groups, selectedGroupId]);

  useEffect(() => {
    setRenameValue(selectedGroup?.name ?? "");
  }, [selectedGroup?.id, selectedGroup?.name]);

  function createGroup(event: React.FormEvent) {
    event.preventDefault();
    const name = normalizeServiceGroupName(newGroupName);
    if (!name || serviceGroupNameExists(groups, name)) return;
    const group = { id: createServiceGroupId(), name, serverIds: [] };
    onGroupsChange([...groups, group]);
    setSelectedGroupId(group.id);
    onActiveGroupChange(group.id);
    setNewGroupName("");
  }

  function renameGroup(event: React.FormEvent) {
    event.preventDefault();
    if (!selectedGroup) return;
    const name = normalizeServiceGroupName(renameValue);
    if (!name || serviceGroupNameExists(groups, name, selectedGroup.id)) {
      setRenameValue(selectedGroup.name);
      return;
    }
    onGroupsChange(groups.map((group) => group.id === selectedGroup.id ? { ...group, name } : group));
  }

  function deleteGroup(groupId: string) {
    const group = groups.find((item) => item.id === groupId);
    if (!group) return;
    if (!window.confirm(`删除分组「${group.name}」？服务器不会被删除。`)) return;
    const nextGroups = groups.filter((item) => item.id !== groupId);
    onGroupsChange(nextGroups);
    if (activeGroupId === groupId) onActiveGroupChange(ALL_SERVICE_GROUP_ID);
    if (selectedGroupId === groupId) setSelectedGroupId(nextGroups[0]?.id ?? "");
  }

  function toggleServer(serverId: number, checked: boolean) {
    if (!selectedGroup) return;
    const currentIds = new Set(selectedGroup.serverIds);
    if (checked) currentIds.add(serverId);
    else currentIds.delete(serverId);
    onGroupsChange(groups.map((group) => group.id === selectedGroup.id ? { ...group, serverIds: [...currentIds] } : group));
  }

  function setAllServers(checked: boolean) {
    if (!selectedGroup) return;
    const serverIds = checked ? servers.map((server) => server.id) : [];
    onGroupsChange(groups.map((group) => group.id === selectedGroup.id ? { ...group, serverIds } : group));
  }

  return (
    <div className="service-dialog-layer" role="presentation" onClick={onClose}>
      <section className="service-dialog service-group-dialog" role="dialog" aria-label="服务器分组管理" onClick={(event) => event.stopPropagation()}>
        <div className="service-dialog-head">
          <h3>服务器分组</h3>
          <button className="service-icon-button" type="button" onClick={onClose} aria-label="关闭">
            <X />
          </button>
        </div>
        <div className="service-dialog-body service-group-dialog-body">
          <section className="service-group-section">
            <h4>新建分组</h4>
            <form className="service-group-create" onSubmit={createGroup}>
              <input value={newGroupName} maxLength={28} onChange={(event) => setNewGroupName(event.target.value)} placeholder="输入组名，例如 香港节点" />
              <button type="submit" disabled={!normalizeServiceGroupName(newGroupName) || serviceGroupNameExists(groups, newGroupName)}>
                <Plus />
                <span>新增</span>
              </button>
            </form>
          </section>

          <section className="service-group-section">
            <h4>选择分组</h4>
            {groups.length ? (
              <div className="service-group-list">
                {groups.map((group) => (
                  <div className={`service-group-row ${selectedGroupId === group.id ? "active" : ""}`} key={group.id}>
                    <button type="button" onClick={() => {
                      setSelectedGroupId(group.id);
                      onActiveGroupChange(group.id);
                    }}>
                      <span>{group.name}</span>
                      <em>{group.serverIds.length} 台</em>
                    </button>
                    <button type="button" onClick={() => deleteGroup(group.id)} aria-label={`删除 ${group.name}`}>
                      <Trash2 />
                    </button>
                  </div>
                ))}
              </div>
            ) : (
              <EmptyText>还没有自定义分组</EmptyText>
            )}
          </section>

          {selectedGroup && (
            <section className="service-group-section">
              <h4>编辑分组</h4>
              <form className="service-group-rename" onSubmit={renameGroup}>
                <input value={renameValue} maxLength={28} onChange={(event) => setRenameValue(event.target.value)} />
                <button type="submit" disabled={!normalizeServiceGroupName(renameValue) || normalizeServiceGroupName(renameValue) === selectedGroup.name}>
                  <CheckCircle2 />
                  <span>保存名称</span>
                </button>
              </form>
              <div className="service-group-bulk">
                <button type="button" onClick={() => setAllServers(true)}>全选</button>
                <button type="button" onClick={() => setAllServers(false)}>清空</button>
              </div>
              <div className="service-group-server-list">
                {servers.map((server) => (
                  <label className="service-group-server-row" key={server.id}>
                    <input
                      type="checkbox"
                      checked={selectedGroup.serverIds.includes(server.id)}
                      onChange={(event) => toggleServer(server.id, event.target.checked)}
                    />
                    <span>
                      <strong>{server.name}</strong>
                      <small>{displayServerAddress(server) || "地址未填写"}</small>
                    </span>
                  </label>
                ))}
              </div>
            </section>
          )}

          <p className="service-group-note">分组只改变当前页面显示范围，不会修改服务器配置、节点、Agent 或 Xray。</p>
        </div>
        <div className="service-dialog-actions">
          <button type="button" onClick={onClose}>关闭</button>
        </div>
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
  onOpenDialog: (kind: "edit" | "xray" | "agent" | "helper" | "connections") => void;
}) {
  const region = useServerRegion(server);

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
          <button className="service-badge success connection-tag" type="button" onClick={() => onOpenDialog("connections")} aria-label={`查看具体连接数，当前 ${formatConnectionCount(connectionMetric)}`} title="具体连接数">
            <span className="connection-tag-icon" aria-hidden="true">🔌</span>
            <span className="connection-tag-value">{formatConnectionCount(connectionMetric)}</span>
          </button>
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
              <TrafficUsageDetails server={server} />
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

function TrafficUsageDetails({ server }: { server: RemoteServer }) {
  const usagePercent = trafficUsagePercent(server);
  const remaining = trafficRemaining(server);
  const resetRemainingText = trafficResetRemainingText(server);
  const resetRemainingParts = resetRemainingText.match(/^剩余 (\d+) (天|小时|分钟)$/);

  return (
    <div className="service-v3-traffic-details">
      <span className="service-billing-remaining-badge" aria-label={resetRemainingText}>
        {resetRemainingParts ? (
          <span aria-hidden="true">
            剩余 <strong>{resetRemainingParts[1]}</strong> {resetRemainingParts[2]}
          </span>
        ) : resetRemainingText}
      </span>
      {usagePercent != null && remaining != null && (
        <>
          <div className="service-v3-traffic-bar" role="progressbar" aria-label={`${server.name} 流量使用率`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(usagePercent)}>
            <span style={{ width: `${usagePercent}%` }} />
          </div>
          <div className="service-v3-traffic-meta">
            <span>剩余 {formatBytes(remaining)}</span>
            <span>{formatPercent(usagePercent)}</span>
          </div>
        </>
      )}
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

type AgentNotice = { kind: "success" | "error" | "info"; text: string };
type AgentPanel = "status" | "sync" | "config" | "website" | "maintenance";

function AgentManager({ server, sessionToken }: { server: RemoteServer; sessionToken: string }) {
  const [panel, setPanel] = useState<AgentPanel>("status");
  const [versionInfo, setVersionInfo] = useState<AgentVersionInfo | null>(null);
  const [systemInfo, setSystemInfo] = useState<RemoteSystemInfo | null>(null);
  const [serviceStatus, setServiceStatus] = useState<XrayServiceStatusResponse | null>(null);
  const [recovery, setRecovery] = useState<XrayRecoveryStatusResponse | null>(null);
  const [snapshots, setSnapshots] = useState<XraySnapshotItem[]>([]);
  const [selectedSnapshot, setSelectedSnapshot] = useState<XraySnapshotItem | null>(null);
  const [websites, setWebsites] = useState<RemoteWebsitesResponse | null>(null);
  const [syncHost, setSyncHost] = useState(displayServerAddress(server));
  const [forceOverride, setForceOverride] = useState(false);
  const [websiteMode, setWebsiteMode] = useState<"list" | "add">("list");
  const [siteDomain, setSiteDomain] = useState("");
  const [siteType, setSiteType] = useState<"static" | "proxy">("static");
  const [siteValue, setSiteValue] = useState("");
  const [entryMode, setEntryMode] = useState("auto");
  const [validation, setValidation] = useState<AgentNotice | null>(null);
  const [notice, setNotice] = useState<AgentNotice | null>(null);
  const [busy, setBusy] = useState("");
  const [streamLog, setStreamLog] = useState("");
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async (showLoading = false) => {
    if (showLoading) setLoading(true);
    const [version, system, services, recoveryStatus, history, siteInventory] = await Promise.allSettled([
      fetchAgentVersionInfo(sessionToken, server.id),
      fetchRemoteSystemInfo(sessionToken, server.id),
      fetchXrayServiceStatus(sessionToken, server.id),
      fetchXrayRecoveryStatus(sessionToken, server.id),
      fetchXraySnapshots(sessionToken, server.id, { limit: 20 }),
      fetchRemoteWebsites(sessionToken, server.id),
    ]);
    if (version.status === "fulfilled") setVersionInfo(version.value);
    if (system.status === "fulfilled") setSystemInfo(system.value);
    if (services.status === "fulfilled") setServiceStatus(services.value);
    if (recoveryStatus.status === "fulfilled") setRecovery(recoveryStatus.value);
    if (history.status === "fulfilled") setSnapshots(history.value.items ?? []);
    if (siteInventory.status === "fulfilled") setWebsites(siteInventory.value);
    if (showLoading) setLoading(false);
  }, [server.id, sessionToken]);

  useEffect(() => {
    let mounted = true;
    setLoading(true);
    refresh(false).finally(() => {
      if (mounted) setLoading(false);
    });
    return () => {
      mounted = false;
    };
  }, [refresh]);

  const currentVersion = formatAgentVersion(versionInfo?.current || systemInfo?.agent_version || server.agent_version);
  const latestVersion = formatAgentVersion(versionInfo?.latest);
  const canMutate = !busy && server.status === "connected" && !server.is_federated;

  async function run(name: string, task: () => Promise<{ message?: string; success?: boolean } | unknown>, successText: string) {
    setBusy(name);
    setNotice(null);
    try {
      const response = await task();
      const message = isRecord(response) ? asString(response.message) : "";
      setNotice({ kind: "success", text: message || successText });
      await refresh(false);
    } catch (error) {
      setNotice({ kind: "error", text: error instanceof Error ? error.message : "操作失败" });
    } finally {
      setBusy("");
    }
  }

  async function openSnapshot(snapshot: XraySnapshotItem) {
    setBusy(`snapshot-${snapshot.id}`);
    setNotice(null);
    try {
      if (snapshot.config_json) {
        setSelectedSnapshot(snapshot);
        return;
      }
      const full = await fetchXraySnapshots(sessionToken, server.id, { limit: 30, withConfig: true });
      const found = (full.items ?? []).find((item) => item.id === snapshot.id) ?? snapshot;
      setSelectedSnapshot(found);
    } catch (error) {
      setNotice({ kind: "error", text: error instanceof Error ? error.message : "读取配置内容失败" });
    } finally {
      setBusy("");
    }
  }

  function confirmAction(text: string) {
    return window.confirm(`${server.name}\n${text}`);
  }

  async function runStream(action: "upgrade" | "uninstall") {
    const label = action === "upgrade" ? "升级 Agent" : "卸载 Agent";
    const detail = action === "upgrade"
      ? "将调用远端 Agent 升级脚本，过程中 Agent 会重启并可能短暂断线。确认执行？"
      : "将卸载远端 mmw-agent，Agent 会停止，主控可能无法继续管理该服务器。确认执行？";
    if (!confirmAction(detail)) return;
    setBusy(action);
    setNotice(null);
    setStreamLog("");
    try {
      await streamAgentAction(sessionToken, server.id, action, (event) => {
        const type = asString(event.type);
        if (type === "output") {
          setStreamLog((value) => `${value}${stripAnsi(asString(event.data))}\n`);
        } else if (type === "complete" || type === "result") {
          const ok = event.success !== false;
          setNotice({ kind: ok ? "success" : "error", text: asString(event.message) || `${label}${ok ? "完成" : "失败"}` });
        } else if (type === "error") {
          setNotice({ kind: "error", text: asString(event.message) || `${label}失败` });
        }
      });
      await refresh(false);
    } catch (error) {
      setNotice({ kind: "error", text: error instanceof Error ? error.message : `${label}失败` });
    } finally {
      setBusy("");
    }
  }

  return (
    <div className="agent-manager">
      <div className="agent-server-line">
        <span>{server.name}</span>
        <button type="button" disabled={Boolean(busy)} onClick={() => void refresh(true)} aria-label="刷新 Agent 状态">
          <RefreshCw />
        </button>
      </div>

      <div className="agent-tabs" role="tablist" aria-label="Agent 管理分类">
        {([
          ["status", "状态"],
          ["sync", "同步"],
          ["config", "配置"],
          ["website", "网站"],
          ["maintenance", "维护"],
        ] as Array<[AgentPanel, string]>).map(([key, label]) => (
          <button key={key} type="button" className={panel === key ? "active" : ""} onClick={() => setPanel(key)}>
            {label}
          </button>
        ))}
      </div>

      {notice && <div className={`agent-notice ${notice.kind}`}>{notice.text}</div>}
      {loading && <div className="agent-loading"><RefreshCw /> 正在读取 Agent 状态...</div>}

      {panel === "status" && (
        <div className="agent-panel">
          <div className="agent-info-grid">
            <InfoBlock label="Agent 版本" value={currentVersion} />
            <InfoBlock label="最新版本" value={latestVersion} />
            <InfoBlock label="在线状态" value={isAgentOnline(server) ? "在线" : "离线"} />
            <InfoBlock label="WS 状态" value={server.ws_connected ? "已连接" : "未连接"} />
            <InfoBlock label="最后心跳" value={server.last_heartbeat ? formatDateTime(server.last_heartbeat) : "--"} />
            <InfoBlock label="Agent 模式" value={formatConnectionMode(server.connection_mode)} />
            <InfoBlock label="Xray 模式" value={formatXrayMode(server.xray_mode)} />
            <InfoBlock label="Xray 状态" value={serviceStatus?.xray?.running ?? server.xray_running ? "运行中" : "停止"} />
            <InfoBlock label="Xray 版本" value={formatAgentVersion(serviceStatus?.xray?.version || server.xray_version)} />
            <InfoBlock label="Nginx 状态" value={serviceStatus?.nginx?.installed ? (serviceStatus.nginx.running ? "运行中" : "已安装未运行") : "未安装"} />
            <InfoBlock label="主机名" value={systemInfo?.hostname || "--"} />
            <InfoBlock label="系统负载" value={systemInfo?.loadavg || "--"} />
          </div>
          {versionInfo?.upgrade_available && <div className="agent-notice info">当前 Agent 低于最新版本，可在维护页升级。</div>}
          {(versionInfo?.current_error || versionInfo?.latest_error) && (
            <div className="agent-notice error">{versionInfo.current_error || versionInfo.latest_error}</div>
          )}
        </div>
      )}

      {panel === "sync" && (
        <div className="agent-panel">
          <section className="agent-card">
            <div className="agent-card-head">
              <div>
                <h4>同步节点</h4>
                <p>读取 Agent 当前入站，按正式规则生成或更新节点。</p>
              </div>
              <RefreshCw />
            </div>
            <label className="agent-field">
              <span>节点地址</span>
              <input value={syncHost} onChange={(event) => setSyncHost(event.target.value)} placeholder="留空时使用服务器地址" />
            </label>
            <label className="agent-check">
              <input type="checkbox" checked={forceOverride} onChange={(event) => setForceOverride(event.target.checked)} />
              <span>强制覆盖已有节点配置</span>
            </label>
            <button type="button" className="agent-primary" disabled={!canMutate || !syncHost.trim()} onClick={() => void run("sync", () => syncRemoteNodes(sessionToken, server.id, { server_host: syncHost.trim(), force_override: forceOverride }), "节点同步完成")}>
              <RefreshCw /> {busy === "sync" ? "同步中..." : "同步节点"}
            </button>
          </section>

          <section className="agent-card">
            <div className="agent-card-head">
              <div>
                <h4>同步节点地址</h4>
                <p>按当前服务器地址刷新已有节点的 server 字段。</p>
              </div>
              <Globe2 />
            </div>
            <button type="button" disabled={!canMutate} onClick={() => void run("sync-address", () => syncRemoteNodeAddress(sessionToken, server.id), "节点地址已同步")}>
              <Globe2 /> {busy === "sync-address" ? "同步中..." : "同步节点地址"}
            </button>
          </section>
        </div>
      )}

      {panel === "config" && (
        <div className="agent-panel">
          <section className="agent-card">
            <div className="agent-card-head">
              <div>
                <h4>配置状态</h4>
                <p>主控保存的 current/pending 快照，用于恢复或接受 Agent 现状。</p>
              </div>
              <Database />
            </div>
            <div className="agent-config-status">
              <span>当前快照：{recovery?.has_current ? shortHash(recovery.current?.config_hash) : "无"}</span>
              <span>待处理恢复：{recovery?.has_pending ? shortHash(recovery.pending?.config_hash) : "无"}</span>
            </div>
            <div className="agent-button-row">
              <button type="button" disabled={!canMutate || !recovery?.has_current} onClick={() => confirmAction("确认把主控 current 配置覆盖到 Agent，并重启 Xray？") && void run("recovery-apply", () => applyXrayRecovery(sessionToken, server.id), "已应用主控配置")}>
                <UploadCloud /> 应用主控配置
              </button>
              <button type="button" disabled={!canMutate || !recovery?.has_pending} onClick={() => confirmAction("确认接受 Agent 当前配置为新的主控 current？该操作不重启 Xray。") && void run("recovery-accept", () => acceptXrayRecovery(sessionToken, server.id), "已接受 Agent 当前配置")}>
                <ShieldCheck /> 接受 Agent 现状
              </button>
              <button type="button" disabled={!canMutate} onClick={() => void run("expect-recovery", () => expectXrayRecovery(sessionToken, server.id), "已登记下次上线自动恢复")}>
                <Clock3 /> 下次上线恢复
              </button>
              <button type="button" className="danger" disabled={!canMutate} onClick={() => confirmAction("确认下发默认配置？会覆盖 Agent 当前 Xray 配置并重启 Xray。") && void run("default-config", () => deployRemoteDefaultConfig(sessionToken, server.id), "默认配置已下发")}>
                <Database /> 下发默认配置
              </button>
            </div>
          </section>

          <section className="agent-card">
            <div className="agent-card-head">
              <div>
                <h4>配置历史</h4>
                <p>可查看历史 JSON，也可恢复到 Agent；恢复前后端会做 Xray 测试。</p>
              </div>
              <Clock3 />
            </div>
            <div className="agent-history-list">
              {snapshots.length === 0 ? (
                <EmptyText>暂无配置历史</EmptyText>
              ) : snapshots.map((snapshot) => (
                <button key={snapshot.id} type="button" className={selectedSnapshot?.id === snapshot.id ? "selected" : ""} onClick={() => void openSnapshot(snapshot)}>
                  <strong>{snapshot.status || "history"}</strong>
                  <span>{formatDateTime(snapshot.created_at)}</span>
                  <code>{shortHash(snapshot.config_hash)}</code>
                  <em>{formatBytes(snapshot.size_bytes)}</em>
                </button>
              ))}
            </div>
            {selectedSnapshot && (
              <div className="agent-snapshot-preview">
                <div>
                  <strong>{selectedSnapshot.status || "历史快照"} #{selectedSnapshot.id}</strong>
                  <button type="button" className="danger" disabled={!canMutate || busy === `restore-${selectedSnapshot.id}`} onClick={() => confirmAction(`确认恢复快照 #${selectedSnapshot.id}？会覆盖 Agent 当前配置并重启 Xray。`) && void run(`restore-${selectedSnapshot.id}`, () => restoreXraySnapshot(sessionToken, selectedSnapshot.id), "历史配置已恢复")}>
                    <UploadCloud /> 恢复
                  </button>
                </div>
                <textarea readOnly value={formatJsonText(selectedSnapshot.config_json || "")} />
              </div>
            )}
          </section>
        </div>
      )}

      {panel === "website" && (
        <div className="agent-panel">
          <section className="agent-card">
            <div className="agent-card-head">
              <div>
                <h4>网站管理</h4>
                <p>读取 Agent Nginx 网站清单，支持添加、验证和删除托管网站。</p>
              </div>
              <Globe2 />
            </div>
            <div className="agent-config-status">
              <span>Nginx：{websites?.nginx?.installed ? (websites.nginx.running ? "运行中" : "已安装") : "未安装"}</span>
              <span>管理方式：{websites?.nginx?.manager || "--"}</span>
              <span>443：{websites?.ports?.["443"] || "空闲"}</span>
            </div>
            {websites?.nginx?.reason && <div className="agent-notice info">{websites.nginx.reason}</div>}
            <div className="agent-button-row">
              {(!websites?.nginx?.installed || !websites?.nginx?.can_manage) && (
                <button type="button" disabled={!canMutate} onClick={() => confirmAction("确认安装或修复 Nginx 管理配置？可能改变远端 Nginx 配置。") && void run("nginx-install", () => installRemoteNginx(sessionToken, server.id), websites?.nginx?.installed ? "Nginx 管理配置已修复" : "Nginx 安装任务已启动")}>
                  <Settings /> {websites?.nginx?.installed ? "修复管理配置" : "安装 Nginx"}
                </button>
              )}
              <button type="button" onClick={() => void refresh(false)} disabled={Boolean(busy)}>
                <RefreshCw /> 刷新
              </button>
            </div>
          </section>
          <div className="agent-subtabs">
            <button type="button" className={websiteMode === "list" ? "active" : ""} onClick={() => setWebsiteMode("list")}>网站列表</button>
            <button type="button" className={websiteMode === "add" ? "active" : ""} onClick={() => setWebsiteMode("add")}>添加网站</button>
          </div>
          {websiteMode === "list" ? (
            <div className="agent-site-list">
              {(websites?.websites ?? []).length === 0 ? <EmptyText>暂无网站配置</EmptyText> : (websites?.websites ?? []).map((site) => (
                <article key={`${site.domain}-${site.path}`} className="agent-site-card">
                  <div>
                    <strong>{site.domain || "--"}</strong>
                    <span>{site.type === "proxy" ? "反向代理" : site.type === "static" ? "静态网站" : "未知类型"}</span>
                    <p>{site.value || site.path || "--"}</p>
                    {site.reason && <p>{site.reason}</p>}
                  </div>
                  <button type="button" className="danger" disabled={!canMutate || !site.managed || site.protected || !site.domain} onClick={() => site.domain && confirmAction(`确认删除网站 ${site.domain}？关联的 Xray 网站路由也会清理。`) && void run(`delete-site-${site.domain}`, () => deleteRemoteWebsite(sessionToken, server.id, site.domain || ""), "网站已删除")}>
                    <Trash2 /> 删除
                  </button>
                </article>
              ))}
            </div>
          ) : (
            <section className="agent-card">
              <label className="agent-field">
                <span>网站域名</span>
                <input value={siteDomain} onChange={(event) => setSiteDomain(event.target.value)} placeholder="example.com" />
              </label>
              <div className="agent-subtabs compact">
                <button type="button" className={siteType === "static" ? "active" : ""} onClick={() => setSiteType("static")}>静态网站</button>
                <button type="button" className={siteType === "proxy" ? "active" : ""} onClick={() => setSiteType("proxy")}>反向代理</button>
              </div>
              <label className="agent-field">
                <span>{siteType === "static" ? "静态目录" : "反代地址"}</span>
                <input value={siteValue} onChange={(event) => setSiteValue(event.target.value)} placeholder={siteType === "static" ? "/var/www/html" : "http://127.0.0.1:8080"} />
              </label>
              <label className="agent-field">
                <span>入口模式</span>
                <select value={entryMode} onChange={(event) => setEntryMode(event.target.value)}>
                  <option value="auto">自动</option>
                  <option value="direct">直接 Nginx</option>
                  <option value="fallback">Xray fallback</option>
                  <option value="tunnel">Xray tunnel</option>
                </select>
              </label>
              {validation && <div className={`agent-notice ${validation.kind}`}>{validation.text}</div>}
              <div className="agent-button-row">
                <button type="button" disabled={!canMutate || !siteValue.trim()} onClick={() => void run("validate-site", () => validateRemoteWebsite(sessionToken, { server_id: server.id, site_type: siteType, site_value: siteValue.trim(), entry_mode: entryMode }).then((response) => { setValidation({ kind: response.success ? "success" : "error", text: response.message || (response.success ? "验证通过" : "验证失败") }); return response; }), "验证完成")}>
                  <ShieldCheck /> 验证
                </button>
                <button type="button" className="agent-primary" disabled={!canMutate || !siteDomain.trim() || !siteValue.trim()} onClick={() => confirmAction(`确认添加网站 ${siteDomain.trim()}？会下发证书和 Nginx 配置，fallback/tunnel 模式还会修改 Xray 并重启。`) && void run("add-site", () => addRemoteWebsite(sessionToken, { server_id: server.id, domain: siteDomain.trim(), site_type: siteType, site_value: siteValue.trim(), entry_mode: entryMode }), "网站已添加")}>
                  <Plus /> 添加网站
                </button>
              </div>
            </section>
          )}
        </div>
      )}

      {panel === "maintenance" && (
        <div className="agent-panel">
          <section className="agent-card">
            <div className="agent-card-head">
              <div>
                <h4>维护</h4>
                <p>升级和卸载会直接作用远端 Agent，执行前必须确认。</p>
              </div>
              <Wrench />
            </div>
            <div className="agent-info-grid two">
              <InfoBlock label="当前版本" value={currentVersion} />
              <InfoBlock label="最新版本" value={latestVersion} />
            </div>
            <div className="agent-button-row">
              <button type="button" className="danger" disabled={!canMutate || busy === "upgrade"} onClick={() => void runStream("upgrade")}>
                <RefreshCw /> {busy === "upgrade" ? "升级中..." : "升级 Agent"}
              </button>
              <button type="button" className="danger" disabled={!canMutate || busy === "uninstall"} onClick={() => void runStream("uninstall")}>
                <Trash2 /> {busy === "uninstall" ? "卸载中..." : "卸载 Agent"}
              </button>
            </div>
            <p className="agent-note">联邦服务器、离线服务器或正在执行其它操作时，维护按钮会禁用。</p>
            {streamLog && <pre className="agent-stream-log">{streamLog}</pre>}
          </section>
        </div>
      )}
    </div>
  );
}

function BatchAgentUpgradeDialog({ servers, sessionToken }: { servers: RemoteServer[]; sessionToken: string }) {
  const targets = useMemo(() => servers.filter((server) => server.status === "connected" && !server.is_federated), [servers]);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<AgentNotice | null>(null);
  const [logs, setLogs] = useState<Record<number, string>>({});

  async function runAll() {
    if (!window.confirm(`确认升级 ${targets.length} 台在线 Agent？\n每台 Agent 会依次重启，可能短暂断线；即使已经是最新版本，也会重新下载并替换同版本二进制。`)) return;
    setBusy(true);
    setNotice(null);
    let failed = 0;
    for (const server of targets) {
      setLogs((current) => ({ ...current, [server.id]: `${server.name}\n` }));
      try {
        await streamAgentAction(sessionToken, server.id, "upgrade", (event) => {
          const type = asString(event.type);
          if (type === "output") {
            setLogs((current) => ({ ...current, [server.id]: `${current[server.id] || ""}${stripAnsi(asString(event.data))}\n` }));
          } else if (type === "complete" || type === "result") {
            setLogs((current) => ({ ...current, [server.id]: `${current[server.id] || ""}${asString(event.message) || "完成"}\n` }));
            if (event.success === false) failed += 1;
          } else if (type === "error") {
            failed += 1;
            setLogs((current) => ({ ...current, [server.id]: `${current[server.id] || ""}${asString(event.message) || "失败"}\n` }));
          }
        });
      } catch (error) {
        failed += 1;
        setLogs((current) => ({ ...current, [server.id]: `${current[server.id] || ""}${error instanceof Error ? error.message : "升级失败"}\n` }));
      }
    }
    setNotice(failed ? { kind: "error", text: `批量升级完成，失败 ${failed} 台` } : { kind: "success", text: `批量升级完成，共 ${targets.length} 台` });
    setBusy(false);
  }

  return (
    <div className="agent-manager">
      <div className="agent-server-line">
        <span>批量升级 Agent</span>
      </div>
      {notice && <div className={`agent-notice ${notice.kind}`}>{notice.text}</div>}
      <section className="agent-card">
        <div className="agent-card-head">
          <div>
            <h4>在线 Agent</h4>
            <p>正式版顶部的 Upgrade All Agents 能力；Custom 会按服务器顺序逐台调用升级 SSE。</p>
          </div>
          <RefreshCw />
        </div>
        <div className="agent-site-list">
          {targets.length === 0 ? <EmptyText>暂无可升级的在线 Agent</EmptyText> : targets.map((server) => (
            <article className="agent-site-card" key={server.id}>
              <div>
                <strong>{server.name}</strong>
                <span>{agentVersion(server)}</span>
                <p>{server.ws_connected ? "WS 已连接" : formatConnectionMode(server.connection_mode)}</p>
              </div>
            </article>
          ))}
        </div>
        <button type="button" className="agent-primary" disabled={busy || targets.length === 0} onClick={() => void runAll()}>
          <RefreshCw /> {busy ? "批量升级中..." : "升级全部在线 Agent"}
        </button>
        {Object.entries(logs).map(([id, log]) => <pre className="agent-stream-log" key={id}>{log}</pre>)}
      </section>
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
  onOpenDialog: (kind: "edit" | "xray" | "agent" | "helper" | "connections") => void;
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
          <ActionButton icon={<RefreshCw />} label="同步节点" onClick={() => onOpenDialog("agent")} />
          <ActionButton icon={<Globe2 />} label="同步节点地址" onClick={() => onOpenDialog("agent")} />
          <ActionButton icon={<Clock3 />} label="配置历史" onClick={() => onOpenDialog("agent")} />
          <ActionButton icon={<Database />} label="下发默认配置" onClick={() => onOpenDialog("agent")} />
          <ActionButton icon={<Plus />} label="添加网站" onClick={() => onOpenDialog("agent")} />
        </ActionGroup>
        <ActionGroup title="Xray 管理">
          <ActionButton icon={<TerminalSquare />} label="配置 / 入站 / 出站 / 路由" onClick={() => onOpenDialog("xray")} />
          <ActionButton icon={<Gauge />} label="指标 / 流量统计 / gRPC" onClick={() => onOpenDialog("xray")} />
        </ActionGroup>
        <ActionGroup title="Agent 管理">
          <ActionButton icon={<Wrench />} label="Agent 管理" onClick={() => onOpenDialog("agent")} />
          <ActionButton icon={<RefreshCw />} label="升级 Agent" danger onClick={() => onOpenDialog("agent")} />
        </ActionGroup>
        <ActionGroup title="Connections Helper">
          <ActionButton icon={<Gauge />} label="具体连接数与限制" onClick={() => onOpenDialog("connections")} />
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
          <ActionButton icon={<Trash2 />} label="卸载 Agent" danger onClick={() => onOpenDialog("agent")} />
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
  sessionUsername,
  servers,
  connectionMetric,
  onChanged,
}: {
  dialog: { kind: "add" | "access" | "edit" | "xray" | "agent" | "helper" | "connections" | "batch-agent"; server?: RemoteServer };
  onClose: () => void;
  onXrayAction: (action: "start" | "stop" | "restart") => void;
  xrayActionBusy: boolean;
  sessionToken: string;
  sessionUsername: string;
  servers: RemoteServer[];
  connectionMetric?: ConnectionMetric;
  onChanged: () => Promise<void>;
}) {
  const server = dialog.server;
  const title = {
    add: "添加服务器",
    access: "接入分享服务器",
    edit: "编辑远程服务器",
    xray: "Xray 管理",
    agent: "Agent 管理",
    helper: "Connections Helper",
    connections: "具体连接数",
    "batch-agent": "批量升级 Agent",
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

        {dialog.kind === "add" ? (
          <AddRemoteServerDialog sessionToken={sessionToken} servers={servers} onChanged={onChanged} onClose={onClose} />
        ) : dialog.kind === "access" ? (
          <AddSharedServerDialog sessionToken={sessionToken} servers={servers} onChanged={onChanged} onClose={onClose} />
        ) : dialog.kind === "xray" && server ? (
          <div className="service-dialog-body">
            <XrayManager server={server} token={sessionToken} username={sessionUsername} />
          </div>
        ) : dialog.kind === "agent" && server ? (
          <div className="service-dialog-body">
            <AgentManager server={server} sessionToken={sessionToken} />
          </div>
        ) : dialog.kind === "batch-agent" ? (
          <div className="service-dialog-body">
            <BatchAgentUpgradeDialog servers={servers} sessionToken={sessionToken} />
          </div>
        ) : dialog.kind === "helper" && server ? (
          <HelperInstallDialog server={server} sessionToken={sessionToken} connectionMetric={connectionMetric} />
        ) : dialog.kind === "connections" && server ? (
          <ConnectionsManager server={server} token={sessionToken} />
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

        {dialog.kind !== "add" && dialog.kind !== "access" && (
          <div className="service-dialog-actions">
            <button type="button" onClick={onClose}>关闭</button>
            {dialog.kind === "edit" && <button type="button" disabled>保存</button>}
          </div>
        )}
      </section>
    </div>
  );
}

type AddServerFormState = {
  name: string;
  pullAddress: string;
  pullAddressV6: string;
  agentPort: string;
  agentToken: string;
  trafficLimitGb: string;
  trafficUsedGb: string;
  resetDay: string;
  ipv6Enabled: boolean;
  xrayMode: "external" | "embedded";
  trafficStatsMode: "both" | "upload" | "download" | "max";
  trafficSource: "xray" | "system";
  ddnsEnabled: boolean;
  ddnsProviderId: number;
  stealSelf: boolean;
  frontService: "xray" | "nginx";
  stealMode: "tunnel" | "fallback";
  use443: boolean;
  domain: string;
  siteType: "static" | "proxy";
  siteValue: string;
};

const addServerInitialState: AddServerFormState = {
  name: "",
  pullAddress: "",
  pullAddressV6: "",
  agentPort: "23889",
  agentToken: "",
  trafficLimitGb: "",
  trafficUsedGb: "",
  resetDay: "1",
  ipv6Enabled: true,
  xrayMode: "external",
  trafficStatsMode: "both",
  trafficSource: "system",
  ddnsEnabled: false,
  ddnsProviderId: 0,
  stealSelf: false,
  frontService: "xray",
  stealMode: "tunnel",
  use443: false,
  domain: "",
  siteType: "static",
  siteValue: "",
};

function AddRemoteServerDialog({
  sessionToken,
  servers,
  onChanged,
  onClose,
}: {
  sessionToken: string;
  servers: RemoteServer[];
  onChanged: () => Promise<void>;
  onClose: () => void;
}) {
  const [form, setForm] = useState<AddServerFormState>(addServerInitialState);
  const [masterUrl, setMasterUrl] = useState("");
  const [dnsProviders, setDnsProviders] = useState<DNSProvider[]>([]);
  const [certificates, setCertificates] = useState<ValidCertificate[]>([]);
  const [loadingMeta, setLoadingMeta] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [notice, setNotice] = useState<{ kind: "success" | "error" | "info"; text: string } | null>(null);
  const [createdServer, setCreatedServer] = useState<RemoteServer | null>(null);
  const [installCommand, setInstallCommand] = useState("");
  const [copied, setCopied] = useState("");

  useEffect(() => {
    let stopped = false;
    async function loadMeta() {
      setLoadingMeta(true);
      try {
        const [master, providers, certs] = await Promise.allSettled([
          fetchMasterUrl(sessionToken),
          fetchDNSProviders(sessionToken),
          fetchValidCertificates(sessionToken),
        ]);
        if (stopped) return;
        if (master.status === "fulfilled") setMasterUrl(master.value.master_url ?? "");
        if (providers.status === "fulfilled") setDnsProviders(providers.value.providers ?? []);
        if (certs.status === "fulfilled") setCertificates((certs.value.certificates ?? []) as ValidCertificate[]);
      } catch {
        if (!stopped) setNotice({ kind: "error", text: "读取添加服务器元数据失败" });
      } finally {
        if (!stopped) setLoadingMeta(false);
      }
    }
    void loadMeta();
    return () => {
      stopped = true;
    };
  }, [sessionToken]);

  useEffect(() => {
    if (!form.ddnsEnabled || form.ddnsProviderId !== 0) return;
    const cert = findCertificateForDomain(certificates, form.pullAddress);
    if (cert?.dns_provider_id) {
      setForm((current) => ({ ...current, ddnsProviderId: cert.dns_provider_id ?? 0 }));
    }
  }, [certificates, form.ddnsEnabled, form.ddnsProviderId, form.pullAddress]);

  const ddnsCertificate = useMemo(() => findCertificateForDomain(certificates, form.pullAddress), [certificates, form.pullAddress]);
  const canSubmit = form.name.trim().length > 0 && !submitting && !createdServer;

  function update<K extends keyof AddServerFormState>(key: K, value: AddServerFormState[K]) {
    setForm((current) => ({ ...current, [key]: value }));
    setNotice(null);
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (!form.name.trim()) {
      setNotice({ kind: "error", text: "服务器名称不能为空" });
      return;
    }
    if (form.use443 && !form.domain.trim()) {
      setNotice({ kind: "error", text: "启用 443 部署时必须填写域名" });
      return;
    }
    if (form.ddnsEnabled && (!form.pullAddress.trim() || isIPAddress(form.pullAddress))) {
      setNotice({ kind: "error", text: "DDNS 开启时，服务器地址必须填写域名" });
      return;
    }

    const port = parseOptionalInt(form.agentPort);
    if (port != null && port !== 0 && (port < 1024 || port > 65535)) {
      setNotice({ kind: "error", text: "Agent 端口应为 1024-65535，或留空使用默认 23889" });
      return;
    }
    const resetDay = parseOptionalInt(form.resetDay);
    if (resetDay != null && resetDay !== 0 && (resetDay < 1 || resetDay > 31)) {
      setNotice({ kind: "error", text: "重置日应为 1-31，或留空表示不自动重置" });
      return;
    }

    const trafficLimit = gbToBytes(form.trafficLimitGb);
    const trafficUsed = gbToBytes(form.trafficUsedGb);
    if (trafficLimit == null || trafficUsed == null) {
      setNotice({ kind: "error", text: "流量字段必须是非负数字" });
      return;
    }

    const payload: RemoteServerCreateRequest = {
      name: form.name.trim(),
      traffic_limit: trafficLimit,
      traffic_used_offset: trafficUsed,
      traffic_reset_day: resetDay ?? 0,
      connection_mode: "auto",
      pull_address: form.pullAddress.trim() || undefined,
      pull_address_v6: form.ddnsEnabled ? form.pullAddressV6.trim() || undefined : undefined,
      pull_port: port ?? undefined,
      listen_port: port ?? undefined,
      pull_token: form.agentToken.trim() || undefined,
      steal_self: form.stealSelf,
      front_service: form.frontService,
      domain: form.domain.trim() || undefined,
      use_443: form.use443 || undefined,
      steal_mode: form.stealSelf ? form.stealMode : undefined,
      site_type: form.stealSelf ? form.siteType : undefined,
      site_value: form.stealSelf ? form.siteValue.trim() || undefined : undefined,
      xray_mode: form.xrayMode,
      traffic_stats_mode: form.trafficStatsMode,
      traffic_source: form.trafficSource,
      ddns_enabled: form.ddnsEnabled,
      ddns_provider_id: form.ddnsProviderId,
      ipv6_enabled: form.ipv6Enabled,
    };

    setSubmitting(true);
    setNotice(null);
    try {
      const response = await createRemoteServer(sessionToken, payload);
      if (!response.success) throw new Error(response.message || "创建服务器失败");
      setCreatedServer(response.server ?? null);
      setInstallCommand(response.install_command ?? "");
      setNotice({ kind: "success", text: response.message || "服务器创建成功，已重新读取服务器列表" });
      await onChanged();
    } catch (err) {
      setNotice({ kind: "error", text: err instanceof Error ? err.message : "创建服务器失败" });
    } finally {
      setSubmitting(false);
    }
  }

  async function copyText(value: string, label: string) {
    if (!value) return;
    try {
      await navigator.clipboard.writeText(value);
      setCopied(label);
      window.setTimeout(() => setCopied((current) => (current === label ? "" : current)), 1800);
    } catch {
      setNotice({ kind: "error", text: "复制失败，请手动选择文本复制" });
    }
  }

  async function copyServerToken() {
    if (!createdServer?.id) return;
    try {
      const response = await revealRemoteServerToken(sessionToken, createdServer.id);
      await copyText(response.token ?? "", "server-token");
    } catch (err) {
      setNotice({ kind: "error", text: err instanceof Error ? err.message : "读取 Token 失败" });
    }
  }

  return (
    <>
      <form id="add-remote-server-form" className="service-dialog-body add-server-form" onSubmit={submit}>
        {notice && (
          <div className={`add-server-notice ${notice.kind}`} role="status">
            {notice.kind === "success" ? <CheckCircle2 /> : notice.kind === "error" ? <AlertTriangle /> : <ShieldCheck />}
            <span>{notice.text}</span>
          </div>
        )}

        {createdServer && installCommand && (
          <section className="add-server-result">
            <div>
              <strong>安装命令</strong>
              <p>在远程服务器 SSH 中执行。Agent 连上后，服务器列表会自动刷新。</p>
            </div>
            <textarea value={installCommand} readOnly spellCheck={false} />
            <div className="add-server-copy-row">
              <button type="button" onClick={() => void copyText(installCommand, "install")}>
                <Copy /> {copied === "install" ? "已复制" : "复制安装命令"}
              </button>
              <button type="button" onClick={() => void copyServerToken()}>
                <KeyRound /> {copied === "server-token" ? "已复制" : "复制 Server Token"}
              </button>
            </div>
          </section>
        )}

        <section className="add-server-section">
          <h4>基本信息</h4>
          <div className="service-form-grid">
            <label>
              <span>服务器名称 *</span>
              <input value={form.name} onChange={(event) => update("name", event.target.value)} placeholder="例如：US Node 1" disabled={Boolean(createdServer)} />
            </label>
            <label>
              <span>服务器地址</span>
              <input value={form.pullAddress} onChange={(event) => update("pullAddress", event.target.value)} placeholder="例如：example.com" disabled={Boolean(createdServer)} onBlur={(event) => {
                if (form.stealSelf && !form.domain.trim() && looksLikeDomain(event.target.value)) update("domain", event.target.value.trim());
              }} />
            </label>
            <label>
              <span>Agent 端口</span>
              <input type="number" value={form.agentPort} onChange={(event) => update("agentPort", event.target.value)} placeholder="23889" disabled={Boolean(createdServer)} />
            </label>
            <label>
              <span>Agent Auth Token（可选）</span>
              <input value={form.agentToken} onChange={(event) => update("agentToken", event.target.value)} placeholder="留空自动生成" disabled={Boolean(createdServer)} />
            </label>
          </div>
        </section>

        <section className="add-server-section">
          <h4>DDNS 与 IPv6</h4>
          <ToggleLine title="DDNS" desc="开启后 Agent 上报 IP 漂移时会同步 A/AAAA 记录，服务器地址必须是域名。" checked={form.ddnsEnabled} disabled={Boolean(createdServer)} onChange={(checked) => update("ddnsEnabled", checked)} />
          {form.ddnsEnabled && (
            <div className="service-form-grid">
              <label>
                <span>DDNS 服务商</span>
                <select value={form.ddnsProviderId} onChange={(event) => update("ddnsProviderId", Number(event.target.value))} disabled={Boolean(createdServer)}>
                  <option value={0}>自动（按证书和 DNS 服务商）</option>
                  {dnsProviders.map((provider) => {
                    const id = provider.id ?? provider.ID ?? 0;
                    return <option key={id} value={id}>{provider.name ?? provider.Name ?? `Provider ${id}`}</option>;
                  })}
                </select>
                {!loadingMeta && form.ddnsProviderId === 0 && form.pullAddress && !ddnsCertificate && (
                  <small>未找到匹配该域名的通配符证书，正式后端会继续按 DNS 服务商兜底校验。</small>
                )}
              </label>
              <label>
                <span>IPv6 域名（AAAA）</span>
                <input value={form.pullAddressV6} onChange={(event) => update("pullAddressV6", event.target.value)} placeholder="留空则与服务器地址相同" disabled={Boolean(createdServer)} />
              </label>
            </div>
          )}
          <ToggleLine title="启用 IPv6" desc="关闭后，服务管理不显示该服务器 v6，添加节点不可选 v6。" checked={form.ipv6Enabled} disabled={Boolean(createdServer)} onChange={(checked) => update("ipv6Enabled", checked)} />
        </section>

        <section className="add-server-section">
          <h4>流量与账期</h4>
          <div className="service-form-grid">
            <label>
              <span>流量额度（GB）</span>
              <input type="number" step="0.01" value={form.trafficLimitGb} onChange={(event) => update("trafficLimitGb", event.target.value)} placeholder="留空为无限流量" disabled={Boolean(createdServer)} />
            </label>
            <label>
              <span>已用流量（GB）</span>
              <input type="number" step="0.01" value={form.trafficUsedGb} onChange={(event) => update("trafficUsedGb", event.target.value)} placeholder="用于迁移/校准" disabled={Boolean(createdServer)} />
            </label>
            <label>
              <span>每月重置日</span>
              <input type="number" min={1} max={31} value={form.resetDay} onChange={(event) => update("resetDay", event.target.value)} placeholder="1-31，留空不重置" disabled={Boolean(createdServer)} />
            </label>
          </div>
        </section>

        <section className="add-server-section">
          <h4>运行模式</h4>
          <RadioGroup label="Xray 模式" value={form.xrayMode} disabled={Boolean(createdServer)} options={[
            { value: "external", label: "External Xray", desc: "独立 Xray 进程，Agent 通过 gRPC 管理。" },
            { value: "embedded", label: "Embedded Xray", desc: "Agent 内嵌 Xray-core，支持自动限速、设备限制及更多节点类型。" },
          ]} onChange={(value) => update("xrayMode", value as AddServerFormState["xrayMode"])} />
          <RadioGroup label="流量统计规则" value={form.trafficStatsMode} disabled={Boolean(createdServer)} options={[
            { value: "both", label: "上行 + 下行" },
            { value: "upload", label: "仅上行" },
            { value: "download", label: "仅下行" },
            { value: "max", label: "取最大（上/下行）" },
          ]} onChange={(value) => update("trafficStatsMode", value as AddServerFormState["trafficStatsMode"])} />
          <RadioGroup label="服务器流量数据源" value={form.trafficSource} disabled={Boolean(createdServer)} options={[
            { value: "xray", label: "Xray 协议流量", desc: "聚合该服务器节点流量，只包含走 Xray 协议的流量。" },
            { value: "system", label: "系统网卡流量", desc: "走 Agent /proc/net/dev 的物理网卡 RX+TX 累计，更接近 VPS 服务商口径。" },
          ]} onChange={(value) => update("trafficSource", value as AddServerFormState["trafficSource"])} />
        </section>

        <section className="add-server-section">
          <h4>Steal Self / 443 部署</h4>
          <ToggleLine title="Steal Self" desc="开启后，安装 Agent 后会自动安装 Xray + Nginx，并可自动部署 443 配置。" checked={form.stealSelf} disabled={Boolean(createdServer)} onChange={(checked) => {
            setForm((current) => ({ ...current, stealSelf: checked, use443: checked ? true : false, domain: checked && !current.domain && looksLikeDomain(current.pullAddress) ? current.pullAddress : current.domain }));
          }} />
          {form.stealSelf && (
            <>
              <RadioGroup label="前置服务" value={form.frontService} disabled={Boolean(createdServer)} options={[
                { value: "xray", label: "Xray" },
                { value: "nginx", label: "Nginx（暂未支持）", disabled: true },
              ]} onChange={(value) => update("frontService", value as AddServerFormState["frontService"])} />
              <RadioGroup label="部署模式" value={form.stealMode} disabled={Boolean(createdServer)} options={[
                { value: "tunnel", label: "Tunnel Mode", desc: "Xray 监听 443，通过 tunnel 转发到 Nginx。" },
                { value: "fallback", label: "Fallback Mode", desc: "Xray fallback 到 Nginx。" },
              ]} onChange={(value) => update("stealMode", value as AddServerFormState["stealMode"])} />
              <ToggleLine title="部署在 443 端口" desc="开启后必须填写域名；Agent 连接后会部署证书、Nginx 与 Xray 443 配置。" checked={form.use443} disabled={Boolean(createdServer) || form.stealSelf} onChange={(checked) => update("use443", checked)} />
              <div className="service-form-grid">
                <label>
                  <span>域名 *</span>
                  <input value={form.domain} onChange={(event) => update("domain", event.target.value)} placeholder="例如：us1.example.com" disabled={Boolean(createdServer)} />
                </label>
                <label>
                  <span>站点类型</span>
                  <select value={form.siteType} onChange={(event) => update("siteType", event.target.value as AddServerFormState["siteType"])} disabled={Boolean(createdServer)}>
                    <option value="static">静态页面</option>
                    <option value="proxy">反向代理</option>
                  </select>
                </label>
                <label className="wide">
                  <span>{form.siteType === "static" ? "静态页面路径" : "反向代理地址"}</span>
                  <input value={form.siteValue} onChange={(event) => update("siteValue", event.target.value)} placeholder={form.siteType === "static" ? "例如：/var/www/html" : "例如：http://127.0.0.1:8080"} disabled={Boolean(createdServer)} />
                </label>
              </div>
            </>
          )}
        </section>

        <section className="add-server-section compact">
          <h4>提交摘要</h4>
          <dl className="add-server-summary">
            <div><dt>正式 API</dt><dd>POST /api/admin/remote-servers/create</dd></div>
            <div><dt>Master URL</dt><dd>{masterUrl || "未配置，后端会回退到请求 Host"}</dd></div>
            <div><dt>服务器数量</dt><dd>{servers.length} 台</dd></div>
            <div><dt>创建副作用</dt><dd>写 Remote Server 记录，生成 Token，返回安装命令；不直接安装 Agent/Xray。</dd></div>
          </dl>
        </section>
      </form>

      <div className="service-dialog-actions">
        <button type="button" onClick={onClose}>{createdServer ? "完成" : "取消"}</button>
        <button type="submit" className="primary" form="add-remote-server-form" disabled={!canSubmit}>
          {submitting ? "生成中..." : "生成 Token"}
        </button>
      </div>
    </>
  );
}

function AddSharedServerDialog({
  sessionToken,
  servers,
  onChanged,
  onClose,
}: {
  sessionToken: string;
  servers: RemoteServer[];
  onChanged: () => Promise<void>;
  onClose: () => void;
}) {
  const [ownerUrl, setOwnerUrl] = useState("");
  const [shareToken, setShareToken] = useState("");
  const [name, setName] = useState("");
  const [prefix, setPrefix] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [notice, setNotice] = useState<{ kind: "success" | "error" | "info"; text: string } | null>(null);
  const [created, setCreated] = useState<{ id: number; name: string; status?: string } | null>(null);

  const canSubmit = ownerUrl.trim().length > 0 && shareToken.trim().length > 0 && !submitting && !created;

  function update(setter: (value: string) => void, value: string) {
    setter(value);
    setNotice(null);
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (!ownerUrl.trim() || !shareToken.trim()) {
      setNotice({ kind: "error", text: "拥有方地址和分享令牌必填" });
      return;
    }

    setSubmitting(true);
    setNotice(null);
    try {
      const response = await addSharedRemoteServer(sessionToken, {
        owner_url: ownerUrl.trim(),
        share_token: shareToken.trim(),
        name: name.trim() || undefined,
        prefix: prefix.trim() || undefined,
      });
      if (response.success === false) throw new Error(response.message || "接入分享服务器失败");
      setCreated({ id: response.id, name: response.name, status: response.status });
      setNotice({ kind: "success", text: `已接入 ${response.name || "共享服务器"}，服务器列表已重新读取` });
      await onChanged();
    } catch (err) {
      setNotice({ kind: "error", text: err instanceof Error ? err.message : "接入分享服务器失败" });
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      <form id="add-shared-server-form" className="service-dialog-body add-server-form shared-server-form" onSubmit={submit}>
        {notice && (
          <div className={`add-server-notice ${notice.kind}`} role="status">
            {notice.kind === "success" ? <CheckCircle2 /> : notice.kind === "error" ? <AlertTriangle /> : <Share2 />}
            <span>{notice.text}</span>
          </div>
        )}

        <section className="add-server-section">
          <h4>接入分享服务器</h4>
          <p className="add-server-section-desc">
            填入拥有方提供的拥有方地址与分享令牌即可接入。接入后可像自己的服务器一样管理，添加节点时建议使用入站前缀区分。
          </p>
          <div className="service-form-grid">
            <label>
              <span>拥有方地址 *</span>
              <input
                value={ownerUrl}
                onChange={(event) => update(setOwnerUrl, event.target.value)}
                placeholder="https://owner.example.com"
                disabled={Boolean(created)}
                inputMode="url"
              />
            </label>
            <label>
              <span>分享令牌 *</span>
              <input
                value={shareToken}
                onChange={(event) => update(setShareToken, event.target.value)}
                placeholder="拥有方生成的令牌"
                disabled={Boolean(created)}
                autoComplete="off"
              />
            </label>
            <label>
              <span>服务器名称（可选）</span>
              <input value={name} onChange={(event) => update(setName, event.target.value)} placeholder="留空则使用拥有方的名称" disabled={Boolean(created)} />
            </label>
            <label>
              <span>入站前缀</span>
              <input value={prefix} onChange={(event) => update(setPrefix, event.target.value)} placeholder="如 myx-" disabled={Boolean(created)} />
              <small>在该分享服务器上新增入站时，标签会自动加上此前缀，避免与拥有方已有入站冲突。设置后固定复用。</small>
            </label>
          </div>
        </section>

        <section className="add-server-section compact">
          <h4>提交摘要</h4>
          <dl className="add-server-summary">
            <div><dt>正式 API</dt><dd>POST /api/admin/remote-servers/add-shared</dd></div>
            <div><dt>必填字段</dt><dd>拥有方地址、分享令牌</dd></div>
            <div><dt>可选字段</dt><dd>服务器名称、入站前缀</dd></div>
            <div><dt>当前服务器数量</dt><dd>{servers.length} 台</dd></div>
            <div><dt>接入副作用</dt><dd>校验拥有方联邦接口，写入本地主控 Remote Server 和分享服务器标记；不会安装 Agent，也不会修改拥有方配置。</dd></div>
            {created && <div><dt>接入结果</dt><dd>#{created.id} · {created.name}{created.status ? ` · ${created.status}` : ""}</dd></div>}
          </dl>
        </section>
      </form>

      <div className="service-dialog-actions">
        <button type="button" onClick={onClose}>{created ? "完成" : "取消"}</button>
        <button type="submit" className="primary" form="add-shared-server-form" disabled={!canSubmit}>
          {submitting ? "接入中..." : "接入"}
        </button>
      </div>
    </>
  );
}

function ToggleLine({ title, desc, checked, disabled, onChange }: { title: string; desc?: string; checked: boolean; disabled?: boolean; onChange: (checked: boolean) => void }) {
  return (
    <label className="add-server-toggle">
      <span>
        <strong>{title}</strong>
        {desc && <small>{desc}</small>}
      </span>
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(event) => onChange(event.target.checked)} />
      <i />
    </label>
  );
}

function RadioGroup({ label, value, options, disabled, onChange }: { label: string; value: string; options: Array<{ value: string; label: string; desc?: string; disabled?: boolean }>; disabled?: boolean; onChange: (value: string) => void }) {
  return (
    <fieldset className="add-server-radio-group">
      <legend>{label}</legend>
      <div>
        {options.map((option) => (
          <label key={option.value} className={option.disabled ? "disabled" : ""}>
            <input type="radio" name={label} value={option.value} checked={value === option.value} disabled={disabled || option.disabled} onChange={() => onChange(option.value)} />
            <span>
              <strong>{option.label}</strong>
              {option.desc && <small>{option.desc}</small>}
            </span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}

function parseOptionalInt(value: string) {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  const parsed = Number(trimmed);
  if (!Number.isFinite(parsed)) return undefined;
  return Math.trunc(parsed);
}

function gbToBytes(value: string) {
  const trimmed = value.trim();
  if (!trimmed) return 0;
  const parsed = Number(trimmed);
  if (!Number.isFinite(parsed) || parsed < 0) return null;
  return Math.round(parsed * 1024 * 1024 * 1024);
}

function isIPAddress(value: string) {
  const trimmed = value.trim();
  if (!trimmed) return false;
  return /^\d{1,3}(?:\.\d{1,3}){3}$/.test(trimmed) || /^[0-9a-f:]+$/i.test(trimmed);
}

function looksLikeDomain(value: string) {
  const trimmed = value.trim().toLowerCase();
  return Boolean(trimmed && trimmed.includes(".") && /^[a-z0-9.-]+$/.test(trimmed) && !isIPAddress(trimmed));
}

function findCertificateForDomain(certificates: ValidCertificate[], domain: string) {
  const normalized = domain.trim().toLowerCase();
  if (!looksLikeDomain(normalized)) return undefined;
  const exact = certificates.find((certificate) => certificate.domain?.toLowerCase() === normalized && Number(certificate.dns_provider_id) > 0);
  if (exact) return exact;
  const parts = normalized.split(".");
  for (let index = 1; index < parts.length - 1; index += 1) {
    const wildcard = `*.${parts.slice(index).join(".")}`;
    const match = certificates.find((certificate) => certificate.domain?.toLowerCase() === wildcard && Number(certificate.dns_provider_id) > 0);
    if (match) return match;
  }
  return undefined;
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
    { key: "nodes", label: "节点管理", icon: Boxes },
    { key: "users", label: "用户", icon: Users },
    { key: "packages", label: "套餐管理", icon: PackageIcon },
    { key: "forward", label: "转发管理", icon: Share2 },
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
    </div>
  );
}

function TrafficRow({ name, up, down, badge, prefix }: { name: string; up: number; down: number; badge?: React.ReactNode; prefix?: React.ReactNode }) {
  return (
    <div className="traffic-row">
      <div>
        <div className="traffic-row-title">
          {prefix}
          <strong>{name}</strong>
        </div>
        {badge && <div className="traffic-row-detail">{badge}</div>}
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

function serviceGroupsStorageKey(username: string) {
  return `${SERVICE_GROUPS_STORAGE_PREFIX}${username || "default"}`;
}

function loadServiceGroups(username: string): ServiceGroup[] {
  if (typeof window === "undefined") return [];
  try {
    const raw = window.localStorage.getItem(serviceGroupsStorageKey(username));
    if (!raw) return [];
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return sanitizeServiceGroups(parsed, []);
  } catch {
    return [];
  }
}

function saveServiceGroups(username: string, groups: ServiceGroup[]) {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(serviceGroupsStorageKey(username), JSON.stringify(groups));
  } catch {
    // Keep the in-memory grouping if the browser refuses localStorage writes.
  }
}

function sanitizeServiceGroups(groups: unknown, servers: RemoteServer[]): ServiceGroup[] {
  if (!Array.isArray(groups)) return [];
  const validServerIds = new Set(servers.map((server) => server.id));
  const seenGroupIds = new Set<string>();
  const seenNames = new Set<string>();
  const sanitized: ServiceGroup[] = [];

  for (const item of groups) {
    if (!item || typeof item !== "object") continue;
    const source = item as Partial<ServiceGroup>;
    const id = typeof source.id === "string" && source.id.trim() ? source.id.trim() : createServiceGroupId();
    const name = normalizeServiceGroupName(source.name);
    if (!name || seenGroupIds.has(id) || seenNames.has(name.toLowerCase())) continue;
    const serverIds = Array.isArray(source.serverIds)
      ? [...new Set(source.serverIds.filter((idValue): idValue is number => Number.isInteger(idValue) && (validServerIds.size === 0 || validServerIds.has(idValue))))]
      : [];
    seenGroupIds.add(id);
    seenNames.add(name.toLowerCase());
    sanitized.push({ id, name, serverIds });
  }
  return serviceGroupsEqual(groups, sanitized) ? groups as ServiceGroup[] : sanitized;
}

function serviceGroupsEqual(left: unknown, right: ServiceGroup[]) {
  if (!Array.isArray(left) || left.length !== right.length) return false;
  return left.every((item, index) => {
    const group = item as Partial<ServiceGroup>;
    const other = right[index];
    return group.id === other.id
      && group.name === other.name
      && Array.isArray(group.serverIds)
      && group.serverIds.length === other.serverIds.length
      && group.serverIds.every((serverId, serverIndex) => serverId === other.serverIds[serverIndex]);
  });
}

function normalizeServiceGroupName(value?: string | null) {
  return value?.trim().replace(/\s+/g, " ").slice(0, 28) ?? "";
}

function serviceGroupNameExists(groups: ServiceGroup[], name: string, exceptId?: string) {
  const normalized = normalizeServiceGroupName(name).toLowerCase();
  if (!normalized) return false;
  return groups.some((group) => group.id !== exceptId && group.name.toLowerCase() === normalized);
}

function createServiceGroupId() {
  return `group-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
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

function serversByName(servers: RemoteServer[]) {
  return new Map(servers.map((server) => [server.name.toLocaleLowerCase(), server]));
}

function stripLeadingCountryFlag(value: string) {
  return value.replace(/^[\u{1F1E6}-\u{1F1FF}]{2}\s*/u, "");
}

function rangeLabel(range: TrafficRange) {
  return range === "today" ? "今天" : range === "week" ? "本周" : "本月";
}

function formatPeriodStart(range: TrafficRange, value?: string) {
  const [, , month, day] = value?.match(/^(\d{4})-(\d{2})-(\d{2})$/) ?? [];
  if (!month || !day) return `${rangeLabel(range)}起始日 00:00 起`;
  if (range === "today") return `今天 · ${Number(month)} 月 ${Number(day)} 日 00:00 起`;
  if (range === "month") return `本月 ${Number(day)} 日 00:00 起`;
  return `${Number(month)} 月 ${Number(day)} 日 00:00 起`;
}

function useServerRegion(server?: RemoteServer) {
  const address = server ? serverRegionAddress(server) : "";
  const fieldKey = server ? serverRegionFieldKey(server) : "";
  const [region, setRegion] = useState(() => server ? serverRegionFromFields(server) ?? (address ? loadingRegion() : unknownRegion()) : unknownRegion());

  useEffect(() => {
    const controller = new AbortController();
    let mounted = true;

    if (!server) {
      setRegion(unknownRegion());
      return () => controller.abort();
    }

    const fromFields = serverRegionFromFields(server);
    if (fromFields) {
      setRegion(fromFields);
      return () => controller.abort();
    }
    if (!address) {
      setRegion(unknownRegion());
      return () => controller.abort();
    }

    setRegion(loadingRegion());
    lookupServerRegion(server, controller.signal).then((nextRegion) => {
      if (mounted && !controller.signal.aborted) setRegion(nextRegion);
    });
    return () => {
      mounted = false;
      controller.abort();
    };
  }, [address, fieldKey, server?.id]);

  return region;
}

function serverRegionFieldKey(server: RemoteServer) {
  return [
    server.country_code,
    server.region_country,
    server.geo_country_code,
    server.country,
    server.geo_country,
    server.region,
    server.region_name,
    server.region_city,
    server.location,
    server.service_location,
    server.displayLocation,
    server.display_location,
    server.countryName,
    server.flag,
  ].join("|");
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

function formatConnectionMode(mode?: string) {
  if (!mode) return "--";
  return mode === "ws" ? "WebSocket" : mode === "pull" ? "Pull" : mode === "push" ? "Push" : mode;
}

function formatXrayMode(mode?: string) {
  if (!mode) return "--";
  return mode === "embedded" ? "Embedded Xray" : mode === "external" ? "External Xray" : mode;
}

function formatAgentVersion(version?: string) {
  if (!version) return "--";
  return formatXrayVersion(stripVersionPrefix(version));
}

function formatXrayVersion(version: string) {
  if (/^xray\b/i.test(version)) return version;
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

function trafficUsagePercent(server: RemoteServer) {
  const total = server.traffic_limit ?? 0;
  if (total <= 0) return null;
  const used = Math.max(server.traffic_used ?? 0, 0);
  return Math.max(0, Math.min(100, (used / total) * 100));
}

function trafficRemaining(server: RemoteServer) {
  const total = server.traffic_limit ?? 0;
  if (total <= 0) return null;
  return Math.max(total - Math.max(server.traffic_used ?? 0, 0), 0);
}

function trafficCycleText(server: RemoteServer) {
  const resetDay = server.traffic_reset_day ?? 0;
  if (resetDay < 1 || resetDay > 31) return "账期未设置";
  const start = currentTrafficCycleStart(server);
  return start ? `账期 ${start.getMonth() + 1}月${start.getDate()}日 00:00 起` : `每月 ${resetDay} 日重置`;
}

function currentTrafficCycleStart(server: RemoteServer) {
  if (server.last_traffic_reset_at) {
    const reset = new Date(server.last_traffic_reset_at);
    if (!Number.isNaN(reset.getTime())) return reset;
  }
  const resetDay = server.traffic_reset_day ?? 0;
  if (resetDay < 1 || resetDay > 31) return null;
  const now = new Date();
  const candidate = trafficResetDate(now.getFullYear(), now.getMonth(), resetDay);
  return candidate.getTime() <= now.getTime() ? candidate : trafficResetDate(now.getFullYear(), now.getMonth() - 1, resetDay);
}

function trafficResetDate(year: number, month: number, resetDay: number) {
  const lastDay = new Date(year, month + 1, 0).getDate();
  return new Date(year, month, Math.min(resetDay, lastDay), 0, 0, 0, 0);
}

function trafficResetRemainingText(server: RemoteServer, now = new Date()) {
  const nextReset = nextTrafficResetAt(server, now);
  if (!nextReset) return "账期未知";

  const remainingMs = nextReset.getTime() - now.getTime();
  if (remainingMs <= 0) return "今日重置";

  const remainingMinutes = Math.max(1, Math.floor(remainingMs / 60_000));
  if (remainingMinutes < 60) return `剩余 ${remainingMinutes} 分钟`;

  const remainingHours = Math.floor(remainingMs / 3_600_000);
  if (remainingHours < 24) return `剩余 ${remainingHours} 小时`;

  return `剩余 ${Math.floor(remainingMs / 86_400_000)} 天`;
}

function nextTrafficResetAt(server: RemoteServer, now: Date) {
  const resetDay = server.traffic_reset_day ?? 0;
  if (resetDay < 1 || resetDay > 31 || Number.isNaN(now.getTime())) return null;

  const thisMonth = trafficResetDateUTC(now.getUTCFullYear(), now.getUTCMonth(), resetDay);
  if (now.getTime() < thisMonth.getTime()) return thisMonth;

  const lastReset = server.last_traffic_reset_at ? new Date(server.last_traffic_reset_at) : null;
  if (!lastReset || Number.isNaN(lastReset.getTime()) || lastReset.getTime() < thisMonth.getTime()) {
    return thisMonth;
  }

  return trafficResetDateUTC(now.getUTCFullYear(), now.getUTCMonth() + 1, resetDay);
}

function trafficResetDateUTC(year: number, month: number, resetDay: number) {
  const lastDay = new Date(Date.UTC(year, month + 1, 0)).getUTCDate();
  return new Date(Date.UTC(year, month, Math.min(resetDay, lastDay)));
}

function trafficResetText(server: RemoteServer) {
  const parts: string[] = [];
  if (server.traffic_reset_day) parts.push(`每月 ${server.traffic_reset_day} 日重置`);
  if (server.last_traffic_reset_at) parts.push(`上次重置 ${formatDateTime(server.last_traffic_reset_at)}`);
  return parts.join(" · ") || trafficLimitText(server);
}

function asString(value: unknown) {
  return typeof value === "string" ? value : value == null ? "" : String(value);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === "object" && !Array.isArray(value));
}

function shortHash(value?: string) {
  if (!value) return "--";
  return value.length > 12 ? `${value.slice(0, 12)}...` : value;
}

function stripAnsi(value: string) {
  return value.replace(/\u001b\[[0-9;]*[a-zA-Z]/g, "");
}

function formatJsonText(value: string) {
  if (!value) return "";
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    return value;
  }
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
    packages: "套餐管理",
    forward: "转发管理",
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
