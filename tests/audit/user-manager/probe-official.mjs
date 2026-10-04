// Explicit opt-in local v0.5.5 harness probe; never imported by the default tests.
// Requires the retained mmwx-test-pg/mmwx-test-official harness and its secure client.
import fs from 'node:fs';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createClient } from '/root/mmwx-custom-artifacts/official-sync-v0.5.5/sc-client.mjs';

const dir = '/root/mmwx-custom-artifacts/user-manager-audit/official';
const base = 'http://127.0.0.1:22889';
if (process.env.AUDIT_OFFICIAL_LOCAL !== '1') throw new Error('Set AUDIT_OFFICIAL_LOCAL=1; local disposable harness only');
const c = await createClient({ base, audience: base, wasmPath: `${dir}/assets/assets/securechan-DYm3iWHV.wasm` });
await c.login('admin', 'AdminTest#2026');
const sql = (s) => execFileSync('docker', ['exec', 'mmwx-test-pg', 'psql', '-X', '-U', 'mmwx', '-d', 'mmwx', '-At', '-v', 'ON_ERROR_STOP=1', '-c', s], { encoding: 'utf8' }).trim();
const q = (s) => "'" + s.replaceAll("'", "''") + "'";
const records = [];
const record = (label, data) => {
  records.push({ label, data });
  fs.writeFileSync(`${dir}/probe-results.json`, JSON.stringify(records, null, 2) + '\n');
  console.log(label, JSON.stringify(data));
  return data;
};
const snap = (username) => JSON.parse(sql(`SELECT json_build_object('user',(SELECT row_to_json(x) FROM (SELECT username,is_active,is_over_limit,disabled_access_enforced,package_id,package_end_date FROM users WHERE username=${q(username)})x),'credentials',(SELECT coalesce(json_agg(x),'[]') FROM (SELECT server_id,inbound_tag,protocol,md5(credential_json) AS credential_hash FROM user_inbound_configs WHERE username=${q(username)})x),'assignments',(SELECT coalesce(json_agg(x),'[]') FROM (SELECT id,package_id,status,legacy_source,package_end_date FROM user_package_assignments WHERE username=${q(username)})x))`));
const op = async (label, hash, payload, opts) => record(label, await c.op(hash, payload, opts));
const u = 'audit-official-life';
const created = await op('create', '1e98343aac1ebc18', { username:u, password:'FixtureOnly#2026', email:'audit@example.invalid', nickname:'audit', remark:'disposable audit fixture' });
assert.equal(created.status, 200, 'fixture user creation');
record('created state', snap(u));
await op('disable without credentials','4b18ad3836973389',{username:u,is_active:false});
record('disabled without credentials state',snap(u));
await op('enable without credentials','4b18ad3836973389',{username:u,is_active:true});
const pkg = await op('create exclusive template','9186047b1bf5ba88',{name:'audit-official-template',description:'audit',nodes:[1],traffic_limit_gb:10,cycle_days:30});
const pid = pkg.data.id;
await op('assign package','4f61a1544c43c7c6',{username:u,package_id:pid,start_date:'2026-10-04',expire_date:'2027-10-04'});
sql(`INSERT INTO user_inbound_configs(username,server_id,inbound_tag,protocol,credential_json) VALUES(${q(u)},1,'audit-in','vless','{"id":"00000000-0000-4000-8000-000000000901","email":"audit-official-life"}')`);
record('before disable with credential',snap(u));
await op('disable with offline inbound','4b18ad3836973389',{username:u,is_active:false});
record('after disable with credential',snap(u));
await op('extend while officially disabled','aa38511ef347e5c4',{username:u,days:1});
record('after extend',snap(u));
await op('reset traffic while officially disabled','4328ba256425b334',null,{params:[u]});
record('after reset traffic',snap(u));
await op('save global limits','06172be9ae18a289',{username:u,speed_limit_override:1,device_limit_override:2,ip_limit_override:2,ip_over_limit_action_override:'reject'});
await op('save node limits','f604bdd74359b26c',{username:u,node_speed_overrides:{1:1},node_device_overrides:{1:2}});
record('after limits',snap(u));
await op('enable with offline inbound','4b18ad3836973389',{username:u,is_active:true});
record('after enable',snap(u));
await op('delete with offline inbound','ee63b72965e4f310',{username:u});
record('after delete',snap(u));
record('template survives official user deletion',JSON.parse(sql(`SELECT json_build_object('package_count',(SELECT count(*) FROM packages WHERE id=${pid}),'credential_count',(SELECT count(*) FROM user_inbound_configs WHERE username=${q(u)}))`)));
// The outer audit restores the pre-probe dump, including sequence values and all seed data.
