import React, { useCallback, useEffect, useMemo, useState } from "react";
import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  Boxes,
  Edit3,
  LayoutGrid,
  List,
  PackagePlus,
  Plus,
  RefreshCw,
  Save,
  Server,
  Share2,
  Trash2,
  X,
} from "lucide-react";
import {
  createPackage,
  deletePackage,
  fetchPackageForwardChains,
  fetchPackages,
  fetchPackageTemplates,
  fetchPackageTrafficGroups,
  fetchPackageTrafficGroupUsage,
  fetchRemoteServers,
  fetchXrayNodes,
  updatePackage,
  savePackageTrafficGroups,
} from "./api";
import type {
  ManagedPackage,
  PackageForwardChain,
  PackagePayload,
  PackageTemplate,
  PackageTrafficGroupNode,
  PackageTrafficGroupUsageResponse,
  RemoteServer,
  XrayNode,
} from "./types";
import { TRAFFIC_GB, retainTrafficGroupNodes, trafficGroupDraft, trafficGroupNodeWarning, trafficGroupPayload, validateTrafficGroups } from "./package-traffic-groups";
import type { PackageTrafficGroupDraft } from "./package-traffic-groups";

type Notice = { tone: "success" | "error" | "info"; text: string } | null;
type ConfirmState = { kind: "delete"; pkg: ManagedPackage } | null;

const emptyPackage = (): PackagePayload => ({
  name: "",
  description: "",
  traffic_limit_gb: 100,
  cycle_days: 30,
  is_reset: true,
  reset_day: 1,
  nodes: [],
  node_multipliers: {},
  node_name_overrides: {},
  node_name_override_enabled: false,
  node_speed_limits: {},
  node_device_limits: {},
  node_traffic_limits: {},
  speed_limit_mbps: 0,
  device_limit: 0,
  forward_rule_limit: 0,
  forward_port_limit: 0,
  forward_speed_mbps: 0,
  forward_conn_limit: 0,
  forward_chains: [],
  traffic_mode: "oneway",
  template_filename: "",
  surge_template_filename: "",
});

// Package updates replace the whole record, so fields this form does not edit
// (for example Loon templates and IP limits) are sent back unchanged.
const PACKAGE_READ_ONLY_FIELDS = new Set(["nodes_configured", "short_code", "created_at", "updated_at"]);

function packageToForm(pkg: ManagedPackage): PackagePayload {
  const preserved = Object.fromEntries(Object.entries(pkg).filter(([key]) => !PACKAGE_READ_ONLY_FIELDS.has(key)));
  return {
    ...emptyPackage(),
    ...preserved,
    id: pkg.id,
    name: pkg.name,
    description: pkg.description ?? "",
    traffic_limit_gb: Number(pkg.traffic_limit_gb) || 0,
    cycle_days: Number(pkg.cycle_days) || 30,
    is_reset: Boolean(pkg.is_reset),
    reset_day: Number(pkg.reset_day) || 1,
    nodes: (pkg.nodes ?? []).map(Number),
    node_multipliers: numericMap(pkg.node_multipliers),
    node_name_overrides: stringMap(pkg.node_name_overrides),
    node_name_override_enabled: Boolean(pkg.node_name_override_enabled),
    node_speed_limits: numericMap(pkg.node_speed_limits),
    node_device_limits: numericMap(pkg.node_device_limits),
    node_traffic_limits: numericMap(pkg.node_traffic_limits),
    speed_limit_mbps: Number(pkg.speed_limit_mbps) || 0,
    device_limit: Number(pkg.device_limit) || 0,
    forward_rule_limit: Number(pkg.forward_rule_limit) || 0,
    forward_port_limit: Number(pkg.forward_port_limit) || 0,
    forward_speed_mbps: Number(pkg.forward_speed_mbps) || 0,
    forward_conn_limit: Number(pkg.forward_conn_limit) || 0,
    forward_chains: (pkg.forward_chains ?? []).map(Number),
    traffic_mode: pkg.traffic_mode || "oneway",
    template_filename: pkg.template_filename ?? "",
    surge_template_filename: pkg.surge_template_filename ?? "",
  };
}

function numericMap(value: Record<number, number> | null | undefined) {
  return Object.fromEntries(Object.entries(value ?? {}).map(([key, item]) => [Number(key), Number(item)]));
}

function stringMap(value: Record<number, string> | null | undefined) {
  return Object.fromEntries(Object.entries(value ?? {}).map(([key, item]) => [Number(key), String(item)]));
}

export function PackageManagementPage({ token }: { token: string }) {
  const [packages, setPackages] = useState<ManagedPackage[]>([]);
  const [nodes, setNodes] = useState<XrayNode[]>([]);
  const [servers, setServers] = useState<RemoteServer[]>([]);
  const [templates, setTemplates] = useState<PackageTemplate[]>([]);
  const [chains, setChains] = useState<PackageForwardChain[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState<Notice>(null);
  const [view, setView] = useState<"cards" | "list">(() => localStorage.getItem("packages-view-mode") === "list" ? "list" : "cards");
  const [editor, setEditor] = useState<{ pkg?: ManagedPackage } | null>(null);
  const [confirm, setConfirm] = useState<ConfirmState>(null);
  const [groupCounts, setGroupCounts] = useState<Record<number, number>>({});
  const [usagePackage, setUsagePackage] = useState<ManagedPackage | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [packageResult, nodeResult, serverResult, templateResult, chainResult] = await Promise.all([
        fetchPackages(token),
        fetchXrayNodes(token),
        fetchRemoteServers(token),
        fetchPackageTemplates(token).catch(() => ({ templates: [] })),
        fetchPackageForwardChains(token).catch(() => ({ chains: [] })),
      ]);
      setPackages(packageResult.packages ?? []);
      setNodes(nodeResult.nodes ?? []);
      setServers(serverResult.servers ?? []);
      setTemplates(templateResult.templates ?? []);
      setChains(chainResult.chains ?? []);
      const groupResults = await Promise.allSettled((packageResult.packages ?? []).map((pkg) => fetchPackageTrafficGroups(token, pkg.id)));
      const counts: Record<number, number> = {};
      groupResults.forEach((result, index) => {
        if (result.status === "fulfilled") counts[packageResult.packages![index].id] = (result.value.groups ?? []).length;
      });
      setGroupCounts(counts);
      setNotice(groupResults.some((result) => result.status === "rejected") ? { tone: "error", text: "部分套餐的流量共享组读取失败，请刷新重试" } : null);
    } catch (error) {
      setNotice({ tone: "error", text: messageOf(error, "读取套餐失败") });
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => { void load(); }, [load]);

  const nodeById = useMemo(() => new Map(nodes.map((node) => [node.id, node])), [nodes]);

  async function handleConfirm() {
    if (!confirm || busy) return;
    const key = `${confirm.kind}-${confirm.pkg.id}`;
    setBusy(key);
    try {
      await deletePackage(token, confirm.pkg.id);
      setNotice({ tone: "success", text: `套餐“${confirm.pkg.name}”已删除` });
      await load();
      setConfirm(null);
    } catch (error) {
      setNotice({ tone: "error", text: messageOf(error, "操作失败") });
    } finally {
      setBusy("");
    }
  }

  return (
    <div className="package-manager">
      <section className="package-hero">
        <div>
          <h1>套餐管理</h1>
          <p>管理套餐额度、周期、模板、节点覆盖与转发权限。</p>
        </div>
        <div className="package-head-actions">
          <button type="button" onClick={() => void load()} aria-label="刷新套餐" title="刷新套餐"><RefreshCw /></button>
          <button className="primary" type="button" onClick={() => setEditor({})}><PackagePlus /><span>创建套餐</span></button>
        </div>
      </section>

      {notice && <div className={`package-notice ${notice.tone}`}><span>{notice.text}</span><button type="button" onClick={() => setNotice(null)} aria-label="关闭提示"><X /></button></div>}

      <section className="package-toolbar" aria-label="套餐显示方式">
        <strong>套餐模板（{packages.length}）</strong>
        <div>
          <button className={view === "cards" ? "active" : ""} type="button" onClick={() => { setView("cards"); localStorage.setItem("packages-view-mode", "cards"); }} aria-label="卡片视图"><LayoutGrid /></button>
          <button className={view === "list" ? "active" : ""} type="button" onClick={() => { setView("list"); localStorage.setItem("packages-view-mode", "list"); }} aria-label="列表视图"><List /></button>
        </div>
      </section>

      {loading ? <div className="package-empty">正在读取套餐...</div> : packages.length === 0 ? (
        <div className="package-empty"><Boxes /><strong>还没有套餐</strong><span>点击“创建套餐”开始配置</span></div>
      ) : (
        <div className={`package-list ${view}`}>
          {packages.map((pkg) => {
            const linked = (pkg.nodes ?? []).map((id) => nodeById.get(id)).filter(Boolean) as XrayNode[];
            const overrides = new Set([
              ...Object.keys(pkg.node_speed_limits ?? {}),
              ...Object.keys(pkg.node_device_limits ?? {}),
              ...Object.keys(pkg.node_traffic_limits ?? {}),
              ...Object.keys(pkg.node_multipliers ?? {}),
              ...Object.keys(pkg.node_name_overrides ?? {}),
            ]).size;
            return (
              <article className="package-card" key={pkg.id}>
                <header>
                  <div><h2>{pkg.name}</h2><p>{pkg.description || "暂无说明"}</p></div>
                  {pkg.short_code && <span className="package-code">{pkg.short_code}</span>}
                </header>
                <div className="package-facts">
                  <PackageFact label="流量额度" value={`${formatNumber(pkg.traffic_limit_gb)} GB`} />
                  <PackageFact label="套餐周期" value={`${pkg.cycle_days} 天`} />
                  <PackageFact label="月度重置" value={pkg.is_reset ? `每月 ${pkg.reset_day} 日` : "不重置"} />
                  <PackageFact label="流量统计" value={pkg.traffic_mode === "twoway" ? "双向" : "单向"} />
                  <PackageFact label="Clash 模板" value={templateName(templates, pkg.template_filename)} />
                  <PackageFact label="Surge 模板" value={templateName(templates, pkg.surge_template_filename)} />
                  <PackageFact label="全局限制" value={`${pkg.speed_limit_mbps > 0 ? `${formatNumber(pkg.speed_limit_mbps)} Mbps` : "不限速"} · ${pkg.device_limit > 0 ? `${pkg.device_limit} 个并发连接` : "连接数不限"}`} />
                  <PackageFact label="节点覆盖" value={overrides > 0 ? `${overrides} 个节点` : "无"} />
                  <PackageFact label="共享组" value={groupCounts[pkg.id] === undefined ? "读取失败" : groupCounts[pkg.id] > 0 ? `${groupCounts[pkg.id]} 组` : "无"} />
                </div>
                <div className="package-node-summary">
                  <Server />
                  <div>
                    <strong>{linked.length > 0 ? `关联 ${linked.length} 个节点` : "未选节点（使用全部节点）"}</strong>
                    <span>{linked.length > 0 ? linked.map((node) => node.node_name).join("、") : "保存空节点列表时由后端按全部节点处理"}</span>
                  </div>
                </div>
                {pkg.forward_rule_limit > 0 && <div className="package-forward-summary"><Share2 />转发：{pkg.forward_rule_limit} 条规则 · {pkg.forward_port_limit || "不限"} 端口 · {(pkg.forward_chains ?? []).length} 条链</div>}
                <footer>
                  <button type="button" onClick={() => setEditor({ pkg })}><Edit3 />编辑</button>
                  <button type="button" onClick={() => setUsagePackage(pkg)}><Share2 />共享组用量</button>
                  <button className="danger" type="button" onClick={() => setConfirm({ kind: "delete", pkg })}><Trash2 />删除</button>
                </footer>
              </article>
            );
          })}
        </div>
      )}

      {editor && (
        <PackageEditor
          token={token}
          pkg={editor.pkg}
          nodes={nodes}
          servers={servers}
          templates={templates}
          chains={chains}
          onClose={() => setEditor(null)}
          onSaved={async (text) => { setEditor(null); setNotice({ tone: "success", text }); await load(); }}
        />
      )}
      {usagePackage && <PackageTrafficUsageDialog token={token} pkg={usagePackage} onClose={() => setUsagePackage(null)} />}
      {confirm && (
        <div className="package-dialog-layer" role="presentation" onMouseDown={() => setConfirm(null)}>
          <section className="package-confirm-dialog" role="alertdialog" aria-modal="true" onMouseDown={(event) => event.stopPropagation()}>
            <AlertTriangle />
            <h2>删除套餐</h2>
            <p>{`确定删除“${confirm.pkg.name}”？已绑定用户会被解绑，这项操作不可撤销。`}</p>
            <div><button type="button" onClick={() => setConfirm(null)}>取消</button><button className="danger" type="button" disabled={Boolean(busy)} onClick={() => void handleConfirm()}>{busy ? "处理中..." : "确认"}</button></div>
          </section>
        </div>
      )}
    </div>
  );
}

function PackageFact({ label, value }: { label: string; value: string }) {
  return <div><span>{label}</span><strong title={value}>{value}</strong></div>;
}

function PackageEditor({ token, pkg, nodes, servers, templates, chains, onClose, onSaved }: {
  token: string;
  pkg?: ManagedPackage;
  nodes: XrayNode[];
  servers: RemoteServer[];
  templates: PackageTemplate[];
  chains: PackageForwardChain[];
  onClose: () => void;
  onSaved: (message: string) => Promise<void>;
}) {
  const [form, setForm] = useState<PackagePayload>(() => pkg ? packageToForm(pkg) : emptyPackage());
  const [tag, setTag] = useState("all");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [resetTouched, setResetTouched] = useState(Boolean(pkg));
  const [groups, setGroups] = useState<PackageTrafficGroupDraft[]>([]);
  const [groupsLoading, setGroupsLoading] = useState(Boolean(pkg));
  const [groupsError, setGroupsError] = useState("");
  const [capabilityError, setCapabilityError] = useState("");
  const [groupNodes, setGroupNodes] = useState<PackageTrafficGroupNode[]>([]);
  const [savedPackageId, setSavedPackageId] = useState(pkg?.id);
  const [creationUnresolved, setCreationUnresolved] = useState(false);
  const tags = useMemo(() => [...new Set(nodes.flatMap((node) => node.tags?.length ? node.tags : node.tag ? [node.tag] : []).filter(Boolean))].sort(), [nodes]);
  const visibleNodes = useMemo(() => tag === "all" ? nodes : nodes.filter((node) => (node.tags ?? [node.tag]).includes(tag)), [nodes, tag]);
  const selectedNodes = useMemo(() => form.nodes.map((id) => nodes.find((node) => node.id === id)).filter(Boolean) as XrayNode[], [form.nodes, nodes]);
  const memberNodes = form.nodes.length > 0 ? selectedNodes : nodes;
  const serverByName = useMemo(() => new Map(servers.map((server) => [server.name, server])), [servers]);
  const clashTemplates = templates.filter((template) => template.type !== "surge" && !template.filename.endsWith(".conf"));
  const surgeTemplates = templates.filter((template) => template.type === "surge" || template.filename.endsWith(".conf"));
  const forwardEnabled = form.forward_rule_limit > 0;

  const loadGroups = useCallback(async () => {
    if (!pkg) return;
    setGroupsLoading(true);
    setGroupsError("");
    const [groupResult, usageResult] = await Promise.allSettled([
      fetchPackageTrafficGroups(token, pkg.id),
      fetchPackageTrafficGroupUsage(token, pkg.id),
    ]);
    if (groupResult.status === "fulfilled") setGroups((groupResult.value.groups ?? []).map(trafficGroupDraft));
    else setGroupsError(messageOf(groupResult.reason, "读取流量共享组失败"));
    if (usageResult.status === "fulfilled") {
      setGroupNodes([...(usageResult.value.nodes ?? []), ...(usageResult.value.usage ?? []).flatMap((row) => row.nodes ?? [])]);
      setCapabilityError("");
    } else setCapabilityError("共享组执行状态读取失败，请保存后在共享组用量中确认");
    setGroupsLoading(false);
  }, [token, pkg]);

  useEffect(() => { void loadGroups(); }, [loadGroups]);

  function patch(next: Partial<PackagePayload>) { setForm((current) => ({ ...current, ...next })); }

  function setSelectedNodes(nextNodes: number[]) {
    const removed = form.nodes.filter((id) => !nextNodes.includes(id));
    setForm((current) => {
      const next = { ...current, nodes: nextNodes };
      if (removed.length > 0) {
        for (const key of ["node_multipliers", "node_name_overrides", "node_speed_limits", "node_device_limits", "node_traffic_limits"] as const) {
          const values = { ...next[key] } as Record<number, number | string>;
          removed.forEach((id) => { delete values[id]; });
          (next as unknown as Record<string, unknown>)[key] = values;
        }
      }
      if (nextNodes.length > 0 && current.nodes.length === 0 && !resetTouched) {
        const node = nodes.find((item) => item.id === nextNodes[0]);
        const server = serverByName.get(node?.original_server ?? "");
        const day = Number(server?.traffic_reset_day);
        if (Number.isInteger(day) && day >= 1 && day <= 31) next.reset_day = day;
      }
      return next;
    });
    const allowed = nextNodes.length > 0 ? nextNodes : nodes.map((node) => node.id).filter((id) => !removed.includes(id));
    setGroups((current) => retainTrafficGroupNodes(current, allowed));
  }

  function setSelected(node: XrayNode, selected: boolean) {
    setSelectedNodes(selected ? [...form.nodes, node.id] : form.nodes.filter((id) => id !== node.id));
  }

  function toggleVisible() {
    const visibleIds = visibleNodes.map((node) => node.id);
    const allSelected = visibleIds.length > 0 && visibleIds.every((id) => form.nodes.includes(id));
    setSelectedNodes(allSelected ? form.nodes.filter((id) => !visibleIds.includes(id)) : [...new Set([...form.nodes, ...visibleIds])]);
  }

  function patchGroup(index: number, values: Partial<PackageTrafficGroupDraft>) {
    setGroups((current) => current.map((group, itemIndex) => itemIndex === index ? { ...group, ...values } : group));
  }

  function memberWarnings(node: XrayNode) {
    const known = groupNodes.filter((item) => item.node_id === node.id);
    const server = serverByName.get(node.original_server ?? "");
    if (!server) known.push({ node_id: node.id, node_name: node.node_name, server_name: "", status: "external_node" });
    else if (server.xray_mode === "embedded") known.push({ node_id: node.id, node_name: node.node_name, server_name: server.name, status: "embedded" });
    return [...new Set(known.map(trafficGroupNodeWarning).filter(Boolean))];
  }

  function moveNode(index: number, offset: number) {
    const target = index + offset;
    if (target < 0 || target >= form.nodes.length) return;
    const next = [...form.nodes];
    [next[index], next[target]] = [next[target], next[index]];
    patch({ nodes: next });
  }

  function setNodeMap(key: "node_multipliers" | "node_name_overrides" | "node_speed_limits" | "node_device_limits" | "node_traffic_limits", nodeId: number, value: string, parser: (input: string) => number | string) {
    setForm((current) => {
      const next = { ...current[key] } as Record<number, number | string>;
      if (value === "") delete next[nodeId];
      else next[nodeId] = parser(value);
      return { ...current, [key]: next };
    });
  }

  function validate() {
    if (!form.name.trim()) return "请输入套餐名称";
    if (!(form.traffic_limit_gb > 0)) return "流量额度必须大于 0 GB";
    if (!(form.cycle_days > 0)) return "套餐周期必须大于 0 天";
    if (form.is_reset && (form.reset_day < 1 || form.reset_day > 31)) return "月度重置日必须在 1 到 31 之间";
    for (const [nodeId, limit] of Object.entries(form.node_traffic_limits)) {
      if (Number(limit) < 0) return `节点 ${nodeId} 的流量额度不能小于 0`;
      if (form.traffic_limit_gb > 0 && Number(limit) > form.traffic_limit_gb) return `节点 ${nodeId} 的流量额度不能超过套餐总额度`;
    }
    const names = Object.values(form.node_name_overrides).map((value) => value.trim()).filter(Boolean);
    if (new Set(names).size !== names.length) return "套餐内节点名称不能重复";
    return validateTrafficGroups(groups, form.traffic_limit_gb, memberNodes.map((node) => node.id), form.node_traffic_limits);
  }

  async function save() {
    if (saving || groupsLoading || groupsError || creationUnresolved) return;
    const validation = validate();
    if (validation) { setError(validation); return; }
    setSaving(true);
    setError("");
    let packageSaved = false;
    try {
      const payload = { ...form, name: form.name.trim(), description: form.description.trim() };
      let packageId = savedPackageId;
      if (packageId) await updatePackage(token, { ...payload, id: packageId });
      else {
        const result = await createPackage(token, payload);
        packageId = result.id;
        if (!packageId) {
          setCreationUnresolved(true);
          throw new Error("套餐已创建，但未返回套餐编号。请关闭编辑器并刷新套餐列表后继续配置共享组，避免重复创建");
        }
        setSavedPackageId(packageId);
      }
      packageSaved = true;
      const result = await savePackageTrafficGroups(token, packageId, groups.map(trafficGroupPayload));
      setGroups((result.groups ?? []).map(trafficGroupDraft));
      await onSaved(pkg ? "套餐已更新并重新读取" : "套餐已创建并重新读取");
    } catch (reason) {
      setError(`${packageSaved ? "套餐已保存，共享组保存或刷新失败，可重试保存。" : ""}${messageOf(reason, "保存套餐失败")}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="package-dialog-layer" role="presentation" onMouseDown={() => { if (!saving) onClose(); }}>
      <section className="package-editor" role="dialog" aria-modal="true" aria-label={pkg ? "编辑套餐" : "创建套餐"} onMouseDown={(event) => event.stopPropagation()}>
        <header><div><h2>{pkg ? "编辑套餐" : "创建套餐"}</h2><p>配置套餐额度、节点、模板、限速与转发权限</p></div><button type="button" disabled={saving} onClick={onClose} aria-label="关闭"><X /></button></header>
        <div className="package-editor-body">
          {error && <div className="package-notice error"><span>{error}</span></div>}
          <fieldset className="package-form-section">
            <legend>基础设置</legend>
            <div className="package-form-grid">
              <label className="wide"><span>套餐名称 *</span><input value={form.name} onChange={(event) => patch({ name: event.target.value })} maxLength={128} /></label>
              <label className="wide"><span>套餐说明</span><textarea rows={3} value={form.description} onChange={(event) => patch({ description: event.target.value })} /></label>
              <NumberField label="流量额度（GB）*" value={form.traffic_limit_gb} min={0.01} step={0.01} onChange={(value) => patch({ traffic_limit_gb: value })} />
              <NumberField label="套餐周期（天）*" value={form.cycle_days} min={1} step={1} onChange={(value) => patch({ cycle_days: value })} />
              <label><span>流量统计方式</span><select value={form.traffic_mode} onChange={(event) => patch({ traffic_mode: event.target.value })}><option value="oneway">单向统计</option><option value="twoway">双向统计</option></select></label>
              <label><span>Clash 模板</span><select value={form.template_filename} onChange={(event) => patch({ template_filename: event.target.value })}><option value="">系统默认</option>{clashTemplates.map((item) => <option key={item.filename} value={item.filename}>{item.name || item.filename}</option>)}</select></label>
              <label><span>Surge 模板</span><select value={form.surge_template_filename} onChange={(event) => patch({ surge_template_filename: event.target.value })}><option value="">系统默认</option>{surgeTemplates.map((item) => <option key={item.filename} value={item.filename}>{item.name || item.filename}</option>)}</select></label>
            </div>
          </fieldset>

          <fieldset className="package-form-section">
            <legend>月度流量重置</legend>
            <label className="package-switch"><div><strong>按月重置流量</strong><small>关闭后只按套餐周期计算</small></div><input type="checkbox" checked={form.is_reset} onChange={(event) => patch({ is_reset: event.target.checked })} /></label>
            {form.is_reset && <div className="package-form-grid"><NumberField label="每月重置日" value={form.reset_day} min={1} max={31} step={1} onChange={(value) => { setResetTouched(true); patch({ reset_day: value }); }} /><p className="package-field-help">新套餐会优先采用第一个已选内部节点所属服务器的流量重置日。</p></div>}
          </fieldset>

          <fieldset className="package-form-section">
            <legend>全局限制</legend>
            <div className="package-form-grid">
              <NumberField label="速度限制（Mbps）" value={form.speed_limit_mbps} min={0} step={0.1} onChange={(value) => patch({ speed_limit_mbps: value })} hint="0 表示不限速" />
              <NumberField label="连接数限制" value={form.device_limit} min={0} step={1} onChange={(value) => patch({ device_limit: value })} hint="每用户最大并发连接数；0 表示不限制。连接数通常远大于设备数，请重新评估数值。" />
            </div>
          </fieldset>

          <fieldset className="package-form-section">
            <legend>转发配额</legend>
            <label className="package-switch"><div><strong>允许套餐用户创建转发</strong><small>开启后可设置规则、端口、速度、连接数和可用转发链</small></div><input type="checkbox" checked={forwardEnabled} onChange={(event) => patch(event.target.checked ? { forward_rule_limit: Math.max(1, form.forward_rule_limit) } : { forward_rule_limit: 0, forward_port_limit: 0, forward_speed_mbps: 0, forward_conn_limit: 0, forward_chains: [] })} /></label>
            {forwardEnabled && <>
              <div className="package-form-grid">
                <NumberField label="转发规则数" value={form.forward_rule_limit} min={1} step={1} onChange={(value) => patch({ forward_rule_limit: value })} />
                <NumberField label="端口数量限制" value={form.forward_port_limit} min={0} step={1} onChange={(value) => patch({ forward_port_limit: value })} hint="0 表示不限" />
                <NumberField label="转发速度（Mbps）" value={form.forward_speed_mbps} min={0} step={0.1} onChange={(value) => patch({ forward_speed_mbps: value })} hint="0 表示不限" />
                <NumberField label="并发连接数" value={form.forward_conn_limit} min={0} step={1} onChange={(value) => patch({ forward_conn_limit: value })} hint="0 表示不限" />
              </div>
              <div className="package-chain-list"><div><strong>允许使用的转发链</strong><span>{form.forward_chains.length}/{chains.length}</span></div>{chains.length === 0 ? <p>还没有转发链。请先在转发管理中创建。</p> : chains.map((chain) => <label key={chain.id}><input type="checkbox" checked={form.forward_chains.includes(chain.id)} onChange={(event) => patch({ forward_chains: event.target.checked ? [...form.forward_chains, chain.id] : form.forward_chains.filter((id) => id !== chain.id) })} /><span>{chain.name}</span><small>{chain.hops?.length ?? 0} 跳</small></label>)}</div>
            </>}
          </fieldset>

          <fieldset className="package-form-section package-node-picker" disabled={groupsLoading || Boolean(groupsError) || saving}>
            <legend>关联节点与单节点覆盖</legend>
            <p className="package-section-note">不选择节点表示使用全部节点。已选顺序就是订阅中的节点顺序。</p>
            <div className="package-node-controls"><label><span>按标签筛选</span><select value={tag} onChange={(event) => setTag(event.target.value)}><option value="all">全部标签（{nodes.length}）</option>{tags.map((item) => <option key={item} value={item}>{item}</option>)}</select></label><button type="button" onClick={toggleVisible}>{visibleNodes.length > 0 && visibleNodes.every((node) => form.nodes.includes(node.id)) ? "取消当前标签" : "选择当前标签"}</button></div>
            <div className="package-node-options">
              {visibleNodes.map((node) => <label className={form.nodes.includes(node.id) ? "selected" : ""} key={node.id}><input type="checkbox" checked={form.nodes.includes(node.id)} onChange={(event) => setSelected(node, event.target.checked)} /><div><strong>{node.node_name}</strong><span>{node.original_server || "外部节点"} · {node.protocol || "未知协议"}</span></div>{node.inbound_tag ? <small>{node.inbound_tag}</small> : <small className="warn">无入站标识</small>}</label>)}
            </div>
            {selectedNodes.length > 0 && <div className="package-selected-nodes">
              <label className="package-switch"><div><strong>启用套餐内节点名称</strong><small>开启后订阅使用下面填写的覆盖名称</small></div><input type="checkbox" checked={form.node_name_override_enabled} onChange={(event) => patch({ node_name_override_enabled: event.target.checked })} /></label>
              {selectedNodes.map((node, index) => <article key={node.id}>
                <header><div><strong>{index + 1}. {node.node_name}</strong><span>{node.original_server || "外部节点"}</span>{groups.filter((group) => group.node_ids.includes(node.id)).map((group) => <span className="package-group-badge" key={group.id ?? groups.indexOf(group)}>共享组：{group.name.trim() || "未命名"}</span>)}</div><div><button type="button" disabled={index === 0} onClick={() => moveNode(index, -1)} aria-label="上移"><ArrowUp /></button><button type="button" disabled={index === selectedNodes.length - 1} onClick={() => moveNode(index, 1)} aria-label="下移"><ArrowDown /></button></div></header>
                <div className="package-node-overrides">
                  <label><span>套餐内名称</span><input value={form.node_name_overrides[node.id] ?? ""} disabled={!form.node_name_override_enabled} placeholder="沿用原节点名称" onChange={(event) => setNodeMap("node_name_overrides", node.id, event.target.value, String)} /></label>
                  <label><span>流量倍率</span><input type="number" min="0.01" step="0.01" value={form.node_multipliers[node.id] ?? ""} placeholder="1" onChange={(event) => setNodeMap("node_multipliers", node.id, event.target.value, Number)} /></label>
                  <label><span>速度覆盖（Mbps）</span><input type="number" min="0" step="0.1" value={form.node_speed_limits[node.id] ?? ""} placeholder="继承全局" onChange={(event) => setNodeMap("node_speed_limits", node.id, event.target.value, Number)} /></label>
                  <label><span>节点连接数</span><input type="number" min="0" step="1" value={form.node_device_limits[node.id] ?? ""} placeholder="沿用套餐通用值" onChange={(event) => setNodeMap("node_device_limits", node.id, event.target.value, Number)} /><small>留空沿用套餐通用值；0 表示不限。路由出站节点继承父节点。</small></label>
                  <label><span>节点流量额度（GB）</span><input type="number" min="0" max={form.traffic_limit_gb || undefined} step="0.01" value={form.node_traffic_limits[node.id] ?? ""} placeholder="不限" onChange={(event) => setNodeMap("node_traffic_limits", node.id, event.target.value, Number)} /></label>
                </div>
              </article>)}
            </div>}
          </fieldset>

          <fieldset className="package-form-section" disabled={groupsLoading || Boolean(groupsError) || saving}>
            <legend>流量共享组</legend>
            <p className="package-section-note">每个用户在当前套餐周期内共用组内额度，达到额度后仅拦截该组节点。套餐总额度、共享组额度与单节点额度同时生效。</p>
            {groupsLoading ? <p className="package-field-help">正在读取共享组与执行状态...</p> : groups.length === 0 && <p className="package-field-help">尚未设置流量共享组。</p>}
            {capabilityError && <p className="package-group-warning">{capabilityError}</p>}
            <div className="package-traffic-groups">
              {groups.map((group, index) => <article className="package-traffic-group" key={group.id ?? `new-${index}`}>
                <header><strong>共享组 {index + 1}</strong><button type="button" onClick={() => setGroups((current) => current.filter((_, itemIndex) => itemIndex !== index))} aria-label={`移除共享组 ${group.name || index + 1}`}><Trash2 />移除</button></header>
                <div className="package-form-grid">
                  <label><span>组名称 *</span><input value={group.name} maxLength={100} onChange={(event) => patchGroup(index, { name: event.target.value })} /></label>
                  <NumberField label="共享额度（GB）*" value={group.limit_gb} min={0.01} max={form.traffic_limit_gb} step={0.01} onChange={(value) => patchGroup(index, { limit_gb: value })} />
                </div>
                <p className="package-field-help">成员节点（{group.node_ids.length}）：{form.nodes.length === 0 ? "套餐未指定节点，可从全部节点中选择" : "从当前关联节点中选择"}；每个节点只能属于一个共享组。</p>
                <div className="package-group-members">
                  {memberNodes.length === 0 && <p className="package-field-help">暂无可选节点</p>}
                  {memberNodes.map((node) => {
                    const other = groups.find((item, itemIndex) => itemIndex !== index && item.node_ids.includes(node.id));
                    const warnings = memberWarnings(node);
                    return <label className={other ? "unavailable" : ""} key={node.id}>
                      <input type="checkbox" checked={group.node_ids.includes(node.id)} disabled={Boolean(other)} onChange={(event) => patchGroup(index, { node_ids: event.target.checked ? [...group.node_ids, node.id] : group.node_ids.filter((id) => id !== node.id) })} />
                      <div><strong>{node.node_name}</strong><span>{node.original_server || "外部节点"}</span>{other && <small>已属于共享组：{other.name.trim() || "未命名"}</small>}{warnings.map((warning) => <small className="package-group-warning" key={warning}>{warning}</small>)}</div>
                    </label>;
                  })}
                </div>
              </article>)}
            </div>
            <button type="button" onClick={() => setGroups((current) => [...current, { name: "", limit_gb: form.traffic_limit_gb, node_ids: [] }])}><Plus />添加共享组</button>
            <p className="package-field-help">流量采集和检查存在数分钟延迟。新建或修改成员后，请在“共享组用量”中确认每个节点的执行状态。</p>
          </fieldset>
          {groupsError && <div className="package-notice error"><span>读取共享组失败，暂时不能保存套餐：{groupsError}</span><button type="button" onClick={() => void loadGroups()}>重试</button></div>}
        </div>
        <footer><button type="button" disabled={saving} onClick={onClose}>取消</button><button className="primary" type="button" disabled={saving || groupsLoading || Boolean(groupsError) || creationUnresolved} onClick={() => void save()}><Save />{saving ? "保存中..." : "保存套餐"}</button></footer>
      </section>
    </div>
  );
}

function PackageTrafficUsageDialog({ token, pkg, onClose }: { token: string; pkg: ManagedPackage; onClose: () => void }) {
  const [data, setData] = useState<PackageTrafficGroupUsageResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try { setData(await fetchPackageTrafficGroupUsage(token, pkg.id)); }
    catch (reason) { setError(messageOf(reason, "读取共享组用量失败")); }
    finally { setLoading(false); }
  }, [token, pkg.id]);

  useEffect(() => { void load(); }, [load]);

  function nodeStatus(node: PackageTrafficGroupNode) {
    const warning = trafficGroupNodeWarning(node);
    return <li key={node.node_id} className={warning ? "package-group-warning" : ""}><strong>{node.node_name || `节点 ${node.node_id}`}</strong><span>{node.server_name || "外部节点"} · {warning || "可执行共享组额度"}</span></li>;
  }

  return <div className="package-dialog-layer" role="presentation" onMouseDown={onClose}>
    <section className="package-editor" role="dialog" aria-modal="true" aria-label="共享组用量" onMouseDown={(event) => event.stopPropagation()}>
      <header><div><h2>共享组用量</h2><p>{pkg.name} · 按用户当前套餐周期统计</p></div><button type="button" onClick={onClose} aria-label="关闭"><X /></button></header>
      <div className="package-editor-body">
        <p className="package-section-note">流量采集和额度检查可能延迟数分钟。未具备执行能力的节点会继续产生流量，请检查下方状态。</p>
        {error && <div className="package-notice error"><span>{error}</span></div>}
        {loading ? <p className="package-field-help">正在读取共享组用量...</p> : !error && (data?.usage?.length ? data.usage.map((row) => {
          const percent = row.limit_bytes > 0 ? Math.min(100, Math.max(0, row.used_bytes / row.limit_bytes * 100)) : 0;
          return <article className={`package-group-usage${row.blocked ? " blocked" : ""}`} key={`${row.assignment_id}-${row.username}-${row.group_id}`}>
            <header><div><strong>{row.username}</strong><span>{row.group_name}</span></div>{row.blocked && <span className="package-group-blocked">已拦截</span>}</header>
            <div className="package-group-usage-amount"><strong>{formatNumber(row.used_bytes / TRAFFIC_GB)} / {formatNumber(row.limit_bytes / TRAFFIC_GB)} GB</strong><span>{formatNumber(row.limit_bytes > 0 ? row.used_bytes / row.limit_bytes * 100 : 0)}%</span></div>
            <progress max={100} value={percent} aria-label={`${row.username} · ${row.group_name} 用量`} />
            {row.blocked && row.nodes?.some((node) => node.status !== "enforced") && <p className="package-group-warning">部分节点无法执行拦截，请查看节点提醒。</p>}
            <p className="package-field-help">周期：{formatCycleDate(row.cycle_start)} 至 {formatCycleDate(row.cycle_end)}</p>
            <ul className="package-group-node-status">{(row.nodes ?? []).map(nodeStatus)}</ul>
          </article>;
        }) : <p className="package-field-help">暂无绑定用户的共享组用量。</p>)}
        {!loading && !error && data?.nodes?.some((node) => node.status !== "enforced") && <div className="package-form-section"><strong>节点执行提醒</strong><ul className="package-group-node-status">{data.nodes.filter((node) => node.status !== "enforced").map(nodeStatus)}</ul></div>}
      </div>
      <footer><button type="button" onClick={onClose}>关闭</button><button type="button" disabled={loading} onClick={() => void load()}><RefreshCw />刷新用量</button></footer>
    </section>
  </div>;
}

function formatCycleDate(value: string | null) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString("zh-CN");
}

function NumberField({ label, value, min, max, step, hint, onChange }: { label: string; value: number; min?: number; max?: number; step?: number; hint?: string; onChange: (value: number) => void }) {
  return <label><span>{label}</span><input type="number" value={Number.isFinite(value) ? value : 0} min={min} max={max} step={step} onChange={(event) => onChange(Number(event.target.value))} />{hint && <small>{hint}</small>}</label>;
}


function templateName(templates: PackageTemplate[], filename?: string) {
  if (!filename) return "系统默认";
  return templates.find((item) => item.filename === filename)?.name || filename;
}

function formatNumber(value: number) {
  return Number(value).toLocaleString("zh-CN", { maximumFractionDigits: 2 });
}

function messageOf(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}
