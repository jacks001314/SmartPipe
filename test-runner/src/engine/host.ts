import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { mkdir, readFile, stat, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { DesktopLease } from '../session.js';
import { SessionMachineClient, type SessionLease } from '../session-client.js';
import { runAgent } from './agent.js';
import { jobModeOf, validateStoryEvents, type SessionSpec } from './contract.js';
import { HotswapExecutor, parseHotswapPlan, type HotswapPlan } from './hotswap.js';
import { materializeInputs, resolveInside, type MaterializedInput } from './inputs.js';
import { JobRegistry, type EngineJobState } from './job-state.js';
import { captureScreenshot } from './screenshot.js';
import { startEngineServer, type EngineServerHandle } from './server.js';
import { loadStories, requiredChecks } from './stories.js';

export interface EngineHostConfig {
  apiURL: string;
  machineID: string;
  stateRoot: string;
  workingDirectory: string;
  environments?: string[];
  allowedRoots?: string[];
  screenshotScript?: string;
  screenshotTitle?: string;
  screenshotCDPPort?: number;
  screenshotCDPURLMatch?: string;
  screenshotCDPProcess?: string;
  codexCommand?: string;
  enginePort?: number;
  maxJobSeconds?: number;
  log?: (message: string) => void;
}

const here = fileURLToPath(new URL('.', import.meta.url));

function defaultScreenshotScript(): string | undefined {
  if (process.platform !== 'win32') return undefined;
  return resolve(here, '../../../scenarios/windows/capture-window.ps1');
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolvePromise, reject) => {
    const timer = setTimeout(() => { signal.removeEventListener('abort', onAbort); resolvePromise(); }, ms);
    const onAbort = () => { clearTimeout(timer); reject(signal.reason); };
    signal.addEventListener('abort', onAbort, {once: true});
  });
}

// Evidence collected through the engine API lives in the job directory, but the
// console and the verdict check read it from SmartPipe. Upload every referenced
// blob before its check or finding, and skip the ones already accepted.
export async function uploadJobEvidence(
  client: {evidence(sessionID: string, generation: number,
    evidence: {sha256: string; mime_type: string; payload: string}): Promise<string>},
  jobID: string, generation: number, job: EngineJobState, sent: Set<string>,
): Promise<void> {
  for (const [sha256, record] of Object.entries(job.evidence)) {
    if (sent.has(sha256)) continue;
    const bytes = await readFile(record.path);
    if (bytes.length > 8 << 20) {
      // The backend caps evidence at 8 MiB; the transcript is uploaded through
      // collectFinalEvidence and anything larger is reported, not silently lost.
      sent.add(sha256);
      continue;
    }
    await client.evidence(jobID, generation, {sha256, mime_type: record.mimeType, payload: bytes.toString('base64')});
    sent.add(sha256);
  }
}

/**
 * EngineHost is the supervisor half of the engine: it owns the machine lease,
 * claims one SmartPipe job, materialises staged inputs, applies the hotswap
 * plan, launches the local Codex agent and reports independently collected
 * evidence back to SmartPipe.
 */
export class EngineHost {
  private lease?: DesktopLease;
  private server?: EngineServerHandle;
  private readonly registry: JobRegistry;
  private readonly log: (message: string) => void;
  private readonly screenshotScript?: string;
  private readonly syncFailures = new Set<string>();
  private readonly sentEvidence = new Set<string>();

  constructor(private readonly config: EngineHostConfig) {
    this.registry = new JobRegistry(join(config.stateRoot, 'engine-jobs'));
    this.log = config.log ?? (message => console.error(message));
    const script = config.screenshotScript ?? defaultScreenshotScript();
    this.screenshotScript = script && existsSync(script) ? script : undefined;
  }

  async startServer(): Promise<EngineServerHandle> {
    const controlToken = createHash('sha256').update(String(process.pid) + Date.now()).digest('hex');
    this.server = await startEngineServer({
      registry: this.registry, stateRoot: join(this.config.stateRoot, 'engine-jobs'),
      controlToken, port: this.config.enginePort ?? 0, screenshotScript: this.screenshotScript,
      screenshotCDPPort: this.config.screenshotCDPPort, screenshotCDPURLMatch: this.config.screenshotCDPURLMatch,
      screenshotCDPProcess: this.config.screenshotCDPProcess,
      log: this.log,
    });
    (globalThis as {__smartpipeEngineControlToken?: string}).__smartpipeEngineControlToken = controlToken;
    this.log('engine api listening on ' + this.server.url);
    return this.server;
  }

  async close(): Promise<void> {
    if (this.server) await this.server.close();
    this.server = undefined;
  }

  async runOnce(signal?: AbortSignal): Promise<boolean> {
    if (this.lease) throw new Error('previous job has not been cleaned');
    const machineToken = process.env.SMARTPIPE_TEST_MACHINE_TOKEN;
    if (!machineToken) throw new Error('SMARTPIPE_TEST_MACHINE_TOKEN must be provisioned by the operator');
    const client = new SessionMachineClient(this.config.apiURL, this.config.machineID, machineToken);
    this.lease = await DesktopLease.acquire(this.config.stateRoot, 4 * 60 * 60 * 1000);
    let assignment: SessionLease | undefined;
    try {
      assignment = await client.claim(this.config.environments ?? [''], signal);
      if (!assignment) return false;
      if (assignment.machine_id !== this.config.machineID || !Number.isSafeInteger(assignment.generation)) {
        throw new Error('invalid session assignment identity');
      }
      await this.runJob(client, assignment, signal);
      return true;
    } finally {
      await this.lease.close();
      this.lease = undefined;
    }
  }

  private async runJob(client: SessionMachineClient, assignment: SessionLease, outer?: AbortSignal): Promise<void> {
    const session = assignment.session;
    const spec = session.spec as SessionSpec;
    const jobID = assignment.session_id;
    const jobRoot = join(this.config.stateRoot, 'engine-jobs', jobID);
    await mkdir(jobRoot, {recursive: true});

    const errors = validateSpec(spec);
    if (errors.length) throw new Error('invalid engine session: ' + errors.join('; '));

    const mode = jobModeOf(spec);
    const startedAt = new Date().toISOString();
    const deadline = assignment.deadline ?? new Date(Date.now() + spec.timeout_seconds * 1000).toISOString();
    await this.registry.create({
      id: jobID, mode, title: spec.title, platform: spec.platform, environment: spec.environment,
      status: 'running', startedAt, deadline, workspace: this.config.workingDirectory, spec,
      inputs: [], stories: [], checks: [], findings: [], evidence: {}, events: [],
    });
    const token = this.registry.newToken(jobID);
    await this.registry.event(jobID, 'claimed', 'machine ' + this.config.machineID + ' claimed session');

    const controller = new AbortController();
    const abort = () => controller.abort(outer?.reason ?? new Error('host interrupted'));
    outer?.addEventListener('abort', abort, {once: true});
    const deadlineMs = Math.max(1000, Date.parse(deadline) - Date.now());
    const deadlineTimer = setTimeout(() => controller.abort(new Error('job deadline exceeded')), deadlineMs);

    let heartbeatTimer: ReturnType<typeof setTimeout> | undefined;
    const heartbeat = async () => {
      try {
        await client.heartbeat(jobID, assignment.generation, controller.signal);
        if (!controller.signal.aborted) heartbeatTimer = setTimeout(() => void heartbeat(), 15_000);
      } catch (error) { controller.abort(error); }
    };
    heartbeatTimer = setTimeout(() => void heartbeat(), 15_000);

    let hotswap: HotswapExecutor | undefined;
    let plan: HotswapPlan | undefined;
    let verdict = 'BLOCKED';
    let summary = '';
    let syncTimer: ReturnType<typeof setInterval> | undefined;
    try {
      const inputs = await materializeInputs({
        jobRoot, spec,
        fetchInput: name => client.input(jobID, assignment.generation, name, controller.signal),
        log: message => { void this.registry.event(jobID, 'input', message); },
      });
      await this.registry.update(jobID, {
        inputs: [...inputs.values()].map(input => ({name: input.name, kind: input.kind, sha256: input.sha256, bytes: input.bytes, root: input.root})),
      });

      const loaded = await loadStories(spec, new Map([...inputs].map(([name, input]) => [name, input.root])));
      for (const warning of loaded.warnings) await this.registry.event(jobID, 'warning', warning);
      if (!loaded.stories.length) throw new Error('no readable story was supplied');
      await this.registry.update(jobID, {stories: loaded.stories});

      const workspace = await this.resolveWorkspace(jobRoot, inputs);
      await this.registry.update(jobID, {workspace});

      if (spec.engine?.hotswap) {
        const input = inputs.get(spec.engine.hotswap.input);
        if (!input) throw new Error('hotswap input was not materialised');
        const planPath = resolveInside(input.root, spec.engine.hotswap.path);
        plan = parseHotswapPlan(await readFile(planPath, 'utf8'));
        const installRoot = plan.install_root ?? spec.inputs?.install_root;
        if (process.platform === 'win32' && installRoot) {
          const manifest = join(installRoot, 'resources', 'release-manifest.json');
          try { await stat(manifest); }
          catch { await this.registry.event(jobID, 'warning', 'install root has no release-manifest.json: ' + installRoot); }
        }
        hotswap = new HotswapExecutor({jobRoot, sourceRoot: dirname(planPath), installRoot, log: message => { void this.registry.event(jobID, 'hotswap', message); }});
        const steps = await hotswap.apply(plan);
        await this.registry.update(jobID, {hotswap: {plan: plan.name, steps: steps as unknown as Array<Record<string, unknown>>}});
      }

      syncTimer = setInterval(() => { void this.sync(client, assignment, jobID); }, 10_000);
      const agent = await runAgent({
        jobID, jobRoot, spec, workspace, stories: loaded.stories,
        engineURL: this.server!.url, engineToken: token,
        command: spec.engine?.agent?.command || this.config.codexCommand,
        timeoutSeconds: spec.engine?.agent?.timeout_seconds ?? spec.timeout_seconds,
        log: message => { void this.registry.event(jobID, 'agent', message); },
        onLine: line => { void this.registry.event(jobID, 'agent-event', truncate(line, 2000)); },
      });
      await this.registry.update(jobID, {agent});
      const job = this.registry.get(jobID);
      verdict = job.verdict ?? 'BLOCKED';
      summary = job.summary ?? '';
      if (!job.verdict) {
        summary = agent.timedOut ? 'agent timed out before finishing the story'
          : 'agent exited with code ' + agent.exitCode + ' without calling engine_finish';
      }
      await this.registry.completed(jobID, verdict as EngineJobState['status'], summary);
    } catch (error) {
      verdict = 'BLOCKED';
      summary = error instanceof Error ? error.message : String(error);
      await this.registry.completed(jobID, 'BLOCKED', summary).catch(() => undefined);
    } finally {
      if (syncTimer) clearInterval(syncTimer);
      try {
        const restorePolicy = spec.engine?.hotswap?.restore_policy ?? 'always';
        const failed = verdict !== 'PASS';
        if (hotswap && (restorePolicy === 'always' || (restorePolicy === 'on_failure' && failed))) {
          await hotswap.restore(summary || 'job finished').catch(() => undefined);
          await this.registry.update(jobID, {hotswap: {
            plan: plan?.name ?? this.registry.get(jobID).hotswap?.plan ?? 'unknown',
            steps: hotswap.steps as unknown as Array<Record<string, unknown>>,
            restored: true,
          }});
        }
        await this.collectFinalEvidence(client, assignment, jobID);
        await this.sync(client, assignment, jobID);
        await this.registry.event(jobID, 'report', 'submitting ' + verdict + ' to SmartPipe').catch(() => undefined);
        try {
          await client.reportWithFindings(jobID, assignment.generation, {
            verdict, summary: summary || 'finished',
            checks: this.registry.get(jobID).checks,
            findings: this.registry.get(jobID).findings as unknown as Array<Record<string, unknown>>,
          });
          await this.registry.event(jobID, 'report', 'SmartPipe accepted ' + verdict).catch(() => undefined);
        } catch (error) {
          const message = error instanceof Error ? error.message : String(error);
          this.log('report failed: ' + message);
          await this.registry.event(jobID, 'error', 'report failed: ' + message).catch(() => undefined);
          throw error;
        }
        // Push the report lifecycle events before the lease is released.
        await this.sync(client, assignment, jobID);
        await client.release(jobID, assignment.generation).catch(error => this.log('release failed: ' + String(error)));
      } finally {
        // Keep the lease alive through evidence collection and reporting: a slow
        // screenshot or a large transcript upload must not expire the machine
        // lease before the verdict is accepted, and a failed report must still
        // clear every timer that would keep the runner process alive.
        controller.abort(new Error('job finished'));
        if (heartbeatTimer) clearTimeout(heartbeatTimer);
        outer?.removeEventListener('abort', abort);
        clearTimeout(deadlineTimer);
      }
    }
  }

  private async resolveWorkspace(jobRoot: string, inputs: Map<string, MaterializedInput>): Promise<string> {
    const workspaceInput = [...inputs.values()].find(input => input.kind === 'workspace');
    if (workspaceInput) return workspaceInput.root;
    const directory = join(jobRoot, 'workspace');
    await mkdir(directory, {recursive: true});
    return directory;
  }

  private async sync(client: SessionMachineClient, assignment: SessionLease, jobID: string): Promise<void> {
    try {
      const job = this.registry.get(jobID);
      await uploadJobEvidence(client, jobID, assignment.generation, job, this.sentEvidence);
      const sent = (globalThis as unknown as Record<string, number>)[jobID] ?? 0;
      const pending = job.checks.slice(sent);
      for (const check of pending) await client.check(jobID, assignment.generation, check);
      (globalThis as unknown as Record<string, number>)[jobID] = job.checks.length;
      const sentFindings = (globalThis as unknown as Record<string, number>)[jobID + ':findings'] ?? 0;
      for (const finding of job.findings.slice(sentFindings)) {
        await client.finding(jobID, assignment.generation, finding as unknown as Record<string, unknown>);
      }
      (globalThis as unknown as Record<string, number>)[jobID + ':findings'] = job.findings.length;
      // The console renders the timeline, but agent JSONL lines belong in the
      // transcript evidence, not in the session event table.
      const cursorKey = jobID + ':events';
      for (let batch = 0; batch < 20; batch++) {
        const cursor = (globalThis as unknown as Record<string, number>)[cursorKey] ?? 0;
        const pendingEvents = job.events
          .filter(event => event.seq > cursor && event.kind !== 'agent-event')
          .slice(0, 100);
        if (!pendingEvents.length) break;
        await client.events(jobID, assignment.generation, pendingEvents.map(event => ({
          seq: event.seq, kind: event.kind, message: truncate(event.message, 4000), at: event.at,
        })));
        const last = pendingEvents[pendingEvents.length - 1];
        if (last) (globalThis as unknown as Record<string, number>)[cursorKey] = last.seq;
      }
    } catch (error) {
      // The final report is authoritative, but a silent failure hides lease
      // and token problems, so record the first failure of each message.
      const message = error instanceof Error ? error.message : String(error);
      if (this.syncFailures.has(message)) return;
      this.syncFailures.add(message);
      await this.registry.event(jobID, 'warning', 'incremental sync failed: ' + message).catch(() => undefined);
    }
  }

  private async collectFinalEvidence(client: SessionMachineClient, assignment: SessionLease, jobID: string): Promise<void> {
    const jobRoot = join(this.config.stateRoot, 'engine-jobs', jobID);
    const directory = join(jobRoot, 'final');
    await mkdir(directory, {recursive: true});
    if (this.screenshotScript) {
      const out = join(directory, 'final-screen.png');
      try {
        const shot = await captureScreenshot(out, {
          script: this.screenshotScript, title: this.config.screenshotTitle,
          cdpPort: this.config.screenshotCDPPort, cdpURLMatch: this.config.screenshotCDPURLMatch,
          cdpProcess: this.config.screenshotCDPProcess,
          timeoutSec: 30,
        });
        if (shot.ok) await this.uploadEvidence(client, assignment, jobID, out, 'image/png', ' via ' + shot.via);
        else await this.registry.event(jobID, 'warning', 'final screenshot failed: ' + (shot.error ?? 'unknown error').slice(0, 300));
      } catch (error) { await this.registry.event(jobID, 'warning', 'final screenshot failed: ' + String(error)); }
    }
    try {
      const script = "Get-Process | Where-Object { $_.MainWindowHandle -ne 0 } | Select-Object Id,ProcessName,MainWindowTitle | ConvertTo-Json -Compress";
      const snapshot = await new Promise<string>((resolvePromise) => {
        const child = spawn('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command', script], {windowsHide: true});
        let stdout = '';
        child.stdout.on('data', chunk => { stdout += chunk.toString(); });
        child.once('close', () => resolvePromise(stdout.trim()));
      });
      if (snapshot) {
        const path = join(directory, 'windows.json');
        await writeFile(path, snapshot);
        await this.uploadEvidence(client, assignment, jobID, path, 'application/json');
      }
    } catch (error) { await this.registry.event(jobID, 'warning', 'process snapshot failed: ' + String(error)); }
    const transcript = this.registry.get(jobID).agent?.transcript;
    if (transcript) {
      try { await this.uploadEvidence(client, assignment, jobID, transcript, 'application/json'); }
      catch (error) { await this.registry.event(jobID, 'warning', 'transcript upload failed: ' + String(error)); }
    }
  }

  private async uploadEvidence(client: SessionMachineClient, assignment: SessionLease, jobID: string, path: string, mimeType: string, suffix = ''): Promise<void> {
    const bytes = await readFile(path);
    if (bytes.length > 8 << 20) {
      await this.registry.event(jobID, 'warning', 'evidence too large to upload: ' + path + ' (' + bytes.length + ' bytes)');
      return;
    }
    const reference = await client.evidence(jobID, assignment.generation, {
      sha256: createHash('sha256').update(bytes).digest('hex'), mime_type: mimeType, payload: bytes.toString('base64'),
    });
    await this.registry.event(jobID, 'evidence', 'uploaded ' + path + suffix, {reference});
  }
}

function truncate(value: string, max: number): string {
  return value.length > max ? value.slice(0, max) : value;
}

function validateSpec(spec: SessionSpec): string[] {
  const errors: string[] = [];
  if (!spec?.project_id) errors.push('project_id is required');
  if (!spec?.platform) errors.push('platform is required');
  if (!spec?.timeout_seconds) errors.push('timeout_seconds is required');
  errors.push(...validateStoryEvents(spec?.events));
  const required = new Set(spec?.required_checks ?? []);
  for (const event of spec?.events ?? []) {
    if (!required.has(event.method)) errors.push('event method is not a required check: ' + event.method);
  }
  return errors;
}
