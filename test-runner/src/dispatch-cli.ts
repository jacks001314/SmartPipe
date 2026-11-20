import { readFile } from 'node:fs/promises';
import { RunnerClient } from './worker.js';
import type { ObserveConfig } from './observe-scenario.js';
import type { CommandScenarioConfig } from './command-scenario.js';
import type { BundleScenarioConfig } from './bundle-scenario.js';
import type { SessionHostConfig } from './session-host.js';

// One desktop owner serves both work types: deterministic release runs first
// (they are gate inputs), then agent sessions. Both share the local lease.
const path = process.argv[2];
if (!path) throw new Error('Usage: node dispatch-cli.js CONFIG.json');
// Windows editors frequently save UTF-8 with a BOM; JSON.parse rejects it.
const config = JSON.parse((await readFile(path, 'utf8')).replace(/^\uFEFF/, '')) as ObserveConfig & {
  apiURL:string; machineID:string; commandScenario?:CommandScenarioConfig;
  bundleScenario?:BundleScenarioConfig; sessionConfig?:string;
};
const token = process.env.SMARTPIPE_TEST_MACHINE_TOKEN;
if (!token) throw new Error('SMARTPIPE_TEST_MACHINE_TOKEN must be provisioned by the operator');
const abort = new AbortController();
process.once('SIGINT', () => abort.abort(new Error('Interrupted')));
process.once('SIGTERM', () => abort.abort(new Error('Terminated')));
const runner = new RunnerClient(config.apiURL, config.machineID, token);
try {
  const scenario = config.bundleScenario
    ? new (await import('./bundle-scenario.js')).BundleSwapScenario(config.bundleScenario)
    : config.commandScenario
      ? new (await import('./command-scenario.js')).CommandScenario(config.commandScenario)
      : new (await import('./observe-scenario.js')).ObserveScenario(config);
  const busy = (error: unknown) => /returned 409/.test(error instanceof Error ? error.message : String(error));
  let runClaimed = false;
  try { runClaimed = await runner.runOnce(scenario, abort.signal); }
  // A stuck release run must not hide queued agent sessions: the desktop lease
  // still serialises both kinds on this machine.
  catch (error) { if (!busy(error)) throw error; console.log(JSON.stringify({status:'busy', kind:'run'})); }
  if (runClaimed) {
    console.log(JSON.stringify({status:'reported', kind:'run'}));
  } else if (config.sessionConfig) {
    const sessionConfig = JSON.parse((await readFile(config.sessionConfig, 'utf8')).replace(/^\uFEFF/, '')) as SessionHostConfig;
    let claimed = false;
    if ((sessionConfig as SessionHostConfig & {useEngine?:boolean}).useEngine) {
      const host = new (await import('./engine/host.js')).EngineHost(sessionConfig);
      await host.startServer();
      try { claimed = await host.runOnce(abort.signal); }
      catch (error) { if (!busy(error)) throw error; console.log(JSON.stringify({status:'busy', kind:'session'})); process.exit(0); }
      finally { await closeWithTimeout(host); }
    } else {
      const host = new (await import('./session-host.js')).SessionHost(sessionConfig);
      try { claimed = await host.runOnce(abort.signal); }
      catch (error) { if (!busy(error)) throw error; console.log(JSON.stringify({status:'busy', kind:'session'})); process.exit(0); }
    }
    console.log(JSON.stringify({status: claimed ? 'reported' : 'idle', kind:'session'}));
  } else {
    console.log(JSON.stringify({status:'idle'}));
  }
} catch (error) {
  console.error('Test assignment stopped: ' + (error instanceof Error ? error.message : 'unknown error'));
  process.exitCode = 1;
}

// One stuck socket must never wedge the polling loop: the supervisor restarts
// the engine on the next iteration with a fresh state root.
async function closeWithTimeout(host: {close(): Promise<void>}): Promise<void> {
  try {
    await Promise.race([
      host.close(),
      new Promise<void>(resolvePromise => setTimeout(resolvePromise, 5_000).unref()),
    ]);
  } catch (error) {
    console.error('engine close failed: ' + (error instanceof Error ? error.message : String(error)));
  }
}
