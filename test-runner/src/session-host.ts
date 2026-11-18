import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, readdir, readFile, stat, writeFile } from 'node:fs/promises';
import { extname, isAbsolute, join, resolve, sep } from 'node:path';
import { executeStep } from './command-scenario.js';
import { DesktopLease } from './session.js';
import { SessionMachineClient, type SessionCommand, type SessionLease } from './session-client.js';

export interface SessionHostConfig {
  apiURL: string;
  machineID: string;
  stateRoot: string;
  workingDirectory: string;
  environments?: string[];
  /** Every file command and process cwd must resolve under one of these roots. */
  allowedRoots: string[];
  /** When set, only these executable prefixes may be launched. */
  execAllowlist?: string[];
  screenshotScript?: string;
  maxCommandMs?: number;
  log?: (message: string) => void;
}

type ExecArgs = {executable?:string;args?:string[];cwd?:string;timeout_ms?:number;env?:Record<string,string>};
type ShellArgs = {script?:string;shell?:'powershell'|'cmd';cwd?:string;timeout_ms?:number};
type ReadArgs = {path?:string;max_bytes?:number;encoding?:'auto'|'utf8'|'base64'};
type WriteArgs = {path?:string;content?:string;encoding?:'utf8'|'base64';mkdir?:boolean};
type ListArgs = {path?:string;depth?:number;limit?:number};
type HttpArgs = {method?:string;url?:string;headers?:Record<string,string>;body?:string;timeout_ms?:number;max_bytes?:number};
type ProcessArgs = {name?:string;limit?:number};
type StartArgs = {executable?:string;args?:string[];cwd?:string;env?:Record<string,string>};
type StopArgs = {pid?:number;tree?:boolean};
type ShotArgs = {title?:string;out?:string;full_screen?:boolean;timeout_ms?:number};
type UploadArgs = {path?:string;mime_type?:string};

const MAX_FILE_BYTES = 8 << 20;
const DEFAULT_COMMAND_MS = 120_000;

function isInside(root: string, candidate: string): boolean {
  const base = resolve(root).toLowerCase();
  const full = resolve(candidate).toLowerCase();
  return full === base || full.startsWith(base + sep) || (process.platform === 'win32' && full.startsWith(base + '/'));
}

function childEnv(extra?: Record<string,string>): Record<string,string> {
  const env: Record<string,string> = {...(extra ?? {})};
  for (const key of ['PATH','SystemRoot','WINDIR','TEMP','TMP','HOME','USERPROFILE','LOCALAPPDATA','APPDATA','PATHEXT','ComSpec','NUMBER_OF_PROCESSORS']) {
    if (process.env[key]) env[key] = process.env[key]!;
  }
  return env;
}

function guessMime(path: string): string {
  switch (extname(path).toLowerCase()) {
    case '.png': return 'image/png';
    case '.jpg': case '.jpeg': return 'image/jpeg';
    case '.json': return 'application/json';
    case '.txt': case '.log': case '.md': case '.csv': return 'text/plain';
    default: return 'application/octet-stream';
  }
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolvePromise, reject) => {
    if (signal.aborted) { reject(signal.reason); return; }
    const timer = setTimeout(() => { signal.removeEventListener('abort', onAbort); resolvePromise(); }, ms);
    const onAbort = () => { clearTimeout(timer); reject(signal.reason); };
    signal.addEventListener('abort', onAbort, {once:true});
  });
}

/**
 * SessionHost runs one agent-driven test session inside the logged-on desktop.
 * It holds the local desktop lease for the whole session, executes a bounded
 * command vocabulary, uploads evidence, and releases the machine only after
 * cleanup. It never inherits the machine token into child processes.
 */
export class SessionHost {
  private lease?: DesktopLease;
  private active?: AbortController;
  private client?: SessionMachineClient;
  private assignment?: SessionLease;
  private runID = '';
  private log: (message: string) => void;
  private readonly roots: string[];
  private readonly screenshotScript?: string;

  constructor(private readonly config: SessionHostConfig) {
    for (const key of ['stateRoot','workingDirectory'] as const) {
      if (!isAbsolute(config[key])) throw new Error('Absolute paths required: ' + key);
    }
    if (!config.allowedRoots?.length || !config.allowedRoots.every(isAbsolute)) throw new Error('allowedRoots must be absolute');
    this.roots = [...new Set([config.workingDirectory, config.stateRoot, ...config.allowedRoots].map(r => resolve(r)))];
    this.screenshotScript = config.screenshotScript ? resolve(config.screenshotScript) : undefined;
    this.log = config.log ?? (message => console.error(message));
  }

  /** Claim and run at most one session. Returns false when the queue is empty. */
  async runOnce(signal?: AbortSignal): Promise<boolean> {
    if (this.lease || this.assignment) throw new Error('Previous session has not been cleaned');
    const client = new SessionMachineClient(this.config.apiURL, this.config.machineID, process.env.SMARTPIPE_TEST_MACHINE_TOKEN ?? '');
    if (!process.env.SMARTPIPE_TEST_MACHINE_TOKEN) throw new Error('SMARTPIPE_TEST_MACHINE_TOKEN must be provisioned by the operator');
    this.client = client;
    const lease = await DesktopLease.acquire(this.config.stateRoot, 4 * 60 * 60 * 1000);
    this.lease = lease;
    let assignment: SessionLease | undefined;
    try {
      assignment = await client.claim(this.config.environments ?? [''], signal);
      if (!assignment) return false;
      if (assignment.machine_id !== this.config.machineID || !Number.isSafeInteger(assignment.generation) || assignment.generation < 1) {
        throw new Error('Invalid session assignment identity');
      }
      this.assignment = assignment;
      this.runID = assignment.session_id;
      await this.runLoop(assignment, signal);
      return true;
    } finally {
      this.assignment = undefined;
      this.client = undefined;
      await lease.close();
      this.lease = undefined;
    }
  }

  private async runLoop(assignment: SessionLease, outer?: AbortSignal): Promise<void> {
    const client = this.client!;
    const controller = new AbortController();
    this.active = controller;
    const onOuter = () => controller.abort(outer?.reason ?? new Error('Host interrupted'));
    outer?.addEventListener('abort', onOuter, {once:true});
    if (outer?.aborted) onOuter();
    const deadlineMs = Date.parse(assignment.deadline) - Date.now();
    const deadline = setTimeout(() => controller.abort(new Error('Session deadline exceeded')), Math.max(1, Math.min(deadlineMs, 4 * 60 * 60 * 1000)));
    let heartbeatTimer: ReturnType<typeof setTimeout> | undefined;
    let heartbeat: Promise<void> = Promise.resolve();
    let active = true;
    const schedule = () => {
      heartbeatTimer = setTimeout(() => {
        heartbeat = (async () => {
          try {
            await this.retry(() => client.heartbeat(assignment.session_id, assignment.generation, controller.signal), controller.signal, 5, 2_000);
            if (active && !controller.signal.aborted) schedule();
          } catch (error) { controller.abort(error); }
        })();
      }, 15_000);
    };
    schedule();
    let finished = false;
    let verdict = '';
    let summary = '';
    try {
      while (!controller.signal.aborted) {
        this.lease?.assertActive();
        const command = await this.retry(() => client.next(assignment.session_id, assignment.generation, 25, controller.signal), controller.signal, 20, 3_000);
        if (command === 'terminal') break;
        if (!command) continue;
        if (command.kind === 'finish') { finished = true; break; }
        const outcome = await this.execute(command, controller.signal);
        await this.retry(() => client.result(assignment.session_id, assignment.generation, command.id!, outcome.status, outcome.result, controller.signal), controller.signal, 5, 2_000);
      }
    } catch (error) {
      if (!controller.signal.aborted) controller.abort(error);
      summary = error instanceof Error ? error.message : String(error);
    } finally {
      active = false;
      if (heartbeatTimer) clearTimeout(heartbeatTimer);
      clearTimeout(deadline);
      outer?.removeEventListener('abort', onOuter);
      await heartbeat.catch(() => undefined);
    }
    if (!finished) {
      verdict = 'BLOCKED';
      if (!summary) summary = controller.signal.reason instanceof Error ? controller.signal.reason.message : 'Session stopped before finish';
      summary = summary.slice(0, 2000);
    }
    this.log('Session ' + this.runID + ' ending: ' + (finished ? 'agent finish' : 'blocked: ' + summary));
    try {
      await this.retry(() => client.report(assignment.session_id, assignment.generation, verdict, summary), undefined, 5, 2_000);
    } catch (error) {
      this.log('Session report failed: ' + (error instanceof Error ? error.message : String(error)));
    }
    try {
      await this.retry(() => client.release(assignment.session_id, assignment.generation), undefined, 5, 2_000);
    } catch (error) {
      this.log('Session release failed: ' + (error instanceof Error ? error.message : String(error)));
      throw error;
    }
  }

  private async execute(command: SessionCommand, signal: AbortSignal): Promise<{status:'DONE'|'FAILED';result:Record<string,unknown>}> {
    try {
      switch (command.kind) {
        case 'exec': return {status:'DONE', result: await this.exec(command.args as ExecArgs, signal)};
        case 'shell': return {status:'DONE', result: await this.shell(command.args as ShellArgs, signal)};
        case 'read_file': return {status:'DONE', result: await this.read(command.args as ReadArgs)};
        case 'write_file': return {status:'DONE', result: await this.write(command.args as WriteArgs)};
        case 'list': return {status:'DONE', result: await this.list(command.args as ListArgs)};
        case 'http': return {status:'DONE', result: await this.http(command.args as HttpArgs, signal)};
        case 'processes': return {status:'DONE', result: await this.processes(command.args as ProcessArgs, signal)};
        case 'start_process': return {status:'DONE', result: await this.start(command.args as StartArgs)};
        case 'stop_process': return {status:'DONE', result: await this.stop(command.args as StopArgs, signal)};
        case 'screenshot': return {status:'DONE', result: await this.screenshot(command.args as ShotArgs, signal)};
        case 'upload_file': return {status:'DONE', result: await this.uploadFile(command.args as UploadArgs)};
        case 'sleep': {
          const ms = Number((command.args as {ms?:number}).ms ?? 1000);
          if (!Number.isFinite(ms) || ms < 0 || ms > 30_000) throw new Error('sleep ms must be 0..30000');
          await sleep(ms, signal);
          return {status:'DONE', result:{slept_ms:ms}};
        }
        default: throw new Error('Unsupported command ' + command.kind);
      }
    } catch (error) {
      return {status:'FAILED', result:{error: error instanceof Error ? error.message : String(error)}};
    }
  }

  /** Retry only ambiguous transport failures. HTTP 409 is a terminal verdict. */
  private async retry<T>(fn: () => Promise<T>, signal: AbortSignal | undefined, attempts: number, delayMs: number): Promise<T> {
    let last: unknown;
    for (let i = 0; i < attempts; i++) {
      if (signal?.aborted) throw signal.reason;
      try { return await fn(); }
      catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        if (/returned 409/.test(message)) throw error;
        last = error;
        this.log('Transient session API error (' + (i + 1) + '/' + attempts + '): ' + message);
        if (signal) await sleep(delayMs, signal).catch(() => undefined);
        else await new Promise(resolvePromise => setTimeout(resolvePromise, delayMs));
      }
    }
    throw last;
  }

  private resolvePath(input: unknown, mustExist: boolean): string {
    if (typeof input !== 'string' || !input.trim() || !isAbsolute(input)) throw new Error('Absolute path required');
    const full = resolve(input);
    if (!this.roots.some(root => isInside(root, full))) throw new Error('Path outside allowed roots: ' + input);
    if (mustExist) void stat(full);
    return full;
  }

  private assertExecutable(input: unknown): string {
    if (typeof input !== 'string' || !input.trim() || !isAbsolute(input)) throw new Error('Absolute executable required');
    const full = resolve(input);
    const allow = (this.config.execAllowlist ?? []).map(prefix => resolve(prefix));
    if (allow.length > 0 && !allow.some(prefix => isInside(prefix, full))) {
      throw new Error('Executable is not allowlisted: ' + input);
    }
    return full;
  }

  private timeout(value: unknown): number {
    const max = this.config.maxCommandMs ?? 600_000;
    const ms = value === undefined ? Math.min(DEFAULT_COMMAND_MS, max) : Number(value);
    if (!Number.isSafeInteger(ms) || ms < 100 || ms > max) throw new Error('timeout_ms must be 100..' + max);
    return ms;
  }

  private async exec(a: ExecArgs, signal: AbortSignal): Promise<Record<string,unknown>> {
    const executable = this.assertExecutable(a.executable);
    const cwd = a.cwd ? this.resolvePath(a.cwd, true) : this.config.workingDirectory;
    if (!Array.isArray(a.args ?? []) || !(a.args ?? []).every(x => typeof x === 'string')) throw new Error('args must be strings');
    const env = a.env && typeof a.env === 'object' ? Object.fromEntries(Object.entries(a.env).map(([k,v]) => [k,String(v)])) : {};
    const outcome = await executeStep({id:'exec', executable, args:a.args ?? [], timeoutMs:this.timeout(a.timeout_ms)}, cwd, env, signal);
    return {executable, exit_code:outcome.exitCode, stdout:outcome.stdout, stderr:outcome.stderr, truncated:outcome.truncated};
  }

  private async shell(a: ShellArgs, signal: AbortSignal): Promise<Record<string,unknown>> {
    if (typeof a.script !== 'string' || !a.script.trim() || a.script.length > 32_000) throw new Error('script is required');
    const cwd = a.cwd ? this.resolvePath(a.cwd, true) : this.config.workingDirectory;
    const shell = a.shell ?? (process.platform === 'win32' ? 'powershell' : 'cmd');
    const executable = process.platform === 'win32'
      ? (shell === 'cmd' ? join(process.env.SystemRoot ?? 'C:\\Windows', 'System32', 'cmd.exe')
        : join(process.env.SystemRoot ?? 'C:\\Windows', 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe'))
      : '/bin/bash';
    const args = process.platform === 'win32'
      ? (shell === 'cmd' ? ['/d','/s','/c',a.script] : ['-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-Command',a.script])
      : ['-c',a.script];
    const outcome = await executeStep({id:'shell', executable, args, timeoutMs:this.timeout(a.timeout_ms)}, cwd, {}, signal);
    return {exit_code:outcome.exitCode, stdout:outcome.stdout, stderr:outcome.stderr, truncated:outcome.truncated};
  }

  private async read(a: ReadArgs): Promise<Record<string,unknown>> {
    const path = this.resolvePath(a.path, false);
    const info = await stat(path);
    if (!info.isFile()) throw new Error('Not a file: ' + path);
    const cap = Math.min(Math.max(Number(a.max_bytes ?? 262_144), 1), MAX_FILE_BYTES);
    const bytes = await readFile(path);
    const slice = bytes.subarray(0, cap);
    const truncated = bytes.length > cap;
    const encoding = a.encoding === 'base64' ? 'base64' : a.encoding === 'utf8' ? 'utf8'
      : (slice.includes(0) ? 'base64' : 'utf8');
    let content = slice.toString(encoding === 'base64' ? 'base64' : 'utf8');
    if (encoding === 'utf8' && Buffer.compare(Buffer.from(content, 'utf8'), slice) !== 0) {
      content = slice.toString('base64');
    }
    return {path, bytes:bytes.length, returned_bytes:slice.length, truncated, encoding, content,
      sha256:createHash('sha256').update(bytes).digest('hex'), mtime:info.mtime.toISOString()};
  }

  private async write(a: WriteArgs): Promise<Record<string,unknown>> {
    const path = this.resolvePath(a.path, false);
    if (typeof a.content !== 'string') throw new Error('content is required');
    const bytes = Buffer.from(a.content, a.encoding === 'base64' ? 'base64' : 'utf8');
    if (bytes.length > MAX_FILE_BYTES) throw new Error('File too large');
    if (a.mkdir !== false) await mkdir(join(path, '..'), {recursive:true});
    await writeFile(path, bytes);
    return {path, bytes:bytes.length, sha256:createHash('sha256').update(bytes).digest('hex')};
  }

  private async list(a: ListArgs): Promise<Record<string,unknown>> {
    const root = this.resolvePath(a.path, true);
    const depth = Math.min(Math.max(Number(a.depth ?? 1), 1), 3);
    const limit = Math.min(Math.max(Number(a.limit ?? 200), 1), 500);
    const entries: Array<Record<string,unknown>> = [];
    const walk = async (dir: string, level: number): Promise<void> => {
      if (entries.length >= limit) return;
      const items = await readdir(dir, {withFileTypes:true});
      items.sort((x,y) => x.name.localeCompare(y.name));
      for (const item of items) {
        if (entries.length >= limit) return;
        const full = join(dir, item.name);
        const info = await stat(full);
        entries.push({path:full, name:item.name, type:item.isDirectory() ? 'dir' : 'file',
          size:item.isDirectory() ? null : info.size, mtime:info.mtime.toISOString()});
        if (item.isDirectory() && level < depth) await walk(full, level + 1);
      }
    };
    await walk(root, 1);
    return {path:root, entries, truncated:entries.length >= limit};
  }

  private async http(a: HttpArgs, signal: AbortSignal): Promise<Record<string,unknown>> {
    if (typeof a.url !== 'string') throw new Error('url is required');
    const url = new URL(a.url);
    if (url.protocol !== 'http:' && url.protocol !== 'https:') throw new Error('Only http(s) URLs are allowed');
    const method = (a.method ?? 'GET').toUpperCase();
    const timeout = Math.min(Math.max(Number(a.timeout_ms ?? 20_000), 100), 60_000);
    const cap = Math.min(Math.max(Number(a.max_bytes ?? 262_144), 1), 1 << 20);
    const headers = Object.fromEntries(Object.entries(a.headers ?? {}).map(([k,v]) => [k,String(v)]));
    const response = await fetch(url, {method, headers, body:a.body, redirect:'manual',
      signal:AbortSignal.any([signal, AbortSignal.timeout(timeout)])});
    const raw = Buffer.from(await response.arrayBuffer());
    const bytes = raw.subarray(0, cap);
    const truncated = raw.length > cap;
    const encoding = bytes.includes(0) ? 'base64' : 'utf8';
    return {status:response.status, headers:Object.fromEntries(response.headers.entries()),
      content_type:response.headers.get('content-type') ?? '', body:bytes.toString(encoding), encoding, bytes:raw.length, truncated};
  }

  private async processes(a: ProcessArgs, signal: AbortSignal): Promise<Record<string,unknown>> {
    const limit = Math.min(Math.max(Number(a.limit ?? 200), 1), 500);
    let items: Array<Record<string,unknown>> = [];
    if (process.platform === 'win32') {
      const exe = join(process.env.SystemRoot ?? 'C:\\Windows', 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe');
      const outcome = await executeStep({id:'processes', executable:exe, timeoutMs:30_000, args:['-NoProfile','-NonInteractive','-Command',
        'Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId,Name,ExecutablePath,CommandLine | ConvertTo-Json -Compress']},
        this.config.workingDirectory, {}, signal);
      if (outcome.exitCode !== 0) throw new Error('Process listing failed: ' + outcome.stderr.slice(0,500));
      const parsed = JSON.parse(outcome.stdout.trim() || '[]') as Record<string,unknown>|Array<Record<string,unknown>>;
      const list = Array.isArray(parsed) ? parsed : [parsed];
      items = list.map(p => ({pid:Number(p.ProcessId), parent_pid:Number(p.ParentProcessId), name:String(p.Name ?? ''),
        path:String(p.ExecutablePath ?? ''), command_line:String(p.CommandLine ?? '')}));
    } else {
      const outcome = await executeStep({id:'processes', executable:'/bin/ps', timeoutMs:30_000,
        args:['-eo','pid=,ppid=,comm=,args=']}, this.config.workingDirectory, {}, signal);
      if (outcome.exitCode !== 0) throw new Error('Process listing failed: ' + outcome.stderr.slice(0,500));
      for (const raw of outcome.stdout.split('\n')) {
        const match = /^(\d+)\s+(\d+)\s+(\S+)\s+(.*)$/.exec(raw.trim());
        if (match) items.push({pid:Number(match[1]), parent_pid:Number(match[2]), name:match[3], path:'', command_line:match[4]});
      }
    }
    const filter = typeof a.name === 'string' && a.name.trim() ? a.name.toLowerCase() : '';
    if (filter) items = items.filter(p => String(p.name).toLowerCase().includes(filter) || String(p.command_line).toLowerCase().includes(filter));
    return {processes:items.slice(0, limit), total:items.length};
  }

  private async start(a: StartArgs): Promise<Record<string,unknown>> {
    const executable = this.assertExecutable(a.executable);
    const cwd = a.cwd ? this.resolvePath(a.cwd, true) : this.config.workingDirectory;
    if (!Array.isArray(a.args ?? []) || !(a.args ?? []).every(x => typeof x === 'string')) throw new Error('args must be strings');
    const child = spawn(executable, a.args ?? [], {cwd, env:childEnv(a.env), detached:true, windowsHide:true, stdio:'ignore'});
    child.unref();
    if (!child.pid) throw new Error('Failed to start process');
    return {pid:child.pid, executable};
  }

  private async stop(a: StopArgs, signal: AbortSignal): Promise<Record<string,unknown>> {
    const pid = Number(a.pid);
    if (!Number.isSafeInteger(pid) || pid < 1) throw new Error('Invalid pid');
    if (process.platform === 'win32') {
      const exe = join(process.env.SystemRoot ?? 'C:\\Windows', 'System32', 'taskkill.exe');
      const args = ['/PID', String(pid), '/F'];
      if (a.tree !== false) args.push('/T');
      const outcome = await executeStep({id:'stop_process', executable:exe, args, timeoutMs:30_000}, this.config.workingDirectory, {}, signal);
      if (outcome.exitCode !== 0 && !/not found|no running instance/i.test(outcome.stderr)) throw new Error('taskkill failed: ' + outcome.stderr.slice(0,500));
    } else {
      try { process.kill(a.tree === false ? pid : -pid, 'SIGKILL'); }
      catch (error) {
        if ((error as {code?:string}).code !== 'ESRCH') {
          try { process.kill(pid, 'SIGKILL'); } catch (inner) { if ((inner as {code?:string}).code !== 'ESRCH') throw inner; }
        }
      }
    }
    return {pid, stopped:true};
  }

  private async screenshot(a: ShotArgs, signal: AbortSignal): Promise<Record<string,unknown>> {
    if (!this.screenshotScript) throw new Error('screenshotScript is not configured on this runner');
    const out = a.out ? this.resolvePath(a.out, false) : join(this.config.stateRoot, 'screenshots', 'shot-' + Date.now() + '-' + Math.random().toString(36).slice(2) + '.png');
    await mkdir(join(out, '..'), {recursive:true});
    const exe = join(process.env.SystemRoot ?? 'C:\\Windows', 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe');
    const args = ['-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',this.screenshotScript,'-OutPath',out];
    if (a.title) args.push('-Title', a.title);
    if (a.full_screen) args.push('-FullScreen');
    const outcome = await executeStep({id:'screenshot', executable:exe, args, timeoutMs:this.timeout(a.timeout_ms)}, this.config.workingDirectory, {}, signal);
    if (outcome.exitCode !== 0) throw new Error('Screenshot failed: ' + (outcome.stderr || outcome.stdout).slice(0,500));
    const bytes = await readFile(out);
    const receipt = await this.uploadEvidence(bytes, 'image/png');
    return {path:out, bytes:bytes.length, mime_type:'image/png', ...receipt};
  }

  private async uploadFile(a: UploadArgs): Promise<Record<string,unknown>> {
    const path = this.resolvePath(a.path, false);
    const info = await stat(path);
    if (!info.isFile() || info.size > MAX_FILE_BYTES) throw new Error('File must be a regular file up to 8 MiB');
    const bytes = await readFile(path);
    const mime = a.mime_type ?? guessMime(path);
    if (!['image/png','image/jpeg','application/json','text/plain'].includes(mime)) {
      throw new Error('Unsupported evidence MIME ' + mime + '; pass mime_type explicitly');
    }
    const receipt = await this.uploadEvidence(bytes, mime);
    return {path, bytes:bytes.length, mime_type:mime, ...receipt};
  }

  private async uploadEvidence(bytes: Buffer, mimeType: string): Promise<{reference:string; sha256:string}> {
    const assignment = this.assignment;
    if (!assignment || !this.client) throw new Error('No active session');
    const sha256 = createHash('sha256').update(bytes).digest('hex');
    const reference = await this.client.evidence(assignment.session_id, assignment.generation,
      {sha256, mime_type:mimeType, payload:bytes.toString('base64')});
    if (reference !== 'sha256:' + sha256) throw new Error('Invalid evidence receipt');
    return {reference, sha256};
  }
}
