import { execFile, spawn } from 'node:child_process';
import { mkdir, readFile, stat, writeFile } from 'node:fs/promises';
import { dirname } from 'node:path';

export interface ScreenshotConfig {
  script?: string;
  title?: string;
  cdpPort?: number;
  cdpURLMatch?: string;
  cdpProcess?: string;
  timeoutSec?: number;
}

export interface ScreenshotResult {
  ok: boolean;
  via: 'cdp' | 'window' | 'screen';
  bytes?: number;
  error?: string;
}

interface CDPTarget {
  type?: string;
  url?: string;
  title?: string;
  webSocketDebuggerUrl?: string;
}

// Chromium paints nothing into a disconnected RDP desktop, so a GDI capture of
// an Electron window comes back uniformly grey. The DevTools protocol asks the
// renderer for a fresh frame and works with no interactive desktop attached.
export function pickPageTarget(targets: CDPTarget[], match: string): CDPTarget | undefined {
  const pages = targets.filter(target => target.type === 'page' && target.webSocketDebuggerUrl);
  const wanted = match.trim().toLowerCase();
  const matched = wanted
    ? pages.find(target => (target.url ?? '').toLowerCase().includes(wanted))
    : undefined;
  return matched ?? pages.find(target => (target.url ?? '').startsWith('app://')) ?? pages[0];
}

async function listTargets(port: number, signal: AbortSignal): Promise<CDPTarget[]> {
  const response = await fetch('http://127.0.0.1:' + port + '/json/list', {redirect: 'error', signal});
  if (!response.ok) throw new Error('cdp target list returned ' + response.status);
  const payload = await response.json();
  return Array.isArray(payload) ? payload as CDPTarget[] : [];
}

function execText(command: string, args: string[]): Promise<string> {
  return new Promise((resolvePromise, reject) => {
    execFile(command, args, {windowsHide: true, maxBuffer: 4 << 20}, (error, stdout) => {
      if (error) reject(error);
      else resolvePromise(stdout);
    });
  });
}

/**
 * Electron ignores DevToolsActivePort, so ask Windows which ports the app's
 * processes actually listen on and keep the one that answers /json/version.
 * This survives force-kill port leaks: every launch gets a fresh port.
 */
export async function discoverCDPPort(processName: string): Promise<number | undefined> {
  if (process.platform !== 'win32') return undefined;
  const image = /\.[a-z0-9]+$/i.test(processName) ? processName : processName + '.exe';
  const [netstat, tasks] = await Promise.all([
    execText('netstat', ['-ano']),
    execText('tasklist', ['/FI', 'IMAGENAME eq ' + image, '/FO', 'CSV', '/NH']).catch(() => ''),
  ]);
  const pids = new Set<string>();
  for (const line of tasks.split(/\r?\n/)) {
    const cells = line.match(/"([^"]*)"/g);
    if (cells && cells.length >= 2) pids.add((cells[1] ?? '').replace(/"/g, '').trim());
  }
  const ports = new Set<number>();
  for (const line of netstat.split(/\r?\n/)) {
    const match = /^\s*TCP\s+\S+:(\d+)\s+\S+\s+LISTENING\s+(\d+)\s*$/i.exec(line);
    if (!match) continue;
    const port = Number(match[1]);
    if (port > 0 && pids.has(match[2] ?? '')) ports.add(port);
  }
  for (const port of ports) {
    try {
      const response = await fetch('http://127.0.0.1:' + port + '/json/version', {signal: AbortSignal.timeout(1_500)});
      if (!response.ok) continue;
      const payload = await response.json() as {webSocketDebuggerUrl?: string};
      if (payload.webSocketDebuggerUrl) return port;
    } catch { /* not a devtools listener */ }
  }
  return undefined;
}

async function captureCDP(outPath: string, config: ScreenshotConfig, deadline: number): Promise<ScreenshotResult> {
  if (!config.cdpPort && !config.cdpProcess) return {ok: false, via: 'cdp', error: 'cdp port is not configured'};
  const match = config.cdpURLMatch ?? 'app://';
  let lastError = '';
  while (Date.now() < deadline) {
    try {
      const candidates: number[] = [];
      if (config.cdpPort) candidates.push(config.cdpPort);
      if (config.cdpProcess) {
        const discovered = await discoverCDPPort(config.cdpProcess);
        if (discovered && !candidates.includes(discovered)) candidates.push(discovered);
      }
      if (!candidates.length) throw new Error('cdp port is not configured');
      for (const port of candidates) {
        try {
          const targets = await listTargets(port, AbortSignal.timeout(Math.min(3_000, Math.max(500, deadline - Date.now()))));
          const target = pickPageTarget(targets, match);
          if (!target?.webSocketDebuggerUrl) throw new Error('no page target on cdp port ' + port);
          // A fresh launch exposes the splash page first; capturing it would make a
          // perfectly healthy app look like a blank test result.
          if (deadline - Date.now() > 1_500 && target.url && !target.url.startsWith('app://') && !target.url.includes(match)) {
            throw new Error('waiting for the ' + match + ' renderer target');
          }
          const data = await captureFrame(target.webSocketDebuggerUrl, deadline);
          await mkdir(dirname(outPath), {recursive: true});
          await writeFile(outPath, data);
          return {ok: true, via: 'cdp', bytes: data.length};
        } catch (error) {
          lastError = error instanceof Error ? error.message : String(error);
        }
      }
    } catch (error) {
      lastError = error instanceof Error ? error.message : String(error);
    }
    await new Promise(resolvePromise => setTimeout(resolvePromise, 500));
  }
  return {ok: false, via: 'cdp', error: lastError || 'cdp capture timed out'};
}

function captureFrame(webSocketURL: string, deadline: number): Promise<Buffer> {
  return new Promise((resolvePromise, reject) => {
    const socket = new WebSocket(webSocketURL);
    const pending = new Map<number, (message: Record<string, unknown>) => void>();
    let seq = 0;
    const timer = setTimeout(() => {
      socket.close();
      reject(new Error('cdp command timed out'));
    }, Math.max(1_000, Math.min(20_000, deadline - Date.now())));
    const finish = (error?: Error, data?: Buffer) => {
      clearTimeout(timer);
      try { socket.close(); } catch { /* socket may already be closed */ }
      if (error) reject(error);
      else resolvePromise(data as Buffer);
    };
    socket.addEventListener('error', () => finish(new Error('cdp socket error')), {once: true});
    socket.addEventListener('open', () => {
      const send = (method: string, params: Record<string, unknown> = {}) => new Promise<Record<string, unknown>>(resolveCommand => {
        const id = ++seq;
        pending.set(id, resolveCommand);
        socket.send(JSON.stringify({id, method, params}));
      });
      void (async () => {
        try {
          await send('Page.enable');
          const shot = await send('Page.captureScreenshot', {format: 'png', captureBeyondViewport: false});
          const result = shot.result as {data?: string} | undefined;
          if (!result?.data) throw new Error('cdp did not return screenshot data');
          finish(undefined, Buffer.from(result.data, 'base64'));
        } catch (error) {
          finish(error instanceof Error ? error : new Error(String(error)));
        }
      })();
    }, {once: true});
    socket.addEventListener('message', event => {
      let message: Record<string, unknown>;
      try { message = JSON.parse(String(event.data)) as Record<string, unknown>; } catch { return; }
      const id = typeof message.id === 'number' ? message.id : 0;
      const handler = pending.get(id);
      if (handler) { pending.delete(id); handler(message); }
    });
  });
}

function runPowerShell(script: string, args: string[], timeoutMs: number): Promise<{code: number; stdout: string; stderr: string}> {
  return new Promise((resolvePromise, reject) => {
    const child = spawn('powershell.exe',
      ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', script, ...args],
      {windowsHide: true, stdio: ['ignore', 'pipe', 'pipe']});
    let stdout = '';
    let stderr = '';
    const timer = setTimeout(() => {
      try { child.kill(); } catch { /* already gone */ }
    }, timeoutMs);
    child.stdout.on('data', chunk => { stdout += chunk.toString(); });
    child.stderr.on('data', chunk => { stderr += chunk.toString(); });
    child.once('error', error => { clearTimeout(timer); reject(error); });
    child.once('close', code => { clearTimeout(timer); resolvePromise({code: code ?? -1, stdout, stderr}); });
  });
}

async function captureWindow(outPath: string, script: string, title: string, timeoutSec: number): Promise<ScreenshotResult> {
  const args = title
    ? ['-OutPath', outPath, '-Title', title, '-TimeoutSec', String(timeoutSec)]
    : ['-OutPath', outPath, '-TimeoutSec', String(timeoutSec)];
  const outcome = await runPowerShell(script, args, (timeoutSec + 30) * 1000);
  if (outcome.code !== 0) {
    return {ok: false, via: title ? 'window' : 'screen', error: outcome.stderr.slice(0, 400) || 'capture exited ' + outcome.code};
  }
  try {
    const info = await stat(outPath);
    await readFile(outPath);
    return {ok: true, via: title ? 'window' : 'screen', bytes: info.size};
  } catch (error) {
    return {ok: false, via: title ? 'window' : 'screen', error: String(error)};
  }
}

/**
 * Capture one screenshot with the strongest available backend: the app's own
 * DevTools endpoint first, then a titled native window, then the full desktop.
 */
export async function captureScreenshot(outPath: string, config: ScreenshotConfig): Promise<ScreenshotResult> {
  const timeoutSec = config.timeoutSec ?? 20;
  const deadline = Date.now() + Math.max(1, timeoutSec) * 1000;
  const failures: string[] = [];
  if (config.cdpPort || config.cdpProcess) {
    const shot = await captureCDP(outPath, config, deadline);
    if (shot.ok) return shot;
    failures.push('cdp: ' + shot.error);
  }
  if (config.script) {
    const title = config.title?.trim() ?? '';
    const shot = await captureWindow(outPath, config.script, title, timeoutSec);
    if (shot.ok) return shot;
    failures.push((title ? 'window: ' : 'screen: ') + shot.error);
    if (title) {
      const fallback = await captureWindow(outPath, config.script, '', timeoutSec);
      if (fallback.ok) return fallback;
      failures.push('screen: ' + fallback.error);
    }
  }
  return {ok: false, via: config.cdpPort || config.cdpProcess ? 'cdp' : config.title ? 'window' : 'screen', error: failures.join('; ') || 'no capture backend configured'};
}
