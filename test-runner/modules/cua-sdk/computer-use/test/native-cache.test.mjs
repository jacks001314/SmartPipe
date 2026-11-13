import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,writeFile,rm,mkdir} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {nativeTarget,hasCompletedNativePayload} from '../../dist/native-assets.js';

test('native cache requires complete nonempty files and matching valid metadata',async()=>{
 const dir=await mkdtemp(join(tmpdir(),'smartpipe-cache-test-'));
 const target=nativeTarget('darwin','x64','0.27.0');
 const metadata={archive:target.archive,version:'0.27.0',checksum:'a'.repeat(64),source:'https://example.invalid/driver'};
 const marker=join(dir,'complete.json');
 try {
  for(const file of [target.library,target.runtime])await writeFile(join(dir,file),'fixture');
  assert.equal(hasCompletedNativePayload(dir,target),false);
  for(const value of ['', 'null','{}',JSON.stringify({...metadata,archive:'wrong'}),JSON.stringify({...metadata,version:'0.28.0'}),JSON.stringify({...metadata,checksum:'bad'})]){
   await writeFile(marker,value);assert.equal(hasCompletedNativePayload(dir,target),false);
  }
  await writeFile(marker,JSON.stringify(metadata));assert.equal(hasCompletedNativePayload(dir,target),true);
  await writeFile(join(dir,target.runtime),'');assert.equal(hasCompletedNativePayload(dir,target),false);
  await rm(join(dir,target.runtime));await mkdir(join(dir,target.runtime));assert.equal(hasCompletedNativePayload(dir,target),false);
 }finally{await rm(dir,{recursive:true,force:true})}
});
