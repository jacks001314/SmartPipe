import { spawn } from 'node:child_process';
import { mkdir, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { resolve, join } from 'node:path';

const root=fileURLToPath(new URL('../',import.meta.url));
const output=resolve(root,process.argv[2]??'reports/online-test');
await mkdir(output,{recursive:true});
const npm=process.platform==='win32'?'npm.cmd':'npm';
const stages=[
 {name:'runner',cwd:'test-runner',command:npm,args:['test']},
 {name:'web',cwd:'web',command:npm,args:['run','build']},
 {name:'backend',cwd:'backend',command:'go',args:['test','./internal/testlab','./internal/store','./internal/adminapi','./internal/releasemcp','-json','-count=1']},
];
const results=[];
for(const stage of stages){
 const start=Date.now();
 const result=await new Promise(resolveResult=>{
  // Windows npm.cmd requires cmd.exe; all shell text here is fixed, not input.
  const windowsNpm=process.platform==='win32'&&stage.command==='npm.cmd';
  const child=spawn(windowsNpm?'cmd.exe':stage.command,windowsNpm?['/d','/s','/c',stage.args[0]==='test'?'npm.cmd test':'npm.cmd run build']:stage.args,{
   cwd:join(root,stage.cwd),env:{...process.env,GOSUMDB:process.env.GOSUMDB==='off'?'sum.golang.org':process.env.GOSUMDB??'sum.golang.org'},windowsHide:true,
   stdio:['ignore','pipe','pipe']
  });
  let stdout='',stderr='';
  child.stdout.on('data',b=>{stdout+=b.toString();});child.stderr.on('data',b=>{stderr+=b.toString();});
  child.once('error',err=>resolveResult({code:-1,stdout,stderr:stderr+'\n'+err.message}));
  child.once('close',code=>resolveResult({code,stdout,stderr}));
 });
 await writeFile(join(output,stage.name+'.log'),result.stdout+'\n'+result.stderr);
 const skipped=[];
 if(stage.name==='backend'){
  for(const line of result.stdout.split('\n')){
   try{const event=JSON.parse(line);if(event.Action==='skip'&&event.Test)skipped.push({package:event.Package,test:event.Test});}catch{}
  }
 }
 const entry={name:stage.name,exitCode:result.code,durationMs:Date.now()-start,skipped,
 status:result.code!==0?'FAIL':skipped.length?'PARTIAL':'PASS',log:stage.name+'.log'};
 results.push(entry);console.log(stage.name+': '+entry.status+' ('+skipped.length+' skipped)');
}
const report={
 generatedAt:new Date().toISOString(),
 status:results.some(r=>r.status==='FAIL')?'FAIL':results.some(r=>r.status==='PARTIAL')?'PARTIAL':'PASS',
 scope:'Code validation only; never a product release acceptance.',
 stages:results,
 unverified:['Windows Qwen native desktop','macOS Qwen native desktop','Syntropy installer and running artifact identity','Real model coding scenario','Upgrade and recovery','Browser interaction of test console'],
};
await writeFile(join(output,'report.json'),JSON.stringify(report,null,2)+'\n');
if(report.status==='FAIL')process.exitCode=1;
// PARTIAL deliberately differs from PASS so CI cannot silently accept skipped DB checks.
else if(report.status==='PARTIAL')process.exitCode=2;
