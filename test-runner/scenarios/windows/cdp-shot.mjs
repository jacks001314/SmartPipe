import { writeFile } from 'node:fs/promises';
import { execFileSync } from 'node:child_process';

// Standalone DevTools screenshot for Electron apps. Unlike a GDI capture this
// asks Chromium for a fresh frame, so it works when RDP is detached and the
// desktop has no visible surface.
const [out, portArg, match = 'app://', processName = 'Syntropy'] = process.argv.slice(2);
const preferredPort = Number(portArg || 0);

function fail(message, code) {
  process.stderr.write(message + '\n');
  process.exit(code);
}

if (!out) fail('usage: node cdp-shot.mjs OUT.png [port] [url-substring]', 2);

async function listTargets(port) {
  const response = await fetch('http://127.0.0.1:' + port + '/json/list', { signal: AbortSignal.timeout(3000) });
  if (!response.ok) throw new Error('http ' + response.status);
  return await response.json();
}

function listeningPorts(name) {
  try {
    const image = /\.[a-z0-9]+$/i.test(name) ? name : name + '.exe';
    const tasks = execFileSync('tasklist', ['/FI', 'IMAGENAME eq ' + image, '/FO', 'CSV', '/NH'], { encoding: 'utf8' });
    const pids = new Set();
    for (const line of tasks.split(/\r?\n/)) {
      const cells = line.match(/"([^"]*)"/g);
      if (cells && cells.length >= 2) pids.add((cells[1] || '').replace(/"/g, '').trim());
    }
    if (!pids.size) return [];
    const netstat = execFileSync('netstat', ['-ano'], { encoding: 'utf8' });
    const ports = [];
    for (const line of netstat.split(/\r?\n/)) {
      const found = /^\s*TCP\s+\S+:(\d+)\s+\S+\s+LISTENING\s+(\d+)\s*$/i.exec(line);
      if (found && pids.has(found[2])) ports.push(Number(found[1]));
    }
    return [...new Set(ports)];
  } catch {
    return [];
  }
}

function candidatePorts() {
  const ports = preferredPort ? [preferredPort] : [];
  for (const port of listeningPorts(processName)) {
    if (!ports.includes(port)) ports.push(port);
  }
  return ports.filter(port => port > 0);
}

function capture(webSocketURL) {
  return new Promise((resolve, reject) => {
    const socket = new WebSocket(webSocketURL);
    const pending = new Map();
    let seq = 0;
    const timer = setTimeout(() => {
      try { socket.close(); } catch { /* already closed */ }
      reject(new Error('cdp capture timed out'));
    }, 20000);
    const done = (error, bytes) => {
      clearTimeout(timer);
      try { socket.close(); } catch { /* already closed */ }
      if (error) reject(error);
      else resolve(bytes);
    };
    socket.onerror = () => done(new Error('cdp socket error'));
    socket.onopen = async () => {
      try {
        socket.onmessage = event => {
          const message = JSON.parse(String(event.data));
          const handler = pending.get(message.id);
          if (handler) { pending.delete(message.id); handler(message); }
        };
        const send = (method, params = {}) => new Promise((res, rej) => {
          const id = ++seq;
          pending.set(id, res);
          socket.send(JSON.stringify({ id, method, params }));
          setTimeout(() => { if (pending.delete(id)) rej(new Error(method + ' timed out')); }, 10000);
        });
        await send('Page.enable');
        const shot = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
        if (!shot.result || !shot.result.data) throw new Error('cdp returned no screenshot data');
        done(undefined, Buffer.from(shot.result.data, 'base64'));
      } catch (error) {
        done(error);
      }
    };
  });
}

const deadline = Date.now() + 30000;
let target;
let fallback;
let lastError = '';
while (Date.now() < deadline && !target) {
  for (const port of candidatePorts()) {
    try {
      const pages = (await listTargets(port)).filter(item => item.type === 'page' && item.webSocketDebuggerUrl);
      target = pages.find(item => (item.url || '').includes(match))
        || pages.find(item => (item.url || '').startsWith('app://'));
      fallback = fallback || pages[0];
      if (target) break;
    } catch (error) {
      lastError = error instanceof Error ? error.message : String(error);
    }
  }
  if (!target) await new Promise(resolve => setTimeout(resolve, 500));
}
// Only fall back to the splash page when the renderer never appeared at all.
target = target || fallback;
if (!target) fail('no CDP page target for ' + processName + (lastError ? ' (' + lastError + ')' : ''), 3);

let bytes;
try {
  bytes = await capture(target.webSocketDebuggerUrl);
} catch (error) {
  fail('cdp capture failed: ' + (error instanceof Error ? error.message : String(error)), 4);
}
await writeFile(out, bytes);
process.stdout.write(JSON.stringify({ url: target.url, bytes: bytes.length }) + '\n');
