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
const config = JSON.parse(await readFile(path, 'utf8')) as ObserveConfig & {
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
  catch (error) { if (!busy(error)) throw error; console.log(JSON.stringify({status:'busy', kind:'run'})); process.exit(0); }
  if (runClaimed) {
    console.log(JSON.stringify({status:'reported', kind:'run'}));
  } else if (config.sessionConfig) {
    const sessionConfig = JSON.parse(await readFile(config.sessionConfig, 'utf8')) as SessionHostConfig;
    const host = new (await import('./session-host.js')).SessionHost(sessionConfig);
    let claimed = false;
    try { claimed = await host.runOnce(abort.signal); }
    catch (error) { if (!busy(error)) throw error; console.log(JSON.stringify({status:'busy', kind:'session'})); process.exit(0); }
    console.log(JSON.stringify({status: claimed ? 'reported' : 'idle', kind:'session'}));
  } else {
    console.log(JSON.stringify({status:'idle'}));
  }
} catch (error) {
  console.error('Test assignment stopped: ' + (error instanceof Error ? error.message : 'unknown error'));
  process.exitCode = 1;
}
