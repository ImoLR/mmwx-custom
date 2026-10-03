import { useEffect, useRef, useState } from "react";
import { AlertTriangle, Loader2, RefreshCw, X } from "lucide-react";
import { repairRelayCredentials } from "./api";
import type { RelayCredentialRepairReport } from "./node-auxiliary-logic";

type ToolNotice = (tone: "success" | "error" | "info", text: string) => void;
const repairStatuses: Record<string, string> = { pending: "待迁移", updated: "已迁移", skipped: "跳过", failed: "失败", aborted: "已中断" };

export function RelayCredentialRepairDialog({ token, onClose, onNotice, onChanged }: { token: string; onClose: () => void; onNotice: ToolNotice; onChanged: () => void | Promise<unknown> }) {
  const [report, setReport] = useState<RelayCredentialRepairReport | null>(null);
  const [busy, setBusy] = useState<"scan" | "apply" | null>(null);
  const [error, setError] = useState("");
  const [confirm, setConfirm] = useState(false);
  const started = useRef("");
  const run = async (apply: boolean) => {
    setBusy(apply ? "apply" : "scan"); setError(""); setConfirm(false);
    try {
      const result = await repairRelayCredentials(token, apply);
      if (result.success === false || !result.report) throw new Error(result.error || "落地出站凭据迁移失败");
      setReport(result.report);
      if (apply) await onChanged();
    } catch (err) {
      const message = err instanceof Error ? err.message : "落地出站凭据迁移失败";
      setReport(null); setError(message); onNotice("error", message);
    } finally { setBusy(null); }
  };
  useEffect(() => { if (started.current !== token) { started.current = token; void run(false); } }, [token]);
  const stats = report ? [
    ["扫描服务器", report.servers_scanned], ["扫描出站", report.outbounds_scanned], [report.dry_run ? "待迁移" : "已迁移", report.updated],
    ["已正确", report.already_ok], ["跳过", report.skipped], ["失败", report.failed],
  ] : [];
  return <div className="node-dialog-layer" role="presentation" onClick={() => { if (!busy) onClose(); }}>
    <section className="node-dialog node-tool-dialog" role="dialog" aria-modal="true" aria-label="落地出站凭据迁移" onClick={(event) => event.stopPropagation()}>
      <header><div><h2>落地出站凭据迁移</h2><p>把落地出站里那把「创建者基础凭据」换成落地节点专属的系统凭据。默认只扫描不修改。</p></div><button type="button" disabled={busy !== null} onClick={onClose} aria-label="关闭"><X /></button></header>
      <div className="node-dialog-body">
        <p className="node-help">中转穿透的流量以前记在创建者(通常是 admin)名下,会把创建者的套餐用量撑爆、被判超额后摘除其全部 client,导致整条中转链对所有用户同时断线,且每个周期都会重来一次。</p>
        {busy && <div className="node-empty" role="status"><Loader2 className="spin" />{busy === "apply" ? "正在迁移…逐台串行、台间留有间隔,请勿关闭本窗口。" : "正在扫描…逐台串行、台间留有间隔,服务器多时可能要几分钟,请勿关闭本窗口。"}</div>}
        {error && <div className="node-danger-panel" role="alert"><p>{error}</p></div>}
        {!busy && report && <>
          <div className="node-chip-row">{stats.map(([label, value]) => <span key={label} className={label === "失败" && Number(value) > 0 ? "bad" : ""}>{label} <strong>{value}</strong></span>)}</div>
          {report.dry_run && report.updated === 0 && <p className="node-help">没有需要迁移的出站,现有凭据都已经是正确的。</p>}
          {!report.dry_run && <p className="node-help">{report.failed > 0 ? `迁移完成,但有 ${report.failed} 条失败。该操作幂等,可以重新打开本窗口再跑一次。` : "迁移完成。"}</p>}
          <p className="node-help">明细(最多 200 条,上方计数为完整值)</p>
          <div style={{ overflowX: "auto" }}>{report.details?.length ? <table className="node-aux-table"><thead><tr><th>状态</th><th>服务器</th><th>出站</th><th>落地节点</th><th>处理</th></tr></thead><tbody>{report.details.map((detail, index) => <tr key={`${index}-${detail.outbound ?? ""}`}><td>{repairStatuses[detail.status] ?? detail.status}</td><td>{detail.server || "—"}</td><td>{detail.outbound || "—"}</td><td>{detail.node_id ? `${detail.node_name || "—"} #${detail.node_id}` : "—"}</td><td>{detail.message}</td></tr>)}</tbody></table> : <p className="node-help">无明细</p>}</div>
        </>}
        {confirm && report && <div className="node-danger-panel" role="alert"><strong><AlertTriangle size={16} /> 确认执行迁移?</strong><p>将改写 {report.updated} 条落地出站的凭据。换 uuid 会打断当前正跑在这些中转上的连接 —— 经过的用户会掉线重连一次。该操作幂等,失败的可以再跑一次。</p><div className="node-dialog-actions"><button type="button" onClick={() => setConfirm(false)}>取消</button><button type="button" className="danger" onClick={() => void run(true)}>确认执行</button></div></div>}
        <div className="node-dialog-actions"><button type="button" disabled={busy !== null} onClick={onClose}>关闭</button>{!busy && !report && <button type="button" onClick={() => void run(false)}><RefreshCw />重新扫描</button>}{!busy && report?.dry_run && report.updated > 0 && !confirm && <button type="button" onClick={() => setConfirm(true)}>执行迁移 ({report.updated})</button>}</div>
      </div>
    </section>
  </div>;
}
