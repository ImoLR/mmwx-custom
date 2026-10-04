// Official v0.5.5 user-feature regression. See features-README.md.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import http from 'node:http';
import {execFileSync} from 'node:child_process';
import {createClient} from '/root/mmwx-custom-artifacts/official-sync-v0.5.5/sc-client.mjs';

if (process.env.AUDIT_OFFICIAL_LOCAL !== '1') throw new Error('Set AUDIT_OFFICIAL_LOCAL=1; loopback fixture only');
const dir = '/root/mmwx-custom-artifacts/user-manager-features';
if (!fs.existsSync(`${dir}/harness-before.dump`)) throw new Error('Back up the harness first');
const configPath = `${dir}/fixture-config.json`, callsPath = `${dir}/agent-calls.jsonl`;
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
  const base = 'http://127.0.0.1:22889';
  const official = await createClient({base,audience:base,wasmPath:'/root/projects/mmwx-custom-user-features/frontend/public/assets/securechan-DYm3iWHV.wasm'});
  await official.login('admin','AdminTest#2026');
  const out = [];
  const record = (label, data) => {
    out.push({label, data});
    fs.writeFileSync(`${dir}/e2e-results.json`,JSON.stringify(out,null,2)+'\n',{mode:0o600});
    console.log(label, JSON.stringify(data));
  };
  const custom = async (username, action, json) => {
    const response = await fetch(`http://127.0.0.1:22890/api/custom/users/${username}/${action}`, {
      method:json === undefined ? 'GET' : 'POST',
      headers:{'Content-Type':'application/json','MM-Authorization':official.token},
      body:json === undefined ? undefined : JSON.stringify(json),
    });
    return {status:response.status,data:await response.json()};
  };
  const feature = (username, action, json={}) => custom(username,`features/${action}`,json);
  const check = (label,r,status=200) => {record(label,r);assert.equal(r.status,status,label);return r.data;};
  const count = (table, where) => Number(sql(`SELECT count(*) FROM ${table} WHERE ${where}`));
  const scalar = s => sql(s).split('\n')[0];
  const createUser = async username => {const r=await official.op('1e98343aac1ebc18',{username,password:'FixtureOnly#2026'});assert.equal(r.status,200);};
  const createPackage = async (name,nodes) => {const r=await official.call('/api/admin/packages/create',{method:'POST',json:{name,nodes,traffic_limit_gb:10,cycle_days:30}});assert.equal(r.status,201);return r.data.id;};
  const createImported = async (username,name) => {
    const r=await official.call('/api/admin/nodes',{method:'POST',json:{node_name:name,protocol:'trojan',clash_config:JSON.stringify({name,type:'trojan',server:'127.0.0.1',port:19443,password:'fixture-only'}),enabled:true}});
    assert.ok([200,201].includes(r.status),JSON.stringify(r));
    const id=Number(scalar(`SELECT id FROM nodes WHERE node_name=${q(name)} ORDER BY id DESC LIMIT 1`));
    assert.ok(id>0);sql(`UPDATE nodes SET username=${q(username)} WHERE id=${id}`);return id;
  };
  const username='uf-feature',other='uf-other';
  await createUser(username);await createUser(other);
  const node=await createImported(username,'uf-import-a'),kept=await createImported(other,'uf-import-b');
  const p1=await createPackage('uf-first',[node]),p2=await createPackage('uf-second',[kept]),p3=await createPackage('uf-third',[kept]);
  const assignment=check('assignment add',await feature(username,'add-assignment',{username,package_id:p1,expire_date:'2027-10-04',traffic_limit_override_gb:1.5,is_reset:true,reset_day:31}));
  const aid=Number(scalar(`SELECT id FROM user_package_assignments WHERE username=${q(username)} AND package_id=${p1}`));
  assert.equal(Number(scalar(`SELECT traffic_limit_override FROM user_package_assignments WHERE id=${aid}`)),1.5*2**30);
  const all=await official.call('/api/admin/users');
  const listed=all.data.users.find(u=>u.username===username);
  record('independent assignment user',{package_id:listed.package_id,assignment_package_ids:listed.assignment_package_ids});
  assert.ok(listed.assignment_package_ids.includes(p1));
  const assignments=check('assignment list',await official.call('/api/admin/package-assignments?username='+username));
  const code=assignments.assignments.find(a=>a.id===aid).short_code;
  const subscription=await fetch(base+'/x/'+code+'?type=clash');
  record('assignment-only subscription',{status:subscription.status,bytes:(await subscription.text()).length});assert.equal(subscription.status,200);
  check('refuse another user assignment',await feature(other,'add-assignment',{username:other,package_id:p1,expire_date:'2027-10-04'}),409);
  check('refuse another user legacy bind',await feature(other,'assign-package',{username:other,package_id:p1,start_date:'2026-10-04',expire_date:'2027-10-04'}),409);
  check('assignment edit unlimited',await feature(username,'edit-assignment',{username,assignment_id:aid,expire_date:'',permanent:true,is_reset:true,reset_day:31,traffic_limit_override_gb:0}));
  assert.equal(Number(scalar(`SELECT traffic_limit_override FROM user_package_assignments WHERE id=${aid}`)),0);
  check('assignment edit inherit',await feature(username,'edit-assignment',{username,assignment_id:aid,expire_date:'2027-10-04',permanent:false,is_reset:false,reset_day:1,traffic_limit_override_gb:null}));
  assert.equal(scalar(`SELECT traffic_limit_override IS NULL FROM user_package_assignments WHERE id=${aid}`),'t');
  check('refuse wrong assignment owner',await feature(other,'unbind-assignment',{username:other,assignment_id:aid}),409);
  check('assignment unbind',await feature(username,'unbind-assignment',{username,assignment_id:aid}));
  assert.equal(count('user_package_assignments',`id=${aid}`),0);
  check('legacy bind',await feature(username,'assign-package',{username,package_id:p1,start_date:'2026-10-04',expire_date:'2027-01-01'}));
  sql(`UPDATE users SET package_end_date='2028-01-01' WHERE username=${q(username)};INSERT INTO user_traffic(server_id,username,uplink,downlink,total_uplink,total_downlink) VALUES(1,${q(username)},1073741824,2147483648,10737418240,21474836480)`);
  check('inherit date and traffic',await feature(username,'assign-package',{username,package_id:p2,start_date:'2026-10-04',expire_date:'2027-01-01',inherit_expire_date:true,inherit_traffic:true}));
  assert.equal(scalar(`SELECT package_end_date::date FROM users WHERE username=${q(username)}`),'2028-01-01');
  assert.equal(Number(scalar(`SELECT uplink FROM user_traffic WHERE username=${q(username)}`)),2**30);
  check('do not inherit date and traffic',await feature(username,'assign-package',{username,package_id:p1,start_date:'2026-10-04',expire_date:'2027-01-01',inherit_expire_date:false,inherit_traffic:false}));
  assert.equal(scalar(`SELECT package_end_date::date FROM users WHERE username=${q(username)}`),'2027-01-01');
  assert.equal(Number(scalar(`SELECT uplink FROM user_traffic WHERE username=${q(username)}`)),0);
  assert.equal(Number(scalar(`SELECT total_uplink FROM user_traffic WHERE username=${q(username)}`)),10*2**30);
  sql(`UPDATE users SET package_end_date='2028-01-01' WHERE username=${q(username)};UPDATE user_traffic SET uplink=1073741824 WHERE username=${q(username)}`);
  check('omitted inherit options preserve old reset default',await feature(username,'assign-package',{username,package_id:p2,start_date:'2026-10-04',expire_date:'2027-01-01'}));
  assert.equal(scalar(`SELECT package_end_date::date FROM users WHERE username=${q(username)}`),'2027-01-01');
  assert.equal(Number(scalar(`SELECT uplink FROM user_traffic WHERE username=${q(username)}`)),0);
  check('user override 2.5 GiB',await official.call('/api/admin/users/traffic-limit',{method:'PUT',json:{username,traffic_limit_override_gb:2.5}}));
  assert.equal(Number(scalar(`SELECT traffic_limit_override FROM users WHERE username=${q(username)}`)),2.5*2**30);
  check('user override inherit',await official.call('/api/admin/users/traffic-limit',{method:'PUT',json:{username,traffic_limit_override_gb:null}}));
  assert.equal(scalar(`SELECT traffic_limit_override IS NULL FROM users WHERE username=${q(username)}`),'t');
  for(const nickname of ['测试昵称','']) {check('nickname '+(nickname||'fallback'),await official.call('/api/admin/users/update-nickname',{method:'POST',json:{username,nickname}}));assert.equal(scalar(`SELECT nickname FROM users WHERE username=${q(username)}`),nickname||username);}
  for(const action of ['kick_oldest','reject'])check('IP '+action,await official.call('/api/admin/users/limits',{method:'PUT',json:{username,speed_limit_override:null,device_limit_override:null,ip_limit_override:2,ip_over_limit_action_override:action}}));
  assert.equal(scalar(`SELECT ip_over_limit_action_override FROM users WHERE username=${q(username)}`),'reject');
  sql(`UPDATE users SET package_end_date='2020-01-01' WHERE username=${q(username)};UPDATE user_package_assignments SET package_end_date='2020-01-01' WHERE username=${q(username)}`);
  const renewal=check('expired custom renew 17 days',await feature(username,'renew',{username,days:17}));
  const expected=new Date();expected.setUTCDate(expected.getUTCDate()+17);assert.equal(renewal.end_date,expected.toISOString().slice(0,10));
  check('renew rejects 3651',await feature(username,'renew',{username,days:3651}),400);
  sql(`INSERT INTO mmwxc_user_lifecycle(username,desired_state,effective_state) VALUES(${q(username)},'disabled','disabled') ON CONFLICT(username) DO UPDATE SET desired_state='disabled',effective_state='disabled'`);
  for(const state of ['disabled','partially_disabled','partially_enabled','enabling','disabling']){
    sql(`UPDATE mmwxc_user_lifecycle SET effective_state=${q(state)} WHERE username=${q(username)}`);
    check('disabled confirmation '+state,await feature(username,'renew',{username,days:1}),409);
  }
  check('disabled confirmed renew',await feature(username,'renew',{username,days:1,confirm_disabled:true}));
  for(const state of ['deleting','delete_partial']){
    sql(`UPDATE mmwxc_user_lifecycle SET desired_state='deleted',effective_state=${q(state)} WHERE username=${q(username)}`);
    check('deletion state refuses '+state,await feature(username,'add-assignment',{username,package_id:p3,expire_date:'2027-10-04',confirm_disabled:true}),409);
  }
  sql(`DELETE FROM mmwxc_user_lifecycle WHERE username=${q(username)}`);
  check('legacy unbind',await feature(username,'unassign-package',{username}));
  const empty=await createPackage('uf-becomes-empty',[node]),mixed=await createPackage('uf-keeps-other',[node,kept]),already=await createPackage('uf-intent-all',[]);
  for(const id of [empty,mixed])sql(`UPDATE packages SET node_speed_limits=${q(JSON.stringify({[node]:2,[kept]:3}))} WHERE id=${id}`);
  const imported=check('imported nodes list',await custom(username,'features/imported-nodes'));assert.ok(imported.nodes.some(n=>n.id===node));
  check('clear imported with package cascade',await feature(username,'clear-imported-nodes'));
  assert.equal(count('nodes',`id=${node}`),0);assert.equal(count('packages',`id=${empty}`),0);
  assert.deepEqual(JSON.parse(scalar(`SELECT nodes FROM packages WHERE id=${mixed}`)),[kept]);
  assert.deepEqual(JSON.parse(scalar(`SELECT node_speed_limits FROM packages WHERE id=${mixed}`)),{[kept]:3});
  assert.deepEqual(JSON.parse(scalar(`SELECT nodes FROM packages WHERE id=${already}`)),[]);
  record('import cascade confirmed',{deleted_empty_package:empty,kept_package:mixed,already_empty_untouched:already});
  const owner='uf-admin-coowner';await createUser(owner);
  const adminCredential={id:'00000000-0000-4000-8000-000000003001',email:'admin-features'},ownCredential={id:'00000000-0000-4000-8000-000000003002',email:owner};
  const tag='uf-admin-coowned';const cfg={inbounds:[{tag,listen:'0.0.0.0',port:19551,protocol:'vless',settings:{decryption:'none',clients:[adminCredential,ownCredential]}}],outbounds:[{protocol:'freedom',tag:'direct'}]};
  fs.writeFileSync(configPath,JSON.stringify(cfg),{mode:0o600});
  sql("UPDATE remote_servers SET connection_mode='pull',pull_address='127.0.0.1',pull_port=23889,ip_address='127.0.0.1',status='connected',last_heartbeat=CURRENT_TIMESTAMP WHERE id=1");
  for(const [u,credential] of [['admin',adminCredential],[owner,ownCredential]])sql(`INSERT INTO user_inbound_configs(username,server_id,inbound_tag,protocol,credential_json) VALUES(${q(u)},1,${q(tag)},'vless',${q(JSON.stringify(credential))})`);
  const nc={name:tag,type:'vless',server:'127.0.0.1',port:19551,uuid:adminCredential.id};
  const nid=Number(scalar(`INSERT INTO nodes(username,raw_url,node_name,protocol,parsed_config,clash_config,inbound_tag,tag,original_server) VALUES('admin','',${q(tag)},'vless',${q(JSON.stringify(nc))},${q(JSON.stringify(nc))},${q(tag)},'Xray','test-srv-1-e') RETURNING id`));
  sql(`INSERT INTO server_xray_config_snapshots(server_id,config_json,config_hash,source,status) VALUES(1,${q(JSON.stringify(cfg))},'uf-before-rotation','master_write','success')`);
  const ownerPackage=await createPackage('uf-admin-coowned-package',[nid]);
  sql(`UPDATE users SET package_id=${ownerPackage},package_end_date='2027-10-04' WHERE username=${q(owner)}`);
  check('replace admin credentials',await feature('admin','replace-credentials'));
  const rotated=JSON.parse(scalar(`SELECT credential_json FROM user_inbound_configs WHERE username='admin' AND inbound_tag=${q(tag)}`));
  assert.notEqual(rotated.id,adminCredential.id);
  sql(`UPDATE nodes SET clash_config=${q(JSON.stringify(nc))},parsed_config=${q(JSON.stringify(nc))} WHERE id=${nid}`);
  const repaired=check('repair own admin credentials',await feature('admin','repair-credentials'));
  assert.equal(repaired.nodes_repaired,1);
  assert.equal(JSON.parse(scalar(`SELECT clash_config FROM nodes WHERE id=${nid}`)).uuid,rotated.id);
  const uris=await official.call('/api/admin/node-uris');assert.equal(uris.status,200);record('URI list',{status:uris.status,count:uris.data.items.length,ownerEntries:uris.data.items.filter(i=>i.username===owner).length});
  const preview=check('deletion preview after admin rotation and repair',await custom(owner,'deletion-preview'));
  const packagePlan=preview.preview.inbound_plan.find(p=>p.package_id===ownerPackage);assert.equal(packagePlan.action,'DELETE_PACKAGE');
  assert.ok(!preview.preview.inbound_plan.some(p=>p.status==='conflict'));
  const crossEmpty=await createPackage('uf-delete-becomes-empty',[nid]),crossKept=await createPackage('uf-delete-keeps-imported',[nid,kept]);
  const updatedPreview=check('user deletion empty-package preview',await custom(owner,'deletion-preview'));
  const emptyPlan=updatedPreview.preview.inbound_plan.find(p=>p.package_id===crossEmpty);assert.equal(emptyPlan.action,'DELETE_EMPTY_PACKAGE');assert.equal(emptyPlan.decision_note,'删除（移除节点后为空）');
  check('delete user cleans and deletes empty packages',await custom(owner,'delete',{}));
  assert.equal(count('users',`username=${q(owner)}`),0);assert.equal(count('packages',`id=${crossEmpty}`),0);
  assert.deepEqual(JSON.parse(scalar(`SELECT nodes FROM packages WHERE id=${crossKept}`)),[kept]);
  record('all feature writes verified',{passed:true});
}
