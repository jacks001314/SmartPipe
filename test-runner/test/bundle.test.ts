import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp,mkdir,writeFile,readFile,rm } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { BundleSwapScenario,type BundleScenarioConfig } from '../src/bundle-scenario.js';
import type { Assignment } from '../src/worker.js';

const platform=(process.platform==='win32'?'windows':process.platform)+'-'+(process.arch==='x64'?'amd64':process.arch);
const tarPath=process.platform==='win32'?join(process.env.SystemRoot??'C:\\Windows','System32','tar.exe'):'/usr/bin/tar';
async function sha(path:string){return createHash('sha256').update(await readFile(path)).digest('hex');}
const node=(id:string,code:string)=>({id,executable:process.execPath,args:['-e',code],timeoutMs:10_000});
const readMain=`const fs=require('fs');const p=require('path').join(process.env.SMARTPIPE_SYNTROPY_ROOT,'app','main.txt');process.exit(fs.readFileSync(p,'utf8')==='%s'?0:1)`;

async function fixture(manifestTarget='app/main.txt',bundleComponentHash?:string){
 const root=await mkdtemp(join(tmpdir(),'sp-bundle-'));
 const install=join(root,'install'),bundleDir=join(root,'bundle');
 await mkdir(join(install,'app'),{recursive:true});
 await mkdir(join(bundleDir,'payload'),{recursive:true});
 await writeFile(join(install,'app','main.txt'),'old');
 await writeFile(join(bundleDir,'payload','main.txt'),'new');
 await writeFile(join(bundleDir,'bundle.manifest.json'),JSON.stringify({format:1,components:[{
  slot:'desktop-assets',source:'payload/main.txt',target:manifestTarget,
  sha256:bundleComponentHash??await sha(join(bundleDir,'payload','main.txt'))}]}));
 const bundle=join(root,'candidate.tar.gz');
 const tar=spawnSync(tarPath,['-czf',bundle,'-C',bundleDir,'.'],{encoding:'utf8'});
 assert.equal(tar.status,0,tar.stderr??'tar failed');
 const config:BundleScenarioConfig={name:'syntropy-hotswap',version:'1',stateRoot:join(root,'state'),workingDirectory:root,
  bundlePath:bundle,installRoot:install,tarPath,scripts:[{path:tarPath,sha256:await sha(tarPath)}],
  stop:[node('stop','process.exit(0)')],start:[node('start','process.exit(0)')],
  checks:[node('content',readMain.replace('%s','new')),
   {...node('screenshot',`require('fs').writeFileSync(require('path').join(process.env.SMARTPIPE_TEST_OUTPUT,'shot.png'),Buffer.from('89504e470d0a1a0a','hex'))`),evidenceFile:'shot.png',evidenceMime:'image/png'}],
  cleanup:[node('restored',readMain.replace('%s','old'))]};
 const assignment:Assignment={machine_id:'m',generation:1,expires_at:new Date(Date.now()+60000).toISOString(),
  run:{id:'run-1',spec:{project_id:'p',source_sha:'a'.repeat(40),artifact_sha256:await sha(bundle),
   scenario:config.name,scenario_version:'1',platform,required_checks:['content','screenshot']}}};
 return {root,install,config,assignment};
}

test('verified bundle swaps a component, attaches screenshot evidence, then restores it',async()=>{
 const f=await fixture();
 const scenario=new BundleSwapScenario(f.config);
 try{
  const report=await scenario.execute(f.assignment,new AbortController().signal);
  assert.deepEqual(report.checks.map(c=>c.status),['PASS','PASS']);
  assert.equal(await readFile(join(f.install,'app','main.txt'),'utf8'),'new');
  await scenario.cleanup();
  assert.equal(await readFile(join(f.install,'app','main.txt'),'utf8'),'old');
  await scenario.uploadEvidence(async(mime,bytes)=>{
   if(mime==='image/png'){assert.equal(bytes.subarray(0,4).toString('hex'),'89504e47');return 'sha256:'+'a'.repeat(64);}
   assert.equal(mime,'application/json');return 'sha256:'+'b'.repeat(64);
  });
  assert.equal(report.checks[1]?.evidence,'sha256:'+'a'.repeat(64));
 }finally{await scenario.cleanup();await rm(f.root,{recursive:true,force:true});}
});

test('wrong bundle digest blocks the run without touching the installation or quarantining it',async()=>{
 const f=await fixture();f.assignment.run.spec.artifact_sha256='c'.repeat(64);
 const scenario=new BundleSwapScenario(f.config);
 try{
  const report=await scenario.execute(f.assignment,new AbortController().signal);
  assert.deepEqual(report.checks.map(c=>c.status),['BLOCKED','BLOCKED']);
  assert.equal(await readFile(join(f.install,'app','main.txt'),'utf8'),'old');
  await scenario.cleanup();
  await scenario.uploadEvidence(async(mime)=>{assert.equal(mime,'application/json');return 'sha256:'+'e'.repeat(64);});
  assert.ok(report.checks.every(c=>c.evidence==='sha256:'+'e'.repeat(64)));
 }finally{await scenario.cleanup();await rm(f.root,{recursive:true,force:true});}
});

test('component digest mismatch blocks before replacing files',async()=>{
 const f=await fixture('app/main.txt','d'.repeat(64));
 const scenario=new BundleSwapScenario(f.config);
 try{
  const report=await scenario.execute(f.assignment,new AbortController().signal);
  assert.deepEqual(report.checks.map(c=>c.status),['BLOCKED','BLOCKED']);
  assert.equal(await readFile(join(f.install,'app','main.txt'),'utf8'),'old');
 }finally{await scenario.cleanup();await rm(f.root,{recursive:true,force:true});}
});

test('target escaping the install root blocks without writing outside it',async()=>{
 const f=await fixture('../escape.txt');
 const scenario=new BundleSwapScenario(f.config);
 try{
  const report=await scenario.execute(f.assignment,new AbortController().signal);
  assert.deepEqual(report.checks.map(c=>c.status),['BLOCKED','BLOCKED']);
  await assert.rejects(readFile(join(f.root,'escape.txt'),'utf8'),{code:'ENOENT'});
 }finally{await scenario.cleanup();await rm(f.root,{recursive:true,force:true});}
});

test('scenario mismatch is a blocked verdict rather than a stuck lease',async()=>{
 const f=await fixture();f.assignment.run.spec.scenario_version='2';
 const scenario=new BundleSwapScenario(f.config);
 try{
  const report=await scenario.execute(f.assignment,new AbortController().signal);
  assert.deepEqual(report.checks.map(c=>c.status),['BLOCKED','BLOCKED']);
  assert.equal(await readFile(join(f.install,'app','main.txt'),'utf8'),'old');
 }finally{await scenario.cleanup();await rm(f.root,{recursive:true,force:true});}
});

test('another live runner holding the lease keeps the run quarantined',async()=>{
 const f=await fixture();
 const { DesktopLease }=await import('../src/session.js');
 const holder=await DesktopLease.acquire(f.config.stateRoot,60_000);
 const scenario=new BundleSwapScenario(f.config);
 try{
  await assert.rejects(scenario.execute(f.assignment,new AbortController().signal),{code:'EEXIST'});
 }finally{await holder.close();await scenario.cleanup();await rm(f.root,{recursive:true,force:true});}
});

test('a check that cannot produce its declared evidence fails',async()=>{
 const f=await fixture();
 f.config.checks[1]={...f.config.checks[1]!,args:['-e','process.exit(0)']};
 const scenario=new BundleSwapScenario(f.config);
 try{
  const report=await scenario.execute(f.assignment,new AbortController().signal);
  assert.equal(report.checks[1]?.status,'FAIL');
 }finally{await scenario.cleanup();await rm(f.root,{recursive:true,force:true});}
});
