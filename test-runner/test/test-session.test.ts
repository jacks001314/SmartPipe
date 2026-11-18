import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { SessionAgentClient } from '../src/session-client.js';
import { SessionHost } from '../src/session-host.js';

type Json = Record<string, unknown>;

async function body(req: IncomingMessage): Promise<Json> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(Buffer.from(chunk));
  return JSON.parse(Buffer.concat(chunks).toString('utf8') || '{}') as Json;
}

function send(res: ServerResponse, status: number, data?: unknown): void {
  if (data === undefined) { res.writeHead(status); res.end(); return; }
  const text = JSON.stringify({data});
  res.writeHead(status, {'Content-Type':'application/json','Content-Length':Buffer.byteLength(text)});
  res.end(text);
}

test('agent client runs a command and downloads evidence', async () => {
  const calls: string[] = [];
  const server = createServer(async (req, res) => {
    calls.push(req.method + ' ' + req.url);
    if (req.url === '/test-session/s1') return send(res, 200, {id:'s1', spec:{story:'x'}, status:'RUNNING'});
    if (req.url === '/test-session/s1/command' && req.method === 'POST') {
      const input = await body(req);
      assert.equal(input.kind, 'read_file');
      return send(res, 202, {id:'c1', seq:1, kind:'read_file', args:{}, status:'PENDING', created_at:''});
    }
    if (req.url?.startsWith('/test-session/s1/command/c1')) {
      return send(res, 200, {id:'c1', seq:1, kind:'read_file', args:{}, status:'DONE', created_at:'', result:{content:'hello'}});
    }
    if (req.url === '/test-session/s1/evidence/abc') {
      res.writeHead(200, {'Content-Type':'text/plain'});
      return res.end('payload');
    }
    send(res, 404);
  });
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
  const port = (server.address() as {port:number}).port;
  try {
    const client = new SessionAgentClient('http://127.0.0.1:' + port, 's1', 'x'.repeat(64));
    const result = await client.run('read_file', {path:'C:\\x'});
    assert.equal(result.status, 'DONE');
    assert.equal(result.content, 'hello');
    const download = await client.download('abc');
    assert.equal(download.bytes.toString(), 'payload');
    assert.ok(calls.some(c => c === 'GET /test-session/s1/command/c1?wait_ms=15000'));
  } finally {
    await new Promise<void>(resolve => server.close(() => resolve()));
  }
});

async function hostFixture(handler: (req: IncomingMessage, res: ServerResponse, step: number) => Promise<void>) {
  let step = 0;
  const server = createServer(async (req, res) => { await handler(req, res, step++); });
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
  const port = (server.address() as {port:number}).port;
  const root = await mkdtemp(join(tmpdir(), 'smartpipe-host-'));
  const work = await mkdtemp(join(tmpdir(), 'smartpipe-work-'));
  return {server, port, root, work};
}

function assignment() {
  return {session_id:'s1', machine_id:'m1', generation:1,
    expires_at:new Date(Date.now() + 60_000).toISOString(),
    deadline:new Date(Date.now() + 600_000).toISOString(),
    session:{id:'s1', spec:{project_id:'p',title:'t',kind:'story',environment:'syntropy',platform:'windows-amd64',story:'x',timeout_seconds:600}, status:'RUNNING', created_at:''}};
}

test('session host executes commands and releases after finish', async () => {
  const file = await mkdtemp(join(tmpdir(), 'smartpipe-data-'));
  const target = join(file, 'hello.txt');
  await writeFile(target, 'hello host');
  const seen: Json[] = [];
  let reports: Json[] = [];
  let released = false;
  const {server, port, root, work} = await hostFixture(async (req, res, step) => {
    const input = req.url === '/test-runner/session/next' ? await body(req) : {};
    seen.push({url:req.url, input});
    if (req.url === '/test-runner/session/claim') return send(res, 200, assignment());
    if (req.url === '/test-runner/session/next') {
      if (step === 1) return send(res, 200, {id:'c1', seq:1, kind:'read_file', args:{path:target}, status:'RUNNING', created_at:''});
      return send(res, 200, {id:'c2', seq:2, kind:'finish', args:{}, status:'RUNNING', created_at:''});
    }
    if (req.url === '/test-runner/session/result') {
      const output = await body(req);
      assert.equal(output.status, 'DONE');
      assert.equal((output.result as Json).content, 'hello host');
      return send(res, 204);
    }
    if (req.url === '/test-runner/session/report') { reports.push(await body(req)); return send(res, 204); }
    if (req.url === '/test-runner/session/release') { released = true; return send(res, 204); }
    send(res, 404);
  });
  process.env.SMARTPIPE_TEST_MACHINE_TOKEN = 'y'.repeat(64);
  try {
    const host = new SessionHost({apiURL:'http://127.0.0.1:' + port, machineID:'m1', stateRoot:root,
      workingDirectory:work, allowedRoots:[file], environments:['syntropy'], log:() => undefined});
    assert.equal(await host.runOnce(), true);
    assert.equal(reports[0]?.verdict, '');
    assert.equal(released, true);
  } finally {
    delete process.env.SMARTPIPE_TEST_MACHINE_TOKEN;
    await new Promise<void>(resolve => server.close(() => resolve()));
  }
});

test('session host refuses paths outside allowed roots', async () => {
  let failed: Json | undefined;
  const {server, port, root, work} = await hostFixture(async (req, res, step) => {
    if (req.url === '/test-runner/session/claim') return send(res, 200, assignment());
    if (req.url === '/test-runner/session/next') {
      if (step === 1) return send(res, 200, {id:'c1', seq:1, kind:'read_file', args:{path:process.platform === 'win32' ? 'C:\\Windows\\win.ini' : '/etc/hostname'}, status:'RUNNING', created_at:''});
      return send(res, 200, {id:'c2', seq:2, kind:'finish', args:{}, status:'RUNNING', created_at:''});
    }
    if (req.url === '/test-runner/session/result') { const output = await body(req); failed = output.result as Json; return send(res, 204); }
    send(res, 204);
  });
  process.env.SMARTPIPE_TEST_MACHINE_TOKEN = 'z'.repeat(64);
  try {
    const host = new SessionHost({apiURL:'http://127.0.0.1:' + port, machineID:'m1', stateRoot:root,
      workingDirectory:work, allowedRoots:[work], log:() => undefined});
    await host.runOnce();
    assert.match(String(failed?.error), /outside allowed roots/);
  } finally {
    delete process.env.SMARTPIPE_TEST_MACHINE_TOKEN;
    await new Promise<void>(resolve => server.close(() => resolve()));
  }
});

test('session host retries a transient backend failure instead of blocking', async () => {
  let nextCalls = 0;
  let reported: Json | undefined;
  const {server, port, root, work} = await hostFixture(async (req, res) => {
    if (req.url === '/test-runner/session/claim') return send(res, 200, assignment());
    if (req.url === '/test-runner/session/next') {
      nextCalls++;
      if (nextCalls === 1) { res.writeHead(503); res.end('unavailable'); return; }
      if (nextCalls === 2) return send(res, 200, {id:'c1', seq:1, kind:'sleep', args:{ms:1}, status:'RUNNING', created_at:''});
      return send(res, 200, {id:'c2', seq:2, kind:'finish', args:{}, status:'RUNNING', created_at:''});
    }
    if (req.url === '/test-runner/session/result') { await body(req); return send(res, 204); }
    if (req.url === '/test-runner/session/report') { reported = await body(req); return send(res, 204); }
    send(res, 204);
  });
  process.env.SMARTPIPE_TEST_MACHINE_TOKEN = 'w'.repeat(64);
  try {
    const host = new SessionHost({apiURL:'http://127.0.0.1:' + port, machineID:'m1', stateRoot:root,
      workingDirectory:work, allowedRoots:[work], log:() => undefined});
    assert.equal(await host.runOnce(), true);
    assert.equal(reported?.verdict, '');
    assert.ok(nextCalls >= 3);
  } finally {
    delete process.env.SMARTPIPE_TEST_MACHINE_TOKEN;
    await new Promise<void>(resolve => server.close(() => resolve()));
  }
});
