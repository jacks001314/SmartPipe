import { readFile } from 'node:fs/promises';
import { EngineHost, type EngineHostConfig } from './host.js';

type EngineConfig = EngineHostConfig & { loop?: boolean; intervalSeconds?: number };

const path = process.argv[2];
if (!path) throw new Error('Usage: node engine/cli.js CONFIG.json');
const config = JSON.parse(await readFile(path, 'utf8')) as EngineConfig;
if (!process.env.SMARTPIPE_TEST_MACHINE_TOKEN) throw new Error('SMARTPIPE_TEST_MACHINE_TOKEN must be provisioned by the operator');

const abort = new AbortController();
process.once('SIGINT', () => abort.abort(new Error('Interrupted')));
process.once('SIGTERM', () => abort.abort(new Error('Terminated')));

const host = new EngineHost(config);
const server = await host.startServer();
const token = (globalThis as {__smartpipeEngineControlToken?: string}).__smartpipeEngineControlToken ?? '';
console.log(JSON.stringify({status: 'ready', url: server.url, control_token: token}));

const intervalMs = Math.max(1000, (config.intervalSeconds ?? 10) * 1000);
try {
  do {
    let claimed = false;
    try {
      claimed = await host.runOnce(abort.signal);
    } catch (error) {
      console.error('engine job failed: ' + (error instanceof Error ? error.message : String(error)));
    }
    if (!claimed && config.loop !== false && !abort.signal.aborted) {
      await new Promise(resolvePromise => setTimeout(resolvePromise, intervalMs));
    }
    if (config.loop === false) break;
  } while (!abort.signal.aborted);
} finally {
  await server.close().catch(() => undefined);
}
