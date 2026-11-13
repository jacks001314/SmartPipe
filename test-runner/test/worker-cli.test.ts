import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,copyFile,writeFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import {createServer} from 'node:http';
import {spawn} from 'node:child_process';

test('command worker CLI starts without desktop MCP dependencies',async()=>{
 const root=await mkdtemp(join(tmpdir(),'smartpipe-cli-'));
 const server=createServer((req,res)=>{
  assert.equal(req.url,'/test-runner/claim');
  res.writeHead(204);res.end();
 });
 try{
  // Deliberately copy only the command runtime, without node_modules or observe-scenario.
  for(const name of ['worker-cli.js','worker.js','command-scenario.js','session.js'])
   await copyFile(resolve('dist/src',name),join(root,name));
  await writeFile(join(root,'package.json'),'{"type":"module"}');
  await new Promise<void>(r=>server.listen(0,'127.0.0.1',r));
  const address=server.address() as {port:number};
  const script=join(root,'check.mjs');
  await writeFile(script,'');
  const config={apiURL:'http://127.0.0.1:'+address.port,machineID:'fixture',
   commandScenario:{name:'cli-check',version:'1',stateRoot:root,artifactPath:script,workingDirectory:root,
    scripts:[{path:script,sha256:'a'.repeat(64)}],
    checks:[{id:'check',executable:process.execPath,args:[script],timeoutMs:1000}],
    cleanup:[{id:'cleanup',executable:process.execPath,args:[script],timeoutMs:1000}]}};
  const path=join(root,'config.json');await writeFile(path,JSON.stringify(config));
  const result=await new Promise<{code:number|null;output:string}>((resolve,reject)=>{
   const child=spawn(process.execPath,[join(root,'worker-cli.js'),path],{
    env:{...process.env,SMARTPIPE_TEST_MACHINE_TOKEN:'synthetic-test-token'},windowsHide:true,
    stdio:['ignore','pipe','pipe'],timeout:10000});
   let output='';child.stdout.on('data',b=>output+=b);child.stderr.on('data',b=>output+=b);
   child.once('error',reject);child.once('close',code=>resolve({code,output}));
  });
  assert.equal(result.code,0,result.output);
  assert.deepEqual(JSON.parse(result.output),{status:'idle'});
 }finally{
  await new Promise<void>(r=>server.close(()=>r()));
  await rm(root,{recursive:true,force:true});
 }
});
