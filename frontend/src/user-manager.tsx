import React, { useCallback, useEffect, useMemo, useState } from "react";
import { QRCodeSVG } from "qrcode.react";
import {
  ArrowLeft,
  Copy,
  Edit3,
  KeyRound,
  Link2,
  PackageCheck,
  Plus,
  Power,
  PowerOff,
  QrCode,
  RefreshCw,
  RotateCcw,
  Search,
  Send,
  ShieldCheck,
  SlidersHorizontal,
  Trash2,
  UserRoundCog,
  X,
} from "lucide-react";
import {
  assignManagedUserPackage,
  addManagedUserPackageAssignment,
  updateManagedUserPackageAssignment,
  deleteManagedUserPackageAssignment,
  fetchManagedUserAvailablePackages,
  updateManagedUserNickname,
  fetchNodeURIs,
  fetchManagedUserImportedNodes,
  clearManagedUserImportedNodes,
  replaceAdminCredentials,
  repairAdminCredentials,
  fetchUserConnections,
  fetchHelperUserConnections,
  createManagedUser,
  deleteManagedUser,
  extendManagedUserPackage,
  fetchManagedUserNodes,
  fetchManagedUsers,
  fetchManagedUserDeletionPreview,
  fetchManagedUserLifecycles,
  fetchManagedUserPackageAssignments,
  fetchManagedUserSubaccounts,
  fetchManagedUserTelegram,
  fetchPackages,
  fetchUserConfig,
  managedSubscriptionUrlFromCode,
  createManagedUserTelegramInvite,
  resetManagedUserPassword,
  resetManagedUserTraffic,
  setManagedUserLifecycleAccess,
  unassignManagedUserPackage,
  unbindManagedUserTelegram,
  updateManagedUserLimits,
  updateManagedUserNodeLimits,
  updateManagedUserRemark,
  updateManagedUserShortCode,
} from "./api";
import type { HelperUserConnectionsResponse, UserConnectionsResponse, NodeURIItem, ManagedPackage, ManagedUser, ManagedUserImportedNode, ManagedUserDeleteResult, ManagedUserDeletionPreview, ManagedUserLifecycle, ManagedUserLifecycleItem, ManagedUserPackageAssignment, UserSubaccount, XrayNode } from "./types";
import { fetchUserManagementData } from "./user-management-state";
import { credentialWriteState, eligiblePackages, matchesExpiry, parseRenewDays, parseTrafficOverride, remainingDays, renewedDate, searchUserURIs, trafficOverrideGB, validUsername } from "./user-manager-logic";
import type { ExpiryFilter } from "./user-manager-logic";

type Notice = { tone: "success" | "error" | "info"; text: string } | null;
type Dialog =
  | { kind: "create" }
  | { kind: "password"; user: ManagedUser }
  | { kind: "profile"; user: ManagedUser }
  | { kind: "package"; user: ManagedUser }
  | { kind: "limits"; user: ManagedUser }
  | { kind: "accounts"; user: ManagedUser; initial?: "all" | "inbound" | "routed" }
  | { kind: "subscription"; user: ManagedUser }
  | { kind: "telegram"; user: ManagedUser }
  | { kind: "delete"; user: ManagedUser }
  | { kind: "renew" | "uris" | "imports" | "replace-admin" | "repair-admin"; user: ManagedUser }
  | null;

const clients = [
  ["auto", "自动识别"],
  ["clash", "Clash"], ["stash", "Stash"], ["shadowrocket", "Shadowrocket"],
  ["clash-to-shadowrocket", "Clash → Shadowrocket"], ["surfboard", "Surfboard"],
  ["surge", "Surge"], ["surgemac", "Surge Mac"], ["clash-to-surge", "Clash → Surge"],
  ["loon", "Loon"], ["clash-to-loon", "Clash → Loon"], ["clash-to-loon-kelee", "Clash → Loon (kelee)"],
  ["qx", "Quantumult X"], ["egern", "Egern"], ["sing-box", "sing-box"], ["v2ray", "V2Ray"], ["uri", "URI"],
] as const;

const randomPassword = () => {
  const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789";
  return Array.from({ length: 12 }, () => chars[Math.floor(Math.random() * chars.length)]).join("");
};

const today = () => new Date().toISOString().slice(0, 10);
const nextMonth = () => {
  const date = new Date();
  date.setMonth(date.getMonth() + 1);
  return date.toISOString().slice(0, 10);
};

function managedUserPackageIds(user: ManagedUser) {
  return user.assignment_package_ids?.length ? user.assignment_package_ids : user.package_id ? [user.package_id] : [];
}

export function UserManagementPage({ token, currentUsername }: { token: string; currentUsername: string }) {
  const [users, setUsers] = useState<ManagedUser[]>([]);
  const [packages, setPackages] = useState<ManagedPackage[]>([]);
  const [nodes, setNodes] = useState<XrayNode[]>([]);
  const [lifecycles, setLifecycles] = useState<Record<string, ManagedUserLifecycle>>({});
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState<Notice>(null);
  const [dialog, setDialog] = useState<Dialog>(null);
  const [query, setQuery] = useState("");
  const [packageFilter, setPackageFilter] = useState("all");
  const [expiryFilter, setExpiryFilter] = useState<ExpiryFilter>("all");
  const [connections, setConnections] = useState<UserConnectionsResponse | null>(null);
  const [helperConnections, setHelperConnections] = useState<HelperUserConnectionsResponse | null>(null);
  const [realtimeError, setRealtimeError] = useState("");
  const [view, setView] = useState<"full" | "renewal">(() => localStorage.getItem("users-view-mode") === "package" ? "renewal" : "full");

  const load = useCallback(async (options?: { background?: boolean; success?: string }) => {
    if (!options?.background) setLoading(true);
    const [result, lifecycleResult] = await Promise.all([
      fetchUserManagementData(
        () => fetchManagedUsers(token),
        () => fetchPackages(token),
        () => fetchManagedUserNodes(token),
      ),
      fetchManagedUserLifecycles(token).then((response) => ({ users: response.users, error: "" })).catch((error) => ({ users: {}, error: messageOf(error, "读取用户生命周期失败") })),
    ]);
    if (result.users) {
      setUsers(result.users);
    }
    setLifecycles(lifecycleResult.users);
    if (result.packages) setPackages(result.packages);
    if (result.nodes) setNodes(result.nodes);
    const failures = lifecycleResult.error ? [...result.failures, lifecycleResult.error] : result.failures;
    if (failures.length > 0) {
      setNotice({
        tone: options?.success ? "info" : "error",
        text: options?.success ? `${options.success}；部分页面数据刷新失败` : failures[0],
      });
    } else if (options?.success) {
      setNotice({ tone: "success", text: options.success });
    }
    if (!options?.background) setLoading(false);
  }, [token]);

  useEffect(() => { void load(); }, [load]);

  useEffect(() => {
    let stopped = false;
    let timer: number | undefined;
    let inFlight = false;
    const isVisible = () => document.visibilityState !== "hidden";
    async function refresh() {
      if (stopped || inFlight || document.visibilityState === "hidden") return;
      inFlight = true;
      const [formal, helper] = await Promise.allSettled([fetchUserConnections(token), fetchHelperUserConnections(token)]);
      if (!stopped) {
        setConnections(formal.status === "fulfilled" ? formal.value : null);
        setHelperConnections(helper.status === "fulfilled" ? helper.value : null);
        setRealtimeError(formal.status === "rejected" || helper.status === "rejected" ? "统计不完整：部分实时数据读取失败" : "");
      }
      inFlight = false;
      if (!stopped && isVisible()) timer = window.setTimeout(() => void refresh(), 5000);
    }
    function visibilityChanged() {
      window.clearTimeout(timer);
      if (document.visibilityState === "visible") void refresh();
    }
    void refresh();
    document.addEventListener("visibilitychange", visibilityChanged);
    return () => { stopped = true; window.clearTimeout(timer); document.removeEventListener("visibilitychange", visibilityChanged); };
  }, [token]);

  const packageById = useMemo(() => new Map(packages.map((pkg) => [pkg.id, pkg])), [packages]);
  const counts = useMemo(() => {
    const result = new Map<string, number>();
    result.set("all", users.filter((user) => user.role !== "admin").length);
    result.set("none", users.filter((user) => managedUserPackageIds(user).length === 0 && user.role !== "admin").length);
    packages.forEach((pkg) => result.set(String(pkg.id), users.filter((user) => managedUserPackageIds(user).includes(pkg.id) && user.role !== "admin").length));
    return result;
  }, [packages, users]);
  const visible = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return users.filter((user) => {
      const packageIds = managedUserPackageIds(user);
      const filterMatch = packageFilter === "all" || (packageFilter === "none" ? packageIds.length === 0 : packageIds.includes(Number(packageFilter)));
      const searchMatch = !normalized || [user.username, user.nickname, user.email, user.remark, user.package_name, user.telegram_username]
        .some((value) => value?.toLowerCase().includes(normalized));
      return filterMatch && searchMatch && (view !== "renewal" || matchesExpiry(user.package_end_date, expiryFilter));
    });
  }, [packageFilter, query, users, view, expiryFilter]);

  async function run(key: string, action: () => Promise<unknown>, success: string) {
    if (busy) return;
    setBusy(key);
    try {
      await action();
      setNotice({ tone: "success", text: success });
      await load();
    } catch (error) {
      setNotice({ tone: "error", text: messageOf(error, "操作失败") });
    } finally {
      setBusy("");
    }
  }

  async function changeAccess(user: ManagedUser, enable: boolean) {
    if (busy) return;
    setBusy(`access-${user.username}`);
    try {
      const response = await setManagedUserLifecycleAccess(token, user.username, enable);
      if (response.result.pending_count > 0) {
        setNotice({ tone: "error", text: `${enable ? "启用" : "禁用"}未完成，还有 ${response.result.pending_count} 个节点待处理，可再次点击重试。` });
      } else {
        setNotice({ tone: "success", text: `用户 ${user.username} 已${enable ? "启用" : "禁用"}` });
      }
      await load({ background: true });
    } catch (error) {
      setNotice({ tone: "error", text: messageOf(error, "更新用户访问状态失败") });
    } finally {
      setBusy("");
    }
  }

  function setViewMode(next: "full" | "renewal") {
    setView(next);
    localStorage.setItem("users-view-mode", next === "renewal" ? "package" : "full");
  }

  return (
    <div className="user-manager">
      <section className="user-hero">
        <div><h1>用户管理</h1><p>查看系统用户，管理账号、套餐、订阅与用户级限制。</p></div>
        <div className="user-head-actions">
          <button type="button" onClick={() => void load()} aria-label="刷新用户"><RefreshCw /></button>
          <button className="primary" type="button" onClick={() => setDialog({ kind: "create" })}><Plus />新增用户</button>
        </div>
      </section>

      {notice && <div className={`user-notice ${notice.tone}`}><span>{notice.text}</span><button type="button" onClick={() => setNotice(null)} aria-label="关闭提示"><X /></button></div>}

      <section className="user-toolbar">
        <label className="user-search"><Search /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索用户名、昵称、邮箱、备注或套餐" /></label>
        <div className="user-view-tabs" aria-label="用户显示方式">
          <button className={view === "full" ? "active" : ""} type="button" onClick={() => setViewMode("full")}>完整视图</button>
          <button className={view === "renewal" ? "active" : ""} type="button" onClick={() => setViewMode("renewal")}>续费视图</button>
        </div>
      </section>

      <section className="user-package-filters" aria-label="按套餐筛选">
        <button className={packageFilter === "all" ? "active" : ""} type="button" onClick={() => setPackageFilter("all")}>全部（{counts.get("all") ?? 0}）</button>
        {packages.map((pkg) => <button className={packageFilter === String(pkg.id) ? "active" : ""} key={pkg.id} type="button" onClick={() => setPackageFilter(String(pkg.id))}>{pkg.name}（{counts.get(String(pkg.id)) ?? 0}）</button>)}
        <button className={packageFilter === "none" ? "active" : ""} type="button" onClick={() => setPackageFilter("none")}>无套餐（{counts.get("none") ?? 0}）</button>
      </section>

      {view === "renewal" && <section className="user-package-filters" aria-label="按到期时间筛选">
        {([["all", "全部到期时间"], ["expired", "已过期"], ["d7", "7 日内"], ["d30", "30 日内"], ["permanent", "长期"]] as const).map(([value, label]) => <button key={value} type="button" className={expiryFilter === value ? "active" : ""} onClick={() => setExpiryFilter(value)}>{label}</button>)}
      </section>}

      {(realtimeError || connections?.connection_count_ready === false || (connections?.excluded_server_names?.length ?? 0) > 0) && <p className="user-delete-pending">{realtimeError || `统计不完整：${connections?.excluded_server_names?.join("、") || "部分服务器暂不支持连接统计"}`}</p>}

      {loading ? <div className="user-empty">正在读取用户...</div> : visible.length === 0 ? <div className="user-empty">没有符合条件的用户</div> : (
        <section className={`user-list ${view}`}>
          {visible.map((user) => {
            const pkg = user.package_id ? packageById.get(user.package_id) : undefined;
            return <UserCard key={user.username} user={user} lifecycle={lifecycles[user.username]} pkg={pkg} busy={busy} view={view} currentUsername={currentUsername}
              realtime={<UserRealtime user={user} connections={connections} helper={helperConnections} />}
              onDialog={setDialog}
              onStatus={(enable) => {
                if (!window.confirm(`确认${enable ? "启用" : "禁用"}用户 ${user.username}？`)) return;
                void changeAccess(user, enable);
              }}
              onExtend={(days) => {
                const confirmed = confirmCredentialWrite(lifecycles[user.username]);
                if (confirmed === null) return;
                void run(`extend-${user.username}`, () => extendManagedUserPackage(token, user.username, days, confirmed), `用户 ${user.username} 已续期 ${days} 天`);
              }}
              onResetTraffic={() => {
                if (!window.confirm(`确认将 ${user.username} 当前流量周期清零？`)) return;
                void run(`traffic-${user.username}`, () => resetManagedUserTraffic(token, user.username), `用户 ${user.username} 流量已重置`);
              }}
              onDelete={() => {
                setDialog({ kind: "delete", user });
              }} />;
          })}
        </section>
      )}

      {dialog?.kind === "create" && <CreateUserDialog token={token} onClose={() => setDialog(null)} onCreated={async (password) => { setDialog(null); setNotice({ tone: "success", text: `用户已创建，初始密码 ${password} 已复制` }); await load(); }} />}
      {dialog?.kind === "password" && <PasswordDialog token={token} user={dialog.user} onClose={() => setDialog(null)} onSaved={(password) => { setDialog(null); setNotice({ tone: "success", text: `新密码 ${password} 已复制` }); }} />}
      {dialog?.kind === "profile" && <ProfileDialog token={token} user={dialog.user} onClose={() => setDialog(null)} onSaved={async () => { setDialog(null); setNotice({ tone: "success", text: "用户资料已更新" }); await load(); }} />}
      {dialog?.kind === "package" && <PackageDialog token={token} user={dialog.user} packages={packages} lifecycle={lifecycles[dialog.user.username]} onClose={() => setDialog(null)} onSaved={async (text) => { setDialog(null); setNotice({ tone: "success", text }); await load(); }} />}
      {dialog?.kind === "renew" && <RenewDialog token={token} user={dialog.user} lifecycle={lifecycles[dialog.user.username]} onClose={() => setDialog(null)} onSaved={async () => { setDialog(null); await load({ success: "用户套餐已续期" }); }} />}
      {dialog?.kind === "uris" && <URIDialog token={token} user={dialog.user} onClose={() => setDialog(null)} />}
      {dialog?.kind === "imports" && <ImportedNodesDialog token={token} user={dialog.user} onClose={() => setDialog(null)} onChanged={async () => { await load({ background: true, success: "已清空该用户导入的节点并清理套餐引用" }); }} />}
      {(dialog?.kind === "replace-admin" || dialog?.kind === "repair-admin") && <AdminCredentialsDialog token={token} user={dialog.user} lifecycle={lifecycles[dialog.user.username]} repair={dialog.kind === "repair-admin"} onClose={() => setDialog(null)} onSaved={async (text) => { setDialog(null); await load({ success: text }); }} />}
      {dialog?.kind === "limits" && <LimitsDialog token={token} user={dialog.user} nodes={nodes} onClose={() => setDialog(null)} onSaved={async () => { setDialog(null); setNotice({ tone: "success", text: "用户限制已更新" }); await load(); }} />}
      {dialog?.kind === "accounts" && <AccountsDialog token={token} user={dialog.user} initial={dialog.initial} onClose={() => setDialog(null)} />}
      {dialog?.kind === "subscription" && <SubscriptionDialog token={token} user={dialog.user} pkg={dialog.user.package_id ? packageById.get(dialog.user.package_id) : undefined} onClose={() => setDialog(null)} onCopied={(client) => setNotice({ tone: "success", text: `${client} 订阅地址已复制` })} />}
      {dialog?.kind === "telegram" && <TelegramDialog token={token} user={dialog.user} onClose={() => setDialog(null)} onChanged={async (text) => { setDialog(null); setNotice({ tone: "success", text }); await load(); }} />}
      {dialog?.kind === "delete" && <DeleteUserDialog token={token} user={dialog.user} lifecycle={lifecycles[dialog.user.username]} onClose={() => setDialog(null)} onResult={async (result) => {
        if (result.user_deleted) {
          setDialog(null);
          setNotice({ tone: "success", text: `用户 ${dialog.user.username} 已删除，用户级关系已重新核对` });
          await load();
          return;
        }
        setNotice({ tone: "error", text: `删除未完成，还有 ${result.pending_count} 个项目待清理，可再次点击删除重试。` });
        await load({ background: true });
      }} />}
    </div>
  );
}

function UserCard({ user, lifecycle, pkg, busy, view, currentUsername, realtime, onDialog, onStatus, onExtend, onResetTraffic, onDelete }: {
  user: ManagedUser; lifecycle?: ManagedUserLifecycle; pkg?: ManagedPackage; busy: string; view: "full" | "renewal";
  currentUsername?: string; realtime?: React.ReactNode;
  onDialog: (dialog: Dialog) => void; onStatus: (enable: boolean) => void; onExtend: (days: number) => void; onResetTraffic: () => void; onDelete: () => void;
}) {
  const used = Number(user.traffic_used) || 0;
  const limit = Number(user.traffic_limit) || 0;
  const percent = limit > 0 ? Math.min(100, (used / limit) * 100) : 0;
  const admin = user.role === "admin";
  const hasPackage = Boolean(user.package_id || user.assignment_package_ids?.length);
  const days = remainingDays(user.package_end_date);
  const officialInactive = user.is_active === false;
  const deleting = lifecycle?.effective_state === "deleting" || lifecycle?.effective_state === "delete_partial";
  const pending = deleting ? lifecycle?.pending_count ?? 0 : 0;
  const accessState = lifecycle?.effective_state ?? "enabled";
  const accessDisabled = accessState === "disabled" || accessState === "partially_disabled" || accessState === "partially_enabled" || accessState === "enabling";
  const accessPending = !deleting && (lifecycle?.pending_count ?? 0) > 0;
  const enableAction = accessState === "disabled" || accessState === "partially_enabled" || accessState === "enabling";
  const accessOperationLabel = lifecycle?.operation === "enable" ? "启用" : "禁用";
  const statusLabel = deleting ? pending > 0 ? "删除未完成" : "正在删除"
    : accessPending ? `${accessOperationLabel}未完成`
      : accessState === "disabling" ? "正在禁用" : accessState === "enabling" ? "正在启用" : accessDisabled ? "已禁用" : "已启用";
  const displayedStatus = officialInactive ? statusLabel === "已启用" ? "官方已停用" : `${statusLabel}（官方已停用）` : statusLabel;
  return (
    <article className={`user-card${accessDisabled || officialInactive ? " disabled" : ""}`}>
      <header>
        <div className="user-identity"><span className="user-avatar">{(user.nickname || user.username).slice(0, 1).toUpperCase()}</span><div><h2>{user.username}</h2><p>{user.nickname || user.username}{user.email ? ` · ${user.email}` : ""}</p></div></div>
        <div className="user-badges"><span>{admin ? "管理员" : "用户"}</span>{!admin && <span className={deleting || accessDisabled || accessPending || officialInactive ? "off" : "ok"}>{displayedStatus}</span>}</div>
      </header>
      {view === "full" && <div className="user-facts">
        <UserFact label="Telegram" value={user.telegram_id ? `@${user.telegram_username || user.telegram_id}` : "未绑定"} />
        <UserFact label="备注" value={user.remark || "—"} />
        <UserFact label="短码" value={user.custom_user_short_code || user.user_short_code || "—"} />
        <UserFact label="限制" value={`${formatLimit(user.speed_limit_override, user.speed_limit_mbps, "Mbps")} · ${formatLimit(user.device_limit_override, user.device_limit, "连接")}`} />
      </div>}
      {view === "full" && realtime}
      <div className="user-package">
        <div><strong>{user.package_name || (hasPackage ? `已绑定 ${user.assignment_package_ids?.length || 1} 个套餐` : "未绑定套餐")}</strong><span>{user.package_end_date ? `到期 ${user.package_end_date}${days === null ? "" : days <= 0 ? " · 已过期" : ` · 剩余 ${days} 天`}` : admin ? "系统管理员" : hasPackage ? "长期有效" : "可绑定套餐后生成订阅"}</span></div>
        {limit > 0 ? <div className="user-traffic"><span><b>{formatBytes(used)}</b> / {formatBytes(limit)} · {percent.toFixed(percent < 10 ? 1 : 0)}%</span><i><em style={{ width: `${percent}%` }} /></i></div> : <span className="user-unlimited">{pkg ? "流量不限" : "—"}</span>}
      </div>
      {view === "renewal" && !admin && <div className="user-renew-actions">{[30, 90, 365].map((days) => <button key={days} type="button" disabled={!hasPackage || deleting || Boolean(busy)} onClick={() => onExtend(days)}>+{days} 天</button>)}<button type="button" disabled={!hasPackage || deleting || Boolean(busy)} onClick={() => onDialog({ kind: "renew", user })}>自定义续期</button></div>}
      <footer>
        <button type="button" onClick={() => onDialog({ kind: "subscription", user })} disabled={!hasPackage}><Link2 />订阅</button>
        <button type="button" onClick={() => onDialog({ kind: "uris", user })}><Link2 />节点 URI</button>
        <button type="button" onClick={() => onDialog({ kind: "package", user })} disabled={admin}><PackageCheck />套餐</button>
        <button type="button" onClick={() => onDialog({ kind: "profile", user })}><Edit3 />资料</button>
        <button type="button" onClick={() => onDialog({ kind: "limits", user })} disabled={admin}><SlidersHorizontal />限制</button>
        <button type="button" onClick={() => onDialog({ kind: "accounts", user, initial: "all" })}><UserRoundCog />子账户</button>
        <button type="button" onClick={() => onDialog({ kind: "imports", user })}>导入节点</button>
        {admin && user.username === currentUsername && <><button type="button" onClick={() => onDialog({ kind: "replace-admin", user })} disabled={Boolean(busy) || deleting}><KeyRound />更换订阅凭据</button><button type="button" onClick={() => onDialog({ kind: "repair-admin", user })} disabled={Boolean(busy) || deleting}><RefreshCw />修复自己节点凭据</button></>}
        <button type="button" onClick={() => onDialog({ kind: "telegram", user })}><Send />Telegram</button>
        {!admin && <button type="button" onClick={() => onDialog({ kind: "password", user })}><KeyRound />重置密码</button>}
        {!admin && <button type="button" onClick={onResetTraffic} disabled={Boolean(busy)}><RotateCcw />重置流量</button>}
        {!admin && !deleting && <button type="button" onClick={() => onStatus(enableAction)} disabled={Boolean(busy)}>{enableAction ? <Power /> : <PowerOff />}{enableAction ? "启用" : "禁用"}{accessPending ? ` (${lifecycle?.pending_count ?? 0})` : ""}</button>}
        {!admin && <button className={`danger user-delete-button${pending > 0 ? " pending" : ""}`} type="button" onClick={onDelete} disabled={Boolean(busy)}>{pending > 0 && <span aria-hidden="true">{pending}</span>}<Trash2 /><b>删除</b></button>}
      </footer>
      {pending > 0 && <p className="user-delete-pending">删除未完成，还有 {pending} 个项目待清理，可再次点击删除重试。</p>}
    </article>
  );
}

function UserFact({ label, value }: { label: string; value: string }) { return <div><span>{label}</span><strong title={value}>{value}</strong></div>; }

function UserRealtime({ user, connections, helper }: { user: ManagedUser; connections: UserConnectionsResponse | null; helper: HelperUserConnectionsResponse | null }) {
  const rate = helper?.user_rates?.[user.username];
  const count = (connections?.connections?.[user.username] ?? 0) + (helper?.connections?.[user.username] ?? 0);
  const ips = Object.entries(connections?.ips?.[user.username] ?? {}).sort((a, b) => b[1] - a[1]);
  const geo = ips.map(([ip]) => {
    const location = connections?.geo?.[ip];
    return [location?.country, location?.province, location?.city, location?.isp].filter(Boolean).join(" ");
  }).filter(Boolean);
  return <div className="user-facts">
    <UserFact label="实时网速 · 5 秒" value={rate?.rate_fresh ? `↑ ${formatBytes(rate.upload_bytes_per_second)}/s · ↓ ${formatBytes(rate.download_bytes_per_second)}/s` : "暂无新鲜数据"} />
    <UserFact label="连接数" value={connections || helper ? `${count}${connections?.connection_count_ready === false ? "+" : ""}` : "—"} />
    <UserFact label={`IP（${ips.length}）`} value={ips.map(([ip, count]) => `${ip} ×${count}`).join("、") || "—"} />
    <UserFact label="地理位置" value={Array.from(new Set(geo)).join("、") || (connections?.geo_available === false ? "暂不可用" : "—")} />
  </div>;
}

function confirmCredentialWrite(lifecycle?: ManagedUserLifecycle) {
  const state = credentialWriteState(lifecycle?.effective_state);
  if (state === "refuse") { window.alert("该用户正在删除或删除未完成，不能执行凭据写入操作"); return null; }
  if (state === "confirm") return window.confirm("该用户处于禁用状态，此操作可能使其节点凭据重新可用") ? true : null;
  return false;
}

function RenewDialog({ token, user, lifecycle, onClose, onSaved }: { token: string; user: ManagedUser; lifecycle?: ManagedUserLifecycle; onClose: () => void; onSaved: () => Promise<void> }) {
  const [days, setDays] = useState("30"); const [saving, setSaving] = useState(false); const [error, setError] = useState("");
  let preview = "";
  try { preview = renewedDate(user.package_end_date, parseRenewDays(days)); } catch { /* Invalid input is explained on save. */ }
  async function save() {
    setError("");
    try {
      const value = parseRenewDays(days);
      const confirmed = confirmCredentialWrite(lifecycle);
      if (confirmed === null) return;
      setSaving(true);
      await extendManagedUserPackage(token, user.username, value, confirmed);
      await onSaved();
    } catch (err) { setError(messageOf(err, "续期失败")); } finally { setSaving(false); }
  }
  return <DialogShell title="自定义续期" subtitle={`用户：${user.username}`} onClose={saving ? () => {} : onClose} footer={<><button type="button" onClick={onClose} disabled={saving}>取消</button><button className="primary" type="button" onClick={() => void save()} disabled={saving}>{saving ? "续期中..." : "确认续期"}</button></>}><div className="user-form one"><label><span>续期天数</span><input type="number" min="1" max="3650" step="1" value={days} onChange={(event) => setDays(event.target.value)} /><small>请输入 1–3650 之间的整数。已过期用户从今天开始计算。</small></label>{preview && <p>预计续期至 {preview}</p>}{error && <p className="user-form-error">{error}</p>}</div></DialogShell>;
}

function URIDialog({ token, user, onClose }: { token: string; user: ManagedUser; onClose: () => void }) {
  const [items, setItems] = useState<NodeURIItem[]>([]); const [query, setQuery] = useState(""); const [server, setServer] = useState("");
  const [loading, setLoading] = useState(true); const [error, setError] = useState(""); const [copied, setCopied] = useState(false);
  useEffect(() => {
    let active = true;
    fetchNodeURIs(token).then((result) => { if (active) setItems((result.items ?? []).filter((item) => item.username === user.username)); })
      .catch((err) => { if (active) setError(messageOf(err, "读取节点 URI 失败")); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [token, user.username]);
  const visible = searchUserURIs(items, user.username, query, server);
  const servers = Array.from(new Set(items.map((item) => item.server_name).filter((name): name is string => Boolean(name))));
  async function copy(value: string) { try { await copyText(value); setCopied(true); setError(""); } catch (err) { setError(messageOf(err, "复制失败，请手动复制")); } }
  return <DialogShell wide title="URI 管理" subtitle={`${user.username} · 各可用节点的分享 URI（使用该用户的子账户凭据）`} onClose={onClose}>
    <div className="user-form"><label><span>节点 / 服务器</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索节点或服务器" /></label><label><span>服务器</span><select value={server} onChange={(event) => setServer(event.target.value)}><option value="">全部服务器</option>{servers.map((name) => <option key={name}>{name}</option>)}</select></label></div>
    <div className="user-renew-actions"><button type="button" disabled={!visible.length} onClick={() => void copy(visible.map((item) => item.uri).join("\n"))}><Copy />复制全部（{visible.length}）</button>{copied && <span>已复制</span>}</div>
    {loading ? <div className="user-empty small">正在读取...</div> : !visible.length ? <div className="user-empty small">暂无数据</div> : <div className="user-account-list">{visible.map((item, index) => <article key={`${item.node_id}-${index}`}><div><strong>{item.node_name}</strong><span>{item.server_name || "—"} · {item.protocol || "—"}</span><code title={item.uri} style={{ overflowWrap: "anywhere", whiteSpace: "normal" }}>{item.uri}</code></div><button type="button" onClick={() => void copy(item.uri)} aria-label={`复制 ${item.node_name} URI`}><Copy /></button></article>)}</div>}
    {error && <p className="user-form-error">{error}</p>}
  </DialogShell>;
}

function ImportedNodesDialog({ token, user, onClose, onChanged }: { token: string; user: ManagedUser; onClose: () => void; onChanged: () => Promise<void> }) {
  const [nodes, setNodes] = useState<ManagedUserImportedNode[]>([]); const [loading, setLoading] = useState(true); const [saving, setSaving] = useState(false); const [error, setError] = useState("");
  const [cleanupPending, setCleanupPending] = useState(false);
  useEffect(() => {
    let active = true;
    fetchManagedUserImportedNodes(token, user.username).then((result) => { if (active) { setNodes(result.nodes ?? []); setCleanupPending(result.cleanup_pending === true); } })
      .catch((err) => { if (active) setError(messageOf(err, "读取导入节点失败")); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [token, user.username]);
  async function clear() {
    if (!cleanupPending && !window.confirm(`确认清空 ${user.username} 导入的 ${nodes.length} 个节点？节点会从所有套餐中移除，移除后为空的套餐也会删除。此操作不可撤销。`)) return;
    setSaving(true); setError("");
    try { await clearManagedUserImportedNodes(token, user.username); setNodes([]); setCleanupPending(false); await onChanged(); }
    catch (err) { setError(messageOf(err, "清空失败，请重试")); try { const result = await fetchManagedUserImportedNodes(token, user.username); setNodes(result.nodes ?? []); setCleanupPending(result.cleanup_pending === true); } catch { /* Preserve the clear error for retry. */ } } finally { setSaving(false); }
  }
  return <DialogShell wide title="导入节点" subtitle={`用户：${user.username}`} onClose={saving ? () => {} : onClose} footer={<><button type="button" onClick={onClose} disabled={saving}>关闭</button><button className="danger" type="button" onClick={() => void clear()} disabled={loading || saving || (!nodes.length && !cleanupPending)}>{saving ? "清空中..." : cleanupPending ? "重试套餐清理" : "清空导入节点"}</button></>}>
    {cleanupPending && <p className="user-form-error">上次套餐清理未完成，请重试清理。</p>}
    {loading ? <div className="user-empty small">正在读取...</div> : !nodes.length ? <div className="user-empty small">该用户没有导入节点</div> : <div className="user-account-list">{nodes.map((node) => <article key={node.id}><div><strong>{node.node_name}</strong><span>{node.server_name || node.original_server || "—"} · {node.node_type || "导入节点"}</span></div></article>)}</div>}{error && <p className="user-form-error">{error}</p>}
  </DialogShell>;
}

function AdminCredentialsDialog({ token, user, lifecycle, repair, onClose, onSaved }: { token: string; user: ManagedUser; lifecycle?: ManagedUserLifecycle; repair: boolean; onClose: () => void; onSaved: (text: string) => Promise<void> }) {
  const [saving, setSaving] = useState(false); const [error, setError] = useState("");
  async function save() {
    const confirmed = confirmCredentialWrite(lifecycle);
    if (confirmed === null) return;
    setSaving(true); setError("");
    try {
      if (repair) {
        const result = await repairAdminCredentials(token, user.username, confirmed);
        const text = `修复完成：更新 ${result.nodes_repaired ?? 0} 个节点配置，补回 ${result.credentials_pushed ?? 0} 条 Xray 凭据，${result.nodes_unchanged ?? 0} 个无需修改，${result.records_unmatched ?? 0} 条凭据未关联节点`;
        if (result.push_failed) { setError(`${text}；${result.push_failed} 条补不回（服务器多半离线，稍后再试）`); return; }
        await onSaved(text);
      } else {
        const result = await replaceAdminCredentials(token, user.username, confirmed);
        await onSaved(`订阅链接已重置，并更换 ${result.credentials_updated ?? 0} 个 Xray 凭据、更新 ${result.nodes_updated ?? 0} 个节点配置；请重新复制并更新订阅。`);
      }
    } catch (err) { setError(messageOf(err, repair ? "修复失败" : "更换凭据失败")); } finally { setSaving(false); }
  }
  return <DialogShell title={repair ? "修复管理员节点配置" : "重置管理员订阅凭据"} subtitle={`当前管理员：${user.username}`} onClose={saving ? () => {} : onClose} footer={<><button type="button" onClick={onClose} disabled={saving}>取消</button><button className={repair ? "primary" : "danger"} type="button" onClick={() => void save()} disabled={saving}>{saving ? "处理中..." : repair ? "确认修复" : "确认更换"}</button></>}>
    <p>{repair ? "使用主控保存的管理员 Xray 入站及路由子账户凭据，修复节点表中不一致的 UUID、密码或 PSK。不会重新生成凭据；如服务器缺少凭据，官方可能补回，结果以返回的补回及失败数量为准。" : "将同时更换管理员的订阅 Token、全部短链接、所有节点上的管理员子账户凭据，并同步更新节点表中的 Clash 配置。旧订阅链接和旧节点凭据会立即失效，完成后请重新复制并更新订阅。"}</p>
    {error && <p className="user-form-error">{error}</p>}
  </DialogShell>;
}

function DialogShell({ title, subtitle, onClose, children, footer, wide = false }: { title: string; subtitle?: string; onClose: () => void; children: React.ReactNode; footer?: React.ReactNode; wide?: boolean }) {
  return <div className="user-dialog-layer" role="presentation" onMouseDown={onClose}><section className={`user-dialog${wide ? " wide" : ""}`} role="dialog" aria-modal="true" aria-label={title} onMouseDown={(event) => event.stopPropagation()}>
    <header><div><h2>{title}</h2>{subtitle && <p>{subtitle}</p>}</div><button type="button" onClick={onClose} aria-label="关闭"><X /></button></header>
    <div className="user-dialog-body">{children}</div>{footer && <footer>{footer}</footer>}
  </section></div>;
}

function CreateUserDialog({ token, onClose, onCreated }: { token: string; onClose: () => void; onCreated: (password: string) => Promise<void> }) {
  const [form, setForm] = useState({ username: "", email: "", nickname: "", password: randomPassword(), remark: "" });
  const [saving, setSaving] = useState(false); const [error, setError] = useState("");
  async function submit(event: React.FormEvent) { event.preventDefault(); if (!validUsername(form.username)) { setError("用户名须为 3–20 个字母、数字或连字符，不支持下划线"); return; } setSaving(true); setError(""); try { const result = await createManagedUser(token, form); await copyText(result.password); await onCreated(result.password); } catch (err) { setError(messageOf(err, "创建用户失败")); } finally { setSaving(false); } }
  return <DialogShell title="新增用户" subtitle="默认生成随机初始密码，创建成功后会自动复制。" onClose={onClose} footer={<><button type="button" onClick={onClose}>取消</button><button className="primary" type="submit" form="create-user-form" disabled={saving}>{saving ? "创建中..." : "确认创建"}</button></>}>
    <form id="create-user-form" className="user-form" onSubmit={submit}>
      <label><span>用户名</span><input required minLength={3} maxLength={20} pattern="[A-Za-z0-9\-]{3,20}" title="3–20 个字母、数字或连字符，不支持下划线" value={form.username} onChange={(event) => setForm({ ...form, username: event.target.value })} /><small>3–20 个字母、数字或连字符，不支持下划线。</small></label>
      <label><span>邮箱</span><input type="email" value={form.email} onChange={(event) => setForm({ ...form, email: event.target.value })} /></label>
      <label><span>昵称</span><input value={form.nickname} onChange={(event) => setForm({ ...form, nickname: event.target.value })} /></label>
      <label><span>初始密码</span><div className="user-input-action"><input required value={form.password} onChange={(event) => setForm({ ...form, password: event.target.value })} /><button type="button" onClick={() => setForm({ ...form, password: randomPassword() })}><RefreshCw /></button></div></label>
      <label className="wide"><span>备注（可选）</span><input value={form.remark} onChange={(event) => setForm({ ...form, remark: event.target.value })} /></label>
      {error && <p className="user-form-error wide">{error}</p>}
    </form>
  </DialogShell>;
}

function PasswordDialog({ token, user, onClose, onSaved }: { token: string; user: ManagedUser; onClose: () => void; onSaved: (password: string) => void }) {
  const [password, setPassword] = useState(randomPassword()); const [saving, setSaving] = useState(false); const [error, setError] = useState("");
  async function save() { setSaving(true); setError(""); try { const result = await resetManagedUserPassword(token, user.username, password); await copyText(result.password); onSaved(result.password); } catch (err) { setError(messageOf(err, "重置密码失败")); } finally { setSaving(false); } }
  return <DialogShell title="重置密码" subtitle={`用户：${user.username}`} onClose={onClose} footer={<><button type="button" onClick={onClose}>取消</button><button className="primary" type="button" onClick={() => void save()} disabled={saving || !password}>{saving ? "保存中..." : "重置并复制"}</button></>}>
    <div className="user-form one"><label><span>新密码</span><div className="user-input-action"><input value={password} onChange={(event) => setPassword(event.target.value)} /><button type="button" onClick={() => setPassword(randomPassword())}><RefreshCw /></button></div></label>{error && <p className="user-form-error">{error}</p>}</div>
  </DialogShell>;
}

function ProfileDialog({ token, user, onClose, onSaved }: { token: string; user: ManagedUser; onClose: () => void; onSaved: () => Promise<void> }) {
  const [nickname, setNickname] = useState(user.nickname ?? "");
  const [remark, setRemark] = useState(user.remark ?? ""); const [shortCode, setShortCode] = useState(user.custom_user_short_code ?? ""); const [saving, setSaving] = useState(false); const [error, setError] = useState("");
  async function save() { const code = shortCode.trim(); if (code && !/^[A-Za-z0-9_-]{2,16}$/.test(code)) { setError("短码只能含字母、数字、下划线或横杠，长度 2–16"); return; } setSaving(true); setError(""); try { await Promise.all([updateManagedUserNickname(token, user.username, nickname.trim()), updateManagedUserRemark(token, user.username, remark), updateManagedUserShortCode(token, user.username, code)]); await onSaved(); } catch (err) { setError(messageOf(err, "更新资料失败")); } finally { setSaving(false); } }
  return <DialogShell title="编辑用户资料" subtitle={`用户：${user.username}`} onClose={onClose} footer={<><button type="button" onClick={onClose}>取消</button><button className="primary" type="button" onClick={() => void save()} disabled={saving}>{saving ? "保存中..." : "保存"}</button></>}>
    <div className="user-form one"><label><span>昵称</span><input value={nickname} onChange={(event) => setNickname(event.target.value)} placeholder={user.username} /><small>留空则显示用户名。</small></label><label><span>备注</span><input value={remark} onChange={(event) => setRemark(event.target.value)} /></label><label><span>自定义短码</span><input value={shortCode} onChange={(event) => setShortCode(event.target.value)} placeholder={user.user_short_code || "留空使用系统短码"} /><small>留空恢复系统短码；允许字母、数字、下划线和横杠。</small></label>{error && <p className="user-form-error">{error}</p>}</div>
  </DialogShell>;
}

function PackageDialog({ token, user, packages, lifecycle, onClose, onSaved }: { token: string; user: ManagedUser; packages: ManagedPackage[]; lifecycle?: ManagedUserLifecycle; onClose: () => void; onSaved: (text: string) => Promise<void> }) {
  const [assignments, setAssignments] = useState<ManagedUserPackageAssignment[]>([]);
  const [availableIds, setAvailableIds] = useState<number[]>([]);
  const [loading, setLoading] = useState(true);
  const [mode, setMode] = useState<"legacy" | "add" | number>("legacy");
  const [packageId, setPackageId] = useState(String(user.package_id ?? ""));
  const [startDate, setStartDate] = useState(today());
  const [expireDate, setExpireDate] = useState(user.package_id ? user.package_end_date || "" : nextMonth());
  const [isReset, setIsReset] = useState(user.is_reset ?? true);
  const [resetDay, setResetDay] = useState(user.reset_day || 1);
  const [traffic, setTraffic] = useState(user.traffic_limit_override_gb == null ? "" : String(user.traffic_limit_override_gb));
  const [inheritExpiry, setInheritExpiry] = useState(false);
  const [inheritTraffic, setInheritTraffic] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    Promise.all([fetchManagedUserPackageAssignments(token, user.username), fetchManagedUserAvailablePackages(token, user.username)])
      .then(([result, available]) => { if (active) { setAssignments(result.assignments ?? []); setAvailableIds(available.package_ids); } })
      .catch((err) => { if (active) setError(messageOf(err, "读取套餐绑定失败")); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [token, user.username]);
  const activeAssignments = assignments.filter((item) => item.status !== "revoked");
  const editing = typeof mode === "number" ? assignments.find((item) => item.id === mode) : undefined;
  const choices = eligiblePackages(packages, availableIds, activeAssignments.map((item) => item.package_id), editing?.package_id ?? (mode === "legacy" ? user.package_id ?? undefined : undefined));
  const changingPackage = mode === "legacy" && Boolean(user.package_id) && Boolean(packageId) && Number(packageId) !== user.package_id;
  function selectMode(next: "legacy" | "add" | number) {
    const item = typeof next === "number" ? assignments.find((entry) => entry.id === next) : undefined;
    setMode(next); setError(""); setInheritExpiry(false); setInheritTraffic(false);
    setPackageId(String(item?.package_id ?? (next === "legacy" ? user.package_id ?? "" : "")));
    setStartDate(item?.package_start_date?.slice(0, 10) || today());
    setExpireDate(item ? item.package_end_date?.slice(0, 10) || "" : next === "legacy" && user.package_id ? user.package_end_date || "" : nextMonth());
    setIsReset(item?.is_reset ?? (next === "legacy" ? user.is_reset ?? true : true));
    setResetDay(item?.reset_day || (next === "legacy" ? user.reset_day || 1 : 1));
    setTraffic(item ? trafficOverrideGB(item.traffic_limit_override) : next === "legacy" && user.traffic_limit_override_gb != null ? String(user.traffic_limit_override_gb) : "");
  }
  async function save() {
    setError("");
    try {
      if (!packageId && mode !== "legacy") throw new Error("请选择套餐");
      if (packageId && !choices.some((pkg) => pkg.id === Number(packageId))) throw new Error("该套餐不可绑定，可能已属于另一用户，请刷新后重试");
      if (packageId && isReset && (!Number.isInteger(resetDay) || resetDay < 1 || resetDay > 31)) throw new Error("每月重置日须为 1–31 的整数");
      const override = parseTrafficOverride(traffic);
      const confirmed = packageId ? confirmCredentialWrite(lifecycle) : false;
      if (confirmed === null) return;
      if (!packageId && !window.confirm(`确认解绑 ${user.username} 的主套餐？`)) return;
      setSaving(true);
      if (!packageId) {
        await unassignManagedUserPackage(token, user.username);
        await onSaved(`用户 ${user.username} 已解绑主套餐`);
      } else {
        const body = { username: user.username, package_id: Number(packageId), start_date: startDate, expire_date: expireDate, permanent: !expireDate, is_reset: isReset, reset_day: resetDay, traffic_limit_override_gb: override, confirm_disabled: confirmed };
        const result = typeof mode === "number" ? await updateManagedUserPackageAssignment(token, { ...body, assignment_id: mode })
          : mode === "add" ? await addManagedUserPackageAssignment(token, body)
            : await assignManagedUserPackage(token, { ...body, ...(changingPackage ? { inherit_expire_date: inheritExpiry, inherit_traffic: inheritTraffic } : {}) });
        await onSaved(result.warnings?.length ? `套餐已更新；${result.warnings.join("；")}` : mode === "add" ? "已新增独立套餐" : "套餐已更新");
      }
    } catch (err) { setError(messageOf(err, "更新套餐失败")); } finally { setSaving(false); }
  }
  async function unbind(item: ManagedUserPackageAssignment) {
    if (!window.confirm(`确认解绑 ${user.username} 的套餐「${item.package_name || item.package_id}」？`)) return;
    setSaving(true); setError("");
    try { await deleteManagedUserPackageAssignment(token, user.username, item.id); await onSaved("套餐已解绑"); }
    catch (err) { setError(messageOf(err, "解绑套餐失败")); } finally { setSaving(false); }
  }
  return <DialogShell title="管理套餐" subtitle={`用户：${user.username}；一个套餐只能绑定一位用户。`} onClose={saving ? () => {} : onClose} footer={<><button type="button" onClick={onClose} disabled={saving}>取消</button><button className="primary" type="button" onClick={() => void save()} disabled={saving || loading}>{saving ? "处理中..." : mode === "add" ? "新增独立套餐" : editing ? "保存该套餐" : "保存主套餐"}</button></>}>
    {loading ? <div className="user-empty small">正在读取套餐绑定...</div> : <>
      <div className="user-subscription-packages">{activeAssignments.map((item) => <article key={item.id}>
        <div><strong>{item.package_name || `套餐 ${item.package_id}`}{item.is_primary ? " · 主套餐" : ""}</strong><span>{item.package_end_date?.slice(0, 10) || "长期有效"} · {formatBytes(item.used_total || 0)} / {(item.traffic_limit_override ?? item.traffic_limit_bytes ?? 0) > 0 ? formatBytes(item.traffic_limit_override ?? item.traffic_limit_bytes ?? 0) : "不限"}</span></div>
        <div className="user-subscription-actions"><button type="button" onClick={() => selectMode(item.id)} disabled={saving}>编辑</button><button className="danger" type="button" onClick={() => void unbind(item)} disabled={saving}>解绑</button></div>
      </article>)}</div>
      <div className="user-account-tabs"><button type="button" className={mode === "legacy" ? "active" : ""} onClick={() => selectMode("legacy")} disabled={saving}>绑定 / 更换主套餐</button><button type="button" className={mode === "add" ? "active" : ""} onClick={() => selectMode("add")} disabled={saving}><Plus />新增独立套餐</button></div>
      <p>同一用户可绑定多个套餐；相同节点会创建独立子账户，流量、连接数和到期时间分别计算。</p>
      <div className="user-form">
        <label className="wide"><span>{editing ? "编辑套餐" : "选择套餐"}</span><select value={packageId} disabled={Boolean(editing) || saving} onChange={(event) => { setPackageId(event.target.value); setTraffic(""); setInheritExpiry(false); setInheritTraffic(false); }}><option value="">{mode === "legacy" ? "不绑定主套餐" : "请选择套餐"}</option>{choices.map((pkg) => <option key={pkg.id} value={pkg.id}>{pkg.name}</option>)}</select><small>仅列未绑定其他用户的套餐；已绑定本用户的套餐请从上方编辑。</small></label>
        {!editing && <label><span>开始日期</span><input type="date" value={startDate} onChange={(event) => setStartDate(event.target.value)} disabled={!packageId || saving} /></label>}
        <label><span>到期日期</span><input type="date" value={expireDate} onChange={(event) => setExpireDate(event.target.value)} disabled={!packageId || saving || (changingPackage && inheritExpiry)} /><small>留空表示长期有效。</small></label>
        {changingPackage && <><label className="user-check wide"><input type="checkbox" checked={inheritExpiry} onChange={(event) => setInheritExpiry(event.target.checked)} /><span>继承原套餐到期时间</span></label><p className="wide">开启后忽略上方日期，沿用用户当前到期时间。</p><label className="user-check wide"><input type="checkbox" checked={inheritTraffic} onChange={(event) => setInheritTraffic(event.target.checked)} /><span>继承本周期已用流量</span></label><p className="wide">{inheritTraffic ? "已用总流量计入新套餐，到新套餐的下一个重置日再归零。" : "不勾选将清零该用户本周期已用流量，且无法找回。"}</p></>}
        <label className="user-check"><input type="checkbox" checked={isReset} onChange={(event) => setIsReset(event.target.checked)} disabled={!packageId || saving} /><span>启用每月流量重置</span></label>
        <label><span>每月重置日</span><input type="number" min="1" max="31" value={resetDay} onChange={(event) => setResetDay(Number(event.target.value))} disabled={!packageId || !isReset || saving} />{isReset && resetDay > 28 && <small>注意：2月仅有28/29天，届时将在月末最后一天重置。</small>}</label>
        <label className="wide"><span>流量覆写（GB）</span><input type="number" min="0" step="any" value={traffic} onChange={(event) => setTraffic(event.target.value)} disabled={!packageId || saving} placeholder="留空使用套餐默认值" /><small>留空继承，0 表示不限；1 GB = 2³⁰ 字节。换套餐或解绑时覆写会被清除，生效最长一个巡检周期。</small></label>
      </div>
    </>}{error && <p className="user-form-error">{error}</p>}
  </DialogShell>;
}

function LimitsDialog({ token, user, nodes, onClose, onSaved }: { token: string; user: ManagedUser; nodes: XrayNode[]; onClose: () => void; onSaved: () => Promise<void> }) {
  const [speed, setSpeed] = useState(user.speed_limit_override == null ? "" : String(user.speed_limit_override)); const [devices, setDevices] = useState(user.device_limit_override == null ? "" : String(user.device_limit_override)); const [nodeSpeed, setNodeSpeed] = useState<Record<number, string>>(() => Object.fromEntries(Object.entries(user.node_speed_limit_overrides ?? {}).map(([id, value]) => [Number(id), String(value)]))); const [nodeDevices, setNodeDevices] = useState<Record<number, string>>(() => Object.fromEntries(Object.entries(user.node_device_limit_overrides ?? {}).map(([id, value]) => [Number(id), String(value)]))); const [saving, setSaving] = useState(false); const [error, setError] = useState("");
  const [ips, setIPs] = useState(user.ip_limit_override == null ? "" : String(user.ip_limit_override));
  const [ipAction, setIPAction] = useState(user.ip_over_limit_action_override ?? "");
  async function save() { if (ips.trim() && (!Number.isInteger(Number(ips)) || Number(ips) < 0)) { setError("同时在线 IP 数须为非负整数"); return; } setSaving(true); setError(""); try { await Promise.all([updateManagedUserLimits(token, { username: user.username, speed_limit_override: optionalNumber(speed), device_limit_override: optionalInt(devices), ip_limit_override: optionalInt(ips), ip_over_limit_action_override: ipAction || null }), updateManagedUserNodeLimits(token, { username: user.username, node_speed_overrides: compactNumberMap(nodeSpeed), node_device_overrides: compactIntMap(nodeDevices) })]); await onSaved(); } catch (err) { setError(messageOf(err, "保存用户限制失败")); } finally { setSaving(false); } }
  return <DialogShell wide title="用户限制" subtitle={`用户：${user.username}；留空继承套餐，0 表示显式不限。`} onClose={onClose} footer={<><button type="button" onClick={onClose}>取消</button><button className="primary" type="button" onClick={() => void save()} disabled={saving}>{saving ? "下发中..." : "保存并下发"}</button></>}>
    <div className="user-form"><label><span>全局速度覆盖（Mbps）</span><input type="number" min="0" step="0.1" value={speed} onChange={(event) => setSpeed(event.target.value)} placeholder={`继承套餐 ${user.speed_limit_mbps ?? 0}`} /></label><label><span>全局连接数覆盖</span><input type="number" min="0" value={devices} onChange={(event) => setDevices(event.target.value)} placeholder={`继承套餐 ${user.device_limit ?? 0}`} /></label><label><span>同时在线 IP 数覆写</span><input type="number" min="0" step="1" value={ips} onChange={(event) => setIPs(event.target.value)} placeholder="留空沿用套餐，0 = 不限" /><small>每台服务器分别计算；WireGuard 入站不适用。</small></label><label><span>IP 超限时</span><select value={ipAction} onChange={(event) => setIPAction(event.target.value)}><option value="">沿用套餐</option><option value="reject">拒绝新连接</option><option value="kick_oldest">顶掉最旧的 IP</option></select><small>{ipAction === "kick_oldest" ? "踢掉最早上线那个 IP 的全部连接，放行新 IP。" : ipAction === "reject" ? "已在线的 IP 不受影响，新的 IP 连不上。" : "沿用套餐的 IP 超限处理方式。"}</small></label></div>
    <div className="user-node-limits"><h3>节点级覆盖</h3><p>节点级值优先于用户全局值。只列正式节点和私有节点。</p>{nodes.map((node) => <article key={node.id}><div><strong>{node.node_name}</strong><span>{node.server || node.original_server || "—"} · {node.protocol || "协议未知"}</span></div><label><span>Mbps</span><input type="number" min="0" step="0.1" value={nodeSpeed[node.id] ?? ""} onChange={(event) => setNodeSpeed({ ...nodeSpeed, [node.id]: event.target.value })} placeholder="继承" /></label><label><span>连接数</span><input type="number" min="0" value={nodeDevices[node.id] ?? ""} onChange={(event) => setNodeDevices({ ...nodeDevices, [node.id]: event.target.value })} placeholder="继承" /></label></article>)}</div>{error && <p className="user-form-error">{error}</p>}
  </DialogShell>;
}

function AccountsDialog({ token, user, initial = "all", onClose }: { token: string; user: ManagedUser; initial?: "all" | "inbound" | "routed"; onClose: () => void }) {
  const [items, setItems] = useState<UserSubaccount[]>([]); const [loading, setLoading] = useState(true); const [error, setError] = useState(""); const [filter, setFilter] = useState(initial);
  useEffect(() => { let active = true; fetchManagedUserSubaccounts(token, user.username).then((result) => { if (active) setItems(result.subaccounts ?? []); }).catch((err) => { if (active) setError(messageOf(err, "读取子账户失败")); }).finally(() => { if (active) setLoading(false); }); return () => { active = false; }; }, [token, user.username]);
  const visible = items.filter((item) => filter === "all" || item.type === filter);
  return <DialogShell wide title="子账户与节点" subtitle={`用户：${user.username}`} onClose={onClose}><div className="user-account-tabs"><button className={filter === "all" ? "active" : ""} onClick={() => setFilter("all")}>全部</button><button className={filter === "inbound" ? "active" : ""} onClick={() => setFilter("inbound")}>入站绑定</button><button className={filter === "routed" ? "active" : ""} onClick={() => setFilter("routed")}>路由出站</button></div>{loading ? <div className="user-empty small">正在读取...</div> : error ? <p className="user-form-error">{error}</p> : visible.length === 0 ? <div className="user-empty small">没有对应记录</div> : <div className="user-account-list">{visible.map((item, index) => <article key={`${item.type}-${item.node_id}-${item.server_id}-${item.inbound_tag}-${index}`}><div><strong>{item.node_name || item.inbound_tag || "未命名绑定"}</strong><span>{item.type === "routed" ? "路由出站" : "入站绑定"} · {item.server_name || "服务器未知"}</span></div><div><code>{item.email || item.identifier || "—"}</code><span>{item.protocol || item.inbound_tag || "—"}</span></div><b className={item.is_active ? "ok" : "off"}>{item.is_active ? "有效" : "暂停"}</b></article>)}</div>}</DialogShell>;
}

type SubscriptionPackage = {
  id: string;
  name: string;
  shortCode: string;
  isPrimary: boolean;
};

function SubscriptionDialog({ token, user, pkg, onClose, onCopied }: { token: string; user: ManagedUser; pkg?: ManagedPackage; onClose: () => void; onCopied: (client: string) => void }) {
  const code = user.custom_user_short_code || user.user_short_code || "";
  const [assignments, setAssignments] = useState<ManagedUserPackageAssignment[]>([]);
  const [subscriptionBaseUrl, setSubscriptionBaseUrl] = useState("");
  const [selection, setSelection] = useState<{ packageId: string; mode: "copy" | "qr" } | null>(null);
  const [qrClient, setQrClient] = useState("auto");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => {
    let active = true;
    Promise.all([
      fetchManagedUserPackageAssignments(token, user.username),
      fetchUserConfig(token).catch(() => undefined),
    ]).then(([packageResult, config]) => {
      if (!active) return;
      setAssignments(packageResult.assignments ?? []);
      setSubscriptionBaseUrl(config?.subscription_url?.trim() ?? "");
    }).catch((err) => {
      if (active) setError(messageOf(err, "读取用户套餐订阅失败"));
    }).finally(() => {
      if (active) setLoading(false);
    });
    return () => { active = false; };
  }, [token, user.username]);

  const subscriptionPackages = useMemo<SubscriptionPackage[]>(() => {
    const current = assignments.filter((assignment) => assignment.short_code).map((assignment) => ({
      id: String(assignment.id),
      name: assignment.package_name || `套餐 ${assignment.package_id}`,
      shortCode: assignment.short_code!,
      isPrimary: Boolean(assignment.is_primary),
    }));
    if (current.length > 0 || !pkg?.short_code || !code) return current;
    return [{ id: "legacy", name: pkg.name || "当前套餐", shortCode: `${pkg.short_code}${code}`, isPrimary: true }];
  }, [assignments, code, pkg]);

  useEffect(() => {
    if (subscriptionPackages.length === 1 && !selection) {
      setSelection({ packageId: subscriptionPackages[0].id, mode: "copy" });
    }
  }, [selection, subscriptionPackages]);

  const selectedPackage = selection ? subscriptionPackages.find((item) => item.id === selection.packageId) : undefined;
  const subscriptionUrl = (selectedPackage && selection)
    ? managedSubscriptionUrlFromCode(selectedPackage.shortCode, selection.mode === "qr" ? qrClient : "auto", subscriptionBaseUrl)
    : "";

  async function copy(client: string, name: string) {
    if (!selectedPackage) return;
    await copyText(managedSubscriptionUrlFromCode(selectedPackage.shortCode, client, subscriptionBaseUrl));
    onCopied(name);
    onClose();
  }

  function selectPackage(item: SubscriptionPackage, mode: "copy" | "qr") {
    setSelection({ packageId: item.id, mode });
    if (mode === "qr") setQrClient("auto");
  }

  return <DialogShell title="复制订阅" subtitle={`${user.username} · 每个套餐使用独立订阅地址`} onClose={onClose}>
    {loading ? <div className="user-empty small">正在读取套餐订阅...</div> : subscriptionPackages.length === 0 ? <p className="user-form-error">该用户暂无可用套餐订阅。</p> : <>
      <div className="user-subscription-packages">{subscriptionPackages.map((item) => <article key={item.id}>
        <div><strong>{item.name}</strong><span>{item.isPrimary ? "主套餐" : "独立套餐"}</span></div>
        <div className="user-subscription-actions">
          <button className={selection?.packageId === item.id && selection.mode === "copy" ? "active" : ""} type="button" onClick={() => selectPackage(item, "copy")}><Copy /><span>复制订阅</span></button>
          <button className={selection?.packageId === item.id && selection.mode === "qr" ? "active" : ""} type="button" onClick={() => selectPackage(item, "qr")}><QrCode /><span>QR code</span></button>
        </div>
      </article>)}</div>
      {selectedPackage && selection?.mode === "copy" && <section className="user-subscription-panel">
        <header><div><strong>{selectedPackage.name}</strong><span>选择客户端格式</span></div>{subscriptionPackages.length > 1 && <button type="button" onClick={() => setSelection(null)}><ArrowLeft /><span>返回套餐</span></button>}</header>
        <div className="user-client-list">{clients.map(([client, name]) => <button key={client} type="button" onClick={() => void copy(client, name)}><Copy /><span>{name}</span></button>)}</div>
      </section>}
      {selectedPackage && selection?.mode === "qr" && <section className="user-subscription-panel user-subscription-qr">
        <header><div><strong>{selectedPackage.name}</strong><span>选择二维码对应的客户端</span></div>{subscriptionPackages.length > 1 && <button type="button" onClick={() => setSelection(null)}><ArrowLeft /><span>返回套餐</span></button>}</header>
        <label><span>客户端格式</span><select value={qrClient} onChange={(event) => setQrClient(event.target.value)}>{clients.map(([client, name]) => <option key={client} value={client}>{name}</option>)}</select></label>
        <div className="user-subscription-qr-code"><QRCodeSVG value={subscriptionUrl} size={216} level="M" title={`${selectedPackage.name} 订阅二维码`} /></div>
      </section>}
    </>}
    {error && <p className="user-form-error">{error}</p>}
  </DialogShell>;
}

function TelegramDialog({ token, user, onClose, onChanged }: { token: string; user: ManagedUser; onClose: () => void; onChanged: (text: string) => Promise<void> }) {
  const [status, setStatus] = useState<{ bound: boolean; telegram_id?: number; telegram_username?: string; bot_url?: string } | null>(null);
  const [invite, setInvite] = useState<{ command: string; expires_at: string; bot_url?: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => { let active = true; fetchManagedUserTelegram(token, user.username).then((result) => { if (active) setStatus(result); }).catch((err) => { if (active) setError(messageOf(err, "读取 Telegram 绑定失败")); }); return () => { active = false; }; }, [token, user.username]);
  async function generate() { setBusy(true); setError(""); try { const result = await createManagedUserTelegramInvite(token, user.username); setInvite(result); await copyText(result.command); } catch (err) { setError(messageOf(err, "生成绑定命令失败")); } finally { setBusy(false); } }
  async function unbind() { if (!window.confirm(`确认解除 ${user.username} 的 Telegram 绑定？`)) return; setBusy(true); setError(""); try { await unbindManagedUserTelegram(token, user.username); await onChanged("Telegram 绑定已解除"); } catch (err) { setError(messageOf(err, "解除绑定失败")); } finally { setBusy(false); } }
  return <DialogShell title="绑定 Telegram" subtitle={`用户：${user.username}`} onClose={onClose} footer={<button type="button" onClick={onClose}>关闭</button>}>
    {!status && !error ? <div className="user-empty small">正在读取...</div> : status?.bound ? <div className="user-telegram-state"><ShieldCheck /><div><strong>已绑定</strong><span>@{status.telegram_username || status.telegram_id}</span></div><button className="danger" type="button" disabled={busy} onClick={() => void unbind()}>解除绑定</button></div> : <div className="user-telegram-flow"><p>Telegram Bot 无法主动联系未开始会话的用户。生成命令后，请让该用户向 Bot 发送一次命令完成绑定。</p>{invite ? <><code>{invite.command}</code><div><button type="button" onClick={() => void copyText(invite.command)}><Copy />复制命令</button>{invite.bot_url && <a href={invite.bot_url} target="_blank" rel="noreferrer">打开 Bot</a>}</div><small>有效期至 {new Date(invite.expires_at).toLocaleString()}</small></> : <button className="primary" type="button" disabled={busy} onClick={() => void generate()}>{busy ? "生成中..." : "生成绑定命令"}</button>}</div>}
    {error && <p className="user-form-error">{error}</p>}
  </DialogShell>;
}

function deletionDecision(item: ManagedUserLifecycleItem) {
  if (item.action === "DELETE_NODE") return item.decision_note || "该用户拥有的外部节点，随用户删除";
  if (item.action === "DELETE_EMPTY_PACKAGE") return "删除（移除节点后为空）";
  if (item.action === "DELETE_PACKAGE") return "删除";
  if (item.action === "KEEP_PACKAGE") return `保留（移除 ${item.deleted_node_ids?.length ?? 0} 个该用户节点）`;
  if (item.action === "CONFLICT") return `冲突：${item.decision_note || `发现 ${item.unknown_credentials || 1} 个无法确认来源的凭据，需要人工检查`}`;
  if (item.action === "REMOVE_USER_ONLY") return item.decision_note || `还有 ${item.remaining_users} 个业务用户使用，保留 Inbound`;
  if (item.default_credentials > 0) return "仅存在创建时管理员 credential，不视为业务共享";
  return "没有其他业务用户，删除整个 Inbound";
}

function DeleteUserDialog({ token, user, lifecycle, onClose, onResult }: { token: string; user: ManagedUser; lifecycle?: ManagedUserLifecycle; onClose: () => void; onResult: (result: ManagedUserDeleteResult) => Promise<void> }) {
  const [preview, setPreview] = useState<ManagedUserDeletionPreview | null>(null);
  const [result, setResult] = useState<ManagedUserDeleteResult | null>(null);
  const [loading, setLoading] = useState(true);
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    fetchManagedUserDeletionPreview(token, user.username)
      .then((result) => { if (active) setPreview(result.preview); })
      .catch((err) => { if (active) setError(messageOf(err, "读取删除清单失败")); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [token, user.username]);

  async function confirmDelete() {
    if (!preview || deleting || blockedByConflicts) return;
    setDeleting(true);
    setError("");
    try {
      const response = await deleteManagedUser(token, user.username);
      setResult(response.result);
      await onResult(response.result);
      if (!response.result.user_deleted) {
        const refreshed = await fetchManagedUserDeletionPreview(token, user.username);
        setPreview(refreshed.preview);
      }
    } catch (err) {
      setError(messageOf(err, "删除用户失败"));
    } finally {
      setDeleting(false);
    }
  }

  const rows: Array<[string, number]> = preview ? [
    ["套餐绑定", preview.package_bindings],
    ["订阅/短码/令牌", preview.subscriptions + preview.sessions_and_tokens],
    ["Telegram 绑定", preview.telegram_bindings],
    ["子账户", preview.subaccounts],
    ["入站绑定", preview.inbound_bindings],
    ["用户私有节点", preview.private_nodes],
    ["中转/路由关系", preview.routed_relations],
    ["用户限制", preview.user_limits],
    ["Custom 连接归属", preview.custom_assignments],
    ["流量记录", preview.traffic_records],
    ["其他私有记录", preview.other_private],
  ] : [];

  const plan = result?.items ?? preview?.inbound_plan ?? [];
  const retrying = (result && !result.user_deleted) || lifecycle?.effective_state === "deleting" || lifecycle?.effective_state === "delete_partial";
  const blockedByConflicts = !retrying && plan.some((item) => item.action === "CONFLICT");

  return <DialogShell title={`删除用户 ${user.username}？`} subtitle="远程访问逐项清理；成功项保留，失败项可重试。" onClose={deleting ? () => {} : onClose} footer={<><button type="button" onClick={onClose} disabled={deleting}>取消</button><button className="danger" type="button" onClick={() => void confirmDelete()} disabled={!preview || deleting || blockedByConflicts}>{deleting ? "删除中..." : retrying ? "重试待清理项" : "确认删除"}</button></>}>
    {loading ? <div className="user-empty small">正在从数据库核对关联关系...</div> : preview ? <div className="user-delete-preview">
      <p>先清理用户节点，再删除用户关联记录：</p>
      {blockedByConflicts && <p className="user-form-error" role="alert">存在冲突，删除不会执行，请先处理以下项目</p>}
      {plan.length > 0 ? <div className="user-delete-plan">{plan.map((item) => <article key={`${item.item_kind}-${item.package_id ?? `${item.server_id}-${item.inbound_tag}`}`} className={item.status}>
        <div><strong>{item.item_kind === "package" ? item.package_name || `套餐 ${item.package_id}` : item.server_name || `Server ${item.server_id}`}</strong>{item.item_kind === "package" ? <>
          <span>该用户节点：{(item.own_nodes || []).map((node) => `${node.name || "节点"}（ID ${node.id}）`).join("、") || "无"}</span>
          <span>其他用户节点：{(item.other_user_nodes || []).map((node) => `${node.name || "节点"}（ID ${node.id}）`).join("、") || "无"}</span>
          {(item.unknown_nodes?.length ?? 0) > 0 && <span>归属待确认节点：{item.unknown_nodes?.map((node) => `${node.name || "节点"}（ID ${node.id}）`).join("、")}</span>}
        </> : item.item_kind === "node" ? <span>该用户节点（ID {item.node_ids?.join("、")}）</span> : <span>{item.inbound_tag} · {item.protocol || "未知协议"}</span>}</div>
        <b>{deletionDecision(item)}</b>
        {item.item_kind === "package" && item.action !== "CONFLICT" && item.decision_note && <p>{item.decision_note}</p>}
        {item.last_error && <p>{item.last_error}</p>}
      </article>)}</div> : <p className="user-delete-empty-plan">没有需要清理的远程 Inbound。</p>}
      <dl>{rows.map(([label, count]) => <div key={label}><dt>{label}</dt><dd>{count}</dd></div>)}</dl>
      <p className="safe">其他用户的节点会保留；该用户的节点会从所有引用它们的套餐中移除，移除后为空的套餐也会删除。</p>
      {result && !result.user_deleted && <p className="user-form-error">删除未完成，还有 {result.pending_count} 个项目待清理，可再次点击删除重试。</p>}
    </div> : null}
    {error && <p className="user-form-error">{error}</p>}
  </DialogShell>;
}

function optionalNumber(value: string) { const text = value.trim(); if (!text) return null; const number = Number(text); return Number.isFinite(number) && number >= 0 ? number : null; }
function optionalInt(value: string) { const number = optionalNumber(value); return number == null ? null : Math.trunc(number); }
function compactNumberMap(values: Record<number, string>) { return Object.fromEntries(Object.entries(values).filter(([, value]) => value.trim() !== "").map(([id, value]) => [Number(id), Math.max(0, Number(value) || 0)])); }
function compactIntMap(values: Record<number, string>) { return Object.fromEntries(Object.entries(compactNumberMap(values)).map(([id, value]) => [Number(id), Math.trunc(value)])); }
function formatLimit(override: number | null | undefined, inherited: number | null | undefined, unit: string) { const value = override == null ? inherited : override; return value && value > 0 ? `${value} ${unit}` : "不限"; }
function formatBytes(value: number) { if (!value) return "0 B"; const units = ["B", "KB", "MB", "GB", "TB"]; const index = Math.min(units.length - 1, Math.floor(Math.log(value) / Math.log(1024))); return `${(value / 1024 ** index).toFixed(index >= 3 ? 2 : 1).replace(/\.0$/, "")} ${units[index]}`; }
async function copyText(value: string) { if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(value); const input = document.createElement("textarea"); input.value = value; input.style.position = "fixed"; input.style.opacity = "0"; document.body.appendChild(input); input.select(); document.execCommand("copy"); input.remove(); }
function messageOf(error: unknown, fallback: string) { return error instanceof Error && error.message ? error.message : fallback; }
