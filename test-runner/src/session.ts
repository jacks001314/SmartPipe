import { mkdir, open, writeFile, appendFile, rm } from 'node:fs/promises';
import { resolve, join } from 'node:path';
import { createHash, randomUUID } from 'node:crypto';

export class DesktopLease {
  private closed = false;
  private constructor(readonly path: string, readonly expiresAt: number) {}
  static async acquire(root: string, ttlMs: number): Promise<DesktopLease> {
    if (!Number.isFinite(ttlMs) || ttlMs < 1000) throw new Error('Invalid lease duration');
    await mkdir(root, { recursive: true });
    const path = join(resolve(root), 'desktop.lock');
    const handle = await open(path, 'wx');
    const expiresAt = Date.now() + ttlMs;
    try {
      await handle.writeFile(JSON.stringify({ pid: process.pid, expiresAt }));
    } catch (e) {
      await handle.close();
      await rm(path, { force: true });
      throw e;
    }
    await handle.close();
    return new DesktopLease(path, expiresAt);
  }
  assertActive(): void {
    if (this.closed || Date.now() >= this.expiresAt) throw new Error('Desktop lease expired or closed');
  }
  async close(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
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
