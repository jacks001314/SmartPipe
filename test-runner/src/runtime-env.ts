import { isAbsolute } from 'node:path';

/** Deliberately narrow child environment; never forwards machine tokens or provider secrets. */
export function runtimeEnv(source: NodeJS.ProcessEnv = process.env): Record<string,string> {
 const env: Record<string,string> = {};
 for (const key of ['PATH','SystemRoot','WINDIR','TEMP','TMP','HOME','USERPROFILE',
  'LOCALAPPDATA','APPDATA','DISPLAY','XAUTHORITY','DBUS_SESSION_BUS_ADDRESS','XDG_RUNTIME_DIR']) {
  if (source[key]) env[key] = source[key]!;
 }
 // Provisioning and execution must agree on the native cache location.
 const cache = source.QWEN_CUA_SDK_CACHE_DIR;
 if (cache) {
  if (!isAbsolute(cache)) throw new Error('Native cache directory must be absolute');
  env.QWEN_CUA_SDK_CACHE_DIR = cache;
 }
 return env;
}
