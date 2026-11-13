// SmartPipe explicit provisioning of the pinned upstream native payload.
// This is intentionally separate from npm install and from desktop execution.
import { resolve } from 'node:path';
import { ensureNativePayload } from './install-native.mjs';
const cache = process.argv[2];
if (!cache) throw new Error('Usage: node scripts/provision-native.mjs CACHE_DIRECTORY');
const env = {
 ...process.env,
 QWEN_CUA_SDK_CACHE_DIR: resolve(cache),
 CUA_DRIVER_USE_UIACCESS: '0',
};
delete env.QWEN_CUA_SDK_NATIVE_DIR;
delete env.QWEN_CUA_SDK_RELEASE_BASE_URL;
const directory = await ensureNativePayload({env,version:'0.27.0'});
console.log(JSON.stringify({directory,version:'0.27.0',scope:'native payload only; no desktop session opened'}));
