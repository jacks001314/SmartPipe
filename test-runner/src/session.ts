import { mkdir, open, writeFile, appendFile, rm, readFile, stat } from 'node:fs/promises';
import { resolve, join } from 'node:path';
import { createHash, randomUUID } from 'node:crypto';

const reclaimStaleMs = 10_000;
interface LeaseRecord { pid?: number; expiresAt?: number; token?: string }

function isProcessAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    // ESRCH means the PID is gone; EPERM and other errors stay conservative.
    return (error as { code?: string }).code !== 'ESRCH';
  }
}

async function readLease(path: string): Promise<{ record?: LeaseRecord; mtimeMs?: number }> {
  try {
    const [raw, info] = await Promise.all([readFile(path, 'utf8'), stat(path)]);
    let record: LeaseRecord | undefined;
    try {
      const parsed = JSON.parse(raw) as LeaseRecord;
      if (parsed && typeof parsed === 'object') record = parsed;
    } catch {
      // A corrupt lock is handled by its age, never by trusting its contents.
    }
    return { record, mtimeMs: info.mtimeMs };
  } catch (error) {
    if ((error as { code?: string }).code === 'ENOENT') return {};
    throw error;
  }
}

async function leaseIsStale(path: string): Promise<boolean> {
  const { record, mtimeMs } = await readLease(path);
  if (!record) return typeof mtimeMs === 'number' && Date.now() - mtimeMs > reclaimStaleMs;
  if (typeof record.expiresAt === 'number' && record.expiresAt <= Date.now()) return true;
  if (typeof record.pid === 'number' && Number.isSafeInteger(record.pid) && record.pid > 0 && !isProcessAlive(record.pid)) return true;
  return false;
}

async function createLease(path: string, ttlMs: number): Promise<DesktopLease> {
  const handle = await open(path, 'wx');
  const token = randomUUID();
  const expiresAt = Date.now() + ttlMs;
  try {
    await handle.writeFile(JSON.stringify({ pid: process.pid, expiresAt, token }));
  } catch (error) {
    await handle.close();
    await rm(path, { force: true });
    throw error;
  }
  await handle.close();
  return new DesktopLease(path, expiresAt, token);
}

function busyLease(path: string, reason: string): Error {
  return Object.assign(new Error(reason + ': ' + path), { code: 'EEXIST' });
}

export class DesktopLease {
  private closed = false;
  constructor(readonly path: string, readonly expiresAt: number, private readonly token: string) {}
  static async acquire(root: string, ttlMs: number): Promise<DesktopLease> {
    if (!Number.isFinite(ttlMs) || ttlMs < 1000) throw new Error('Invalid lease duration');
    await mkdir(root, { recursive: true });
    const path = join(resolve(root), 'desktop.lock');
    for (let attempt = 0; attempt < 3; attempt++) {
      try {
        return await createLease(path, ttlMs);
      } catch (error) {
        if ((error as { code?: string }).code !== 'EEXIST') throw error;
      }
      const current = await readLease(path);
      // The holder closed between our create attempt and the read: retry.
      if (!current.record && current.mtimeMs === undefined) continue;
      if (!(await leaseIsStale(path))) throw busyLease(path, 'Desktop lease is held');
      const reclaim = path + '.reclaim';
      try {
        const info = await stat(reclaim);
        if (Date.now() - info.mtimeMs > reclaimStaleMs) await rm(reclaim, { force: true });
      } catch (error) {
        if ((error as { code?: string }).code !== 'ENOENT') throw error;
      }
      let guard;
      try {
        guard = await open(reclaim, 'wx');
      } catch (error) {
        if ((error as { code?: string }).code === 'EEXIST') throw busyLease(path, 'Desktop lease reclamation in progress');
        throw error;
      }
      try {
        // Re-check under the guard: another worker may have installed a fresh lock.
        const guarded = await readLease(path);
        if (!guarded.record && guarded.mtimeMs === undefined) continue;
        if (!(await leaseIsStale(path))) throw busyLease(path, 'Desktop lease is held');
        await rm(path, { force: true });
        return await createLease(path, ttlMs);
      } finally {
        try { await guard.close(); } finally { await rm(reclaim, { force: true }); }
      }
    }
    throw busyLease(path, 'Desktop lease contention');
  }
  assertActive(): void {
    if (this.closed || Date.now() >= this.expiresAt) throw new Error('Desktop lease expired or closed');
  }
  async close(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    const { record } = await readLease(this.path);
    if (record && record.token !== this.token) return;
    await rm(this.path, { force: true });
  }
}

export class Evidence {
  private constructor(readonly directory: string) {}
  static async create(root: string, metadata: Record<string, unknown>): Promise<Evidence> {
    const directory = join(resolve(root), randomUUID());
    await mkdir(directory, { recursive: true });
    await writeFile(join(directory, 'manifest.json'), JSON.stringify({
      ...metadata, startedAt: new Date().toISOString(), platform: process.platform,
      arch: process.arch, node: process.version,
    }, null, 2));
    return new Evidence(directory);
  }
  async record(tool: string, result: Record<string, unknown>, actionId: string): Promise<void> {
    const images: Array<{ path: string; sha256: string }> = [];
    if (Array.isArray(result.content)) {
      for (const block of result.content) {
        if (block?.type !== 'image' || typeof block.data !== 'string') continue;
        const bytes = Buffer.from(block.data, 'base64');
        if (bytes.length > 20 * 1024 * 1024) throw new Error('Evidence image exceeds limit');
        const extension = block.mimeType === 'image/png' ? 'png' : block.mimeType === 'image/jpeg' ? 'jpg' : 'bin';
        const name = actionId + '-' + images.length + '.' + extension;
        await writeFile(join(this.directory, name), bytes, { flag: 'wx' });
        images.push({ path: name, sha256: createHash('sha256').update(bytes).digest('hex') });
      }
    }
    // Neither typed text nor model-visible output is copied into the action log.
    await appendFile(join(this.directory, 'actions.jsonl'), JSON.stringify({
      actionId, tool, at: new Date().toISOString(), isError: result.isError === true, images,
    }) + '\n');
  }
}

/** Serializes desktop actions. A timeout/error is never automatically retried. */
export class SerialDesktop {
  private tail: Promise<unknown> = Promise.resolve();
  constructor(private lease: DesktopLease) {}
  run<T>(fn: () => Promise<T>): Promise<T> {
    const pending = this.tail.then(() => { this.lease.assertActive(); return fn(); });
    this.tail = pending.catch(() => {});
    return pending;
  }
}
