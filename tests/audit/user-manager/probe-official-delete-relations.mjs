// Explicit local-harness probe; see README.md in this directory.
import fs from 'node:fs';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {createClient} from '/root/mmwx-custom-artifacts/official-sync-v0.5.5/sc-client.mjs';
if(process.env.AUDIT_OFFICIAL_LOCAL!=='1')throw new Error('Local audit opt-in required');
const dir='/root/mmwx-custom-artifacts/user-manager-audit/official',base='http://127.0.0.1:22889';
function assertClean(rows){const row=rows.find(x=>x.label==='after owner deletion').data;assert.equal(row.api_token+row.webauthn,0,'audit UA-O02: deleting an official user must remove API token and WebAuthn records');}
if(process.env.AUDIT_OFFICIAL_RESULTS_ONLY==='1'){assertClean(JSON.parse(fs.readFileSync(`${dir}/delete-relations-results.json`,'utf8')));process.exit(0);}
const sql=s=>execFileSync('docker',['exec','mmwx-test-pg','psql','-X','-U','mmwx','-d','mmwx','-At','-v','ON_ERROR_STOP=1','-c',s],{encoding:'utf8'}).trim();
const c=await createClient({base,audience:base,wasmPath:`${dir}/assets/assets/securechan-DYm3iWHV.wasm`});
await c.login('admin','AdminTest#2026');
const out=[];const put=(label,data)=>{out.push({label,data});fs.writeFileSync(`${dir}/delete-relations-results.json`,JSON.stringify(out,null,2)+'\n');console.log(label,JSON.stringify(data));};
const aid=sql("SELECT id FROM user_package_assignments WHERE username='audit-repush' AND COALESCE(legacy_source,0)=0 ORDER BY id DESC LIMIT 1");
if(aid){put('unbind assignment',await c.op('14c870669f657266',{username:'audit-repush',assignment_id:Number(aid)}));put('after unbind assignment row',sql(`SELECT id,status FROM user_package_assignments WHERE id=${aid}`));}
for(const username of ['audit-owner','audit-consumer'])put(`create ${username}`,await c.op('1e98343aac1ebc18',{username,password:'FixtureOnly#2026'}));
const nid=sql("INSERT INTO nodes(username,raw_url,node_name,protocol,parsed_config,clash_config) VALUES('audit-owner','','audit-shared-reference','vless','{}','{}') RETURNING id").split('\n')[0];
const p=await c.op('9186047b1bf5ba88',{name:'audit-consumer-package',cycle_days:30,traffic_limit_gb:10,nodes:[Number(nid)]});
put('consumer package created',p);
put('consumer package bind',await c.op('4f61a1544c43c7c6',{username:'audit-consumer',package_id:p.data.id,start_date:'2026-10-04',expire_date:'2027-10-04'}));
sql(`INSERT INTO forward_chain_nodes(node_id,chain_id,port,owner_username,billing_assignment_id) VALUES(${nid},901,901,'audit-owner',901); INSERT INTO user_api_tokens(username,token_hash,name) VALUES('audit-owner','audit-owner-token','audit'); INSERT INTO webauthn_credentials(username,credential_id,credential) VALUES('audit-owner','audit-owner-webauthn','{}')`);
put('delete owner',await c.op('ee63b72965e4f310',{username:'audit-owner'}));
put('after owner deletion',JSON.parse(sql(`SELECT json_build_object('owner_user',(SELECT count(*) FROM users WHERE username='audit-owner'),'node',(SELECT count(*) FROM nodes WHERE id=${nid}),'other_user',(SELECT count(*) FROM users WHERE username='audit-consumer'),'other_package_nodes',(SELECT nodes FROM packages WHERE id=${p.data.id}),'forward_reference',(SELECT count(*) FROM forward_chain_nodes WHERE owner_username='audit-owner'),'api_token',(SELECT count(*) FROM user_api_tokens WHERE username='audit-owner'),'webauthn',(SELECT count(*) FROM webauthn_credentials WHERE username='audit-owner'))`)));

if(process.env.AUDIT_ASSERT_OFFICIAL_DELETE_CLEAN==='1')assertClean(out);
