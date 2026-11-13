import { readFile } from 'node:fs/promises';
import { RunnerClient } from './worker.js';
import type { ObserveConfig } from './observe-scenario.js';
import type { CommandScenarioConfig } from './command-scenario.js';

const path=process.argv[2];
if(!path)throw new Error('Usage: node worker-cli.js CONFIG.json');
const config=JSON.parse(await readFile(path,'utf8')) as ObserveConfig & {apiURL:string;machineID:string;commandScenario?:CommandScenarioConfig};
const token=process.env.SMARTPIPE_TEST_MACHINE_TOKEN;
if(!token)throw new Error('SMARTPIPE_TEST_MACHINE_TOKEN must be provisioned by the operator');
const abort=new AbortController();
process.once('SIGINT',()=>abort.abort(new Error('Interrupted')));
process.once('SIGTERM',()=>abort.abort(new Error('Terminated')));
const runner=new RunnerClient(config.apiURL,config.machineID,token);
try {
 const scenario=config.commandScenario
  ?new (await import('./command-scenario.js')).CommandScenario(config.commandScenario)
  :new (await import('./observe-scenario.js')).ObserveScenario(config);
 const claimed=await runner.runOnce(scenario,abort.signal);
 console.log(JSON.stringify({status:claimed?'reported':'idle'}));
} catch(error){
 console.error('Test assignment stopped: '+(error instanceof Error?error.message:'unknown error'));
 process.exitCode=1;
}
