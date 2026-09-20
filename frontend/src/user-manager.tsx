import React, { useCallback, useEffect, useMemo, useState } from "react";
import {
  Copy,
  Edit3,
  KeyRound,
  Link2,
  PackageCheck,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  Send,
  ShieldCheck,
  SlidersHorizontal,
  Trash2,
  UserCheck,
  UserRoundCog,
  UserX,
  X,
} from "lucide-react";
import {
  assignManagedUserPackage,
  createManagedUser,
  deleteManagedUser,
  extendManagedUserPackage,
  fetchManagedUserNodes,
  fetchManagedUsers,
  fetchManagedUserSubaccounts,
  fetchManagedUserTelegram,
  fetchPackages,
  managedSubscriptionUrl,
  createManagedUserTelegramInvite,
  resetManagedUserPassword,
  resetManagedUserTraffic,
  setManagedUserStatus,
  unassignManagedUserPackage,
  unbindManagedUserTelegram,
  updateManagedUserLimits,
  updateManagedUserNodeLimits,
  updateManagedUserRemark,
  updateManagedUserShortCode,
} from "./api";
import type { ManagedPackage, ManagedUser, UserSubaccount, XrayNode } from "./types";

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
  | null;

const clients = [
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

export function UserManagementPage({ token }: { token: string }) {
  const [users, setUsers] = useState<ManagedUser[]>([]);
  const [packages, setPackages] = useState<ManagedPackage[]>([]);
  const [nodes, setNodes] = useState<XrayNode[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState<Notice>(null);
  const [dialog, setDialog] = useState<Dialog>(null);
  const [query, setQuery] = useState("");
  const [packageFilter, setPackageFilter] = useState("all");
  const [view, setView] = useState<"full" | "renewal">(() => localStorage.getItem("users-view-mode") === "package" ? "renewal" : "full");

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [userResult, packageResult, nodeResult] = await Promise.all([
        fetchManagedUsers(token),
        fetchPackages(token),
        fetchManagedUserNodes(token),
      ]);
      setUsers(userResult.users ?? []);
      setPackages(packageResult.packages ?? []);
      setNodes(nodeResult.nodes ?? []);
    } catch (error) {
      setNotice({ tone: "error", text: messageOf(error, "读取用户失败") });
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => { void load(); }, [load]);

  const packageById = useMemo(() => new Map(packages.map((pkg) => [pkg.id, pkg])), [packages]);
  const counts = useMemo(() => {
    const result = new Map<string, number>();
    result.set("all", users.filter((user) => user.role !== "admin").length);
    result.set("none", users.filter((user) => !user.package_id && user.role !== "admin").length);
    packages.forEach((pkg) => result.set(String(pkg.id), users.filter((user) => user.package_id === pkg.id && user.role !== "admin").length));
    return result;
  }, [packages, users]);
  const visible = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return users.filter((user) => {
      const filterMatch = packageFilter === "all" || (packageFilter === "none" ? !user.package_id : String(user.package_id ?? "") === packageFilter);
      const searchMatch = !normalized || [user.username, user.nickname, user.email, user.remark, user.package_name, user.telegram_username]
        .some((value) => value?.toLowerCase().includes(normalized));
      return filterMatch && searchMatch;
    });
  }, [packageFilter, query, users]);

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

      {loading ? <div className="user-empty">正在读取用户...</div> : visible.length === 0 ? <div className="user-empty">没有符合条件的用户</div> : (
        <section className={`user-list ${view}`}>
          {visible.map((user) => {
            const pkg = user.package_id ? packageById.get(user.package_id) : undefined;
            return <UserCard key={user.username} user={user} pkg={pkg} busy={busy} view={view}
              onDialog={setDialog}
              onStatus={() => {
                if (!window.confirm(`确认${user.is_active ? "禁用" : "启用"}用户 ${user.username}？`)) return;
                void run(`status-${user.username}`, () => setManagedUserStatus(token, user.username, !user.is_active), `用户 ${user.username} 已${user.is_active ? "禁用" : "启用"}`);
              }}
              onExtend={(days) => void run(`extend-${user.username}`, () => extendManagedUserPackage(token, user.username, days), `用户 ${user.username} 已续期 ${days} 天`)}
              onResetTraffic={() => {
                if (!window.confirm(`确认将 ${user.username} 当前流量周期清零？`)) return;
                void run(`traffic-${user.username}`, () => resetManagedUserTraffic(token, user.username), `用户 ${user.username} 流量已重置`);
              }}
              onDelete={() => {
                if (!window.confirm(`确认永久删除用户 ${user.username}？账号、订阅绑定、节点与相关设置会一并删除，且不可撤销。`)) return;
                void run(`delete-${user.username}`, () => deleteManagedUser(token, user.username), `用户 ${user.username} 已删除`);
              }} />;
          })}
        </section>
      )}

      {dialog?.kind === "create" && <CreateUserDialog token={token} onClose={() => setDialog(null)} onCreated={async (password) => { setDialog(null); setNotice({ tone: "success", text: `用户已创建，初始密码 ${password} 已复制` }); await load(); }} />}
      {dialog?.kind === "password" && <PasswordDialog token={token} user={dialog.user} onClose={() => setDialog(null)} onSaved={(password) => { setDialog(null); setNotice({ tone: "success", text: `新密码 ${password} 已复制` }); }} />}
      {dialog?.kind === "profile" && <ProfileDialog token={token} user={dialog.user} onClose={() => setDialog(null)} onSaved={async () => { setDialog(null); setNotice({ tone: "success", text: "用户资料已更新" }); await load(); }} />}
      {dialog?.kind === "package" && <PackageDialog token={token} user={dialog.user} packages={packages} onClose={() => setDialog(null)} onSaved={async (text) => { setDialog(null); setNotice({ tone: "success", text }); await load(); }} />}
      {dialog?.kind === "limits" && <LimitsDialog token={token} user={dialog.user} nodes={nodes} onClose={() => setDialog(null)} onSaved={async () => { setDialog(null); setNotice({ tone: "success", text: "用户限制已更新" }); await load(); }} />}
      {dialog?.kind === "accounts" && <AccountsDialog token={token} user={dialog.user} initial={dialog.initial} onClose={() => setDialog(null)} />}
      {dialog?.kind === "subscription" && <SubscriptionDialog user={dialog.user} pkg={dialog.user.package_id ? packageById.get(dialog.user.package_id) : undefined} onClose={() => setDialog(null)} onCopied={(client) => setNotice({ tone: "success", text: `${client} 订阅地址已复制` })} />}
      {dialog?.kind === "telegram" && <TelegramDialog token={token} user={dialog.user} onClose={() => setDialog(null)} onChanged={async (text) => { setDialog(null); setNotice({ tone: "success", text }); await load(); }} />}
    </div>
  );
}

function UserCard({ user, pkg, busy, view, onDialog, onStatus, onExtend, onResetTraffic, onDelete }: {
  user: ManagedUser; pkg?: ManagedPackage; busy: string; view: "full" | "renewal";
  onDialog: (dialog: Dialog) => void; onStatus: () => void; onExtend: (days: number) => void; onResetTraffic: () => void; onDelete: () => void;
}) {
  const used = Number(user.traffic_used) || 0;
  const limit = Number(user.traffic_limit) || 0;
  const percent = limit > 0 ? Math.min(100, (used / limit) * 100) : 0;
  const admin = user.role === "admin";
  return (
    <article className={`user-card${!user.is_active ? " disabled" : ""}`}>
      <header>
        <div className="user-identity"><span className="user-avatar">{(user.nickname || user.username).slice(0, 1).toUpperCase()}</span><div><h2>{user.username}</h2><p>{user.nickname || "—"}{user.email ? ` · ${user.email}` : ""}</p></div></div>
        <div className="user-badges"><span>{admin ? "管理员" : "用户"}</span><span className={user.is_active ? "ok" : "off"}>{user.is_active ? "已启用" : "已禁用"}</span></div>
      </header>
      {view === "full" && <div className="user-facts">
        <UserFact label="Telegram" value={user.telegram_id ? `@${user.telegram_username || user.telegram_id}` : "未绑定"} />
        <UserFact label="备注" value={user.remark || "—"} />
        <UserFact label="短码" value={user.custom_user_short_code || user.user_short_code || "—"} />
        <UserFact label="限制" value={`${formatLimit(user.speed_limit_override, user.speed_limit_mbps, "Mbps")} · ${formatLimit(user.device_limit_override, user.device_limit, "连接")}`} />
      </div>}
      <div className="user-package">
        <div><strong>{user.package_name || "未绑定套餐"}</strong><span>{user.package_end_date ? `到期 ${user.package_end_date}` : admin ? "系统管理员" : "可绑定套餐后生成订阅"}</span></div>
        {limit > 0 ? <div className="user-traffic"><span><b>{formatBytes(used)}</b> / {formatBytes(limit)} · {percent.toFixed(percent < 10 ? 1 : 0)}%</span><i><em style={{ width: `${percent}%` }} /></i></div> : <span className="user-unlimited">{pkg ? "流量不限" : "—"}</span>}
      </div>
      {view === "renewal" && !admin && <div className="user-renew-actions"><button type="button" disabled={!user.package_id || Boolean(busy)} onClick={() => onExtend(30)}>+30 天</button><button type="button" disabled={!user.package_id || Boolean(busy)} onClick={() => onExtend(90)}>+90 天</button><button type="button" disabled={!user.package_id || Boolean(busy)} onClick={() => onExtend(365)}>+365 天</button></div>}
      <footer>
        <button type="button" onClick={() => onDialog({ kind: "subscription", user })} disabled={!user.package_id || !(user.custom_user_short_code || user.user_short_code)}><Link2 />订阅</button>
        <button type="button" onClick={() => onDialog({ kind: "package", user })} disabled={admin}><PackageCheck />套餐</button>
        <button type="button" onClick={() => onDialog({ kind: "profile", user })}><Edit3 />资料</button>
        <button type="button" onClick={() => onDialog({ kind: "limits", user })} disabled={admin}><SlidersHorizontal />限制</button>
        <button type="button" onClick={() => onDialog({ kind: "accounts", user, initial: "all" })}><UserRoundCog />子账户</button>
        <button type="button" onClick={() => onDialog({ kind: "telegram", user })}><Send />Telegram</button>
        {!admin && <button type="button" onClick={() => onDialog({ kind: "password", user })}><KeyRound />重置密码</button>}
        {!admin && <button type="button" onClick={onResetTraffic} disabled={Boolean(busy)}><RotateCcw />重置流量</button>}
        {!admin && <button type="button" onClick={onStatus} disabled={Boolean(busy)}>{user.is_active ? <UserX /> : <UserCheck />}{user.is_active ? "禁用" : "启用"}</button>}
        {!admin && <button className="danger" type="button" onClick={onDelete} disabled={Boolean(busy)}><Trash2 />删除</button>}
      </footer>
    </article>
  );
}

function UserFact({ label, value }: { label: string; value: string }) { return <div><span>{label}</span><strong title={value}>{value}</strong></div>; }

function DialogShell({ title, subtitle, onClose, children, footer, wide = false }: { title: string; subtitle?: string; onClose: () => void; children: React.ReactNode; footer?: React.ReactNode; wide?: boolean }) {
  return <div className="user-dialog-layer" role="presentation" onMouseDown={onClose}><section className={`user-dialog${wide ? " wide" : ""}`} role="dialog" aria-modal="true" aria-label={title} onMouseDown={(event) => event.stopPropagation()}>
    <header><div><h2>{title}</h2>{subtitle && <p>{subtitle}</p>}</div><button type="button" onClick={onClose} aria-label="关闭"><X /></button></header>
    <div className="user-dialog-body">{children}</div>{footer && <footer>{footer}</footer>}
  </section></div>;
}

function CreateUserDialog({ token, onClose, onCreated }: { token: string; onClose: () => void; onCreated: (password: string) => Promise<void> }) {
  const [form, setForm] = useState({ username: "", email: "", nickname: "", password: randomPassword(), remark: "" });
  const [saving, setSaving] = useState(false); const [error, setError] = useState("");
  async function submit(event: React.FormEvent) { event.preventDefault(); setSaving(true); setError(""); try { const result = await createManagedUser(token, form); await copyText(result.password); await onCreated(result.password); } catch (err) { setError(messageOf(err, "创建用户失败")); } finally { setSaving(false); } }
  return <DialogShell title="新增用户" subtitle="默认生成随机初始密码，创建成功后会自动复制。" onClose={onClose} footer={<><button type="button" onClick={onClose}>取消</button><button className="primary" type="submit" form="create-user-form" disabled={saving}>{saving ? "创建中..." : "确认创建"}</button></>}>
    <form id="create-user-form" className="user-form" onSubmit={submit}>
      <label><span>用户名</span><input required value={form.username} onChange={(event) => setForm({ ...form, username: event.target.value })} /></label>
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
  const [remark, setRemark] = useState(user.remark ?? ""); const [shortCode, setShortCode] = useState(user.custom_user_short_code ?? ""); const [saving, setSaving] = useState(false); const [error, setError] = useState("");
  async function save() { const code = shortCode.trim(); if (code && !/^[A-Za-z0-9_-]{2,16}$/.test(code)) { setError("短码只能含字母、数字、下划线或横杠，长度 2–16"); return; } setSaving(true); setError(""); try { await Promise.all([updateManagedUserRemark(token, user.username, remark), updateManagedUserShortCode(token, user.username, code)]); await onSaved(); } catch (err) { setError(messageOf(err, "更新资料失败")); } finally { setSaving(false); } }
  return <DialogShell title="编辑用户资料" subtitle={`用户：${user.username}`} onClose={onClose} footer={<><button type="button" onClick={onClose}>取消</button><button className="primary" type="button" onClick={() => void save()} disabled={saving}>{saving ? "保存中..." : "保存"}</button></>}>
    <div className="user-form one"><label><span>备注</span><input value={remark} onChange={(event) => setRemark(event.target.value)} /></label><label><span>自定义短码</span><input value={shortCode} onChange={(event) => setShortCode(event.target.value)} placeholder={user.user_short_code || "留空使用系统短码"} /><small>留空恢复系统短码；允许字母、数字、下划线和横杠。</small></label>{error && <p className="user-form-error">{error}</p>}</div>
  </DialogShell>;
}

function PackageDialog({ token, user, packages, onClose, onSaved }: { token: string; user: ManagedUser; packages: ManagedPackage[]; onClose: () => void; onSaved: (text: string) => Promise<void> }) {
  const [packageId, setPackageId] = useState(String(user.package_id ?? "")); const [startDate, setStartDate] = useState(today()); const [expireDate, setExpireDate] = useState(user.package_end_date || nextMonth()); const [isReset, setIsReset] = useState(user.is_reset ?? true); const [resetDay, setResetDay] = useState(user.reset_day || 1); const [saving, setSaving] = useState(false); const [error, setError] = useState("");
  async function save() { setSaving(true); setError(""); try { if (!packageId) { await unassignManagedUserPackage(token, user.username); await onSaved(`用户 ${user.username} 已解绑套餐`); } else { const result = await assignManagedUserPackage(token, { username: user.username, package_id: Number(packageId), start_date: startDate, expire_date: expireDate, is_reset: isReset, reset_day: resetDay }); await onSaved(result.warnings?.length ? `套餐已更新；${result.warnings.join("；")}` : `用户 ${user.username} 套餐已更新`); } } catch (err) { setError(messageOf(err, "更新套餐失败")); } finally { setSaving(false); } }
  return <DialogShell title="管理套餐" subtitle={`用户：${user.username}`} onClose={onClose} footer={<><button type="button" onClick={onClose}>取消</button><button className="primary" type="button" onClick={() => void save()} disabled={saving}>{saving ? "处理中..." : "保存套餐"}</button></>}>
    <div className="user-form"><label className="wide"><span>套餐</span><select value={packageId} onChange={(event) => setPackageId(event.target.value)}><option value="">不绑定套餐</option>{packages.map((pkg) => <option key={pkg.id} value={pkg.id}>{pkg.name}</option>)}</select></label><label><span>开始日期</span><input type="date" value={startDate} onChange={(event) => setStartDate(event.target.value)} disabled={!packageId} /></label><label><span>到期日期</span><input type="date" value={expireDate} onChange={(event) => setExpireDate(event.target.value)} disabled={!packageId} /></label><label className="user-check"><input type="checkbox" checked={isReset} onChange={(event) => setIsReset(event.target.checked)} disabled={!packageId} /><span>启用每月流量重置</span></label><label><span>每月重置日</span><input type="number" min="1" max="31" value={resetDay} onChange={(event) => setResetDay(Number(event.target.value))} disabled={!packageId || !isReset} /></label>{error && <p className="user-form-error wide">{error}</p>}</div>
  </DialogShell>;
}

function LimitsDialog({ token, user, nodes, onClose, onSaved }: { token: string; user: ManagedUser; nodes: XrayNode[]; onClose: () => void; onSaved: () => Promise<void> }) {
  const [speed, setSpeed] = useState(user.speed_limit_override == null ? "" : String(user.speed_limit_override)); const [devices, setDevices] = useState(user.device_limit_override == null ? "" : String(user.device_limit_override)); const [nodeSpeed, setNodeSpeed] = useState<Record<number, string>>(() => Object.fromEntries(Object.entries(user.node_speed_limit_overrides ?? {}).map(([id, value]) => [Number(id), String(value)]))); const [nodeDevices, setNodeDevices] = useState<Record<number, string>>(() => Object.fromEntries(Object.entries(user.node_device_limit_overrides ?? {}).map(([id, value]) => [Number(id), String(value)]))); const [saving, setSaving] = useState(false); const [error, setError] = useState("");
  async function save() { setSaving(true); setError(""); try { await Promise.all([updateManagedUserLimits(token, { username: user.username, speed_limit_override: optionalNumber(speed), device_limit_override: optionalInt(devices) }), updateManagedUserNodeLimits(token, { username: user.username, node_speed_overrides: compactNumberMap(nodeSpeed), node_device_overrides: compactIntMap(nodeDevices) })]); await onSaved(); } catch (err) { setError(messageOf(err, "保存用户限制失败")); } finally { setSaving(false); } }
  return <DialogShell wide title="用户限制" subtitle={`用户：${user.username}；留空继承套餐，0 表示显式不限。`} onClose={onClose} footer={<><button type="button" onClick={onClose}>取消</button><button className="primary" type="button" onClick={() => void save()} disabled={saving}>{saving ? "下发中..." : "保存并下发"}</button></>}>
    <div className="user-form"><label><span>全局速度覆盖（Mbps）</span><input type="number" min="0" step="0.1" value={speed} onChange={(event) => setSpeed(event.target.value)} placeholder={`继承套餐 ${user.speed_limit_mbps ?? 0}`} /></label><label><span>全局连接数覆盖</span><input type="number" min="0" value={devices} onChange={(event) => setDevices(event.target.value)} placeholder={`继承套餐 ${user.device_limit ?? 0}`} /></label></div>
    <div className="user-node-limits"><h3>节点级覆盖</h3><p>节点级值优先于用户全局值。只列正式节点和私有节点。</p>{nodes.map((node) => <article key={node.id}><div><strong>{node.node_name}</strong><span>{node.server || node.original_server || "—"} · {node.protocol || "协议未知"}</span></div><label><span>Mbps</span><input type="number" min="0" step="0.1" value={nodeSpeed[node.id] ?? ""} onChange={(event) => setNodeSpeed({ ...nodeSpeed, [node.id]: event.target.value })} placeholder="继承" /></label><label><span>连接数</span><input type="number" min="0" value={nodeDevices[node.id] ?? ""} onChange={(event) => setNodeDevices({ ...nodeDevices, [node.id]: event.target.value })} placeholder="继承" /></label></article>)}</div>{error && <p className="user-form-error">{error}</p>}
  </DialogShell>;
}

function AccountsDialog({ token, user, initial = "all", onClose }: { token: string; user: ManagedUser; initial?: "all" | "inbound" | "routed"; onClose: () => void }) {
  const [items, setItems] = useState<UserSubaccount[]>([]); const [loading, setLoading] = useState(true); const [error, setError] = useState(""); const [filter, setFilter] = useState(initial);
  useEffect(() => { let active = true; fetchManagedUserSubaccounts(token, user.username).then((result) => { if (active) setItems(result.subaccounts ?? []); }).catch((err) => { if (active) setError(messageOf(err, "读取子账户失败")); }).finally(() => { if (active) setLoading(false); }); return () => { active = false; }; }, [token, user.username]);
  const visible = items.filter((item) => filter === "all" || item.type === filter);
  return <DialogShell wide title="子账户与节点" subtitle={`用户：${user.username}`} onClose={onClose}><div className="user-account-tabs"><button className={filter === "all" ? "active" : ""} onClick={() => setFilter("all")}>全部</button><button className={filter === "inbound" ? "active" : ""} onClick={() => setFilter("inbound")}>入站绑定</button><button className={filter === "routed" ? "active" : ""} onClick={() => setFilter("routed")}>路由出站</button></div>{loading ? <div className="user-empty small">正在读取...</div> : error ? <p className="user-form-error">{error}</p> : visible.length === 0 ? <div className="user-empty small">没有对应记录</div> : <div className="user-account-list">{visible.map((item, index) => <article key={`${item.type}-${item.node_id}-${item.server_id}-${item.inbound_tag}-${index}`}><div><strong>{item.node_name || item.inbound_tag || "未命名绑定"}</strong><span>{item.type === "routed" ? "路由出站" : "入站绑定"} · {item.server_name || "服务器未知"}</span></div><div><code>{item.email || item.identifier || "—"}</code><span>{item.protocol || item.inbound_tag || "—"}</span></div><b className={item.is_active ? "ok" : "off"}>{item.is_active ? "有效" : "暂停"}</b></article>)}</div>}</DialogShell>;
}

function SubscriptionDialog({ user, pkg, onClose, onCopied }: { user: ManagedUser; pkg?: ManagedPackage; onClose: () => void; onCopied: (client: string) => void }) {
  const code = user.custom_user_short_code || user.user_short_code || "";
  async function copy(client: string, name: string) { if (!pkg?.short_code || !code) return; await copyText(managedSubscriptionUrl(pkg.short_code, code, client)); onCopied(name); onClose(); }
  return <DialogShell title="复制订阅" subtitle={`${user.username} · ${pkg?.name || "未绑定套餐"}`} onClose={onClose}><div className="user-client-list">{clients.map(([client, name]) => <button key={client} type="button" onClick={() => void copy(client, name)} disabled={!pkg?.short_code || !code}><Copy /><span>{name}</span></button>)}</div>{(!pkg?.short_code || !code) && <p className="user-form-error">套餐短码或用户短码不可用，暂时无法生成订阅地址。</p>}</DialogShell>;
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

function optionalNumber(value: string) { const text = value.trim(); if (!text) return null; const number = Number(text); return Number.isFinite(number) && number >= 0 ? number : null; }
function optionalInt(value: string) { const number = optionalNumber(value); return number == null ? null : Math.trunc(number); }
function compactNumberMap(values: Record<number, string>) { return Object.fromEntries(Object.entries(values).filter(([, value]) => value.trim() !== "").map(([id, value]) => [Number(id), Math.max(0, Number(value) || 0)])); }
function compactIntMap(values: Record<number, string>) { return Object.fromEntries(Object.entries(compactNumberMap(values)).map(([id, value]) => [Number(id), Math.trunc(value)])); }
function formatLimit(override: number | null | undefined, inherited: number | null | undefined, unit: string) { const value = override == null ? inherited : override; return value && value > 0 ? `${value} ${unit}` : "不限"; }
function formatBytes(value: number) { if (!value) return "0 B"; const units = ["B", "KB", "MB", "GB", "TB"]; const index = Math.min(units.length - 1, Math.floor(Math.log(value) / Math.log(1024))); return `${(value / 1024 ** index).toFixed(index >= 3 ? 2 : 1).replace(/\.0$/, "")} ${units[index]}`; }
async function copyText(value: string) { if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(value); const input = document.createElement("textarea"); input.value = value; input.style.position = "fixed"; input.style.opacity = "0"; document.body.appendChild(input); input.select(); document.execCommand("copy"); input.remove(); }
function messageOf(error: unknown, fallback: string) { return error instanceof Error && error.message ? error.message : fallback; }
