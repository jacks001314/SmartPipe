import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { copyFile, mkdir, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { basename, dirname, extname, isAbsolute, join, resolve, sep } from 'node:path';
import { resolveInside } from './inputs.js';

export interface HotswapCopyStep { op: 'copy'; source: string; target: string; sha256?: string; optional?: boolean }
export interface HotswapRemoveStep { op: 'remove'; target: string; optional?: boolean }
export interface HotswapStopStep { op: 'stop_process'; name: string; tree?: boolean }
export interface HotswapStartStep {
  op: 'start_process'; executable: string; args?: string[]; cwd?: string; env?: Record<string, string>;
}
export interface HotswapWaitStep { op: 'wait_http'; url: string; status?: number[]; timeout_ms?: number; headers?: Record<string, string> }
export interface HotswapExecStep {
  op: 'exec'; executable: string; args?: string[]; cwd?: string; timeout_ms?: number; allow_failure?: boolean;
}
export interface HotswapSleepStep { op: 'sleep'; ms: number }
export interface HotswapNoteStep { op: 'note'; note: string }
export type HotswapStep = HotswapCopyStep | HotswapRemoveStep | HotswapStopStep | HotswapStartStep |
  HotswapWaitStep | HotswapExecStep | HotswapSleepStep | HotswapNoteStep;

export interface HotswapPlan {
  schema: number;
  name: string;
  environment?: string;
  install_root?: string;
  allow_asar?: boolean;
  steps: HotswapStep[];
  restore_steps?: HotswapStep[];
}

interface Backup { target: string; backup: string; existed: boolean }

export interface AppliedStep {
  index: number;
  op: string;
  status: 'ok' | 'failed' | 'skipped';
  detail?: string;
  at: string;
  phase?: 'apply' | 'restore';
}

function inside(root: string, candidate: string): boolean {
  const normalize = (value: string) => process.platform === 'win32' ? resolve(value).toLowerCase() : resolve(value);
  const base = normalize(root);
  const full = normalize(candidate);
  return full === base || full.startsWith(base + sep);
}

function wait(ms: number): Promise<void> {
  return new Promise(resolvePromise => setTimeout(resolvePromise, ms));
}

function run(command: string, args: string[], options: {cwd?: string; timeoutMs?: number} = {}): Promise<{code: number; stdout: string; stderr: string}> {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(command, args, {cwd: options.cwd, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe']});
    let stdout = '';
    let stderr = '';
    const timer = setTimeout(() => {
      if (process.platform === 'win32') void run('taskkill', ['/PID', String(child.pid), '/T', '/F']);
      else child.kill('SIGKILL');
    }, Math.max(1000, options.timeoutMs ?? 120_000));
    child.stdout.on('data', chunk => { if (stdout.length < 1 << 20) stdout += chunk.toString(); });
    child.stderr.on('data', chunk => { if (stderr.length < 1 << 20) stderr += chunk.toString(); });
    child.once('error', error => { clearTimeout(timer); reject(error); });
    child.once('close', code => { clearTimeout(timer); resolvePromise({code: code ?? -1, stdout, stderr}); });
  });
}

export function parseHotswapPlan(raw: string): HotswapPlan {
  const plan = JSON.parse(raw) as HotswapPlan;
  if (plan?.schema !== 1) throw new Error('unsupported hotswap plan schema');
  if (!plan.name?.trim()) throw new Error('hotswap plan name is required');
  if (!Array.isArray(plan.steps) || plan.steps.length === 0 || plan.steps.length > 64) {
    throw new Error('hotswap plan needs 1..64 steps');
  }
  if (plan.restore_steps !== undefined) {
    if (!Array.isArray(plan.restore_steps) || plan.restore_steps.length > 32) {
      throw new Error('hotswap restore_steps must be a list of at most 32 steps');
    }
    for (const step of plan.restore_steps) {
      if (!step || typeof step.op !== 'string') throw new Error('hotswap restore step is missing op');
    }
  }
  for (const step of plan.steps) {
    if (!step || typeof step.op !== 'string') throw new Error('hotswap step is missing op');
  }
  return plan;
}

function processName(executable: string): string {
  return basename(executable, extname(executable));
}

async function processIDs(name: string): Promise<number[]> {
  if (process.platform === 'win32') {
    const outcome = await run('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command',
      `(Get-Process -Name '${name}' -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Id) -join ','`]);
    return outcome.stdout.split(/[\s,]+/).map(value => Number(value)).filter(value => Number.isInteger(value) && value > 0);
  }
  const outcome = await run('pgrep', ['-f', name]);
  return outcome.stdout.split(/\s+/).map(value => Number(value)).filter(value => Number.isInteger(value) && value > 0);
}

/**
 * Executes an explicit component replacement plan. The engine never guesses how
 * to install or restart a product component: start/stop/health steps come from
 * SmartPipe's scheduler. What the engine guarantees is confinement, hashing,
 * backup and rollback.
 */
export class HotswapExecutor {
  private backups: Backup[] = [];
  private started: HotswapStartStep[] = [];
  private stopped: HotswapStopStep[] = [];
  private readonly applied: AppliedStep[] = [];
  private plan?: HotswapPlan;

  constructor(private readonly options: {
    jobRoot: string;
    sourceRoot: string;
    installRoot?: string;
    log?: (message: string) => void;
  }) {}

  get steps(): AppliedStep[] { return [...this.applied]; }

  private targetPath(step: {target: string}): string {
    if (isAbsolute(step.target)) {
      if (this.options.installRoot && !inside(this.options.installRoot, step.target)) {
        throw new Error('hotswap target outside install_root: ' + step.target);
      }
      return step.target;
    }
    if (!this.options.installRoot) throw new Error('relative hotswap target needs install_root: ' + step.target);
    return resolveInside(this.options.installRoot, step.target);
  }

  private async backupTarget(target: string): Promise<void> {
    if (this.backups.some(item => item.target === target)) return;
    const directory = join(this.options.jobRoot, 'hotswap-backup');
    await mkdir(directory, {recursive: true});
    const backup = join(directory, this.backups.length + '-' + basename(target));
    try {
      const info = await stat(target);
      if (!info.isFile()) throw new Error('hotswap copy only replaces files: ' + target);
      await copyFile(target, backup);
      this.backups.push({target, backup, existed: true});
    } catch (error) {
      if ((error as {code?: string}).code === 'ENOENT') {
        this.backups.push({target, backup, existed: false});
        return;
      }
      throw error;
    }
  }

  private async stopProcess(step: HotswapStopStep): Promise<string> {
    if (!/^[A-Za-z0-9_.-]{1,64}$/.test(step.name)) throw new Error('invalid process name');
    const pids = await processIDs(step.name);
    if (!pids.length) return step.name + ' already stopped';
    for (const pid of pids) {
      if (process.platform === 'win32') {
        if (step.tree) await run('taskkill', ['/PID', String(pid), '/T', '/F']);
        else await run('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command',
          `Stop-Process -Id ${pid} -Force -ErrorAction SilentlyContinue`]);
      } else {
        await run('kill', ['-9', String(pid)]);
      }
    }
    // Windows keeps an image locked until every handle is gone; copying the
    // replacement or restoring the backup before that point fails with EBUSY.
    const deadline = Date.now() + 15_000;
    while (Date.now() < deadline) {
      if (!(await processIDs(step.name)).length) break;
      await wait(250);
    }
    const remaining = await processIDs(step.name);
    if (remaining.length) throw new Error('process did not exit: ' + step.name + ' pids=' + remaining.join(','));
    this.stopped.push(step);
    return step.name + (step.tree ? ' tree' : '') + ' pids=' + pids.join(',');
  }

  private async executeStep(step: HotswapStep, index: number, phase: 'apply' | 'restore' = 'apply'): Promise<void> {
    const at = new Date().toISOString();
    const done = (status: AppliedStep['status'], detail?: string) => {
      this.applied.push({index, op: step.op, status, detail, at, ...(phase === 'restore' ? {phase} : {})});
    };
    try {
      switch (step.op) {
        case 'copy': {
          const target = this.targetPath(step);
          if (basename(target).toLowerCase() === 'app.asar' && !this.allowAsar()) {
            throw new Error('app.asar replacement is disabled: production builds verify ASAR integrity; use an installer or a dev build');
          }
          const source = resolveInside(this.options.sourceRoot, step.source);
          const bytes = await readFile(source);
          if (step.sha256 && createHash('sha256').update(bytes).digest('hex') !== step.sha256.toLowerCase()) {
            throw new Error('hotswap source hash mismatch: ' + step.source);
          }
          await this.backupTarget(target);
          await mkdir(dirname(target), {recursive: true});
          await writeFile(target, bytes);
          done('ok', target);
          return;
        }
        case 'remove': {
          const target = this.targetPath(step);
          await this.backupTarget(target);
          await rm(target, {force: true});
          done('ok', target);
          return;
        }
        case 'stop_process': {
          done('ok', await this.stopProcess(step));
          return;
        }
        case 'start_process': {
          if (!isAbsolute(step.executable)) throw new Error('start_process needs an absolute executable');
          const child = spawn(step.executable, step.args ?? [], {
            cwd: step.cwd, env: {...process.env, ...(step.env ?? {})}, detached: true, windowsHide: true, stdio: 'ignore',
          });
          child.unref();
          this.started.push(step);
          done('ok', step.executable + ' pid=' + child.pid);
          return;
        }
        case 'wait_http': {
          const deadline = Date.now() + Math.max(100, step.timeout_ms ?? 30_000);
          const expects = step.status ?? [200];
          let last = '';
          while (Date.now() < deadline) {
            try {
              const response = await fetch(step.url, {headers: step.headers, redirect: 'manual', signal: AbortSignal.timeout(5_000)});
              last = String(response.status);
              if (expects.includes(response.status)) {
                done('ok', step.url + ' -> ' + response.status);
                return;
              }
            } catch (error) {
              last = error instanceof Error ? error.message : String(error);
            }
            await wait(500);
          }
          throw new Error('health check timed out: ' + step.url + ' last=' + last);
        }
        case 'exec': {
          if (!isAbsolute(step.executable)) throw new Error('exec needs an absolute executable');
          const outcome = await run(step.executable, step.args ?? [], {cwd: step.cwd, timeoutMs: step.timeout_ms});
          const detail = 'exit=' + outcome.code + ' ' + (outcome.stdout || outcome.stderr).slice(0, 500);
          if (outcome.code !== 0 && !step.allow_failure) throw new Error('exec failed: ' + detail);
          done('ok', detail);
          return;
        }
        case 'sleep':
          await wait(Math.min(Math.max(0, step.ms), 60_000));
          done('ok');
          return;
        case 'note':
          done('ok', step.note);
          return;
        default:
          throw new Error('unsupported hotswap op ' + (step as {op: string}).op);
      }
    } catch (error) {
      done('failed', error instanceof Error ? error.message : String(error));
      throw error;
    }
  }

  private allowAsarFlag = false;

  private allowAsar(): boolean {
    return this.allowAsarFlag;
  }

  async apply(plan: HotswapPlan): Promise<AppliedStep[]> {
    this.options.log?.('applying hotswap plan ' + plan.name + ' (' + plan.steps.length + ' steps)');
    this.plan = plan;
    this.allowAsarFlag = plan.allow_asar === true;
    for (let index = 0; index < plan.steps.length; index++) {
      const step = plan.steps[index];
      if (!step) throw new Error('missing hotswap step ' + index);
      await this.executeStep(step, index);
      this.options.log?.('hotswap step ' + index + ' ' + step.op + ' ok');
    }
    return this.steps;
  }

  /**
   * Restore in the only safe order on Windows: stop the processes the plan
   * started (releasing file handles), write backups back, then run the plan's
   * explicit restore_steps so the product can be brought up again.
   */
  async restore(reason: string): Promise<void> {
    this.options.log?.('restoring hotswap plan: ' + reason);
    for (const step of [...this.started].reverse()) {
      try {
        await this.stopProcess({op: 'stop_process', name: processName(step.executable), tree: true});
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        this.options.log?.('restore: failed to stop ' + step.executable + ': ' + message);
        this.applied.push({index: this.applied.length, op: 'stop_process', status: 'failed',
          detail: 'restore: ' + message, at: new Date().toISOString(), phase: 'restore'});
      }
    }
    this.started = [];
    for (const backup of [...this.backups].reverse()) {
      try {
        if (backup.existed) {
          await mkdir(dirname(backup.target), {recursive: true});
          await copyFile(backup.backup, backup.target);
        } else {
          await rm(backup.target, {force: true});
        }
      } catch (error) {
        this.options.log?.('restore failed for ' + backup.target + ': ' + (error instanceof Error ? error.message : String(error)));
        this.applied.push({index: this.applied.length, op: 'restore', status: 'failed',
          detail: backup.target + ': ' + (error instanceof Error ? error.message : String(error)),
          at: new Date().toISOString(), phase: 'restore'});
      }
    }
    const steps = this.plan?.restore_steps ?? [];
    for (let index = 0; index < steps.length; index++) {
      const step = steps[index];
      if (!step) continue;
      try {
        await this.executeStep(step, index, 'restore');
        this.options.log?.('hotswap restore step ' + index + ' ' + step.op + ' ok');
      } catch (error) {
        this.options.log?.('hotswap restore step ' + index + ' ' + step.op + ' failed: ' +
          (error instanceof Error ? error.message : String(error)));
      }
    }
    if (this.stopped.length && !steps.length) {
      this.options.log?.('note: plan stopped processes but defined no restore_steps; the product stays stopped');
    }
  }
}
