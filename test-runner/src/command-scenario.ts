import { spawn } from 'node:child_process';
import { createReadStream } from 'node:fs';
import { readFile, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { isAbsolute, join } from 'node:path';
import { DesktopLease, Evidence } from './session.js';
import type { Assignment, Report, Scenario } from './worker.js';

export interface CommandStep {
 id:string; executable:string; args:string[]; timeoutMs:number;
}
export interface CommandScenarioConfig {
 name:string; version:string; stateRoot:string; artifactPath:string; workingDirectory:string;
 // Versioned local script files whose digests must be pinned by the operator.
 scripts:Array<{path:string;sha256:string}>;
 setup?:CommandStep[]; checks:CommandStep[]; cleanup:CommandStep[];
}
type Outcome={id:string;exitCode:number|null;stdout:string;stderr:string;truncated:boolean};
const validID=/^[a-zA-Z0-9_-]{1,80}$/;
export function validateCommandConfig(c:CommandScenarioConfig):void{
 if(!c.name||!c.version||![c.stateRoot,c.artifactPath,c.workingDirectory].every(isAbsolute))throw new Error('Invalid command scenario paths');
 if(!c.checks?.length||!c.cleanup?.length||!c.scripts?.length)throw new Error('Checks, pinned scripts and cleanup are required');
 const ids=new Set<string>();
 for(const step of [...c.setup??[],...c.checks,...c.cleanup]){
  if(!validID.test(step.id)||ids.has(step.id)||!isAbsolute(step.executable)||!Array.isArray(step.args)||
   !step.args.every(a=>typeof a==='string')||!Number.isSafeInteger(step.timeoutMs)||step.timeoutMs<100||step.timeoutMs>600_000)throw new Error('Invalid or duplicate step');
  ids.add(step.id);
 }
 for(const script of c.scripts)if(!isAbsolute(script.path)||!/^[a-f0-9]{64}$/.test(script.sha256))throw new Error('Invalid script pin');
}
export async function executeStep(step:CommandStep,cwd:string,extra:Record<string,string>,signal:AbortSignal):Promise<Outcome>{
 signal.throwIfAborted();
 const env:Record<string,string>={...extra};
 for(const key of ['PATH','SystemRoot','WINDIR','TEMP','TMP','HOME','USERPROFILE','LOCALAPPDATA','APPDATA'])if(process.env[key])env[key]=process.env[key]!;
 return new Promise((resolve,reject)=>{
  const child=spawn(step.executable,step.args,{cwd,env,shell:false,windowsHide:true,stdio:['ignore','pipe','pipe']});
  let stdout='',stderr='',truncated=false,stopped=false;
  const limit=128*1024;
  const append=(which:'stdout'|'stderr',data:Buffer)=>{
   const prior=which==='stdout'?stdout:stderr;const text=data.toString('utf8');
   const next=(prior+text).slice(0,limit);if(prior.length+text.length>limit)truncated=true;
   if(which==='stdout')stdout=next;else stderr=next;
  };
  child.stdout.on('data',(b:Buffer)=>append('stdout',b));child.stderr.on('data',(b:Buffer)=>append('stderr',b));
  const stop=()=>{stopped=true;child.kill('SIGKILL');};
  const timer=setTimeout(stop,step.timeoutMs);
  signal.addEventListener('abort',stop,{once:true});if(signal.aborted)stop();
  child.once('error',error=>{clearTimeout(timer);signal.removeEventListener('abort',stop);reject(error);});
  child.once('close',code=>{
   clearTimeout(timer);signal.removeEventListener('abort',stop);
   // Never claim cleanup from killing the direct process; descendants must be
   // stopped and verified by the separately provisioned cleanup script.
   if(stopped)reject(new Error('Step cancelled or timed out: '+step.id));
   else resolve({id:step.id,exitCode:code,stdout,stderr,truncated});
  });
 });
}
export class CommandScenario implements Scenario{
 readonly capabilities:string[];
 private lease?:DesktopLease;
 private outcomes:Outcome[]=[];
 private report?:Report;
 private context?:Record<string,string>;
 private evidence?:Evidence;
 private cleanupStarted=false;
 constructor(private config:CommandScenarioConfig){validateCommandConfig(config);this.capabilities=[config.name+'@'+config.version];}
 async execute(a:Assignment,signal:AbortSignal):Promise<Report>{
  if(this.lease)throw new Error('Previous assignment has not been cleaned');
  this.context=undefined;this.report=undefined;this.evidence=undefined;
  const c=this.config,s=a.run.spec;
  if(s.scenario!==c.name||s.scenario_version!==c.version)throw new Error('Scenario mismatch');
  const platform=(process.platform==='win32'?'windows':process.platform)+'-'+(process.arch==='x64'?'amd64':process.arch);
  if(s.platform!==platform)throw new Error('Platform mismatch');
  if(!s.required_checks.every(id=>c.checks.some(step=>step.id===id)))throw new Error('Required check not deployed locally');
  this.outcomes=[];this.cleanupStarted=false;
  signal.throwIfAborted();
  for(const pin of c.scripts){
   const digest=createHash('sha256').update(await readFile(pin.path)).digest('hex');
   if(digest!==pin.sha256)throw new Error('Scenario script changed: '+pin.path);
  }
  this.lease=await DesktopLease.acquire(c.stateRoot,20*60_000);
  const hash=createHash('sha256');
  for await(const chunk of createReadStream(c.artifactPath,{signal}))hash.update(chunk);
  if(hash.digest('hex')!==s.artifact_sha256)throw new Error('Artifact hash mismatch');
  this.evidence=await Evidence.create(join(c.stateRoot,'runs'),{runId:a.run.id,spec:s,scenarioConfigSHA256:createHash('sha256').update(JSON.stringify(c)).digest('hex')});
  this.context={SMARTPIPE_TEST_RUN_ID:a.run.id,SMARTPIPE_TEST_ARTIFACT:c.artifactPath,
   SMARTPIPE_TEST_ARTIFACT_SHA256:s.artifact_sha256,SMARTPIPE_TEST_SOURCE_SHA:s.source_sha,
   SMARTPIPE_TEST_OUTPUT:this.evidence.directory};
  let setupOK=true;
  for(const step of c.setup??[]){
   this.lease.assertActive();const result=await executeStep(step,c.workingDirectory,this.context,signal);this.outcomes.push(result);
   if(result.exitCode!==0){setupOK=false;break;}
  }
  const checks:Report['checks']=[];
  for(const step of c.checks){
   if(!setupOK){checks.push({id:step.id,status:'BLOCKED',evidence:''});continue;}
   this.lease.assertActive();const result=await executeStep(step,c.workingDirectory,this.context,signal);this.outcomes.push(result);
   checks.push({id:step.id,status:result.exitCode===0?'PASS':'FAIL',evidence:''});
  }
  this.report={source_sha:s.source_sha,artifact_sha256:s.artifact_sha256,scenario_version:s.scenario_version,platform:s.platform,checks};
  return this.report;
 }
 async cleanup():Promise<void>{
  if(!this.lease)return;
  if(this.cleanupStarted)throw new Error('Cleanup outcome uncertain; machine must remain quarantined');
  this.cleanupStarted=true;
  if(this.context){
   for(const step of this.config.cleanup){
    const result=await executeStep(step,this.config.workingDirectory,this.context,new AbortController().signal);this.outcomes.push(result);
    if(result.exitCode!==0)throw new Error('Cleanup verification failed: '+step.id);
   }
  }
  if(this.evidence)await writeFile(join(this.evidence.directory,'checks.json'),JSON.stringify(this.outcomes,null,2));
  await this.lease.close();this.lease=undefined;
 }
 async uploadEvidence(upload:(mime:string,bytes:Buffer)=>Promise<string>):Promise<void>{
  if(!this.report)return;
  if(this.lease)throw new Error('Cleanup must succeed before uploading a report');
  const reference=await upload('application/json',Buffer.from(JSON.stringify(this.outcomes)));
  for(const check of this.report.checks)check.evidence=reference;
 }
}
