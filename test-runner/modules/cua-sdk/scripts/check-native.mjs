// Load-only ABI check. Does not construct a host, request permissions or send input.
import {execFileSync} from 'node:child_process';
import {resolve} from 'node:path';
import {createHash} from 'node:crypto';
import {createReadStream} from 'node:fs';
import {writeFile} from 'node:fs/promises';
import {nativeTarget,cachedNativeDirectory,hasCompletedNativePayload} from '../dist/native-assets.js';
const [cache,output]=process.argv.slice(2);
if(!cache||!output)throw new Error('Usage: node scripts/check-native.mjs CACHE_DIRECTORY NEW_REPORT_JSON');
process.env.QWEN_CUA_SDK_CACHE_DIR=resolve(cache);
delete process.env.QWEN_CUA_SDK_NATIVE_DIR;
const report={version:'0.27.0',platform:process.platform,arch:process.arch,node:process.version,status:'FAIL',scope:'native module load only; no desktop session'};
try {
 if(process.platform==='darwin'){
  const osVersion=execFileSync('/usr/bin/sw_vers',['-productVersion'],{encoding:'utf8'}).trim();
  report.osVersion=osVersion;
  report.minimumOSVersion='13.0';
  const major=Number(osVersion.split('.')[0]);
  if(!Number.isInteger(major)||major<13){
   report.code='UNSUPPORTED_OS_VERSION';
   throw new Error('Pinned native driver 0.27.0 requires macOS 13.0 or newer; host is '+osVersion);
  }
 }
 const target=nativeTarget(process.platform,process.arch,'0.27.0');
 const directory=cachedNativeDirectory(target,'0.27.0',process.env);
 if(!hasCompletedNativePayload(directory,target))throw new Error('Pinned native cache is incomplete');
 report.files={};
 for(const name of [target.library,target.runtime]){
  const hash=createHash('sha256');
  for await(const bytes of createReadStream(resolve(directory,name)))hash.update(bytes);
  report.files[name]=hash.digest('hex');
 }
 // Generated binding initialization checks the native FFI contract, without creating a host.
 await import('../dist/native/index.js');
 report.status='PASS';
}catch(error){report.error=error instanceof Error?error.message:String(error);process.exitCode=1}
await writeFile(resolve(output),JSON.stringify(report,null,2)+'\n',{flag:'wx'});
console.log(JSON.stringify(report));
