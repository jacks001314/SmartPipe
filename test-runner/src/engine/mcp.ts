import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import type { CallToolResult } from '@modelcontextprotocol/sdk/types.js';
import { z } from 'zod';

const base = (process.env.SMARTPIPE_ENGINE_URL ?? '').replace(/\/$/, '');
const token = process.env.SMARTPIPE_ENGINE_TOKEN ?? '';
const jobID = process.env.SMARTPIPE_ENGINE_JOB ?? '';
if (!base || !token || !jobID) {
  throw new Error('SMARTPIPE_ENGINE_URL, SMARTPIPE_ENGINE_TOKEN and SMARTPIPE_ENGINE_JOB are required');
}

function text(value: unknown, isError = false): CallToolResult {
  return {content:[{type:'text', text: typeof value === 'string' ? value : JSON.stringify(value, null, 2)}], ...(isError ? {isError:true} : {})};
}

async function call<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(base + '/v1/jobs/' + encodeURIComponent(jobID) + path, {
    ...init,
    redirect: 'error',
    headers: {'Content-Type': 'application/json', Authorization: 'Bearer ' + token, ...(init.headers ?? {})},
    signal: AbortSignal.timeout(60_000),
  });
  const raw = await response.text();
  const payload = raw ? JSON.parse(raw) as {data?: T; error?: string} : {};
  if (!response.ok) throw new Error(payload.error || 'engine returned ' + response.status);
  return payload.data as T;
}

async function run<T>(fn: () => Promise<T>): Promise<CallToolResult> {
  try { return text(await fn()); }
  catch (error) { return text({error: error instanceof Error ? error.message : String(error)}, true); }
}

interface JobView {
  id: string; mode: string; title: string; platform: string; environment: string; status: string;
  workspace: string; deadline: string;
  spec: {events?: Array<{id: string; title: string; trigger?: string; trigger_kind?: string; method: string; expected?: string}>};
  stories: Array<{id: string; title: string; input: string; path: string; required_checks: string[];
    events: Array<{id: string; title: string; trigger?: string; trigger_kind?: string; method: string; expected?: string}>}>;
  checks: unknown[]; findings: unknown[]; inputs: Array<{name: string; kind: string; sha256: string; bytes: number}>;
  hotswap?: {plan: string; steps: Array<Record<string, unknown>>; restored?: boolean; error?: string};
}

const server = new McpServer(
  {name:'smartpipe-engine', version:'0.2.0'},
  {instructions: `You are the Codex agent inside the SmartPipe test engine, running on a real test machine.
SmartPipe already fetched and staged every input: never clone or pull source yourself.
Start with engine_job, then read the assigned story with engine_story_read and follow it.
Use your native shell and process tools to operate the product. Every PASS check needs evidence:
capture a screenshot or upload a real artifact first. Report defects with engine_finding_report.
End with engine_finish only when the story is complete or genuinely blocked.`},
);

server.registerTool('engine_job', {
  title: 'Read the job',
  description: 'Return the job context: mode, workspace, inputs with hashes, stories, hotswap result, checks and findings. Call this first.',
  inputSchema: {},
  annotations: {readOnlyHint: true},
}, async () => run(async () => {
  const job = await call<JobView>('');
  return {
    id: job.id, mode: job.mode, title: job.title, platform: job.platform, environment: job.environment,
    status: job.status, workspace: job.workspace, deadline: job.deadline,
    inputs: job.inputs, stories: job.stories, events: job.spec.events ?? [], hotswap: job.hotswap ?? null,
    checks: job.checks, findings: job.findings,
  };
}));

server.registerTool('engine_story_list', {
  title: 'List stories',
  description: 'List the Markdown stories assigned to this job.',
  inputSchema: {},
  annotations: {readOnlyHint: true},
}, async () => run(() => call('/stories')));

server.registerTool('engine_story_read', {
  title: 'Read a story',
  description: 'Return one story by id, including its Markdown body and required checks.',
  inputSchema: {id: z.string().min(1).max(80)},
  annotations: {readOnlyHint: true},
}, async ({id}) => run(() => call('/stories/' + encodeURIComponent(id))));

server.registerTool('engine_check_record', {
  title: 'Record a check',
  description: 'Record or replace one acceptance check. PASS must reference evidence (sha256:...).',
  inputSchema: {
    id: z.string().min(1).max(80),
    status: z.enum(['PASS', 'FAIL', 'BLOCKED', 'SKIPPED']),
    evidence: z.string().max(200).optional(),
    note: z.string().max(4000).optional(),
  },
  annotations: {readOnlyHint: false},
}, async input => run(() => call('/checks', {method: 'POST', body: JSON.stringify(input)})));

server.registerTool('engine_finding_report', {
  title: 'Report a finding',
  description: 'Record a defect. Use severity blocker when it must prevent PASS. Attach sha256 evidence references.',
  inputSchema: {
    id: z.string().min(1).max(80).optional(),
    severity: z.enum(['blocker', 'critical', 'major', 'minor', 'info']),
    title: z.string().min(1).max(200),
    detail: z.string().max(8000).optional(),
    repro: z.string().max(4000).optional(),
    expected: z.string().max(2000).optional(),
    actual: z.string().max(2000).optional(),
    evidence: z.array(z.string().regex(/^sha256:[a-f0-9]{64}$/)).max(16).optional(),
    check_ref: z.string().max(80).optional(),
  },
  annotations: {readOnlyHint: false},
}, async input => run(() => call('/findings', {method: 'POST', body: JSON.stringify(input)})));

server.registerTool('engine_evidence_upload', {
  title: 'Upload evidence',
  description: 'Register a file from the test machine or inline base64 as evidence and return a sha256 reference.',
  inputSchema: {
    path: z.string().optional().describe('Absolute path on the test machine.'),
    base64: z.string().optional().describe('Inline bytes, base64 encoded.'),
    mime_type: z.enum(['image/png', 'image/jpeg', 'application/json', 'text/plain', 'text/markdown']).optional(),
  },
  annotations: {readOnlyHint: false},
}, async input => run(() => call('/evidence', {method: 'POST', body: JSON.stringify(input)})));

server.registerTool('engine_evidence_capture', {
  title: 'Capture the desktop',
  description: 'Capture a window (by title substring) or the full screen and register it as evidence.',
  inputSchema: {title: z.string().max(200).optional(), full_screen: z.boolean().optional()},
  annotations: {readOnlyHint: true},
}, async input => run(() => call('/capture', {method: 'POST', body: JSON.stringify(input)})));

server.registerTool('engine_log', {
  title: 'Write an event',
  description: 'Append a progress note to the job event stream.',
  inputSchema: {message: z.string().min(1).max(2000)},
  annotations: {readOnlyHint: false},
}, async ({message}) => run(() => call('/events', {method: 'POST', body: JSON.stringify({message})})));

server.registerTool('engine_finish', {
  title: 'Finish the job',
  description: 'Submit the verdict. Required checks and blocker findings are enforced by SmartPipe.',
  inputSchema: {verdict: z.enum(['PASS', 'FAIL', 'BLOCKED']), summary: z.string().min(1).max(4000)},
  annotations: {readOnlyHint: false},
}, async input => run(() => call('/finish', {method: 'POST', body: JSON.stringify(input)})));

process.once('SIGINT', () => { void server.close(); });
process.once('SIGTERM', () => { void server.close(); });
await server.connect(new StdioServerTransport());
