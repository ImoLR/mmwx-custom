import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { createServer } from 'node:http';
import { readFileSync, readdirSync, writeFileSync, mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const frontend=path.join(root,'frontend');const require=createRequire(path.join(frontend,'package.json'));
const {chromium}=require('playwright');const ts=require('typescript');const {build}=require('esbuild');
const output=process.env.HELPER_BATCH_EVIDENCE || '/root/mmwx-preview/helper-batch-upgrade';mkdirSync(output,{recursive:true});
const source=readFileSync(path.join(frontend,'src/main.tsx'),'utf8');const tree=ts.createSourceFile('main.tsx',source,ts.ScriptTarget.Latest,true,ts.ScriptKind.TSX);
const names=['CoreControlPanel','InfoBlock','coreVersionLabel','componentUpgradeState','formatDateTime','coreModeBadgeState'];
const functions=tree.statements.filter(n=>ts.isFunctionDeclaration(n)&&names.includes(n.name?.text)).map(n=>n.getText(tree)).join('\n');
const bundle=await build({stdin:{contents:`
import React,{useCallback,useEffect,useState,useRef} from 'react';
import {createRoot} from 'react-dom/client';import {RefreshCw} from 'lucide-react';
import {UpgradeProgressView} from './src/upgrade-progress';
import {HelperBatchUpgrade} from './src/helper-batch';
import {componentUpgradeLabel,upgradeRunning,progressForComponent,upgradeBlocked,officialReconnectText} from './src/upgrade-progress-state';
import {serverCoreVersion} from './src/xray-capabilities';
const fetchCoreMode=async()=>window.mode;
const fetchCustomReleaseInfo=async()=>({release:{latest_helper_version:'v0.7.4',latest_core_version:'8f5b614',helper_artifacts:{amd64:{url:'fixture',sha256:'fixture'}}}});
const fetchCustomAgentStatus=async()=>structuredClone(window.fixture);
const dispatchCustomAgentAction=async()=>{window.dispatches++;await new Promise(resolve=>setTimeout(resolve,1000));window.fixture.pending=[{id:'fixture',action:'helper.update',created_at:new Date().toISOString()}];return {command:{id:'fixture'}}};
${functions}
function App(){const [shown,setShown]=useState(true);const server={id:16,name:'PQS TW',xray_mode:'external',last_heartbeat:window.mode.last_official_heartbeat};const badge=coreModeBadgeState(server,{configured:true,fresh:true,desired_mode:'external',repair_status:window.mode.intent.repair_status,waiting_official_since:window.mode.intent.waiting_official_since});return <><button id="navigate" onClick={()=>setShown(!shown)}>切换页面</button>{shown&&(window.batch?<HelperBatchUpgrade token="fixture"/>:<><p className="fixture-badge">{badge.label}</p><CoreControlPanel server={server} sessionToken="fixture"/></>)}</>};
createRoot(document.getElementById('root')).render(<App/>);`,resolveDir:frontend,loader:'tsx'},define:{'import.meta.env':'{}'},bundle:true,write:false,format:'iife'});
const css=readFileSync(path.join(frontend,'dist/assets',readdirSync(path.join(frontend,'dist/assets')).find(n=>n.endsWith('.css'))));
const now=Date.parse('2026-10-16T16:00:00Z');const at=seconds=>new Date(now-seconds*1000).toISOString();
const base={success:true,status:{helper:{installed:true,version:'v0.7.1'},core:{installed:true,version:'8f5b614'},architecture:'amd64',reported_at:at(2)},pending:[],results:[]};
function progress(phase){return {...base,status:{...base.status,helper:{installed:true,version:phase==='success'?'v0.7.4':'v0.7.1'},update:{component:'helper',phase,started_at:at(50),updated_at:at(8),target_version:'v0.7.4',percent:phase==='downloading'?42:undefined,bytes_done:4404019,bytes_total:10485760,message:phase==='failed'?'SHA256 校验失败，未替换文件':'',log:[{at:at(50),phase:'dispatching',text:'已下发升级任务 → v0.7.4'},{at:at(8),phase,text:phase==='success'?'已确认新版本 v0.7.4':'正在处理升级'}]}}};}
const rows=[{server_id:'2',name:'Boil HKT 99u',version:'v0.7.1',phase:'queued'},{server_id:'12',name:'Boil Hinet 158',version:'v0.7.1',phase:'queued'},{server_id:'5',name:'Boil Hinet 马年',version:'v0.7.4',phase:'skipped',reason:'已是最新'},{server_id:'16',name:'PQS TW',version:'v0.7.4',phase:'skipped',reason:'已是最新'},{server_id:'11',name:'Breadcloud JP',version:'v0.7.1',phase:'skipped',reason:'离线或 Helper 未上报'},{server_id:'1',name:'Boil HKT',version:'v0.1.0',phase:'skipped',first_upgrade:true,reason:'需要首次升级，请先在单台 Helper 面板完成'}];
const batchBase={success:true,version:'v0.7.4',preview:rows,job:null};
const job={id:'fixture-batch',phase:'running',started_at:at(100),updated_at:at(8),stopped:false,rows:[{...rows[0],phase:'success'},{...rows[1],phase:'downloading'},{...rows[2],phase:'queued',version:'v0.7.1'},{...rows[3],phase:'failed',reason:'下载校验失败'},{...rows[4],phase:'reconnecting'},rows[5]]};
const cases=['running','success','dismissed','failed','batch-dialog','batch-panel','batch-finished','pqs-short','pqs-long','double-click'];
const server=createServer((req,res)=>{if(req.url==='/bundle.js'){res.setHeader('Content-Type','text/javascript');res.end(bundle.outputFiles[0].text)}else if(req.url==='/style.css'){res.setHeader('Content-Type','text/css');res.end(css)}else{res.setHeader('Content-Type','text/html; charset=utf-8');res.end('<meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/style.css"><style>body{padding:12px}#root{max-width:980px;margin:auto}#navigate{margin-bottom:12px}.core-control-panel{margin-top:0}.fixture-badge{color:#168061;overflow-wrap:anywhere}</style><div id="root"></div><script src="/bundle.js"></script>')}});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const url=`http://127.0.0.1:${server.address().port}`;const results=[],errors=[];let browser;
try{
 browser=await chromium.launch({headless:true,executablePath:process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE});
 for(const width of [390,1440]) for(const name of cases){
  const page=await browser.newPage({viewport:{width,height:1000},timezoneId:'Asia/Taipei'});page.on('pageerror',e=>errors.push(e.message));
  await page.clock.install({time:new Date(now)});
  let data=structuredClone(batchBase);
  if(name==='batch-panel')data.job=structuredClone(job);
  if(name==='batch-finished')data.job={...structuredClone(job),phase:'success',finished_at:at(8),rows:job.rows.map(r=>r.phase==='skipped'?r:{...r,phase:'success'})};
  let posts=0;
  await page.route('**/*',async route=>{const u=new URL(route.request().url());if(u.origin!==url)return route.abort();if(u.pathname==='/api/custom/helper-batch'){if(route.request().method()==='POST')posts++;return route.fulfill({json:data})}return route.continue()});
  const fixture=name==='running'?progress('downloading'):name==='success'||name==='dismissed'?progress('success'):name==='failed'?progress('failed'):base;
  const mode={configured:true,controller_mode:'external',current_mode:'external',last_official_heartbeat:at(250),intent:{desired_core_mode:'external',repair_status:name.startsWith('pqs')?'waiting_official':'healthy',waiting_official_since:at(name==='pqs-long'?190:40)}};
  await page.addInitScript(({fixture,mode,batch})=>{window.fixture=fixture;window.mode=mode;window.batch=batch;window.dispatches=0},{fixture,mode,batch:name.startsWith('batch')});
  await page.goto(url);await page.locator(name.startsWith('batch')?'.helper-batch':'.core-control-panel').waitFor();
  if(name==='batch-dialog'){await page.getByRole('button',{name:'一键升级全部 Helper',exact:true}).click();await page.getByRole('dialog').waitFor();assert.match(await page.getByRole('dialog').innerText(),/将升级 2 台/);assert.match(await page.getByRole('dialog').innerText(),/跳过 4 台/)}
  if(name==='batch-panel'){await page.locator('.helper-batch-panel').waitFor();assert.equal(await page.getByRole('button',{name:'批量升级进行中'}).isDisabled(),true);assert.match(await page.locator('.helper-batch-panel').innerText(),/已完成 2\/5 · 失败 1/)}
  if(name==='running'){await page.locator('.upgrade-steps').waitFor();assert.equal(await page.getByRole('button',{name:'升级进行中'}).first().isDisabled(),true);assert.equal(await page.locator('.upgrade-meter > span').evaluate(el=>getComputedStyle(el).backgroundColor),'rgb(22, 128, 97)')}
  if(name==='failed'){await page.locator('.upgrade-finished').waitFor();assert.match(await page.locator('.upgrade-finished').innerText(),/SHA256/)}
  if(['success','dismissed','failed','batch-finished'].includes(name)){
   await page.locator('.upgrade-progress').waitFor();const text=await page.locator('.upgrade-progress').innerText();assert.doesNotMatch(text,/最后更新/);await page.clock.runFor(2000);assert.equal(await page.locator('.upgrade-progress').innerText(),text);
   if(name==='success'||name==='batch-finished'){await page.clock.runFor(61_000);assert.equal(await page.locator('.upgrade-meter').count(),0)}
   if(name==='dismissed'){await page.getByRole('button',{name:'关闭升级进度'}).click();await page.locator('#navigate').click();await page.locator('#navigate').click();assert.equal(await page.locator('.upgrade-progress').count(),0);await page.reload();await page.locator('.core-control-panel').waitFor();assert.equal(await page.locator('.upgrade-progress').count(),0)}
  }
  if(name==='pqs-short')assert.match(await page.locator('.core-control-panel').innerText(),/正常（官方 Agent 正在重连）/);
  if(name==='pqs-long')assert.match(await page.locator('.core-control-panel').innerText(),/官方 Agent 连接中断 3 分钟.*最后官方心跳/s);
  if(name==='double-click'){const b=page.getByRole('button',{name:'升级 Helper',exact:true});await b.waitFor();await b.evaluate(el=>{el.click();el.click()});assert.equal(await page.evaluate(()=>window.dispatches),1);assert.equal(await page.getByRole('button',{name:'升级进行中'}).first().isDisabled(),true);await page.clock.runFor(1200);assert.equal(await page.getByRole('button',{name:'升级进行中'}).first().isDisabled(),true)}
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,`${name} overflow`);
  await page.screenshot({path:path.join(output,`${name}-${width}.png`),fullPage:true,animations:'disabled'});assert.equal(posts,0);results.push({name,width,passed:true});await page.close();
 }
 assert.deepEqual(errors,[]);
}finally{await browser?.close();await new Promise(resolve=>server.close(resolve));writeFileSync(path.join(output,'results.json'),JSON.stringify({results,errors},null,2));}
writeFileSync(path.join(output,'index.html'),`<!doctype html><meta charset="utf-8"><title>Helper 批量升级验收</title><style>body{font-family:system-ui;background:#f5f6f8;max-width:1200px;margin:24px auto;padding:12px}section{background:white;padding:16px;margin-bottom:20px}img{max-width:100%;height:auto}a{color:#168061}</style><h1>Helper 批量升级验收</h1><p>本地夹具 · 台湾时间 · 无真实订阅或凭证。绿色运行、静态完成、关闭持久化、失败、批量确认、批量进度、PQS 文案。</p>${results.filter(r=>r.name!=='double-click').map(r=>`<section><h2>${r.name} · ${r.width}px</h2><a href="${r.name}-${r.width}.png"><img src="${r.name}-${r.width}.png" loading="lazy"></a></section>`).join('')}`);
console.log(`${results.length} desktop/mobile fixtures passed`);
