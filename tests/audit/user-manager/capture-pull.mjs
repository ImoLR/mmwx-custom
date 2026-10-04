import http from 'node:http';
import fs from 'node:fs';
const dir='/root/mmwx-custom-artifacts/user-manager-audit/official';
const pending=new Map();
http.createServer(async(req,res)=>{
let data='';for await(const chunk of req)data+=chunk;
fs.appendFileSync(dir+'/pull-capture.jsonl',JSON.stringify({at:new Date().toISOString(),url:req.url,method:req.method,body:data})+'\n');
let config=JSON.parse(fs.readFileSync(dir+'/fixture-config.json','utf8'));
if(req.method!=='GET'&&data){try{let body=JSON.parse(data);if(req.url.endsWith('/xray/config-transaction')){if(body.action==='prepare')pending.set(body.operation_id,JSON.parse(body.config));if(body.action==='activate'&&pending.has(body.operation_id)){config=pending.get(body.operation_id);fs.writeFileSync(dir+'/fixture-config.json',JSON.stringify(config));}if(body.action==='commit')pending.delete(body.operation_id);}if(req.url.endsWith('/xray/config')){const next=body.config??body;if(typeof next==='string')config=JSON.parse(next);else config=next;fs.writeFileSync(dir+'/fixture-config.json',JSON.stringify(config));}}catch{}}
res.writeHead(200,{'Content-Type':'application/json'});
res.end(JSON.stringify({success:true,data:config,config:JSON.stringify(config),inbounds:config.inbounds,outbounds:config.outbounds}));
}).listen(23889,'127.0.0.1');
