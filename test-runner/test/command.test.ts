import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp,writeFile,readFile,rm } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { CommandScenario,executeStep,type CommandScenarioConfig } from '../src/command-scenario.js';
import type { Assignment } from '../src/worker.js';
async function fixture(){
 const root=await mkdtemp(join(tmpdir(),'sp-command-'));
 const script=join(root,'fixture.mjs'),artifact=join(root,'artifact.bin');
 const source="import fs from 'node:fs'; if(process.argv[2]==='setup')fs.copyFileSync(process.env.SMARTPIPE_TEST_ARTIFACT,'installed.bin'); else if(process.argv[2]==='check'){if(fs.readFileSync('installed.bin','utf8')!=='candidate')process.exit(1); console.log('file verified');} else if(process.argv[2]==='cleanup'){fs.rmSync('installed.bin',{force:true});} else process.exit(2);";
 await writeFile(script,source);await writeFile(artifact,'candidate');
 const step=(id:string,mode:string)=>({id,executable:process.execPath,args:[script,mode],timeoutMs:5000});
 const config:CommandScenarioConfig={name:'fixture-install',version:'1',stateRoot:join(root,'state'),artifactPath:artifact,workingDirectory:root,
 scripts:[{path:script,sha256:createHash('sha256').update(source).digest('hex')}],
 setup:[step('install','setup')],checks:[step('file','check')],cleanup:[step('stop','cleanup')]};
 const assignment:Assignment={machine_id:'m',generation:1,expires_at:new Date(Date.now()+60000).toISOString(),run:{id:'r',spec:{
 project_id:'p',source_sha:'a'.repeat(40),artifact_sha256:createHash('sha256').update('candidate').digest('hex'),
 scenario:config.name,scenario_version:'1',platform:(process.platform==='win32'?'windows':process.platform)+'-'+(process.arch==='x64'?'amd64':process.arch),required_checks:['file']}}};
 return {root,config,assignment};
}
test('real commands install fixture, independently verify, clean and upload report',async()=>{
 const f=await fixture();const scenario=new CommandScenario(f.config);
 try{
  const report=await scenario.execute(f.assignment,new AbortController().signal);
  assert.equal(report.checks[0]?.status,'PASS');
  assert.equal(await readFile(join(f.root,'installed.bin'),'utf8'),'candidate');
  await scenario.cleanup();
  await assert.rejects(readFile(join(f.root,'installed.bin')));
  await scenario.uploadEvidence(async(mime,bytes)=>{
   assert.equal(mime,'application/json');
   const rows=JSON.parse(bytes.toString());assert.deepEqual(rows.map((r:{id:string})=>r.id),['install','file','stop']);
   return 'sha256:'+'f'.repeat(64);
  });
  assert.equal(report.checks[0]?.evidence,'sha256:'+'f'.repeat(64));
 }finally{await scenario.cleanup();await rm(f.root,{recursive:true,force:true});}
});
test('modified pinned script rejected before setup',async()=>{
 const f=await fixture();const scenario=new CommandScenario(f.config);
 try{
  await writeFile(f.config.scripts[0]!.path,'changed');
  await assert.rejects(scenario.execute(f.assignment,new AbortController().signal),/script changed/);
  await assert.rejects(readFile(join(f.root,'installed.bin')));
 }finally{await scenario.cleanup();await rm(f.root,{recursive:true,force:true});}
});
test('nonzero independent check records FAIL',async()=>{
 const f=await fixture();f.config.checks[0]!.args[1]='fail';
 const scenario=new CommandScenario(f.config);
 try{
  const report=await scenario.execute(f.assignment,new AbortController().signal);
  assert.equal(report.checks[0]?.status,'FAIL');await scenario.cleanup();
 }finally{await scenario.cleanup();await rm(f.root,{recursive:true,force:true});}
});
test('timed out process does not report success',async()=>{
 await assert.rejects(executeStep({id:'hang',executable:process.execPath,args:['-e','setInterval(()=>{},1000)'],timeoutMs:100},process.cwd(),{},new AbortController().signal),/timed out/);
});
