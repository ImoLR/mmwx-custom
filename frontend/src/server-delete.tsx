import React, { useEffect, useState } from "react";
import { AlertTriangle, CheckCircle2, Copy, History, LoaderCircle, Trash2 } from "lucide-react";
import { CompactSheet } from "./compact-ui";
import { fetchServerDeletionPreview, fetchServerDeletion, fetchServerDeletions, executeServerDeletion } from "./api";
import { canConfirmServerDeletion, groupServerDeletion } from "./server-delete-state";
import type { ServerDeletionPlan } from "./server-delete-state";

const connectionLabels: Record<string, string> = {
  mmwxc_connection_assignments: "手动连接分配", user_inbound_configs: "用户入站凭据",
  package_assignment_inbound_configs: "套餐分配凭据", user_subaccounts: "路由子账户",
  package_assignment_subaccounts: "套餐路由子账户", user_outbounds: "用户出站",
};
const helperLabels: Record<string, string> = {
  online: "Helper 在线，支持卸载", unavailable: "Helper 离线或未安装", outdated: "Helper 版本过旧",
  waiting: "等待 Helper 确认停止 Core", core_stopped: "自有 Core 已停止，Helper 正在自卸载",
  failed: "Helper 卸载失败", unconfirmed: "Helper 卸载未获确认",
};

export function ServerDeletionPreview({ plan, uninstallAgent, onUninstallAgentChange, executing = false }: {
  plan: ServerDeletionPlan; uninstallAgent: boolean; onUninstallAgentChange?: (value: boolean) => void; executing?: boolean;
}) {
  const groups = groupServerDeletion(plan);
  const nodes = (items: ServerDeletionPlan["nodes"]) => items.length ? <ul className="server-delete-rows">{items.map((node) => <li key={node.id}><strong>{node.name}</strong><small>{node.server_name || "导入节点"} · #{node.id}</small><span>{node.reason}</span></li>)}</ul> : <p className="server-delete-empty">无</p>;
  return <div className="server-delete-preview">
    <section><h3>服务器</h3><div className="server-delete-target"><strong>{plan.server_name}</strong><span>#{plan.server_id}</span></div><p>将永久删除此服务器及以下节点。空套餐也会删除，绑定用户的账号保留。</p></section>
    <section><h3>将删除的节点 <b>{groups.direct.length}</b></h3>{nodes(groups.direct)}</section>
    <section><h3>受影响的其他节点 <b>{groups.dependants.length}</b></h3>{nodes(groups.dependants)}{plan.attention.length > 0 && <div className="server-delete-attention"><strong>需注意</strong><ul>{plan.attention.map((note) => <li key={note}>{note}</li>)}</ul></div>}</section>
    <section><h3>套餐（删除 / 移除节点）</h3>{plan.packages.length ? <ul className="server-delete-rows">{[...groups.deletedPackages, ...groups.trimmedPackages].map((pkg) => <li key={pkg.id}><strong><em className={pkg.delete ? "delete" : "trim"}>{pkg.delete ? "删除套餐" : "移除节点"}</em>{pkg.name}</strong><span>绑定用户：{pkg.users.join("、") || "无"}</span><small>移除 {pkg.deleted_node_ids.length} 个节点{pkg.delete ? "，清空后不保留套餐" : `，保留 ${pkg.node_ids.length - pkg.deleted_node_ids.length} 个`}</small></li>)}</ul> : <p className="server-delete-empty">无受影响套餐</p>}</section>
    <section><h3>流量组</h3>{plan.groups.length ? <ul className="server-delete-rows">{plan.groups.map((group) => <li key={group.id}><strong><em className={group.delete ? "delete" : "trim"}>{group.delete ? "删除空组" : "移除节点"}</em>{group.name}</strong><small>套餐 #{group.package_id} · {group.delete ? "没有剩余节点" : `保留 ${group.node_ids.length} 个节点，共享额度 ${Math.round(group.limit_bytes / 1073741824 * 100) / 100} GB`}</small></li>)}</ul> : <p className="server-delete-empty">无受影响流量组</p>}</section>
    <section><h3>连接 / 分配</h3>{Object.keys(plan.connections).length ? <dl className="server-delete-counts">{Object.entries(plan.connections).map(([key, count]) => <div key={key}><dt>{connectionLabels[key] || key}</dt><dd>{count}</dd></div>)}</dl> : <p className="server-delete-empty">无连接分配记录</p>}<p>关联的连接设置、身份和界面缓存会一并清理。</p></section>
    <section><h3>机器端清理状态</h3><p className="server-delete-helper">{helperLabels[plan.machine.helper] || plan.machine.helper}</p><label className="server-delete-checkbox"><input type="checkbox" checked={uninstallAgent} disabled={executing || !onUninstallAgentChange} onChange={(event) => onUninstallAgentChange?.(event.target.checked)} />删除前卸载官方 Agent</label><p>{plan.machine.official_agent}</p>
      {(plan.machine.outstanding.length > 0 || !uninstallAgent) && <div className="server-delete-warning" role="alert"><AlertTriangle /><div><strong>机器上的入站可能仍接受连接</strong><ul>{plan.machine.outstanding.map((message) => <li key={message}>{message}</li>)}{!uninstallAgent && <li>未选择卸载官方 Agent，请另行停止并清理其 Core。</li>}</ul><p>以下命令仅在这台已删除的服务器执行：</p><pre>{plan.machine.manual_command}</pre><button type="button" onClick={() => void navigator.clipboard.writeText(plan.machine.manual_command)}><Copy />复制清理命令</button></div></div>}
    </section>
  </div>;
}

export function ServerDeleteDialog({ token, server, initialPlan, onClose, onChanged }: {
  token: string; server: { id: number; name: string }; initialPlan?: ServerDeletionPlan; onClose: () => void; onChanged: () => Promise<void>;
}) {
  const [plan, setPlan] = useState<ServerDeletionPlan | null>(initialPlan || null);
  const [loading, setLoading] = useState(!initialPlan);
  const [running, setRunning] = useState(false);
  const [confirmation, setConfirmation] = useState("");
  const [uninstallAgent, setUninstallAgent] = useState(initialPlan?.uninstall_agent ?? true);
  const [error, setError] = useState("");
  useEffect(() => {
    if (initialPlan) return;
    let active = true;
    void fetchServerDeletionPreview(token, server.id).then((response) => { if (active) setPlan(response.plan); }).catch((err) => { if (active) setError(err.message); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [token, server.id, initialPlan]);
  async function execute() {
    if (!canConfirmServerDeletion(plan, confirmation, running) || !plan) return;
    setRunning(true); setError(""); let current = plan;
    try {
      do {
        const result = await executeServerDeletion(token, server.id, { confirm_name: confirmation, fingerprint: current.fingerprint, operation_id: current.operation_id, uninstall_agent: uninstallAgent });
        current = result.plan; setPlan(current);
        if (current.state !== "deleting") break;
        await new Promise((resolve) => setTimeout(resolve, 1000));
      } while (true);
      await onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : "删除请求失败");
      try { const saved = await fetchServerDeletion(token, server.id); setPlan(saved.plan); } catch { /* A preflight failure has no saved plan. */ }
    } finally { setRunning(false); }
  }
  const done = plan?.state === "deleted";
  return <CompactSheet title={done ? "服务器已删除" : "删除服务器"} subtitle={server.name} className="server-delete-dialog" bodyClassName="server-delete-body" closeDisabled={running} onClose={running ? () => {} : onClose} footer={<><button type="button" onClick={onClose} disabled={running}>{done ? "关闭" : "取消"}</button>{!done && <button className="danger" type="button" disabled={!canConfirmServerDeletion(plan, confirmation, running)} onClick={() => void execute()}>{running ? <LoaderCircle className="spin" /> : <Trash2 />}{running ? "正在清理" : plan?.operation_id ? "继续 / 重试删除" : "确认删除"}</button>}</>}>
    {loading && <p role="status">正在核对服务器及关联数据…</p>}
    {error && <p className="server-delete-warning" role="alert">{error}</p>}
    {plan && <>
      {plan.operation_id && <section className="server-delete-progress" aria-live="polite"><p><strong>{done ? "官方数据复核通过" : "删除进度"}</strong> · {groupServerDeletion(plan).completed}/{plan.steps.length}</p><code>操作编号：{plan.operation_id}</code><ol>{plan.steps.map((step, index) => <li key={index} className={step.status}>{step.status === "completed" ? <CheckCircle2 /> : step.status === "failed" ? <AlertTriangle /> : <span className="server-delete-step-dot" />}<div><span>{step.name}</span>{step.last_error && <small>{step.last_error}</small>}</div></li>)}</ol></section>}
      <ServerDeletionPreview plan={plan} uninstallAgent={uninstallAgent} onUninstallAgentChange={setUninstallAgent} executing={running || Boolean(plan.operation_id)} />
      {!done && <label className="server-delete-confirm"><span>输入完整服务器名称 <strong>{plan.server_name}</strong> 以确认</span><input autoComplete="off" value={confirmation} disabled={running} onChange={(event) => setConfirmation(event.target.value)} placeholder={plan.server_name} aria-label="确认服务器名称" /></label>}
    </>}
  </CompactSheet>;
}

export function ServerDeletionHistory({ token, onOpen }: { token: string; onOpen: (plan: ServerDeletionPlan) => void }) {
  const [plans, setPlans] = useState<ServerDeletionPlan[]>([]);
  const [error, setError] = useState("");
  useEffect(() => { let active = true; const refresh = () => void fetchServerDeletions(token).then((result) => { if (active) { setPlans(result.plans); setError(""); } }).catch(() => { if (active) setError("删除记录暂时无法读取"); }); refresh(); const timer = window.setInterval(refresh, 10000); return () => { active = false; window.clearInterval(timer); }; }, [token]);
  if (!plans.length && !error) return null;
  return <details className="server-delete-history"><summary><History />删除记录{plans.some((plan) => plan.state !== "deleted") && <b>有待处理任务</b>}</summary>{error && <p>{error}</p>}{plans.map((plan) => <button key={plan.operation_id} type="button" onClick={() => onOpen(plan)}><span>{plan.server_name}</span><small>{plan.state !== "deleted" ? "未完成 · 继续清理" : plan.machine.outstanding.length ? "已删除 · 机器仍待清理" : "已删除"}</small></button>)}</details>;
}
