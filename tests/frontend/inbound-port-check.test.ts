import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { checkInboundPort, inboundListenProtocols, listeningUnavailableMessage, listeningUnreachable, pickAvailablePort, unavailablePorts, type ListeningPortsResponse } from "../../frontend/src/inbound-port-check.ts";
import type { XrayObject } from "../../frontend/src/types.ts";

// 「Boil HKT 99u」(2) on 2026-10-28 as Helper v0.7.14 reports it: the External
// Core serves 10015/10016/10018 (+ loopback API/metrics), LeiKwan's backup
// lkbk-core holds 10017 TCP+UDP, plus sshd, the official Agent and kernel WireGuard.
const report: ListeningPortsResponse = {
  success: true, available: true, supported: true, helper_version: "v0.7.14",
  listening: {
    scanned_at: "2026-10-28T06:10:00Z",
    ports: [
      { port: 22, protocol: "tcp", address: "*", pid: 521, process: "sshd" },
      { port: 10015, protocol: "tcp", address: "*", pid: 231078, process: "xray", owner: "core", inbound_tag: "wings ss" },
      { port: 10016, protocol: "tcp", address: "*", pid: 231078, process: "xray", owner: "core", inbound_tag: "自己用 ss-10016" },
      { port: 10016, protocol: "udp", address: "*", pid: 231078, process: "xray", owner: "core", inbound_tag: "自己用 ss-10016" },
      { port: 10017, protocol: "tcp", address: "*", pid: 212883, process: "lkbk-core" },
      { port: 10017, protocol: "udp", address: "*", pid: 212883, process: "lkbk-core" },
      { port: 10018, protocol: "tcp", address: "*", pid: 231078, process: "xray", owner: "core", inbound_tag: "king ss-10017" },
      { port: 10018, protocol: "udp", address: "*", pid: 231078, process: "xray", owner: "core", inbound_tag: "king ss-10017" },
      { port: 23889, protocol: "tcp", address: "127.0.0.1", pid: 475, process: "mmw-agent", owner: "agent" },
      { port: 38889, protocol: "tcp", address: "127.0.0.1", pid: 231078, process: "xray", owner: "core" },
      { port: 40100, protocol: "udp", address: "*", pid: 6001, process: "hy-test" },
      { port: 51820, protocol: "udp", address: "*" },
    ],
  },
};
const usedPorts = [10015, 10016, 10018, 46736];
const ss2022 = (port: number, extra: XrayObject = {}): XrayObject => ({ tag: "king ss-new", protocol: "shadowsocks", port, settings: { method: "2022-blake3-aes-128-gcm", network: "tcp,udp", clients: [] }, streamSettings: { network: "tcp" }, ...extra });
const vless = (port: number, extra: XrayObject = {}): XrayObject => ({ tag: "vless-new", protocol: "vless", port, streamSettings: { network: "tcp", security: "reality" }, ...extra });
const create = { originalPort: 0, usedPorts };

test("a new inbound on a port another program holds is flagged with the process and blocks saving", () => {
  for (const item of [ss2022(10017), vless(10017)]) {
    assert.deepEqual(checkInboundPort(report, item, create), { kind: "machine", blocking: true, message: "端口 10017 已被本机进程 lkbk-core (PID 212883) 占用" });
  }
  // Our own Core's loopback metrics port is still a busy machine port.
  assert.equal(checkInboundPort(report, vless(38889), create).message, "端口 38889 已被本机进程 xray (PID 231078) 占用");
  assert.equal(checkInboundPort(report, vless(22), create).message, "端口 22 已被本机进程 sshd (PID 521) 占用");
  // A kernel socket has no process.
  assert.deepEqual(checkInboundPort(report, { protocol: "wireguard", port: 51820 }, create), { kind: "machine", blocking: true, message: "端口 51820 已被本机占用（系统内核或已退出的进程）" });
});

test("ports of other inbounds keep the existing message; free ports pass", () => {
  assert.deepEqual(checkInboundPort(report, ss2022(10016), create), { kind: "inbound", blocking: true, message: "端口 10016 已被其他入站占用" });
  // Without the config list (another server of a multi-server create) the Helper names the inbound.
  assert.deepEqual(checkInboundPort(report, ss2022(10016), { originalPort: 0, usedPorts: [] }), { kind: "inbound", blocking: true, message: "端口 10016 已被其他入站（自己用 ss-10016）占用" });
  assert.deepEqual(checkInboundPort(report, ss2022(20000), create), { kind: "free", blocking: false, message: "" });
  assert.equal(checkInboundPort(report, ss2022(0), create).kind, "free");
});

test("editing keeps the inbound's own port but not a port shared with a foreign process", () => {
  const king = ss2022(10018, { tag: "king ss-10017" });
  assert.equal(checkInboundPort(report, king, { originalPort: 10018, usedPorts }).kind, "free");
  assert.equal(checkInboundPort(report, { ...king, port: 10017 }, { originalPort: 10018, usedPorts }).message, "端口 10017 已被本机进程 lkbk-core (PID 212883) 占用");
  assert.equal(checkInboundPort(report, { ...king, port: 10016 }, { originalPort: 10018, usedPorts }).message, "端口 10016 已被其他入站占用");
  // The 10-25 state: the inbound itself on 10017 next to lkbk-core is still a conflict.
  const shared = { ...report, listening: { ports: [...report.listening!.ports, { port: 10017, protocol: "tcp" as const, address: "*", pid: 231078, process: "xray", owner: "core" as const, inbound_tag: "king ss-10017" }] } };
  assert.deepEqual(checkInboundPort(shared, { ...king, port: 10017 }, { originalPort: 10017, usedPorts: [...usedPorts, 10017] }), { kind: "machine", blocking: true, message: "端口 10017 已被本机进程 lkbk-core (PID 212883) 占用" });
});

test("TCP and UDP are judged per protocol, and binds on different addresses do not collide", () => {
  assert.deepEqual(inboundListenProtocols(ss2022(1)), { tcp: true, udp: true });
  assert.deepEqual(inboundListenProtocols(ss2022(1, { settings: { network: "tcp" } })), { tcp: true, udp: false });
  assert.deepEqual(inboundListenProtocols({ protocol: "shadowsocks" }), { tcp: true, udp: true });
  assert.deepEqual(inboundListenProtocols({ protocol: "hysteria", streamSettings: { network: "hysteria" } }), { tcp: false, udp: true });
  assert.deepEqual(inboundListenProtocols({ protocol: "mieru", settings: { transport: "tcp" } }), { tcp: true, udp: true });
  assert.deepEqual(inboundListenProtocols({ protocol: "tunnel", settings: { network: "tcp,udp" } }), { tcp: true, udp: true });
  assert.deepEqual(inboundListenProtocols({ protocol: "socks", settings: { udp: true } }), { tcp: true, udp: true });
  // A UDP-only holder blocks SS (tcp,udp) and Hysteria2 but not VLESS over TCP or SS narrowed to TCP.
  assert.equal(checkInboundPort(report, ss2022(40100), create).message, "端口 40100 已被本机进程 hy-test (PID 6001) 占用");
  assert.equal(checkInboundPort(report, { protocol: "hysteria", port: 40100, streamSettings: { network: "hysteria" } }, create).kind, "machine");
  assert.equal(checkInboundPort(report, vless(40100), create).kind, "free");
  assert.equal(checkInboundPort(report, ss2022(40100, { settings: { network: "tcp" } }), create).kind, "free");
  // 127.0.0.1:38889 collides with a wildcard or loopback bind, not with another concrete address.
  assert.equal(checkInboundPort(report, vless(38889, { listen: "0.0.0.0" }), create).kind, "machine");
  assert.equal(checkInboundPort(report, vless(38889, { listen: "127.0.0.1" }), create).kind, "machine");
  assert.equal(checkInboundPort(report, vless(38889, { listen: "10.0.0.2" }), create).kind, "free");
  assert.equal(checkInboundPort(report, vless(10017, { listen: "10.0.0.2" }), create).kind, "machine");
  assert.equal(checkInboundPort(report, vless(10017, { listen: "/dev/shm/vless.sock" }), create).kind, "free");
});

test("a missing, old, stale or failed Helper report never blocks and says so neutrally", () => {
  const unavailable = { kind: "unavailable", blocking: false, message: listeningUnavailableMessage };
  assert.equal(listeningUnavailableMessage, "无法检测本机端口占用（Helper 未上报/需升级）");
  for (const value of [null, undefined, listeningUnreachable, { available: true, supported: false, helper_version: "v0.7.13", listening: null }, { available: false, supported: false, helper_version: "v0.7.14" }, { available: true, supported: true, listening: { ports: [], error: "no /proc/net socket tables are readable" } }]) {
    assert.deepEqual(checkInboundPort(value as ListeningPortsResponse, ss2022(10017), create), unavailable);
  }
  // The config check still applies without the Helper.
  assert.equal(checkInboundPort(listeningUnreachable, ss2022(10016), create).message, "端口 10016 已被其他入站占用");
});

test("随机 skips other inbounds and every busy machine port", () => {
  const busy = unavailablePorts(report, usedPorts, 10018);
  for (const port of [10015, 10016, 10017, 22, 38889, 40100, 51820, 46736]) assert.ok(busy.has(port), `port ${port} must be skipped`);
  assert.ok(!busy.has(10018), "the edited inbound's own port stays available");
  assert.ok(unavailablePorts(listeningUnreachable, usedPorts, 0).has(10016));
  const none = new Set(Array.from({ length: 22768 }, (_, index) => index + 10000));
  none.delete(10017);
  assert.equal(pickAvailablePort(none, () => 0), 10017);
  none.add(10017);
  assert.equal(pickAvailablePort(none, () => 0.5), 0);
  for (let draw = 0; draw < 200; draw++) {
    const port = pickAvailablePort(busy);
    assert.ok(port >= 10000 && port <= 32767 && !busy.has(port));
  }
});

test("every Custom form that creates or edits an inbound port runs the machine check on save", () => {
  const editor = readFileSync(new URL("../../frontend/src/xray-manager.tsx", import.meta.url), "utf8");
  const tools = readFileSync(new URL("../../frontend/src/node-manager-tools.tsx", import.meta.url), "utf8");
  // ObjectEditor (Xray 管理 create/edit + node page create/edit) re-reads the report before saving.
  assert.match(editor, /checkInboundPort\(await listening\.refresh\(\), next, \{ originalPort, usedPorts \}\)/);
  // A multi-server create checks every further server against its own inbounds.
  assert.match(editor, /fetchListeningPorts\(token, target\.id\)[\s\S]{0,400}checkInboundPort\(report, next, \{ originalPort: 0, usedPorts:/);
  // 端口转发 (tunnel) creation checks before its write.
  assert.match(tools, /checkInboundPort\(report, inbound,[\s\S]{0,200}if \(check\.blocking\) throw new Error\(check\.message\);\s*await mutateRemoteInbound\(token, sid, \{ action: "add", inbound \}\)/);
});
