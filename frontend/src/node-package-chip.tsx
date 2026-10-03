import { useCallback, useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { Loader2, Plus, X } from "lucide-react";
import { fetchNodePackageMemberships, toggleNodePackageMembership } from "./api";
import { membershipNeedsExpandWarning } from "./node-auxiliary-logic";
import type { NodePackageMembership, NodePackageMemberships } from "./node-auxiliary-logic";

type ToolNotice = (tone: "success" | "error" | "info", text: string) => void;

export function useNodePackages(token: string, onNotice: ToolNotice, nodeIds?: string) {
  const [data, setData] = useState<NodePackageMemberships>({});
  const [loading, setLoading] = useState(true);
  const refresh = useCallback(async () => {
    setLoading(true);
    try { setData(await fetchNodePackageMemberships(token)); }
    catch (error) { onNotice("error", error instanceof Error ? error.message : "读取套餐节点失败"); }
    finally { setLoading(false); }
  }, [onNotice, token]);
  useEffect(() => { if (nodeIds !== "") void refresh(); }, [refresh, nodeIds]);
  return { memberships: data.memberships ?? {}, packages: data.packages ?? [], loading, refresh };
}

export function NodePackageChip({ token, nodeId, memberships, packages, loading = false, onChanged, onNotice }: {
  token: string;
  nodeId: number;
  memberships: NodePackageMembership[];
  packages: NodePackageMembership[];
  loading?: boolean;
  onChanged: () => void | Promise<unknown>;
  onNotice: ToolNotice;
}) {
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState<number | null>(null);
  const [confirm, setConfirm] = useState<number | null>(null);
  const memberIds = new Set(memberships.map((item) => item.package_id));
  const close = () => { if (pending === null) { setOpen(false); setConfirm(null); } };
  const toggle = async (item: NodePackageMembership, member: boolean) => {
    setPending(item.package_id);
    try {
      const result = await toggleNodePackageMembership(token, { package_id: item.package_id, node_id: nodeId, member });
      if (result.success === false) throw new Error(result.error || "更新套餐节点失败");
      setConfirm(null);
      await onChanged();
      onNotice("success", "已更新套餐节点");
    } catch (error) { setConfirm(null); onNotice("error", error instanceof Error ? error.message : "更新套餐节点失败"); }
    finally { setPending(null); }
  };
  return <>
    <button type="button" className="node-package-chip node-chip-row" title="点击调整该节点所属的套餐" disabled={loading} onClick={(event) => { event.stopPropagation(); setOpen(true); }}>
      {memberships.length ? <>{memberships.slice(0, 3).map((item) => <span key={item.package_id} className={item.all_nodes ? "all-nodes" : ""} title={item.all_nodes ? `${item.package_name}:该套餐未配置节点,默认包含全部节点` : item.package_name}>{item.package_name}</span>)}{memberships.length > 3 && <span title={memberships.map((item) => item.package_name).join("\n")}>+{memberships.length - 3}</span>}</> : <span><Plus size={12} />加入套餐</span>}
    </button>
    {open && createPortal(<div className="node-dialog-layer" role="presentation" onClick={close}>
      <section className="node-dialog node-tool-dialog" role="dialog" aria-modal="true" aria-label="该节点所属套餐" onClick={(event) => event.stopPropagation()}>
        <header><div><h2>该节点所属套餐</h2><p>点击调整该节点所属的套餐</p></div><button type="button" disabled={pending !== null} onClick={close} aria-label="关闭"><X /></button></header>
        <div className="node-dialog-body">
          {!packages.length ? <p className="node-help">还没有任何套餐</p> : <div className="node-tool-list">{packages.map((item) => <div key={item.package_id}>
            <label className="node-package-option"><input type="checkbox" checked={memberIds.has(item.package_id)} disabled={pending !== null || loading} onChange={(event) => {
              if (membershipNeedsExpandWarning(item, event.target.checked)) { setConfirm(item.package_id); return; }
              setConfirm(null); void toggle(item, event.target.checked);
            }} /><span>{item.package_name}</span>{item.all_nodes && <small>全部节点</small>}{pending === item.package_id && <Loader2 className="spin" size={16} />}</label>
            {confirm === item.package_id && <div className="node-confirm-panel"><p>该套餐当前未配置节点(= 包含全部节点)。移除这一个会把它落成一份显式清单,之后新建的节点不会再自动进这个套餐。</p><div className="node-dialog-actions"><button type="button" disabled={pending !== null} onClick={() => setConfirm(null)}>取消</button><button type="button" className="danger" disabled={pending !== null} onClick={() => void toggle(item, false)}>仍要移除</button></div></div>}
          </div>)}</div>}
        </div>
      </section>
    </div>, document.body)}
  </>;
}
