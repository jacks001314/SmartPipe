import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import http from 'node:http';
import { createHash } from 'node:crypto';
import { validateEngineSpec, validateFinding, validateStoryEvents, type SessionSpec } from '../src/engine/contract.js';
import { buildAgentPrompt } from '../src/engine/agent.js';
import { parseFrontMatter, loadStories, requiredChecks } from '../src/engine/stories.js';
import { HotswapExecutor, parseHotswapPlan } from '../src/engine/hotswap.js';
import { assertSafeArchiveEntries } from '../src/engine/inputs.js';
import { JobRegistry } from '../src/engine/job-state.js';
import { startEngineServer } from '../src/engine/server.js';
import { uploadJobEvidence } from '../src/engine/host.js';
import { captureScreenshot, pickPageTarget } from '../src/engine/screenshot.js';

function jobSpec(overrides: Partial<SessionSpec> = {}): SessionSpec {
  return {
    project_id: 'p', title: 't', mode: 'story', environment: 'syntropy', platform: 'windows-amd64',
    timeout_seconds: 600, ...overrides,
  };
}

test('story front matter keeps metadata and leaves the body intact', () => {
  const {meta, body} = parseFrontMatter('---\nid: smoke\ntitle: Install smoke\nrequired_checks:\n  - app_installed\n  - app_window\n---\n# Steps\nDo it.\n');
  assert.equal(meta.id, 'smoke');
  assert.equal(meta.title, 'Install smoke');
  assert.deepEqual(meta.required_checks, ['app_installed', 'app_window']);
  assert.match(body, /# Steps/);
});

test('engine stories resolve inside their verified input and merge checks', async () => {
  const root = await mkdtemp(join(tmpdir(), 'engine-story-'));
  await writeFile(join(root, 'a.md'), '---\nrequired_checks: [ui]\n---\n# A\n');
  const spec = jobSpec({
    engine: {inputs: [{name: 'stories', kind: 'stories', sha256: 'a'.repeat(64), unpack: 'tar.gz'}],
      stories: [{input: 'stories', path: 'a.md', required_checks: ['extra']}]},
  });
  const loaded = await loadStories(spec, new Map([['stories', root]]));
  assert.equal(loaded.stories.length, 1);
  assert.deepEqual(requiredChecks(loaded.stories).sort(), ['extra', 'ui']);
  assert.deepEqual(loaded.warnings, []);
});

test('inline story is preserved when no story file is named', async () => {
  const loaded = await loadStories(jobSpec({story: '# Inline\nrun it', required_checks: ['x']}), new Map());
  assert.equal(loaded.stories[0]?.id, 'inline');
  assert.deepEqual(loaded.stories[0]?.required_checks, ['x']);
});

test('story events ride along with the inline story and reach the agent prompt', async () => {
  const events = [{id: 'open-settings', title: 'Open settings', trigger_kind: 'ui' as const,
    trigger: 'click Settings', method: 'settings_open', expected: 'settings visible'}];
  const spec = jobSpec({story: '# Inline\nrun it', required_checks: ['settings_open'], events});
  const loaded = await loadStories(spec, new Map());
  assert.deepEqual(loaded.stories[0]?.events, events);
  const prompt = buildAgentPrompt({jobID: 'job-1', spec, workspace: 'C:\\ws', stories: loaded.stories});
  assert.match(prompt, /open-settings \| Open settings \| method: settings_open/);
  assert.match(prompt, /engine_check_record with each method id verbatim/);
});

test('story event validation rejects duplicate methods and bad trigger kinds', () => {
  assert.deepEqual(validateStoryEvents([
    {id: 'a', title: 'A', method: 'check_a', trigger_kind: 'shell'},
    {id: 'b', title: 'B', method: 'check_b', trigger_kind: 'ui'},
  ]), []);
  const errors = validateStoryEvents([
    {id: 'a', title: 'A', method: 'same'},
    {id: 'b', title: 'B', method: 'same', trigger_kind: 'sms' as never},
  ]);
  assert.ok(errors.some(error => error.includes('duplicate event method')));
  assert.ok(errors.some(error => error.includes('invalid trigger_kind')));
});

test('engine contract rejects unsafe inputs, stories and hotswap references', () => {
  const good = jobSpec({engine: {inputs: [{name: 'ws', kind: 'workspace', sha256: 'a'.repeat(64), unpack: 'tar.gz', target: 'workspace'}]}});
  assert.deepEqual(validateEngineSpec(good.engine), []);
  const bad = jobSpec({engine: {inputs: [{name: 'ws', kind: 'workspace', sha256: 'zz', unpack: 'rar' as never, target: '../x'}],
    stories: [{input: 'missing', path: 'C:\\a.md'}], hotswap: {input: 'missing', path: '..\\plan.json'}}});
  const errors = validateEngineSpec(bad.engine);
  assert.ok(errors.length >= 5, errors.join('; '));
  assert.deepEqual(validateFinding({id: 'f1', severity: 'blocker', title: 'x'}), []);
  assert.ok(validateFinding({id: 'f1', severity: 'urgent' as never, title: 'x'}).length > 0);
});

test('archive guard rejects absolute paths, escapes and links', () => {
  assertSafeArchiveEntries(['-rw-r--r-- 0/0 10 2026-11-18 10:00 file.txt', 'drwxr-xr-x 0/0 0 2026-11-18 10:00 dir/']);
  assert.throws(() => assertSafeArchiveEntries(['-rw-r--r-- 0/0 10 2026-11-18 10:00 /etc/passwd']));
  assert.throws(() => assertSafeArchiveEntries(['-rw-r--r-- 0/0 10 2026-11-18 10:00 ../x']));
  assert.throws(() => assertSafeArchiveEntries(['lrwxrwxrwx 0/0 0 2026-11-18 10:00 link -> /etc/passwd']));
});

test('hotswap copies, hashes, backs up and restores the original file', async () => {
  const root = await mkdtemp(join(tmpdir(), 'engine-hs-'));
  const install = join(root, 'install');
  const source = join(root, 'src');
  await mkdir(join(install, 'resources'), {recursive: true});
  await mkdir(source, {recursive: true});
  await writeFile(join(install, 'resources', 'nbot.exe'), 'old');
  await writeFile(join(source, 'nbot.exe'), 'new');
  const executor = new HotswapExecutor({jobRoot: join(root, 'job'), sourceRoot: source, installRoot: install});
  const steps = await executor.apply({schema: 1, name: 'swap', steps: [
    {op: 'copy', source: 'nbot.exe', target: 'resources/nbot.exe'},
    {op: 'note', note: 'swapped'},
  ]});
  assert.equal(steps.filter(step => step.status === 'ok').length, 2);
  assert.equal(await readFile(join(install, 'resources', 'nbot.exe'), 'utf8'), 'new');
  await executor.restore('test');
  assert.equal(await readFile(join(install, 'resources', 'nbot.exe'), 'utf8'), 'old');
});

test('hotswap refuses app.asar unless the plan explicitly allows it', async () => {
  const root = await mkdtemp(join(tmpdir(), 'engine-asar-'));
  await mkdir(join(root, 'install', 'resources'), {recursive: true});
  await mkdir(join(root, 'src'), {recursive: true});
  await writeFile(join(root, 'install', 'resources', 'app.asar'), 'old');
  await writeFile(join(root, 'src', 'app.asar'), 'new');
  const executor = new HotswapExecutor({jobRoot: join(root, 'job'), sourceRoot: join(root, 'src'), installRoot: join(root, 'install')});
  await assert.rejects(() => executor.apply({schema: 1, name: 'asar', steps: [
    {op: 'copy', source: 'app.asar', target: 'resources/app.asar'},
  ]}), /ASAR integrity/);
});

test('hotswap restore runs explicit restore_steps after writing backups back', async () => {
  const root = await mkdtemp(join(tmpdir(), 'engine-hs-restore-'));
  const install = join(root, 'install');
  const source = join(root, 'src');
  await mkdir(join(install, 'resources'), {recursive: true});
  await mkdir(source, {recursive: true});
  await writeFile(join(install, 'resources', 'a.txt'), 'old');
  await writeFile(join(source, 'a.txt'), 'new');
  const executor = new HotswapExecutor({jobRoot: join(root, 'job'), sourceRoot: source, installRoot: install});
  await executor.apply({schema: 1, name: 'swap', steps: [
    {op: 'copy', source: 'a.txt', target: 'resources/a.txt'},
  ], restore_steps: [{op: 'note', note: 'product restarted'}]});
  assert.equal(await readFile(join(install, 'resources', 'a.txt'), 'utf8'), 'new');
  await executor.restore('test');
  assert.equal(await readFile(join(install, 'resources', 'a.txt'), 'utf8'), 'old');
  const restored = executor.steps.filter(step => step.phase === 'restore');
  assert.equal(restored.length, 1);
  assert.equal(restored[0]?.detail, 'product restarted');
});

test('hotswap plan validation bounds restore_steps', () => {
  const good = parseHotswapPlan(JSON.stringify({schema: 1, name: 'p', steps: [{op: 'note', note: 'x'}],
    restore_steps: [{op: 'note', note: 'restart'}]}));
  assert.equal(good.restore_steps?.length, 1);
  assert.throws(() => parseHotswapPlan(JSON.stringify({schema: 1, name: 'p', steps: [{op: 'note', note: 'x'}],
    restore_steps: Array.from({length: 33}, () => ({op: 'note', note: 'x'}))})), /restore_steps/);
});

test('engine API records checks, findings, evidence and finish with a job token', async () => {
  const root = await mkdtemp(join(tmpdir(), 'engine-api-'));
  const registry = new JobRegistry(root);
  await registry.create({
    id: 'job1', mode: 'story', title: 't', platform: 'windows-amd64', environment: 'syntropy',
    status: 'running', startedAt: new Date().toISOString(), deadline: new Date(Date.now() + 60_000).toISOString(),
    workspace: root, spec: jobSpec(), inputs: [], stories: [], checks: [], findings: [], evidence: {}, events: [],
  });
  const token = registry.newToken('job1');
  const server = await startEngineServer({registry, stateRoot: root, controlToken: 'control', port: 0});
  try {
    const headers = {'Content-Type': 'application/json', Authorization: 'Bearer ' + token};
    const base = server.url + '/v1/jobs/job1';
    let response = await fetch(base, {headers});
    assert.equal(response.status, 200);
    response = await fetch(base + '/evidence', {method: 'POST', headers,
      body: JSON.stringify({base64: Buffer.from('hello').toString('base64'), mime_type: 'text/plain'})});
    const evidence = await response.json() as {data: {reference: string}};
    assert.match(evidence.data.reference, /^sha256:/);
    response = await fetch(base + '/checks', {method: 'POST', headers,
      body: JSON.stringify({id: 'ui', status: 'PASS', evidence: evidence.data.reference, note: 'seen'})});
    assert.equal(response.status, 200);
    response = await fetch(base + '/findings', {method: 'POST', headers,
      body: JSON.stringify({id: 'f1', severity: 'minor', title: 'typo', evidence: [evidence.data.reference]})});
    assert.equal(response.status, 200);
    response = await fetch(base + '/finish', {method: 'POST', headers, body: JSON.stringify({verdict: 'PASS', summary: 'done'})});
    assert.equal(response.status, 200);
    assert.equal(registry.get('job1').status, 'finishing');
    response = await fetch(base, {headers: {Authorization: 'Bearer wrong'}});
    assert.equal(response.status, 401);
  } finally {
    await server.close();
  }
});

test('engine server creates a missing job directory instead of failing to start', async () => {
  const root = await mkdtemp(join(tmpdir(), 'engine-state-'));
  const stateRoot = join(root, 'engine-jobs');
  const registry = new JobRegistry(stateRoot);
  const server = await startEngineServer({registry, stateRoot, controlToken: 'control', port: 0});
  try {
    const runtime = JSON.parse(await readFile(join(stateRoot, 'engine-runtime.json'), 'utf8')) as {url: string; port: number};
    assert.equal(runtime.url, server.url);
    assert.ok(runtime.port > 0);
  } finally {
    await server.close();
  }
});

test('engine server closes even when a client keeps a keep-alive socket open', async () => {
  const root = await mkdtemp(join(tmpdir(), 'engine-close-'));
  const registry = new JobRegistry(root);
  const server = await startEngineServer({registry, stateRoot: root, controlToken: 'control', port: 0});
  const agent = new http.Agent({keepAlive: true, maxSockets: 1});
  await new Promise<void>((resolvePromise, reject) => {
    http.get(server.url + '/v1/health', {agent}, response => {
      response.resume();
      response.once('end', () => resolvePromise());
    }).once('error', reject);
  });
  const started = Date.now();
  await server.close();
  agent.destroy();
  assert.ok(Date.now() - started < 2_000, 'close must not wait for the idle socket');
});

test('job events strip control characters that break PowerShell JSON parsing', async () => {
  const root = await mkdtemp(join(tmpdir(), 'engine-events-'));
  const registry = new JobRegistry(root);
  await registry.create({
    id: 'job1', mode: 'story', title: 't', platform: 'windows-amd64', environment: 'syntropy',
    status: 'running', startedAt: new Date().toISOString(), deadline: new Date(Date.now() + 60_000).toISOString(),
    workspace: root, spec: jobSpec(), inputs: [], stories: [], checks: [], findings: [], evidence: {}, events: [],
  });
  const event = await registry.event('job1', 'agent', 'line\u0000with\u0007controls\nkept');
  assert.equal(event.message, 'linewithcontrols\nkept');
});

test('engine uploads agent evidence to SmartPipe once before reporting', async () => {
  const root = await mkdtemp(join(tmpdir(), 'engine-evidence-'));
  const file = join(root, 'note.txt');
  await writeFile(file, 'hello evidence');
  const sha256 = createHash('sha256').update('hello evidence').digest('hex');
  const sent = new Set<string>();
  const uploaded: string[] = [];
  const client = {evidence: async (_session: string, _generation: number, evidence: {sha256: string}) => {
    uploaded.push(evidence.sha256);
    return 'sha256:' + evidence.sha256;
  }};
  const job = {
    evidence: {[sha256]: {sha256, mimeType: 'text/plain', path: file, bytes: 14}},
  } as unknown as import('../src/engine/job-state.js').EngineJobState;
  await uploadJobEvidence(client, 'job1', 1, job, sent);
  await uploadJobEvidence(client, 'job1', 1, job, sent);
  assert.deepEqual(uploaded, [sha256]);
  assert.ok(sent.has(sha256));
});

test('cdp target selection prefers the app renderer over the splash page', () => {
  const target = pickPageTarget([
    {type: 'page', url: 'data:text/html,<title>Syntropy</title>', webSocketDebuggerUrl: 'ws://splash'},
    {type: 'page', url: 'app://renderer/index.html', webSocketDebuggerUrl: 'ws://app'},
    {type: 'service_worker', url: 'app://sw.js', webSocketDebuggerUrl: 'ws://sw'},
  ], '');
  assert.equal(target?.webSocketDebuggerUrl, 'ws://app');
});

test('cdp target selection honours an explicit url match', () => {
  const target = pickPageTarget([
    {type: 'page', url: 'app://renderer/index.html', webSocketDebuggerUrl: 'ws://app'},
    {type: 'page', url: 'app://renderer/pet.html', webSocketDebuggerUrl: 'ws://pet'},
  ], 'pet.html');
  assert.equal(target?.webSocketDebuggerUrl, 'ws://pet');
});

test('an unreachable cdp endpoint falls back to the native window capture', {skip: process.platform !== 'win32'}, async () => {
  const root = await mkdtemp(join(tmpdir(), 'engine-shot-'));
  const script = join(root, 'fake-capture.ps1');
  await writeFile(script, [
    'param([string]$OutPath,[string]$Title="",[int]$TimeoutSec=20,[switch]$FullScreen)',
    '$dir = Split-Path -Parent $OutPath',
    'if ($dir -and -not (Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }',
    '$bytes = [byte[]](137,80,78,71)',
    '[IO.File]::WriteAllBytes($OutPath, $bytes)',
    'Write-Output ("title=" + $Title)',
  ].join("\n"));
  const out = join(root, 'final', 'shot.png');
  const result = await captureScreenshot(out, {script, title: 'Syntropy', cdpPort: 1, timeoutSec: 2});
  assert.equal(result.ok, true);
  assert.equal(result.via, 'window');
  assert.equal((await readFile(out)).length, 4);
});
