import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import type { Finding, SessionCheck } from './contract.js';
import { validateFinding } from './contract.js';
import type { JobRegistry } from './job-state.js';
import { captureScreenshot } from './screenshot.js';

export interface EngineServerOptions {
  registry: JobRegistry;
  stateRoot: string;
  controlToken: string;
  port: number;
  host?: string;
  screenshotScript?: string;
  screenshotCDPPort?: number;
  screenshotCDPURLMatch?: string;
  screenshotCDPProcess?: string;
  log?: (message: string) => void;
}

export interface EngineServerHandle {
  url: string;
  port: number;
  close: () => Promise<void>;
}

const MAX_BODY = 12 << 20;

async function body(req: IncomingMessage): Promise<Buffer> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    size += (chunk as Buffer).length;
    if (size > MAX_BODY) throw new Error('request body too large');
    chunks.push(chunk as Buffer);
  }
  return Buffer.concat(chunks);
}

function json(res: ServerResponse, status: number, value: unknown): void {
  const payload = Buffer.from(JSON.stringify(value));
  res.writeHead(status, {'Content-Type': 'application/json', 'Content-Length': payload.length});
  res.end(payload);
}

function bearer(req: IncomingMessage): string {
  const header = req.headers.authorization ?? '';
  return header.toLowerCase().startsWith('bearer ') ? header.slice(7).trim() : '';
}

/**
 * Local control plane for one engine instance. The Codex agent talks to this
 * server through MCP; the supervisor talks to SmartPipe. Nothing here is
 * reachable from outside loopback and every job route needs its job token.
 */
export async function startEngineServer(options: EngineServerOptions): Promise<EngineServerHandle> {
  const server = createServer(async (req, res) => {
    try {
      const url = new URL(req.url ?? '/', 'http://127.0.0.1');
      const parts = url.pathname.split('/').filter(Boolean);
      if (parts[0] !== 'v1') return json(res, 404, {error: 'not found'});
      if (parts[1] === 'health' && req.method === 'GET') {
        return json(res, 200, {status: 'ok', jobs: options.registry.list().length});
      }
      if (parts[1] === 'jobs' && parts.length === 2 && req.method === 'GET') {
        if (bearer(req) !== options.controlToken) return json(res, 401, {error: 'unauthorized'});
        return json(res, 200, {data: options.registry.list().map(job => ({
          id: job.id, mode: job.mode, title: job.title, status: job.status,
          checks: job.checks.length, findings: job.findings.length, started_at: job.startedAt,
        }))});
      }
      if (parts[1] !== 'jobs' || parts.length < 3) return json(res, 404, {error: 'not found'});
      const jobID = decodeURIComponent(parts[2] ?? '');
      if (!jobID) return json(res, 400, {error: 'job id is required'});
      if (!options.registry.has(jobID)) return json(res, 404, {error: 'unknown job'});
      if (!options.registry.authorize(jobID, bearer(req))) return json(res, 401, {error: 'unauthorized'});
      const job = options.registry.get(jobID);

      if (parts.length === 3 && req.method === 'GET') return json(res, 200, {data: job});
      if (parts[3] === 'stories' && req.method === 'GET' && parts.length === 4) {
        return json(res, 200, {data: job.stories.map(story => ({
          id: story.id, title: story.title, input: story.input, path: story.path,
          required_checks: story.required_checks, events: story.events, bytes: story.body.length,
        }))});
      }
      if (parts[3] === 'stories' && req.method === 'GET' && parts.length === 5) {
        const story = job.stories.find(item => item.id === decodeURIComponent(parts[4] ?? ''));
        if (!story) return json(res, 404, {error: 'unknown story'});
        return json(res, 200, {data: story});
      }
      if (parts[3] === 'checks' && req.method === 'POST') {
        const input = JSON.parse((await body(req)).toString('utf8')) as SessionCheck;
        if (!input.id || !['PASS', 'FAIL', 'BLOCKED', 'SKIPPED'].includes(input.status)) {
          return json(res, 400, {error: 'invalid check'});
        }
        const checks = await options.registry.check(jobID, input);
        return json(res, 200, {data: {checks}});
      }
      if (parts[3] === 'findings' && req.method === 'POST') {
        const input = JSON.parse((await body(req)).toString('utf8')) as Finding;
        input.id ||= 'f' + (job.findings.length + 1);
        const errors = validateFinding(input);
        if (errors.length) return json(res, 400, {error: errors.join('; ')});
        const finding = await options.registry.finding(jobID, input);
        return json(res, 200, {data: finding});
      }
      if (parts[3] === 'evidence' && req.method === 'POST') {
        const input = JSON.parse((await body(req)).toString('utf8')) as {path?: string; base64?: string; mime_type?: string; name?: string};
        let bytes: Buffer;
        let mime = input.mime_type || 'application/octet-stream';
        if (input.base64) bytes = Buffer.from(input.base64, 'base64');
        else if (input.path) {
          bytes = await readFile(input.path);
          if (!input.mime_type) mime = /\.png$/i.test(input.path) ? 'image/png' : /\.json$/i.test(input.path) ? 'application/json' : 'text/plain';
        } else return json(res, 400, {error: 'path or base64 is required'});
        const reference = await options.registry.addEvidence(jobID, input.name || input.path || '', mime, bytes);
        return json(res, 200, {data: {reference, bytes: bytes.length}});
      }
      if (parts[3] === 'capture' && req.method === 'POST') {
        if (process.platform !== 'win32' || (!options.screenshotScript && !options.screenshotCDPPort && !options.screenshotCDPProcess)) {
          return json(res, 501, {error: 'capture is not configured'});
        }
        const input = JSON.parse((await body(req)).toString('utf8')) as {title?: string; full_screen?: boolean};
        const directory = join(options.stateRoot, jobID, 'screenshots');
        await mkdir(directory, {recursive: true});
        const out = join(directory, 'screen-' + Date.now() + '.png');
        const shot = await captureScreenshot(out, {
          script: options.screenshotScript,
          title: input.title,
          cdpPort: input.full_screen ? undefined : options.screenshotCDPPort,
          cdpURLMatch: options.screenshotCDPURLMatch,
          cdpProcess: input.full_screen ? undefined : options.screenshotCDPProcess,
          timeoutSec: 30,
        });
        if (!shot.ok) return json(res, 500, {error: 'capture failed: ' + (shot.error ?? 'unknown error').slice(0, 400)});
        const bytes = await readFile(out);
        const reference = await options.registry.addEvidence(jobID, out, 'image/png', bytes);
        return json(res, 200, {data: {reference, path: out, bytes: bytes.length, via: shot.via}});
      }
      if (parts[3] === 'evidence' && req.method === 'GET' && parts.length === 5) {
        const sha = decodeURIComponent(parts[4] ?? '').replace(/^sha256:/, '');
        const record = job.evidence[sha];
        if (!record) return json(res, 404, {error: 'unknown evidence'});
        const bytes = await readFile(record.path);
        res.writeHead(200, {'Content-Type': record.mimeType, 'Content-Length': bytes.length});
        return res.end(bytes);
      }
      if (parts[3] === 'events' && req.method === 'POST') {
        const input = JSON.parse((await body(req)).toString('utf8')) as {message?: string; kind?: string};
        if (!input.message) return json(res, 400, {error: 'message is required'});
        const event = await options.registry.event(jobID, input.kind || 'agent', input.message.slice(0, 2000));
        return json(res, 200, {data: event});
      }
      if (parts[3] === 'finish' && req.method === 'POST') {
        const input = JSON.parse((await body(req)).toString('utf8')) as {verdict?: string; summary?: string};
        if (!input.verdict || !['PASS', 'FAIL', 'BLOCKED'].includes(input.verdict) || !input.summary?.trim()) {
          return json(res, 400, {error: 'verdict and summary are required'});
        }
        await options.registry.finish(jobID, input.verdict as 'PASS' | 'FAIL' | 'BLOCKED', input.summary);
        return json(res, 200, {data: {status: 'finishing'}});
      }
      return json(res, 404, {error: 'not found'});
    } catch (error) {
      options.log?.('engine api error: ' + (error instanceof Error ? error.message : String(error)));
      return json(res, 500, {error: error instanceof Error ? error.message : String(error)});
    }
  });
  await new Promise<void>((resolvePromise, reject) => {
    server.once('error', reject);
    server.listen(options.port, options.host ?? '127.0.0.1', () => resolvePromise());
  });
  const address = server.address();
  const port = typeof address === 'object' && address ? address.port : options.port;
  const url = 'http://' + (options.host ?? '127.0.0.1') + ':' + port;
  try {
    // The job directory is created lazily by the registry, so the runtime file
    // must not assume it already exists. A failed write used to leave the
    // listening socket open and hang the whole runner loop.
    await mkdir(options.stateRoot, {recursive: true});
    await writeFile(join(options.stateRoot, 'engine-runtime.json'), JSON.stringify({url, port, pid: process.pid}, null, 2));
  } catch (error) {
    closeServer(server);
    throw error;
  }
  return {
    url, port,
    close: () => new Promise<void>((resolvePromise, reject) => {
      server.close(error => error ? reject(error) : resolvePromise());
      // A model process may leave a keep-alive socket open. Without this the
      // close callback never fires and the runner loop stops claiming jobs.
      server.closeAllConnections();
      setTimeout(() => resolvePromise(), 5_000).unref();
    }),
  };
}

function closeServer(server: import('node:http').Server): void {
  server.closeAllConnections();
  server.close(() => undefined);
}
