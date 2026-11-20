// The engine consumes inputs that SmartPipe's scheduler already acquired. It
// never runs `git clone` and never resolves branches: a job only contains
// immutable blobs with hashes plus the story/hotswap references that point
// inside them.

export type JobMode = 'story' | 'bug' | 'explore' | 'hotswap' | 'gate';
export type CheckStatus = 'PASS' | 'FAIL' | 'BLOCKED' | 'SKIPPED';
export type Severity = 'blocker' | 'critical' | 'major' | 'minor' | 'info';

export interface EngineInputSpec {
  name: string;
  kind: 'workspace' | 'artifacts' | 'stories' | 'hotswap' | 'file';
  sha256: string;
  bytes?: number;
  unpack?: 'tar.gz' | 'zip' | 'none';
  target?: string;
}

export interface EngineStorySpec {
  input: string;
  path: string;
  title?: string;
  required_checks?: string[];
}

export interface HotswapSpec {
  input: string;
  path: string;
  restore_policy?: 'always' | 'on_failure' | 'never';
}

export interface EngineAgentSpec {
  command?: string;
  model?: string;
  sandbox?: 'read-only' | 'workspace-write' | 'danger-full-access';
  args?: string[];
  extra_args?: string[];
  timeout_seconds?: number;
  env?: Record<string, string>;
}

/** One story trigger and the check id (test method) that validates it. */
export interface StoryEventSpec {
  id: string;
  title: string;
  trigger?: string;
  trigger_kind?: 'ui' | 'shell' | 'http' | 'manual';
  method: string;
  expected?: string;
}

export interface EngineSpec {
  inputs?: EngineInputSpec[];
  stories?: EngineStorySpec[];
  hotswap?: HotswapSpec;
  agent?: EngineAgentSpec;
  env?: Record<string, string>;
  workdir?: string;
}

export interface SessionSpec {
  project_id: string;
  title: string;
  kind?: string;
  mode?: JobMode;
  environment: string;
  platform: string;
  story_ref?: string;
  story?: string;
  inputs?: Record<string, string>;
  required_checks?: string[];
  events?: StoryEventSpec[];
  timeout_seconds: number;
  work_dir?: string;
  engine?: EngineSpec;
}

export interface SessionCheck {
  id: string;
  status: CheckStatus;
  evidence?: string;
  note?: string;
  updated_at?: string;
}

export interface Finding {
  id: string;
  severity: Severity;
  status?: 'open' | 'observed' | 'fixed' | 'wontfix';
  title: string;
  detail?: string;
  repro?: string;
  expected?: string;
  actual?: string;
  evidence?: string[];
  check_ref?: string;
}

export interface StoryDocument {
  id: string;
  input: string;
  path: string;
  title: string;
  required_checks: string[];
  events: StoryEventSpec[];
  body: string;
}

export const MODES: JobMode[] = ['story', 'bug', 'explore', 'hotswap', 'gate'];
export const TRIGGER_KINDS = ['ui', 'shell', 'http', 'manual'];
const INPUT_KINDS = ['workspace', 'artifacts', 'stories', 'hotswap', 'file'];
const UNPACK = ['tar.gz', 'zip', 'none'];
const SANDBOX = ['read-only', 'workspace-write', 'danger-full-access'];

export function isSafeRelative(value: unknown): value is string {
  if (typeof value !== 'string') return false;
  const trimmed = value.trim();
  if (!trimmed || trimmed.length > 256) return false;
  if (trimmed.startsWith('/') || trimmed.startsWith('\\') || /^[a-zA-Z]:[\\/]/.test(trimmed)) return false;
  if (trimmed.includes('//') || trimmed.includes('\\\\')) return false;
  return trimmed.split(/[\\/]/).every(part => part && part !== '.' && part !== '..');
}

export function validCheckID(value: unknown): value is string {
  return typeof value === 'string' && /^[a-zA-Z0-9_.-]{1,80}$/.test(value);
}

/** Validate the story event contract the console authored. */
export function validateStoryEvents(events: StoryEventSpec[] | undefined): string[] {
  if (!events?.length) return [];
  const errors: string[] = [];
  if (events.length > 64) errors.push('too many story events');
  const ids = new Set<string>();
  const methods = new Set<string>();
  for (const event of events) {
    if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(event.id ?? '')) errors.push('invalid event id ' + event.id);
    if (ids.has(event.id)) errors.push('duplicate event id ' + event.id);
    ids.add(event.id);
    if (!event.title?.trim() || event.title.length > 200) errors.push('invalid event title ' + event.id);
    if (!validCheckID(event.method)) errors.push('invalid event method ' + event.method);
    if (methods.has(event.method)) errors.push('duplicate event method ' + event.method);
    methods.add(event.method);
    if (event.trigger_kind && !TRIGGER_KINDS.includes(event.trigger_kind)) errors.push('invalid trigger_kind for ' + event.id);
    if ((event.trigger ?? '').length > 4000 || (event.expected ?? '').length > 4000) errors.push('event text too long for ' + event.id);
  }
  return errors;
}

/** Validate the engine half of a session defensively. The server already did this. */
export function validateEngineSpec(spec: EngineSpec | undefined): string[] {
  if (!spec) return [];
  const errors: string[] = [];
  const inputs = spec.inputs ?? [];
  if (inputs.length > 16) errors.push('too many engine inputs');
  const names = new Set<string>();
  for (const input of inputs) {
    if (!/^[a-z][a-z0-9_-]{0,31}$/.test(input.name)) errors.push('invalid input name ' + input.name);
    if (names.has(input.name)) errors.push('duplicate input ' + input.name);
    names.add(input.name);
    if (!INPUT_KINDS.includes(input.kind)) errors.push('invalid input kind ' + input.kind);
    if (!/^[a-f0-9]{64}$/i.test(input.sha256)) errors.push('invalid sha256 for ' + input.name);
    if (input.unpack && !UNPACK.includes(input.unpack)) errors.push('invalid unpack for ' + input.name);
    if (input.target && !isSafeRelative(input.target)) errors.push('invalid target for ' + input.name);
    if (input.bytes !== undefined && (!Number.isSafeInteger(input.bytes) || input.bytes < 0)) errors.push('invalid size for ' + input.name);
  }
  for (const story of spec.stories ?? []) {
    if (!names.has(story.input)) errors.push('story references unknown input ' + story.input);
    if (!isSafeRelative(story.path)) errors.push('invalid story path ' + story.path);
    for (const check of story.required_checks ?? []) if (!validCheckID(check)) errors.push('invalid story check ' + check);
  }
  if (spec.hotswap) {
    if (!names.has(spec.hotswap.input)) errors.push('hotswap references unknown input');
    if (!isSafeRelative(spec.hotswap.path)) errors.push('invalid hotswap path');
    if (spec.hotswap.restore_policy && !['always', 'on_failure', 'never'].includes(spec.hotswap.restore_policy)) {
      errors.push('invalid restore policy');
    }
  }
  if (spec.agent?.sandbox && !SANDBOX.includes(spec.agent.sandbox)) errors.push('invalid agent sandbox');
  return errors;
}

export function validateFinding(finding: Finding): string[] {
  const errors: string[] = [];
  if (!validCheckID(finding.id)) errors.push('invalid finding id');
  if (!['blocker', 'critical', 'major', 'minor', 'info'].includes(finding.severity)) errors.push('invalid severity');
  if (finding.status && !['open', 'observed', 'fixed', 'wontfix'].includes(finding.status)) errors.push('invalid status');
  if (!finding.title?.trim() || finding.title.length > 200) errors.push('title is required and limited to 200 chars');
  for (const ref of finding.evidence ?? []) {
    if (!/^sha256:[a-f0-9]{64}$/.test(ref)) errors.push('evidence must be sha256 references');
  }
  if ((finding.evidence ?? []).length > 16) errors.push('too many evidence references');
  if (finding.check_ref && !validCheckID(finding.check_ref)) errors.push('invalid check_ref');
  return errors;
}

export function jobModeOf(spec: SessionSpec): JobMode {
  const mode = spec.mode ?? spec.kind ?? 'story';
  return (MODES as string[]).includes(mode) ? mode as JobMode : 'story';
}
