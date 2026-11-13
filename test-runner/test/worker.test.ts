import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { createHash } from 'node:crypto';
import { RunnerClient, type Report } from '../src/worker.js';

const report: Report={source_sha:'a',artifact_sha256:'b',scenario_version:'1',platform:'windows-amd64',checks:[]};
async function fixture(failHeartbeat=false, failEvidence=false) {
 const calls:string[]=[];
 const server=createServer((req,res)=>{
  calls.push(req.url!);
  if(req.headers.authorization!=='Bearer machine-secret'){res.writeHead(401).end();return;}
  res.setHeader('Content-Type','application/json');
  if(req.url==='/test-runner/claim') res.end(JSON.stringify({data:{
   machine_id:'m',generation:1,expires_at:new Date(Date.now()+60_000).toISOString(),
   run:{id:'r',spec:{...report,project_id:'p',scenario:'observe',required_checks:[]}}
  }}));
  else if(req.url==='/test-runner/heartbeat'){
   if(failHeartbeat)res.writeHead(409).end();
   else res.end(JSON.stringify({data:{expires_at:new Date(Date.now()+60_000).toISOString()}}));
  } else if(req.url==='/test-runner/evidence'){
   if(failEvidence)res.writeHead(400).end();
   else res.end(JSON.stringify({data:{reference:'sha256:'+createHash('sha256').update('proof').digest('hex')}}));
  } else res.writeHead(204).end();
 });
 await new Promise<void>(r=>server.listen(0,'127.0.0.1',r));
 const addr=server.address() as {port:number};
 return {calls,client:new RunnerClient('http://127.0.0.1:'+addr.port,'m','machine-secret'),
 close:()=>new Promise<void>(r=>server.close(()=>r()))};
}
test('worker cleans before report and releases only after accepted report',async()=>{
 const f=await fixture();let cleaned=false;
 try{
  assert.equal(await f.client.runOnce({
   execute:async()=>report,
   cleanup:async()=>{cleaned=true;assert.equal(f.calls.includes('/test-runner/report'),false);}
  }),true);
  assert.equal(cleaned,true);
  assert.deepEqual(f.calls,['/test-runner/claim','/test-runner/report','/test-runner/release']);
 }finally{await f.close();}
});
test('lease rejection aborts scenario, cleans, and never submits success or releases',async()=>{
 const f=await fixture(true);let cleaned=false;
 try{
  await assert.rejects(f.client.runOnce({
   execute:async(_a,signal)=>new Promise<Report>((_resolve,reject)=>{
    signal.addEventListener('abort',()=>reject(signal.reason),{once:true});
   }),
   cleanup:async()=>{cleaned=true;}
  },undefined,5),/409/);
  assert.equal(cleaned,true);
  assert.deepEqual(f.calls,['/test-runner/claim','/test-runner/heartbeat']);
 }finally{await f.close();}
});
test('remote cleartext and embedded credentials are rejected',()=>{
 assert.throws(()=>new RunnerClient('http://remote.example','m','t'),/HTTPS/);
 assert.throws(()=>new RunnerClient('https://user:pass@example.com','m','t'),/Invalid/);
});

test('healthy heartbeat cannot extend the absolute run deadline',async()=>{
 const f=await fixture();let cleaned=false;
 try{
  await assert.rejects(f.client.runOnce({
   execute:async(_a,signal)=>new Promise<Report>((_resolve,reject)=>signal.addEventListener('abort',()=>reject(signal.reason),{once:true})),
   cleanup:async()=>{cleaned=true;}
  },undefined,5,40),/Run deadline/);
  assert.equal(cleaned,true);
  assert.equal(f.calls.includes('/test-runner/report'),false);
  assert.equal(f.calls.includes('/test-runner/release'),false);
 }finally{await f.close();}
});

test('evidence is uploaded after cleanup and before report',async()=>{
 const f=await fixture();let cleaned=false;
 try{
  await f.client.runOnce({execute:async()=>report,cleanup:async()=>{cleaned=true;},
   uploadEvidence:async upload=>{assert.equal(cleaned,true);assert.equal(await upload('text/plain',Buffer.from('proof')),'sha256:'+createHash('sha256').update('proof').digest('hex'));}
  });
  assert.deepEqual(f.calls,['/test-runner/claim','/test-runner/evidence','/test-runner/report','/test-runner/release']);
 }finally{await f.close();}
});
test('failed evidence upload never sends success report',async()=>{
 const f=await fixture(false,true);
 try{
  await assert.rejects(f.client.runOnce({execute:async()=>report,cleanup:async()=>{},
   uploadEvidence:async upload=>{await upload('text/plain',Buffer.from('proof'));}
  }),/400/);
  assert.deepEqual(f.calls,['/test-runner/claim','/test-runner/evidence']);
 }finally{await f.close();}
});
