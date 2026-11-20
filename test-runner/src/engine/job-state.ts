import { createHash, randomBytes } from 'node:crypto';
import { appendFile, mkdir, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import type { Finding, JobMode, SessionCheck, SessionSpec, StoryDocument } from './contract.js';

export interface JobEvent {
  seq: number;
  at: string;
  kind: string;
  message: string;
  detail?: Record<string, unknown>;
}

export interface EvidenceRecord { sha256: string; mimeType: string; path: string; bytes: number }

export interface EngineJobState {
  id: string;
  mode: JobMode;
  title: string;
  platform: string;
  environment: string;
  status: 'running' | 'finishing' | 'PASS' | 'FAIL' | 'BLOCKED' | 'CANCELLED';
  startedAt: string;
  deadline: string;
  workspace: string;
  spec: SessionSpec;
  inputs: Array<{name: string; kind: string; sha256: string; bytes: number; root: string}>;
  stories: StoryDocument[];
  checks: SessionCheck[];
  findings: Finding[];
  hotswap?: {plan: string; steps: Array<Record<string, unknown>>; restored?: boolean; error?: string};
  agent?: {command: string; exitCode: number; timedOut: boolean; transcript: string; lastMessage: string};
  verdict?: string;
  summary?: string;
  finishedAt?: string;
  evidence: Record<string, EvidenceRecord>;
  events: JobEvent[];
}

function extFor(mime: string): string {
  switch (mime) {
    case 'image/png': return '.png';
    case 'image/jpeg': return '.jpg';
    case 'application/json': return '.json';
    case 'text/plain': return '.txt';
    case 'text/markdown': return '.md';
    default: return '.bin';
  }
}

// Windows PowerShell 5.1 rejects some raw control characters when it reads the
// state file back. Node can parse them, but the operator console cannot.
function sanitize(value: string): string {
  return value.replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/g, '');
}

export class JobRegistry {
  private readonly jobs = new Map<string, EngineJobState>();
  private readonly waiters = new Map<string, Array<() => void>>();
  private readonly tokens = new Map<string, string>();

  constructor(private readonly stateRoot: string) {}

  newToken(jobID: string): string {
    const token = randomBytes(24).toString('hex');
    this.tokens.set(jobID, token);
    return token;
  }

  authorize(jobID: string, token: string): boolean {
    return Boolean(token) && this.tokens.get(jobID) === token;
  }

  async create(state: EngineJobState): Promise<void> {
    this.jobs.set(state.id, state);
    await this.persist(state);
  }

  async update(jobID: string, patch: Partial<EngineJobState>): Promise<EngineJobState> {
    const job = this.get(jobID);
    Object.assign(job, patch);
    await this.persist(job);
    return job;
  }

  get(jobID: string): EngineJobState {
    const job = this.jobs.get(jobID);
    if (!job) throw new Error('unknown job ' + jobID);
    return job;
  }

  has(jobID: string): boolean { return this.jobs.has(jobID); }

  list(): EngineJobState[] {
    return [...this.jobs.values()].sort((a, b) => a.startedAt.localeCompare(b.startedAt));
  }

  private async persist(job: EngineJobState): Promise<void> {
    const directory = join(this.stateRoot, job.id);
    await mkdir(directory, {recursive: true});
    await writeFile(join(directory, 'state.json'), JSON.stringify(job, null, 2));
  }

  async event(jobID: string, kind: string, message: string, detail?: Record<string, unknown>): Promise<JobEvent> {
    const job = this.get(jobID);
    const event: JobEvent = {seq: job.events.length + 1, at: new Date().toISOString(), kind, message: sanitize(message), ...(detail ? {detail} : {})};
    job.events.push(event);
    if (job.events.length > 4000) job.events.splice(0, job.events.length - 4000);
    const directory = join(this.stateRoot, job.id);
    await mkdir(directory, {recursive: true});
    await appendFile(join(directory, 'events.ndjson'), JSON.stringify(event) + '\n');
    await this.persist(job);
    return event;
  }

  async check(jobID: string, check: SessionCheck): Promise<SessionCheck[]> {
    const job = this.get(jobID);
    if (job.status !== 'running') throw new Error('job is not running');
    const existing = job.checks.findIndex(item => item.id === check.id);
    const next = {...check, updated_at: new Date().toISOString()};
    if (existing >= 0) job.checks[existing] = next; else job.checks.push(next);
    await this.event(jobID, 'check', check.id + ' ' + check.status, {evidence: check.evidence ?? ''});
    return job.checks;
  }

  async finding(jobID: string, finding: Finding): Promise<Finding> {
    const job = this.get(jobID);
    const next: Finding = {...finding, status: finding.status ?? 'open'};
    const existing = job.findings.findIndex(item => item.id === next.id);
    if (existing >= 0) job.findings[existing] = next; else job.findings.push(next);
    await this.event(jobID, 'finding', next.id + ' ' + next.severity + ' ' + next.title);
    return next;
  }

  async addEvidence(jobID: string, filename: string, mimeType: string, bytes: Buffer): Promise<string> {
    if (!bytes.length || bytes.length > 8 << 20) throw new Error('evidence must be 1 byte..8 MiB');
    const job = this.get(jobID);
    const sha256 = createHash('sha256').update(bytes).digest('hex');
    const directory = join(this.stateRoot, jobID, 'evidence');
    await mkdir(directory, {recursive: true});
    const path = join(directory, sha256 + extFor(mimeType));
    await writeFile(path, bytes);
    job.evidence[sha256] = {sha256, mimeType, path, bytes: bytes.length};
    await this.event(jobID, 'evidence', (filename || sha256) + ' ' + bytes.length + ' bytes', {sha256, mime_type: mimeType});
    return 'sha256:' + sha256;
  }

  async finish(jobID: string, verdict: 'PASS' | 'FAIL' | 'BLOCKED', summary: string): Promise<void> {
    const job = this.get(jobID);
    if (job.status !== 'running') throw new Error('job already finished');
    job.status = 'finishing';
    job.verdict = verdict;
    job.summary = summary.slice(0, 4000);
    await this.event(jobID, 'finish', 'agent requested ' + verdict);
    for (const wake of this.waiters.get(jobID) ?? []) wake();
    this.waiters.delete(jobID);
  }

  async completed(jobID: string, status: EngineJobState['status'], summary?: string): Promise<void> {
    const job = this.get(jobID);
    job.status = status;
    if (summary) job.summary = summary.slice(0, 4000);
    job.finishedAt ??= new Date().toISOString();
    await this.event(jobID, 'completed', status);
  }

  waitForFinish(jobID: string, signal: AbortSignal): Promise<void> {
    const job = this.get(jobID);
    if (job.status !== 'running') return Promise.resolve();
    return new Promise((resolvePromise, reject) => {
      const wake = () => { cleanup(); resolvePromise(); };
      const abort = () => { cleanup(); reject(signal.reason ?? new Error('aborted')); };
      const cleanup = () => {
        signal.removeEventListener('abort', abort);
        const list = this.waiters.get(jobID) ?? [];
        this.waiters.set(jobID, list.filter(item => item !== wake));
      };
      this.waiters.set(jobID, [...(this.waiters.get(jobID) ?? []), wake]);
      signal.addEventListener('abort', abort, {once: true});
      if (signal.aborted) abort();
    });
  }

  async loadFromDisk(jobID: string): Promise<EngineJobState | undefined> {
    try {
      const raw = await readFile(join(this.stateRoot, jobID, 'state.json'), 'utf8');
      const state = JSON.parse(raw) as EngineJobState;
      this.jobs.set(jobID, state);
      return state;
    } catch { return undefined; }
  }
}
