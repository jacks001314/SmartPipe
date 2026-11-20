import { readFile } from 'node:fs/promises';
import { SessionHost, type SessionHostConfig } from './session-host.js';

const path = process.argv[2];
if (!path) throw new Error('Usage: node session-host-cli.js CONFIG.json');
const config = JSON.parse((await readFile(path, 'utf8')).replace(/^\uFEFF/, '')) as SessionHostConfig;
if (!process.env.SMARTPIPE_TEST_MACHINE_TOKEN) throw new Error('SMARTPIPE_TEST_MACHINE_TOKEN must be provisioned by the operator');
const abort = new AbortController();
process.once('SIGINT', () => abort.abort(new Error('Interrupted')));
process.once('SIGTERM', () => abort.abort(new Error('Terminated')));
const host = new SessionHost(config);
try {
  const claimed = await host.runOnce(abort.signal);
  console.log(JSON.stringify({status: claimed ? 'reported' : 'idle'}));
} catch (error) {
  console.error('Test session host stopped: ' + (error instanceof Error ? error.message : 'unknown error'));
  process.exitCode = 1;
}
