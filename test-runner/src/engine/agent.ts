import { spawn } from 'node:child_process';
import { createWriteStream } from 'node:fs';
import { mkdir, readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { SessionSpec, StoryDocument } from './contract.js';
import { jobModeOf } from './contract.js';

export interface AgentRunOptions {
  jobID: string;
  jobRoot: string;
  spec: SessionSpec;
  workspace: string;
  stories: StoryDocument[];
  engineURL: string;
  engineToken: string;
  command?: string;
  timeoutSeconds: number;
  env?: Record<string, string>;
  log?: (message: string) => void;
  onLine?: (line: string) => void;
}

export interface AgentRunResult {
  exitCode: number;
  timedOut: boolean;
  transcript: string;
  lastMessage: string;
  command: string;
  startedAt: string;
  finishedAt: string;
}

const here = fileURLToPath(new URL('.', import.meta.url));

function tomlString(value: string): string {
  return '"' + value.replace(/\\/g, '\\\\').replace(/"/g, '\\"') + '"';
}

function tomlInlineTable(values: Record<string, string>): string {
  return '{' + Object.entries(values).map(([key, value]) => key + '=' + tomlString(value)).join(',') + '}';
}

export function defaultCodexCommand(): string {
  if (process.env.SMARTPIPE_TEST_CODEX) return process.env.SMARTPIPE_TEST_CODEX;
  if (process.env.CODEX_BIN) return process.env.CODEX_BIN;
  if (process.platform === 'win32' && process.env.USERPROFILE) {
    return join(process.env.USERPROFILE, '.gcode', 'packages', 'app-server-daemon', 'current', 'bin', 'codex.exe');
  }
  return 'codex';
}

export function buildAgentPrompt(options: Pick<AgentRunOptions, 'jobID' | 'spec' | 'workspace' | 'stories'>): string {
  const mode = jobModeOf(options.spec);
  const lines = [
    'You are the SmartPipe test engine agent executing job ' + options.jobID + ' on a real ' + options.spec.platform + ' machine.',
    'Mode: ' + mode + '. Environment: ' + options.spec.environment + '.',
    'The SmartPipe scheduler already prepared every artifact. Do not clone or pull source; use only the staged workspace and inputs.',
    'Workspace: ' + options.workspace,
    '',
    'Required workflow:',
    '1. Call engine_job to read the job context, input hashes and required checks.',
    '2. Call engine_story_list, then engine_story_read for the story you are executing. Follow the story exactly.',
    '3. Use your native shell, filesystem and process tools to inspect and operate the product on this machine.',
    '4. For every acceptance criterion call engine_check_record. PASS always needs evidence: capture a screenshot or upload a file first.',
    '5. For every defect call engine_finding_report with severity, reproduction, expected and actual behaviour, and attach evidence.',
    '6. Verify observable product state. A zero exit code is not proof of a working UI, service or workflow.',
    '7. Call engine_finish with PASS, FAIL or BLOCKED only when the story is complete or genuinely blocked.',
    '',
    'Stories assigned to this job:',
  ];
  if (!options.stories.length) lines.push('- (inline story in engine_job)');
  for (const story of options.stories) {
    lines.push('- ' + story.id + ' [' + story.input + ':' + story.path + '] ' + story.title +
      (story.required_checks.length ? ' (required checks: ' + story.required_checks.join(', ') + ')' : ''));
  }
  const events = options.spec.events ?? [];
  if (events.length) {
    lines.push('', 'Events scoped to this run. Each event maps to one test method; record that method as a check:');
    for (const event of events) {
      lines.push('- ' + event.id + ' | ' + event.title + ' | method: ' + event.method +
        (event.trigger ? ' | trigger(' + (event.trigger_kind ?? 'manual') + '): ' + event.trigger : '') +
        (event.expected ? ' | expected: ' + event.expected : ''));
    }
    lines.push('Execute only these events and call engine_check_record with each method id verbatim. ' +
      'PASS needs evidence; report defects with engine_finding_report and link them via check_ref.');
  }
  return lines.join('\n');
}

function killTree(pid: number | undefined): void {
  if (!pid) return;
  if (process.platform === 'win32') {
    spawn('taskkill', ['/PID', String(pid), '/T', '/F'], {windowsHide: true, stdio: 'ignore'}).unref();
  } else {
    try { process.kill(-pid, 'SIGKILL'); } catch { /* already gone */ }
  }
}

/** Launch the local Codex CLI. The engine supervises it; the CLI owns the model loop. */
export async function runAgent(options: AgentRunOptions): Promise<AgentRunResult> {
  const command = options.command || defaultCodexCommand();
  const mcpScript = join(here, 'mcp.js');
  const lastMessage = join(options.jobRoot, 'agent-last-message.txt');
  const transcript = join(options.jobRoot, 'agent-transcript.jsonl');
  await mkdir(options.jobRoot, {recursive: true});
  const engineEnv = {
    SMARTPIPE_ENGINE_URL: options.engineURL,
    SMARTPIPE_ENGINE_TOKEN: options.engineToken,
    SMARTPIPE_ENGINE_JOB: options.jobID,
  };
  const args = [
    'exec',
    '--skip-git-repo-check',
    '--cd', options.workspace,
    '--sandbox', options.spec.engine?.agent?.sandbox ?? 'danger-full-access',
    '--json',
    '--output-last-message', lastMessage,
    '-c', 'mcp_servers.smartpipe_test.command=' + tomlString(process.execPath),
    '-c', 'mcp_servers.smartpipe_test.args=[' + tomlString(mcpScript) + ']',
    '-c', 'mcp_servers.smartpipe_test.env=' + tomlInlineTable(engineEnv),
  ];
  if (options.spec.engine?.agent?.model) args.push('--model', options.spec.engine.agent.model);
  args.push(...(options.spec.engine?.agent?.args ?? []), ...(options.spec.engine?.agent?.extra_args ?? []), '-');

  const prompt = buildAgentPrompt(options);
  const env = {
    ...process.env,
    ...(options.spec.engine?.env ?? {}),
    ...(options.spec.engine?.agent?.env ?? {}),
    ...(options.env ?? {}),
  };
  // The machine token must never reach the model-controlled process tree.
  delete env.SMARTPIPE_TEST_MACHINE_TOKEN;
  const startedAt = new Date().toISOString();
  options.log?.('starting agent: ' + command + ' (cwd ' + options.workspace + ')');
  const output = createWriteStream(transcript, {flags: 'a'});
  const child = spawn(command, args, {cwd: options.workspace, env, windowsHide: true, stdio: ['pipe', 'pipe', 'pipe']});
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    options.log?.('agent timeout reached, killing process tree');
    killTree(child.pid);
  }, Math.max(60_000, options.timeoutSeconds * 1000));
  let pending = '';
  const consume = (chunk: Buffer, stream: 'stdout' | 'stderr') => {
    output.write(JSON.stringify({at: new Date().toISOString(), stream, data: chunk.toString()}) + '\n');
    if (stream !== 'stdout') return;
    pending += chunk.toString();
    let index: number;
    while ((index = pending.indexOf('\n')) >= 0) {
      const line = pending.slice(0, index).trim();
      pending = pending.slice(index + 1);
      if (line) options.onLine?.(line);
    }
  };
  child.stdout.on('data', chunk => consume(chunk, 'stdout'));
  child.stderr.on('data', chunk => consume(chunk, 'stderr'));
  child.stdin.end(prompt);
  const exitCode = await new Promise<number>((resolvePromise, reject) => {
    child.once('error', reject);
    child.once('close', code => resolvePromise(code ?? -1));
  }).catch(error => {
    options.log?.('agent spawn failed: ' + (error instanceof Error ? error.message : String(error)));
    return -1;
  });
  clearTimeout(timer);
  await new Promise<void>(resolvePromise => output.end(resolvePromise));
  let last = '';
  try { last = await readFile(lastMessage, 'utf8'); } catch { /* model did not write one */ }
  return {
    exitCode, timedOut, transcript, lastMessage: last,
    command: command + ' ' + args.join(' '), startedAt, finishedAt: new Date().toISOString(),
  };
}
