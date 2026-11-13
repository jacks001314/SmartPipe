import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, rm, readdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { ObserveScenario } from '../src/observe-scenario.js';
import type { Assignment } from '../src/worker.js';

test('artifact mismatch blocks before desktop process and cleanup releases lock',async()=>{
 const root=await mkdtemp(join(tmpdir(),'smartpipe-observe-'));
 const artifact=join(root,'artifact.bin');await writeFile(artifact,'candidate');
 const scenario=new ObserveScenario({stateRoot:root,artifactPath:artifact,app:'Syntropy',command:process.execPath,
 args:[fileURLToPath(new URL('./upstream-fixture.js',import.meta.url))]});
 const a:Assignment={machine_id:'m',generation:1,expires_at:new Date(Date.now()+60000).toISOString(),run:{id:'r',spec:{
 project_id:'p',source_sha:'a'.repeat(40),artifact_sha256:'b'.repeat(64),scenario:'desktop-observe',scenario_version:'1',
 platform:(process.platform==='win32'?'windows':process.platform)+'-'+(process.arch==='x64'?'amd64':process.arch),
 required_checks:['desktop_observed']}}};
 try{
  await assert.rejects(scenario.execute(a,new AbortController().signal),/SHA256 mismatch/);
  await scenario.cleanup();
  assert.deepEqual(await readdir(root),['artifact.bin']);
 }finally{await scenario.cleanup();await rm(root,{recursive:true,force:true});}
});
