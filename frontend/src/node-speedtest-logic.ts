import type { SpeedTestResult, XrayNode } from "./types";

export const SPEED_TEST_TIMEOUT = 15_000;
export const SPEED_TEST_PRO_REQUIRED = "节点测速是 PRO 功能,请升级许可证";

export function speedTestState(result?: SpeedTestResult, now = Date.now()): "idle" | "running" | "timeout" | "failed" | "ok" {
  if (!result) return "idle";
  if (result.status === "running") {
    const started = result.created_at ? new Date(result.created_at).getTime() : 0;
    return started && now - started > SPEED_TEST_TIMEOUT ? "timeout" : "running";
  }
  return result.status === "failed" ? "failed" : "ok";
}

export function speedTestError(error: unknown) {
  const message = error instanceof Error ? error.message : String(error || "");
  return /(?:PRO|许可证|HTTP 403)/i.test(message) ? SPEED_TEST_PRO_REQUIRED : message || "测速失败";
}

export function speedTestLatest(results: SpeedTestResult[]) {
  const latest = new Map<number, SpeedTestResult>();
  for (const result of results) {
    if (result.node_id == null) continue;
    const previous = latest.get(result.node_id);
    if (!previous || new Date(result.created_at || 0).getTime() >= new Date(previous.created_at || 0).getTime()) latest.set(result.node_id, result);
  }
  return latest;
}

export function sortSpeedTestResults(results: SpeedTestResult[], sort: "time" | "speed" | "latency") {
  return [...results].sort((left, right) => {
    if (sort === "speed") return (Number(right.down_mbps) || 0) - (Number(left.down_mbps) || 0);
    if (sort === "latency") {
      const latency = (result: SpeedTestResult) => result.status === "ok" && typeof result.latency_ms === "number" && result.latency_ms >= 0 ? result.latency_ms : Infinity;
      const difference = latency(left) - latency(right);
      if (difference && Number.isFinite(difference)) return difference;
      if (latency(left) !== latency(right)) return latency(left) < latency(right) ? -1 : 1;
    }
    return new Date(right.created_at || 0).getTime() - new Date(left.created_at || 0).getTime();
  });
}

export function speedTestNodeTags(node: XrayNode) {
  return node.tags?.length ? node.tags : node.tag ? [node.tag] : [];
}

export function filterSpeedTestNodes(nodes: XrayNode[], protocols: Set<string>, tags: Set<string>) {
  return nodes.filter((node) => (!protocols.size || protocols.has(node.protocol || "")) && (!tags.size || speedTestNodeTags(node).some((tag) => tags.has(tag))));
}

export function toggleVisibleSpeedTestNodes(selected: Set<number>, visible: number[]) {
  const next = new Set(selected);
  const remove = visible.length > 0 && visible.every((id) => next.has(id));
  for (const id of visible) remove ? next.delete(id) : next.add(id);
  return next;
}

export function speedTesterCommands(origin: string, token: string, name: string) {
  const scripts = "https://raw.githubusercontent.com/mmwx-group/mmwX-plugins/refs/heads/main/speedtest/scripts";
  const shellQuote = (value: string) => "'" + value.replace(/'/g, "'\\''") + "'";
  const powershellQuote = (value: string) => "'" + value.replace(/'/g, "''") + "'";
  return [
    { label: "Linux / macOS 一键运行", command: `curl -fsSL ${scripts}/install.sh | bash -s -- -master ${shellQuote(origin)} -token ${shellQuote(token)}` },
    { label: "Windows PowerShell 一键运行", command: `irm ${scripts}/install.ps1 -OutFile install.ps1; .\\install.ps1 -Master ${powershellQuote(origin)} -Token ${powershellQuote(token)}` },
    { label: "Docker 一键启动", command: `docker run -d --name mmwx-speedtester --restart unless-stopped -e ${shellQuote(`MMWX_MASTER=${origin}`)} -e ${shellQuote(`MMWX_SPEEDTEST_TOKEN=${token}`)} -e ${shellQuote(`MMWX_SPEEDTEST_NAME=${name}`)} -v mmwx-speedtester-data:/data ghcr.io/mmwx-group/mmwx-speedtester:latest` },
  ];
}
