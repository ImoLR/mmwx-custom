// Local v0.5.5 + Custom regression harness. See lifecycle-README.md.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import http from 'node:http';
import {execFileSync} from 'node:child_process';
import {createClient} from '/root/mmwx-custom-artifacts/official-sync-v0.5.5/sc-client.mjs';

if (process.env.AUDIT_OFFICIAL_LOCAL !== '1') throw new Error('Set AUDIT_OFFICIAL_LOCAL=1; local fixture only');
const dir = '/root/mmwx-custom-artifacts/user-lifecycle-fixes';
if (!fs.existsSync(`${dir}/harness-before.dump`)) throw new Error('Back up the harness first');
const configPath = `${dir}/fixture-config.json`;
const callsPath = `${dir}/agent-calls.jsonl`;

if (process.argv.includes('--agent')) {
  const pending = new Map();
  http.createServer(async (req, res) => {
    let data = '';
    for await (const chunk of req) data += chunk;
    fs.appendFileSync(callsPath, JSON.stringify({at:new Date().toISOString(),url:req.url,method:req.method,body:data})+'\n', {mode:0o600});
    let config = JSON.parse(fs.readFileSync(configPath, 'utf8'));
    if (req.method !== 'GET' && data) {
      const body = JSON.parse(data);
      if (req.url.endsWith('/xray/config-transaction')) {
        if (body.action === 'prepare') pending.set(body.operation_id, JSON.parse(body.config));
        if (body.action === 'activate' && pending.has(body.operation_id)) config = pending.get(body.operation_id);
        if (body.action === 'commit') pending.delete(body.operation_id);
      }
      if (req.url.endsWith('/xray/config')) {
        const next = body.config ?? body;
        config = typeof next === 'string' ? JSON.parse(next) : next;
      }
      if (req.url.endsWith('/inbounds')) {
        if (body.action === 'remove') config.inbounds = config.inbounds.filter(i=>i.tag!==body.tag);
        if (body.action === 'replace') config.inbounds = config.inbounds.map(i=>i.tag===body.tag ? body.inbound : i);
        if (body.action === 'remove-client') {
          const inbound = config.inbounds.find(i=>i.tag===body.tag);
          if (inbound) {
            for (const key of ['clients','users','accounts']) {
              if (inbound.settings?.[key]) inbound.settings[key] = inbound.settings[key].filter(c=>!Object.entries(body.client).every(([k,v])=>c[k]===v));
            }
          }
        }
      }
      fs.writeFileSync(configPath, JSON.stringify(config), {mode:0o600});
    }
    res.writeHead(200, {'Content-Type':'application/json'});
    res.end(JSON.stringify({success:true,data:config,config:JSON.stringify(config),inbounds:config.inbounds,outbounds:config.outbounds}));
  }).listen(23889, '127.0.0.1');
} else {
  const sql = s => execFileSync('docker', ['exec','mmwx-test-pg','psql','-X','-U','mmwx','-d','mmwx','-At','-v','ON_ERROR_STOP=1','-c',s], {encoding:'utf8'}).trim();
  const q = s => "'" + String(s).replaceAll("'", "''") + "'";
  const readConfig = () => JSON.parse(fs.readFileSync(configPath, 'utf8'));
  const writeConfig = config => fs.writeFileSync(configPath, JSON.stringify(config), {mode:0o600});
  const base = 'http://127.0.0.1:22889';
  const official = await createClient({base,audience:base,wasmPath:'/root/mmwx-custom-artifacts/user-manager-audit/official/assets/assets/securechan-DYm3iWHV.wasm'});
  const login = await official.login('admin','AdminTest#2026');
  const results = [];
  const record = (label, data) => {
    results.push({label,data});
    fs.writeFileSync(`${dir}/${process.argv.includes('--probe') ? 'api-probe' : 'e2e-results'}.json`,JSON.stringify(results,null,2)+'\n',{mode:0o600});
    console.log(label, JSON.stringify(data));
  };
  sql("UPDATE remote_servers SET connection_mode='pull',pull_address='127.0.0.1',pull_port=23889,ip_address='127.0.0.1',status='connected',last_heartbeat=CURRENT_TIMESTAMP WHERE id=1");
  const addNode = (tag, port, credential, name=tag) => {
    const cfg = {name,type:'vless',server:'127.0.0.1',port,uuid:credential.id};
    return Number(sql(`INSERT INTO nodes(username,raw_url,node_name,protocol,parsed_config,clash_config,inbound_tag,tag,original_server) VALUES('admin','',${q(name)},'vless',${q(JSON.stringify(cfg))},${q(JSON.stringify(cfg))},${q(tag)},'Xray','test-srv-1-e') RETURNING id`).split('\n')[0]);
  };
  if (process.argv.includes('--probe')) {
    const tag = 'lifecycle-probe-in';
    const credential = {id:'00000000-0000-4000-8000-000000001001',email:'lifecycle-probe'};
    writeConfig({inbounds:[{tag,listen:'0.0.0.0',port:19501,protocol:'vless',settings:{decryption:'none',clients:[credential]}}],outbounds:[{protocol:'freedom',tag:'direct'}]});
    const nid = addNode(tag,19501,credential);
    const removed = await official.call('/api/admin/remote/inbounds?server_id=1',{method:'POST',json:{action:'remove',tag}});
    record('remote remove', {response:removed,nodeRows:Number(sql(`SELECT count(*) FROM nodes WHERE id=${nid}`)),inboundPresent:readConfig().inbounds.some(i=>i.tag===tag)});
    assert.equal(removed.status,200);
    assert.equal(Number(sql(`SELECT count(*) FROM nodes WHERE id=${nid}`)),0);
    assert.equal(readConfig().inbounds.some(i=>i.tag===tag),false);
    const deleted = await official.call(`/api/admin/nodes/${nid}`,{method:'DELETE'});
    record('node REST delete', {response:deleted,nodeRows:Number(sql(`SELECT count(*) FROM nodes WHERE id=${nid}`))});
    assert.equal(deleted.status,200);
    assert.equal(Number(sql(`SELECT count(*) FROM nodes WHERE id=${nid}`)),0);
    const created = await official.op('9186047b1bf5ba88',{name:'lifecycle-probe-package',nodes:[],traffic_limit_gb:1,cycle_days:30});
    assert.equal(created.status,201);
    const id = created.data.id;
    const list = await official.call('/api/admin/packages');
    const pkg = list.data.packages.find(p=>p.id===id);
    record('package payload',pkg);
    for (const path of ['/api/admin/packages',`/api/admin/packages/${id}`]) {
      const r = await official.call(path,{method:'PUT',json:{...pkg,description:'lifecycle REST update probe'}});
      record(`package REST PUT ${path}`,r);
    }
    record('package page update op',await official.op('f9bed75c75a38c5f',{...pkg,description:'lifecycle official op update probe'}));
    record('package probe cleanup',await official.op('3398a1ee75247290',{id},{params:[String(id)]}));
    const owner = 'lf-probe-owner', other = 'lf-probe-other';
    for (const username of [owner, other]) {
      if (!sql(`SELECT username FROM users WHERE username=${q(username)}`)) assert.equal((await official.op('1e98343aac1ebc18',{username,password:'FixtureOnly#2026'})).status,200);
    }
    const ownCredential = {id:'00000000-0000-4000-8000-000000001002',email:owner};
    const otherCredential = {id:'00000000-0000-4000-8000-000000001003',email:other};
    const ownTag = 'lifecycle-ref-own', otherTag = 'lifecycle-ref-other';
    const ownID = addNode(ownTag,19502,ownCredential), otherID = addNode(otherTag,19503,otherCredential);
    writeConfig({inbounds:[{tag:ownTag,port:19502,protocol:'vless',settings:{decryption:'none',clients:[ownCredential]}},{tag:otherTag,port:19503,protocol:'vless',settings:{decryption:'none',clients:[otherCredential]}}],outbounds:[{protocol:'freedom',tag:'direct'}]});
    for (const [username,tag,credential] of [[owner,ownTag,ownCredential],[other,otherTag,otherCredential]]) sql(`INSERT INTO user_inbound_configs(username,server_id,inbound_tag,protocol,credential_json) VALUES(${q(username)},1,${q(tag)},'vless',${q(JSON.stringify(credential))})`);
    const refPackage = await official.op('9186047b1bf5ba88',{name:`lifecycle-probe-references-${ownID}`,nodes:[ownID,otherID],traffic_limit_gb:10,cycle_days:30,node_traffic_limits:{[ownID]:1,[otherID]:2},node_speed_limits:{[ownID]:3,[otherID]:4}});
    assert.equal(refPackage.status,201);
    const refID = refPackage.data.id;
    sql(`UPDATE users SET package_id=${refID},package_start_date='2026-10-04',package_end_date='2027-10-04' WHERE username=${q(owner)}`);
    record('remove with package refs',await official.call('/api/admin/remote/inbounds?server_id=1',{method:'POST',json:{action:'remove',tag:ownTag}}));
    const packageAfterRemove = (await official.call('/api/admin/packages')).data.packages.find(p=>p.id===refID);
    record('package after inbound removal',{package:packageAfterRemove,stored:JSON.parse(sql(`SELECT row_to_json(p) FROM (SELECT nodes,node_speed_limits,node_traffic_limits FROM packages WHERE id=${refID})p`)),nodeRows:Number(sql(`SELECT count(*) FROM nodes WHERE id=${ownID}`))});
    const callsBefore = fs.readFileSync(callsPath,'utf8').length;
    record('prune package via official update',await official.op('f9bed75c75a38c5f',{...packageAfterRemove,nodes:[otherID],node_traffic_limits:{[otherID]:2},node_speed_limits:{[otherID]:4}}));
    await new Promise(resolve=>setTimeout(resolve,500));
    const calls = fs.readFileSync(callsPath,'utf8').slice(callsBefore).trim().split('\n').filter(Boolean).map(JSON.parse);
    record('package update credential push',{oldInboundPresent:readConfig().inbounds.some(i=>i.tag===ownTag),oldCredentialPresent:JSON.stringify(readConfig()).includes(ownCredential.id),calls});
  } else {
    throw new Error('Use --probe or --agent; end-to-end mode is added with the product fixes');
  }
}
