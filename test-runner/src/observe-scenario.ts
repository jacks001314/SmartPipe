import { runtimeEnv } from './runtime-env.js';
import { createReadStream } from 'node:fs';
import { createHash, randomUUID } from 'node:crypto';
import { isAbsolute, join } from 'node:path';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';
import { DesktopLease, Evidence } from './session.js';
import type { Assignment, Report, Scenario } from './worker.js';

export interface ObserveConfig {
 stateRoot: string; artifactPath: string; app: string;
 command: string; args: string[];
}
/** Initial read-only Qwen Node REPL scenario, not a product acceptance suite. */
export class ObserveScenario implements Scenario {
 readonly capabilities = ['desktop-observe@1'];
 private client?: Client;
 private lease?: DesktopLease;
 private screenshot?: {mimeType:string;bytes:Buffer};
 private report?: Report;
 constructor(private config: ObserveConfig) {
  if (![config.stateRoot,config.artifactPath,config.command].every(isAbsolute)) throw new Error('Absolute paths required');
 }
 async execute(a: Assignment, signal: AbortSignal): Promise<Report> {
  this.screenshot=undefined;this.report=undefined;
  const s=a.run.spec;
  if(s.scenario!=='desktop-observe'||s.scenario_version!=='1') throw new Error('Unsupported local scenario');
  const platform=(process.platform==='win32'?'windows':process.platform)+'-'+(process.arch==='x64'?'amd64':process.arch);
  if(platform!==s.platform) throw new Error('Assigned platform differs from actual platform');
  signal.throwIfAborted();
  this.lease=await DesktopLease.acquire(this.config.stateRoot,20*60_000);
  const hash=createHash('sha256');
  for await(const chunk of createReadStream(this.config.artifactPath,{signal})) hash.update(chunk);
  if(hash.digest('hex')!==s.artifact_sha256) throw new Error('Local artifact SHA256 mismatch');
  const evidence=await Evidence.create(join(this.config.stateRoot,'runs'),{
   kind:'desktop-observe',runId:a.run.id,spec:s,verdict:'NOT_EVALUATED',
  });
  this.client=new Client({name:'smartpipe-observer',version:'0.1.0'});
  // The machine API token must not be inherited by the REPL child.
 const env = runtimeEnv();
  const stop=()=>{void this.client?.close();};
  signal.addEventListener('abort',stop,{once:true});
  try {
   await this.client.connect(new StdioClientTransport({command:this.config.command,args:this.config.args,env,stderr:'inherit'}));
   signal.throwIfAborted();
   this.lease.assertActive();
   const state=await this.client.callTool({name:'js',arguments:{code:'await cua.getState();'}},undefined,{signal,timeout:30_000});
   await evidence.record('js',state,randomUUID());
   if(state.isError)throw new Error('Qwen desktop discovery failed');
   this.lease.assertActive();
   const observation=await this.client.callTool({name:'js',arguments:{
    code:'var observedApp = await cua.getApp('+JSON.stringify(this.config.app)+'); await observedApp.getAXStateAndScreenshot();'
   }},undefined,{signal,timeout:30_000});
   await evidence.record('js',observation,randomUUID());
   signal.throwIfAborted();
   const image=Array.isArray(observation.content)?observation.content.find(c=>c.type==='image'):undefined;
   if(image && typeof image.data==='string' && typeof image.mimeType==='string') this.screenshot={mimeType:image.mimeType,bytes:Buffer.from(image.data,'base64')};
   this.report = {
    source_sha:s.source_sha,artifact_sha256:s.artifact_sha256,scenario_version:s.scenario_version,platform:s.platform,
    checks:[{id:'desktop_observed',status:!observation.isError&&image?'PASS':'BLOCKED',evidence:''}],
   };
   return this.report;
  } finally { signal.removeEventListener('abort',stop); }
 }
 async uploadEvidence(upload:(mimeType:string,bytes:Buffer)=>Promise<string>):Promise<void>{
  if(this.screenshot && this.report?.checks[0]) {
   this.report.checks[0].evidence=await upload(this.screenshot.mimeType,this.screenshot.bytes);
  }
 }
 async cleanup(): Promise<void> {
  // If upstream termination fails, do not release the local lock.
  await this.client?.close();this.client=undefined;
  await this.lease?.close();this.lease=undefined;
 }
}
