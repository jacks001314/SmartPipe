import { test } from 'node:test';
import assert from 'node:assert/strict';
import { resolve } from 'node:path';
import { runtimeEnv } from '../src/runtime-env.js';
test('runtime passes provisioned native cache but excludes tokens and code injection settings',()=>{
 const cache=resolve('native-test-cache');
 const env=runtimeEnv({PATH:'test-path',QWEN_CUA_SDK_CACHE_DIR:cache,
 SMARTPIPE_TEST_MACHINE_TOKEN:'fake-token',OPENAI_API_KEY:'fake-key',
 NODE_OPTIONS:'--require=untrusted',QWEN_CUA_SDK_RELEASE_BASE_URL:'https://invalid'});
 assert.deepEqual(env,{PATH:'test-path',QWEN_CUA_SDK_CACHE_DIR:cache});
});
test('relative native cache cannot drift with child working directory',()=>{
 assert.throws(()=>runtimeEnv({QWEN_CUA_SDK_CACHE_DIR:'relative'}),/absolute/);
});
