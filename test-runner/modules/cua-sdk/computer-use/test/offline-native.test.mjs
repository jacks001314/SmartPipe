import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,writeFile,rm,access} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {offlineTransport} from '../../scripts/offline-native.mjs';
import {ensureNativePayload} from '../../scripts/install-native.mjs';

test('offline incomplete archive fails checksum without marking cache complete',async()=>{
 const root=await mkdtemp(join(tmpdir(),'smartpipe-offline-test-'));
 try {
  const archive=join(root,'cua-driver-rs-0.27.0-darwin-universal-binary.tar.gz');
  await writeFile(archive,'incomplete download');
  const fetchImpl=await offlineTransport(archive);
  await assert.rejects(ensureNativePayload({env:{QWEN_CUA_SDK_CACHE_DIR:root},platform:'darwin',arch:'x64',version:'0.27.0',fetchImpl}),/checksum mismatch/);
  await assert.rejects(access(join(root,'0.27.0','darwin-universal','complete.json')));
  await assert.rejects(fetchImpl('https://example.invalid/file'),/does not match/);
  await assert.rejects(offlineTransport(join(root,'unknown.zip')),/Unsupported/);
 }finally{await rm(root,{recursive:true,force:true})}
});
