import { readFile, writeFile } from 'node:fs/promises';
import { preflight } from './preflight.js';
const [configPath,reportPath]=process.argv.slice(2);
if(!configPath||!reportPath) throw new Error('Usage: preflight-cli CONFIG_JSON REPORT_JSON');
const config=JSON.parse(await readFile(configPath,'utf8'));
if(typeof config.command!=='string'||!Array.isArray(config.args)||!config.args.every((v:unknown)=>typeof v==='string'))
 throw new Error('Config requires command and string args');
const result=await preflight({command:config.command,args:config.args});
await writeFile(reportPath,JSON.stringify({...result,scope:'MCP discovery only; no desktop or product acceptance'},null,2)+'\n',{flag:'wx'});
console.log(result.status);
if(result.status!=='READY')process.exitCode=1;
