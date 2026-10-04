// Opt-in regression probe: the fake Agent captures official writes, never runs a Core.
// Start official/capture-pull.mjs in the official container's network namespace first.
// AUDIT_ASSERT_DISABLED=1 turns observations into the S1 regression assertion.
import fs from 'node:fs';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createClient } from '/root/mmwx-custom-artifacts/official-sync-v0.5.5/sc-client.mjs';
if (process.env.AUDIT_OFFICIAL_LOCAL !== '1') throw new Error('Set AUDIT_OFFICIAL_LOCAL=1; disposable loopback harness only');
const dir='/root/mmwx-custom-artifacts/user-manager-audit/official';
const base='http://127.0.0.1:22889',u='audit-repush';
const sql=s=>execFileSync('docker',['exec','mmwx-test-pg','psql','-X','-U','mmwx','-d','mmwx','-At','-v','ON_ERROR_STOP=1','-c',s],{encoding:'utf8'}).trim();
const q=s=>"'"+s.replaceAll("'","''")+"'";
const c=await createClient({base,audience:base,wasmPath:`${dir}/assets/assets/securechan-DYm3iWHV.wasm`});
await c.login('admin','AdminTest#2026');
const original={id:'00000000-0000-4000-8000-000000000901',email:u};
const disabled={id:'00000000-0000-4000-8000-000000000902',email:u};
const initial={inbounds:[{tag:'audit-in',listen:'0.0.0.0',port:19001,protocol:'vless',settings:{decryption:'none',clients:[disabled]}}],outbounds:[{protocol:'freedom',tag:'direct'}]};
if(!sql(`SELECT username FROM users WHERE username=${q(u)}`)){
  assert.equal((await c.op('1e98343aac1ebc18',{username:u,password:'FixtureOnly#2026'})).status,200);
  sql(`INSERT INTO user_inbound_configs(username,server_id,inbound_tag,protocol,credential_json) VALUES(${q(u)},1,'audit-in','vless',${q(JSON.stringify(original))}); INSERT INTO mmwxc_user_lifecycle(username,desired_state,effective_state) VALUES(${q(u)},'disabled','disabled')`);
}
const cfg={name:'audit-vless',type:'vless',server:'127.0.0.1',port:19001,uuid:original.id};
let nid=sql("SELECT id FROM nodes WHERE inbound_tag='audit-in' LIMIT 1");
if(!nid)nid=sql(`INSERT INTO nodes(username,raw_url,node_name,protocol,parsed_config,clash_config,inbound_tag,tag) VALUES('admin','','audit-vless','vless',${q(JSON.stringify(cfg))},${q(JSON.stringify(cfg))},'audit-in','Xray') RETURNING id` ).split('\n')[0];
let pid=sql("SELECT id FROM packages WHERE name='audit-repush-template' LIMIT 1");
if(!pid){let r=await c.op('9186047b1bf5ba88',{name:'audit-repush-template',nodes:[Number(nid)],traffic_limit_gb:10,cycle_days:30});assert.equal(r.status,201);pid=String(r.data.id);}
sql(`UPDATE users SET package_id=${pid},package_start_date='2026-10-04',package_end_date='2027-10-04',is_active=1,disabled_access_enforced=0 WHERE username=${q(u)}`);
sql(`UPDATE remote_servers SET connection_mode='pull',pull_address='127.0.0.1',pull_port=23889,ip_address='127.0.0.1',status='connected',last_heartbeat=CURRENT_TIMESTAMP WHERE id=1; UPDATE nodes SET original_server='test-srv-1-e' WHERE id=${nid}`);
const cfgB={...cfg,name:'audit-vless-b',port:19002,uuid:'00000000-0000-4000-8000-000000000903'};
let nidB=sql("SELECT id FROM nodes WHERE inbound_tag='audit-in-b' LIMIT 1");
if(!nidB)nidB=sql(`INSERT INTO nodes(username,raw_url,node_name,protocol,parsed_config,clash_config,inbound_tag,tag,original_server) VALUES('admin','','audit-vless-b','vless',${q(JSON.stringify(cfgB))},${q(JSON.stringify(cfgB))},'audit-in-b','Xray','test-srv-1-e') RETURNING id`).split('\n')[0];
initial.inbounds.push({tag:'audit-in-b',listen:'0.0.0.0',port:19002,protocol:'vless',settings:{decryption:'none',clients:[{id:cfgB.uuid,email:'audit-admin-b'}]}});
sql(`UPDATE packages SET nodes='[${nid}]' WHERE id=${pid}`);
const results=[];
async function probe(label,action,reset=true){
  sql("UPDATE remote_servers SET status='connected',last_heartbeat=CURRENT_TIMESTAMP WHERE id=1");
  if(reset)fs.writeFileSync(`${dir}/fixture-config.json`,JSON.stringify(initial));
  const before=fs.readFileSync(`${dir}/pull-capture.jsonl`,'utf8').length;
  const response=await action();
  // Official mutation handlers may schedule the actual push asynchronously.
  await new Promise(resolve=>setTimeout(resolve,400));
  const calls=fs.readFileSync(`${dir}/pull-capture.jsonl`,'utf8').slice(before).trim().split('\n').filter(Boolean).map(JSON.parse).filter(r=>!r.url.endsWith('/traffic')&&!r.url.endsWith('/speed'));
  const newCredentials=calls.filter(r=>r.method!=='GET'&&r.url.endsWith('/xray/config-transaction')).flatMap(r=>{try{let b=JSON.parse(r.body);if(b.action!=='prepare')return [];return JSON.parse(b.config).inbounds.flatMap(i=>(i.settings?.clients??[]).filter(x=>x.email?.startsWith(u)&&x.id!==disabled.id).map(x=>({inbound:i.tag,email:x.email})));}catch{return [];}});
  const dangerous=calls.filter(r=>r.method!=='GET'&&r.body.includes(original.id));
  results.push({label,response,calls,originalCredentialPushed:dangerous.length>0,newCredentials,newCredentialPushed:newCredentials.some(x=>x.inbound==='audit-in-b'),officialState:JSON.parse(sql(`SELECT row_to_json(x) FROM (SELECT is_active,disabled_access_enforced FROM users WHERE username=${q(u)})x`)),lifecycle:sql(`SELECT effective_state FROM mmwxc_user_lifecycle WHERE username=${q(u)}`)});
  fs.writeFileSync(`${dir}/repush-results.json`,JSON.stringify(results,null,2)+'\n');
  console.log(label,response.status,'writes',calls.filter(r=>r.method!=='GET').length,'originalPushed',dangerous.length);
}
await probe('global limits save',()=>c.op('06172be9ae18a289',{username:u,speed_limit_override:2,device_limit_override:2,ip_limit_override:2,ip_over_limit_action_override:'reject'}));
await probe('per-node limits save',()=>c.op('f604bdd74359b26c',{username:u,node_speed_overrides:{[nid]:2},node_device_overrides:{[nid]:2}}));
await probe('extend package',()=>c.op('aa38511ef347e5c4',{username:u,days:1}));
await probe('reset traffic',()=>c.op('4328ba256425b334',null,{params:[u]}));
await probe('node config edit',()=>c.call(`/api/admin/nodes/${nid}/config`,{method:'PUT',json:{clash_config:JSON.stringify({...cfg,name:'audit-vless-edited'})}}));
await probe('repair credentials',()=>c.op('3a9dc0d3e205e57e',null));
await probe('assign same package',()=>c.op('4f61a1544c43c7c6',{username:u,package_id:Number(pid),start_date:'2026-10-04',expire_date:'2027-10-04'}));
const pkgs=await c.call('/api/admin/packages');const pkg=pkgs.data.packages.find(p=>p.id===Number(pid));
await probe('package node change',()=>c.op('f9bed75c75a38c5f',{...pkg,nodes:[Number(nid),Number(nidB)]}));
await probe('new independent package assignment',async()=>{const p=await c.op('9186047b1bf5ba88',{name:'audit-new-assignment',nodes:[Number(nidB)],cycle_days:30,traffic_limit_gb:10});return c.op('cc1103fc27dba96b',{username:u,package_id:p.data.id,expire_date:'2027-10-04',permanent:false});});
const codes=sql(`SELECT short_code FROM user_package_assignments WHERE username=${q(u)} AND status='active' ORDER BY id`).split('\n').filter(Boolean);
const subscriptions=[];for(const code of codes){const response=await fetch(`${base}/x/${code}?type=clash`);const body=await response.text();subscriptions.push({status:response.status,bytes:body.length,containsOriginalCredential:body.includes(original.id),containsUsername:body.includes(u)});}
fs.writeFileSync(`${dir}/subscription-results.json`,JSON.stringify({customEffectiveState:sql(`SELECT effective_state FROM mmwxc_user_lifecycle WHERE username=${q(u)}`),officialActive:Number(sql(`SELECT is_active FROM users WHERE username=${q(u)}`)),subscriptions},null,2)+'\n');
await probe('official disable control',()=>c.op('4b18ad3836973389',{username:u,is_active:false}));
await probe('extend after official disable',()=>c.op('aa38511ef347e5c4',{username:u,days:1}),false);
if(process.env.AUDIT_ASSERT_NATIVE_DISABLED==='1'){const native=results.find(r=>r.label==='extend after official disable');assert.equal(native.officialState.is_active,0);assert.equal(native.originalCredentialPushed,false,'audit UA-O01: extending an officially disabled user must not reinstall their credential');}
if(process.env.AUDIT_ASSERT_NEW_ACCESS==='1')assert.equal(results.some(r=>r.newCredentialPushed),false,'audit S2: a disabled user must not gain working credentials through new nodes or assignments');
if(process.env.AUDIT_ASSERT_DISABLED==='1')assert.equal(results.filter(r=>r.originalCredentialPushed).length,0,'audit S1: official actions must not push the original credential of a Custom-disabled user');
