import { access, realpath } from 'node:fs/promises';
import { isAbsolute } from 'node:path';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';

export interface PreflightConfig { command: string; args: string[]; timeoutMs?: number }
export interface PreflightResult {
 status: 'READY' | 'BLOCKED'; platform: string; node: string;
 checks: { id: string; ok: boolean }[];
 tools: string[];
}
/** Protocol readiness only. Never executes desktop actions or claims product acceptance. */
export async function preflight(config: PreflightConfig): Promise<PreflightResult> {
 const result: PreflightResult = {status:'BLOCKED',platform:process.platform+'-'+process.arch,
 node:process.versions.node,checks:[],tools:[]};
 const check=(id:string,ok:boolean)=>{result.checks.push({id,ok});return ok;};
 if(!check('node_24_or_newer',Number(process.versions.node.split('.')[0])>=24)) return result;
 if(!check('absolute_executable',isAbsolute(config.command))) return result;
 try { await access(await realpath(config.command)); } catch { check('executable_exists',false); return result; }
 check('executable_exists',true);
 const env: Record<string,string>={};
 for(const key of ['PATH','SystemRoot','WINDIR','TEMP','TMP','HOME','USERPROFILE','LOCALAPPDATA','APPDATA']){
  if(process.env[key]) env[key]=process.env[key]!;
 }
 const client=new Client({name:'smartpipe-preflight',version:'0.1.0'});
 const transport=new StdioClientTransport({command:config.command,args:config.args,env,stderr:'ignore'});
 const abort=new AbortController();
 const timeout=config.timeoutMs??10_000;
 const timer=setTimeout(()=>{abort.abort();void client.close();},timeout);
 try {
  await client.connect(transport,{signal:abort.signal,timeout});
  check('mcp_connect',true);
  const seen=new Set<string>();
  let cursor:string|undefined;
  do {
   const page=await client.listTools(cursor?{cursor}:{},{signal:abort.signal,timeout});
   result.tools.push(...page.tools.map(t=>t.name));
   cursor=page.nextCursor;
   if(cursor && seen.has(cursor)) throw new Error('Repeated cursor');
   if(cursor) seen.add(cursor);
   if(seen.size>100) throw new Error('Too many pages');
  } while(cursor);
  check('qwen_js_tool',result.tools.includes('js'));
  check('qwen_reset_tool',result.tools.includes('js_reset'));
  result.status=result.checks.every(c=>c.ok)?'READY':'BLOCKED';
 } catch {
  check('mcp_discovery',false);
 } finally {
  clearTimeout(timer);
  await client.close();
  await transport.close();
 }
 return result;
}
