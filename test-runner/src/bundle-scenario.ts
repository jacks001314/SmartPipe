import { createReadStream } from 'node:fs';
import { copyFile, mkdir, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { dirname, isAbsolute, join, resolve, sep } from 'node:path';
import { DesktopLease, Evidence } from './session.js';
import { executeStep, type CommandStep } from './command-scenario.js';
import type { Assignment, Report, Scenario, Spec } from './worker.js';

/** A component is one file inside the candidate bundle. */
export interface BundleComponent {
 slot?:string;
 /** Path inside the extracted bundle. */
 source:string;
 /** Path relative to installRoot that gets replaced. */
 target:string;
 sha256:string;
 /** Optional unix mode for executables (ignored on Windows). */
 mode?:number;
}
export interface BundleManifest {
 format:1;
 /** Optional expected product version; checks can assert it via SMARTPIPE_EXPECT_VERSION. */
 version?:string;
 components:BundleComponent[];
}

export interface BundleScenarioConfig {
 name:string; version:string;
 stateRoot:string; workingDirectory:string;
 /** Candidate .tar.gz whose SHA-256 must equal the assigned artifact_sha256. */
 bundlePath:string;
 /** Absolute installation root the targets are resolved against. */
 installRoot:string;
 tarPath?:string;
 scripts:Array<{path:string;sha256:string}>;
 stop:CommandStep[]; start:CommandStep[]; checks:CommandStep[]; cleanup:CommandStep[];
}
type Outcome={id:string;exitCode:number|null;stdout:string;stderr:string;truncated:boolean;note?:string;
 evidence?:{path:string;sha256:string;mimeType:string}};
interface Planned { component:BundleComponent; target:string; source:string; existed:boolean; sha256?:string; mode?:number }
const validID=/^[a-zA-Z0-9_-]{1,80}$/;
const validHash=/^[a-f0-9]{64}$/;

function checkStep(step:CommandStep,ids:Set<string>):void{
 if(!validID.test(step.id)||ids.has(step.id)||!isAbsolute(step.executable)||!Array.isArray(step.args)||
  !step.args.every(a=>typeof a==='string')||!Number.isSafeInteger(step.timeoutMs)||step.timeoutMs<100||step.timeoutMs>600_000)throw new Error('Invalid or duplicate step');
 ids.add(step.id);
}
export function validateBundleConfig(c:BundleScenarioConfig):void{
 if(!c.name||!c.version)throw new Error('Scenario name and version are required');
 if(![c.stateRoot,c.workingDirectory,c.bundlePath,c.installRoot].every(isAbsolute))throw new Error('Invalid bundle scenario paths');
 if(!c.scripts?.length||!c.checks?.length||!c.cleanup?.length||!c.stop?.length||!c.start?.length)throw new Error('Pinned scripts, stop/start, checks and cleanup are required');
 for(const script of c.scripts)if(!isAbsolute(script.path)||!validHash.test(script.sha256))throw new Error('Invalid script pin');
 const ids=new Set<string>();
 for(const step of [...c.stop,...c.start,...c.checks,...c.cleanup])checkStep(step,ids);
}

/** Resolve a bundle-relative target and refuse anything that escapes installRoot. */
function safeJoin(root:string,relative:string):string{
 if(!relative||isAbsolute(relative)||/^[a-zA-Z]:/.test(relative))throw new Error('Bundle path must be relative: '+relative);
 const parts=relative.split(/[\\/]+/);
 if(parts.some(p=>p===''||p==='.'||p==='..'))throw new Error('Bundle path has invalid segments: '+relative);
 const base=resolve(root),full=resolve(join(base,...parts));
 if(full!==base&&!full.startsWith(base+sep))throw new Error('Bundle path escapes install root: '+relative);
 return full;
}
function parseManifest(raw:string):BundleManifest{
 const manifest=JSON.parse(raw) as BundleManifest;
 if(!manifest||manifest.format!==1||!Array.isArray(manifest.components))throw new Error('Unsupported bundle manifest');
 if(manifest.version!==undefined&&(typeof manifest.version!=='string'||!manifest.version.trim()))throw new Error('Invalid bundle version');
 if(manifest.components.length<1||manifest.components.length>64)throw new Error('Bundle must contain 1..64 components');
 const targets=new Set<string>();
 for(const component of manifest.components){
  if(typeof component.source!=='string'||typeof component.target!=='string'||!validHash.test(component.sha256))throw new Error('Invalid component entry');
  if(component.mode!==undefined&&(!Number.isInteger(component.mode)||component.mode<0||component.mode>0o777))throw new Error('Invalid component mode');
  const key=component.target.replace(/\\/g,'/').toLowerCase();
  if(targets.has(key))throw new Error('Duplicate bundle target: '+component.target);
  targets.add(key);
 }
 return manifest;
}
async function sha256File(path:string):Promise<string>{
 const hash=createHash('sha256');
 for await(const chunk of createReadStream(path))hash.update(chunk);
 return hash.digest('hex');
}

/**
 * BundleSwapScenario replaces files of an installed product with a verified
 * candidate bundle, starts it, runs independent checks (optionally attaching a
 * screenshot per check), then restores the previous files during cleanup.
 */
export class BundleSwapScenario implements Scenario{
 readonly capabilities:string[];
 private lease?:DesktopLease;
 private outcomes:Outcome[]=[];
 private report?:Report;
 private context?:Record<string,string>;
 private evidence?:Evidence;
 private plan:Planned[]=[];
 private runID="";
 private touched=false;
 private cleanupStarted=false;
 constructor(private config:BundleScenarioConfig){validateBundleConfig(config);this.capabilities=[config.name+'@'+config.version];}
 private async run(step:CommandStep,signal:AbortSignal):Promise<Outcome>{
  const result=await executeStep(step,this.config.workingDirectory,this.context??{},signal);
  this.outcomes.push(result);
  return result;
 }
 private tarExecutable():string{
  if(this.config.tarPath)return this.config.tarPath;
  return process.platform==='win32'?join(process.env.SystemRoot??'C:\\Windows','System32','tar.exe'):'/usr/bin/tar';
 }
 /**
  * A failure before the first installed file is touched is a verdict, not a
  * reason to quarantine the machine. The run becomes BLOCKED and the lease is
  * released after cleanup, so a stale candidate cannot stall the lab.
  */
 private blockedReport(s:Spec,reason:string):Report{
  const message=reason.length>2000?reason.slice(0,2000):reason;
  this.outcomes.push({id:'preflight',exitCode:null,stdout:'',stderr:message,truncated:false,note:message});
  this.report={source_sha:s.source_sha,artifact_sha256:s.artifact_sha256,scenario_version:s.scenario_version,platform:s.platform,
   checks:this.config.checks.map(step=>({id:step.id,status:'BLOCKED' as const,evidence:''}))};
  return this.report;
 }
 async execute(a:Assignment,signal:AbortSignal):Promise<Report>{
  if(this.lease)throw new Error('Previous assignment has not been cleaned');
  this.context=undefined;this.report=undefined;this.evidence=undefined;this.plan=[];this.runID="";this.touched=false;
  this.outcomes=[];this.cleanupStarted=false;
  const c=this.config,s=a.run.spec;
  // validate/prepare failures are safe to report; lease conflicts and any
  // failure after the installation is touched still leave the machine busy.
  let stage:'validate'|'lease'|'prepare'|'mutate'='validate';
  try{
  if(s.scenario!==c.name||s.scenario_version!==c.version)throw new Error('Scenario mismatch');
  const platform=(process.platform==='win32'?'windows':process.platform)+'-'+(process.arch==='x64'?'amd64':process.arch);
  if(s.platform!==platform)throw new Error('Platform mismatch');
  if(!s.required_checks.every(id=>c.checks.some(step=>step.id===id)))throw new Error('Required check not deployed locally');
  signal.throwIfAborted();
  for(const pin of c.scripts){
   if(await sha256File(pin.path)!==pin.sha256)throw new Error('Scenario script changed: '+pin.path);
  }
  stage='lease';
  this.lease=await DesktopLease.acquire(c.stateRoot,20*60_000);
  stage='prepare';
  this.runID=a.run.id;
  if(await sha256File(c.bundlePath)!==s.artifact_sha256)throw new Error('Bundle hash mismatch');
  this.evidence=await Evidence.create(join(c.stateRoot,'runs'),{runId:a.run.id,spec:s,
   scenarioConfigSHA256:createHash('sha256').update(JSON.stringify(c)).digest('hex'),installRoot:c.installRoot});
  const extract=join(this.evidence.directory,'bundle');
  await mkdir(extract,{recursive:true});
  const extracted=await this.run({id:'extract',executable:this.tarExecutable(),args:['-xzf',c.bundlePath,'-C',extract],timeoutMs:180_000},signal);
  if(extracted.exitCode!==0)throw new Error('Bundle extraction failed: '+(extracted.stderr||extracted.stdout));
  const manifest=parseManifest(await readFile(join(extract,'bundle.manifest.json'),'utf8'));
  this.plan=[];
  for(const component of manifest.components){
   const source=safeJoin(extract,component.source);
   const target=safeJoin(c.installRoot,component.target);
   if(await sha256File(source)!==component.sha256)throw new Error('Bundle component hash mismatch: '+component.target);
   this.plan.push({component,target,source,existed:false});
  }
  const backupRoot=join(c.stateRoot,'backups',a.run.id);
  for(const item of this.plan){
   try{
    const info=await stat(item.target);
    if(!info.isFile())throw new Error('existing target is not a file: '+item.target);
    item.existed=true;item.sha256=await sha256File(item.target);item.mode=process.platform==='win32'?undefined:info.mode&0o777;
    const backup=join(backupRoot,...item.component.target.split(/[\\/]+/));
    await mkdir(dirname(backup),{recursive:true});
    await copyFile(item.target,backup);
   }catch(error){
    if((error as {code?:string}).code!=='ENOENT')throw error;
   }
  }
  this.context={SMARTPIPE_TEST_RUN_ID:a.run.id,SMARTPIPE_TEST_ARTIFACT:c.bundlePath,
   SMARTPIPE_TEST_ARTIFACT_SHA256:s.artifact_sha256,SMARTPIPE_TEST_SOURCE_SHA:s.source_sha,
   SMARTPIPE_TEST_OUTPUT:this.evidence.directory,SMARTPIPE_SYNTROPY_ROOT:c.installRoot};
  if(manifest.version)this.context.SMARTPIPE_EXPECT_VERSION=manifest.version;
  let setupOK=true;
  stage='mutate';
  this.touched=true;
  for(const step of c.stop){
   this.lease.assertActive();const result=await this.run(step,signal);
   if(result.exitCode!==0){setupOK=false;break;}
  }
  if(setupOK){
   try{
    for(const item of this.plan){
     await mkdir(dirname(item.target),{recursive:true});
     await copyFile(item.source,item.target);
     if(item.component.mode!==undefined&&process.platform!=='win32')await (await import('node:fs/promises')).chmod(item.target,item.component.mode);
    }
   }catch(error){setupOK=false;this.outcomes.push({id:'swap',exitCode:null,stdout:'',stderr:'',truncated:false,note:(error as Error).message});}
  }
  if(setupOK){
   for(const step of c.start){
    this.lease.assertActive();const result=await this.run(step,signal);
    if(result.exitCode!==0){setupOK=false;break;}
   }
  }
  const checks:Report['checks']=[];
  for(const step of c.checks){
   if(!setupOK){checks.push({id:step.id,status:'BLOCKED',evidence:''});continue;}
   this.lease.assertActive();const result=await this.run(step,signal);
   if(result.exitCode===0&&step.evidenceFile){
    try{
     const path=isAbsolute(step.evidenceFile)?step.evidenceFile:join(this.evidence.directory,step.evidenceFile);
     const info=await stat(path);
     if(!info.isFile()||info.size===0||info.size>8*1024*1024)throw new Error('evidence file must be 1 byte..8 MiB');
     const mimeType=step.evidenceMime??(process.platform==='win32'&&/\.bmp$/i.test(path)?'image/png':'image/png');
     result.evidence={path,sha256:await sha256File(path),mimeType};
    }catch(error){result.exitCode=null;result.note='evidence: '+(error as Error).message;}
   }
   checks.push({id:step.id,status:result.exitCode===0?'PASS':'FAIL',evidence:''});
  }
  this.report={source_sha:s.source_sha,artifact_sha256:s.artifact_sha256,scenario_version:s.scenario_version,platform:s.platform,checks};
  return this.report;
  }catch(error){
   if(stage==='mutate'||stage==='lease'||signal.aborted)throw error;
   return this.blockedReport(s,error instanceof Error?error.message:String(error));
  }
 }
 async cleanup():Promise<void>{
  if(!this.lease)return;
  if(this.cleanupStarted)throw new Error('Cleanup outcome uncertain; machine must remain quarantined');
  this.cleanupStarted=true;
  if(!this.touched){
   if(this.evidence)await writeFile(join(this.evidence.directory,'checks.json'),JSON.stringify(this.outcomes,null,2));
   await this.lease.close();this.lease=undefined;return;
  }
  const failures:string[]=[];
  for(const step of this.config.stop){
   try{const result=await this.run(step,new AbortController().signal);if(result.exitCode!==0)failures.push(step.id);}
   catch{failures.push(step.id);}
  }
  for(const item of [...this.plan].reverse()){
   if(item.existed){
    const backup=join(this.config.stateRoot,'backups',this.runID,...item.component.target.split(/[\\/]+/));
    await copyFile(backup,item.target);
    if(await sha256File(item.target)!==item.sha256)throw new Error('Restored digest mismatch: '+item.component.target);
    if(item.mode!==undefined&&process.platform!=='win32')await (await import('node:fs/promises')).chmod(item.target,item.mode);
   }else{
    await rm(item.target,{force:true});
   }
  }
  for(const step of this.config.cleanup){
   try{const result=await this.run(step,new AbortController().signal);if(result.exitCode!==0)failures.push(step.id);}
   catch{failures.push(step.id);}
  }
  if(this.evidence)await writeFile(join(this.evidence.directory,'checks.json'),JSON.stringify(this.outcomes,null,2));
  await this.lease.close();this.lease=undefined;
  if(failures.length)throw new Error('Cleanup verification failed: '+failures.join(', '));
 }
 async uploadEvidence(upload:(mime:string,bytes:Buffer)=>Promise<string>):Promise<void>{
  if(!this.report)return;
  if(this.lease)throw new Error('Cleanup must succeed before uploading a report');
  const byId=new Map(this.outcomes.map(o=>[o.id,o]));
  let shared:string|undefined;
  for(const check of this.report.checks){
   const evidence=byId.get(check.id)?.evidence;
   if(evidence){check.evidence=await upload(evidence.mimeType,await readFile(evidence.path));continue;}
   shared??=await upload('application/json',Buffer.from(JSON.stringify(this.outcomes)));
   check.evidence=shared;
  }
 }
}
