import assert from "node:assert/strict";
import test from "node:test";
import { remoteServerEditChanges, remoteServerUpdatePayload } from "../src/server-update.ts";
import type { RemoteServerUpdateRequest } from "../src/types.ts";

test("mode changes retain DDNS, IPv6 pull, ports, hidden pull token and unknown fields", () => {
  const server = {
    id: 1, name: "fixture", xray_mode: "embedded", ddns_enabled: true, ddns_provider_id: 3,
    pull_address: "test.example", pull_address_v6: "2001:db8::7", pull_port: 7777, listen_port: 12345,
    expires_at: "2027-03-18T00:00:00Z", notify_muted: true, auto_renew: true,
    provider_name: "keep", site_value: "keep", future_field: "keep", traffic_used: 500, token: "", agent_token: "",
  };
  for (const mode of ["embedded", "external"]) {
    const payload = remoteServerUpdatePayload(server, { xray_mode: mode }, "hidden-pull-token");
    assert.deepEqual(payload, {
      ...Object.fromEntries(Object.entries(server).filter(([key]) => !["token", "agent_token", "traffic_used"].includes(key))),
      expires_at: "2027-03-18", xray_mode: mode, pull_token: "hidden-pull-token",
    });
  }
  assert.equal(server.expires_at, "2027-03-18T00:00:00Z");
});

test("a mode-only edit does not write rounded traffic, fallback address or default expiry", () => {
  const baseline = { id: 1, traffic_limit: 122997796864, pull_address: "203.0.113.9", expires_at: "2027-01-01", xray_mode: "embedded" } as RemoteServerUpdateRequest;
  const changes = remoteServerEditChanges({ ...baseline, xray_mode: "external" }, baseline);
  assert.deepEqual(changes, { id: 1, xray_mode: "external" });
  const payload = remoteServerUpdatePayload({ id: 1, name: "fixture", traffic_limit: 123000000000, pull_address: "", expires_at: null }, changes, "secret");
  assert.equal(payload.traffic_limit, 123000000000);
  assert.equal(payload.pull_address, "");
  assert.equal(payload.expires_at, null);
});

test("explicit traffic edits and empty IPv6 addresses remain intentional changes", () => {
  const baseline = { id: 1, traffic_used: undefined, pull_address_v6: "2001:db8::7" } as RemoteServerUpdateRequest;
  const changes = remoteServerEditChanges({ ...baseline, traffic_used: 0, pull_address_v6: "" }, baseline);
  const payload = remoteServerUpdatePayload({ id: 1, name: "fixture", traffic_used: 99 }, changes, "secret");
  assert.equal(payload.traffic_used, 0);
  assert.equal(payload.pull_address_v6, "");
});
