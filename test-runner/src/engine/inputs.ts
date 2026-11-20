import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { dirname, isAbsolute, join, resolve, sep } from 'node:path';
import type { EngineInputSpec, SessionSpec } from './contract.js';

export interface MaterializedInput {
  name: string;
  kind: string;
  sha256: string;
  bytes: number;
  blob: string;
  root: string;
  unpacked: boolean;
}

export interface MaterializeOptions {
  jobRoot: string;
  spec: SessionSpec;
  fetchInput: (name: string) => Promise<Buffer>;
  log?: (message: string) => void;
}

function inside(root: string, candidate: string): boolean {
  const base = resolve(root);
  const full = resolve(candidate);
  return full === base || full.startsWith(base + sep);
}

function run(command: string, args: string[]): Promise<{code: number; stdout: string; stderr: string}> {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(command, args, {windowsHide: true, stdio: ['ignore', 'pipe', 'pipe']});
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', chunk => { if (stdout.length < 4 << 20) stdout += chunk.toString(); });
    child.stderr.on('data', chunk => { if (stderr.length < 1 << 20) stderr += chunk.toString(); });
    child.once('error', reject);
    child.once('close', code => resolvePromise({code: code ?? -1, stdout, stderr}));
  });
}

/**
 * Reject absolute paths, drive letters, parent escapes and link entries before
 * extraction. The archive comes from SmartPipe, but a compromised scheduler
 * must not be able to write outside the job root.
 */
export function assertSafeArchiveEntries(entries: string[]): void {
  for (const raw of entries) {
    const entry = raw.trim();
    if (!entry) continue;
    if (/^[lh][rwxsStT-]{9}/.test(entry)) throw new Error('archive contains a link entry: ' + entry);
    const named = /^\S+\s+\S+\s+\S+\s+\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}(?::\d{2})?\s+(.*)$/.exec(entry);
    const path = named?.[1] ?? entry;
    if (path.startsWith('/') || path.startsWith('\\') || /^[a-zA-Z]:[\\/]/.test(path)) {
      throw new Error('archive entry is absolute: ' + path);
    }
    if (path.split(/[\\/]/).some(part => part === '..')) throw new Error('archive entry escapes: ' + path);
  }
}

async function listArchive(blob: string): Promise<string[]> {
  const listing = await run('tar', ['-tvf', blob]);
  if (listing.code !== 0) throw new Error('tar listing failed: ' + listing.stderr.slice(0, 500));
  return listing.stdout.split(/\r?\n/).filter(Boolean);
}

async function unpack(blob: string, dest: string, format: string): Promise<void> {
  if (format !== 'tar.gz' && format !== 'zip') return;
  const entries = await listArchive(blob);
  assertSafeArchiveEntries(entries);
  const extracted = await run('tar', ['-xf', blob, '-C', dest]);
  if (extracted.code !== 0) throw new Error('tar extract failed: ' + extracted.stderr.slice(0, 500));
}

function defaultTarget(input: EngineInputSpec): string {
  if (input.unpack === 'tar.gz' || input.unpack === 'zip') return input.target || join('inputs', input.name);
  // A plain file input materialises as <target>/<name> unless the scheduler
  // gave an explicit file path. The story/plan reference then stays relative.
  return input.target || join('inputs', input.name, input.name);
}

/** Download, hash-verify and unpack one engine input. */
export async function materializeInputs(options: MaterializeOptions): Promise<Map<string, MaterializedInput>> {
  const inputs = options.spec.engine?.inputs ?? [];
  const result = new Map<string, MaterializedInput>();
  if (!inputs.length) return result;
  const blobRoot = join(options.jobRoot, 'inputs');
  await mkdir(blobRoot, {recursive: true});
  for (const input of inputs) {
    const blob = join(blobRoot, input.name + '.blob');
    let bytes: Buffer | undefined;
    try {
      const existing = await readFile(blob);
      if (createHash('sha256').update(existing).digest('hex') === input.sha256.toLowerCase()) bytes = existing;
    } catch { /* not staged yet */ }
    if (!bytes) {
      options.log?.('downloading engine input ' + input.name);
      bytes = await options.fetchInput(input.name);
      const digest = createHash('sha256').update(bytes).digest('hex');
      if (digest !== input.sha256.toLowerCase()) {
        throw new Error('engine input ' + input.name + ' hash mismatch: got ' + digest);
      }
      if (input.bytes && input.bytes !== bytes.length) {
        throw new Error('engine input ' + input.name + ' size mismatch');
      }
      const tmp = blob + '.part';
      await writeFile(tmp, bytes);
      await rename(tmp, blob);
    }
    const target = join(options.jobRoot, defaultTarget(input));
    if (!inside(options.jobRoot, target)) throw new Error('input target escapes the job root: ' + target);
    const unpacked = input.unpack === 'tar.gz' || input.unpack === 'zip';
    let root = target;
    if (unpacked) {
      await mkdir(target, {recursive: true});
      const marker = join(target, '.complete');
      try {
        await readFile(marker);
      } catch {
        await rm(target, {recursive: true, force: true});
        await mkdir(target, {recursive: true});
        await unpack(blob, target, input.unpack!);
        await writeFile(marker, input.sha256);
      }
    } else if (input.kind === 'file' || input.kind === 'hotswap') {
      await mkdir(dirname(target), {recursive: true});
      await writeFile(target, bytes);
      root = dirname(target);
    } else {
      await mkdir(target, {recursive: true});
      await writeFile(target, bytes);
      root = dirname(target);
    }
    result.set(input.name, {
      name: input.name, kind: input.kind, sha256: input.sha256, bytes: bytes.length, blob, root, unpacked,
    });
  }
  return result;
}

export function resolveInside(root: string, relative: string): string {
  if (isAbsolute(relative)) throw new Error('absolute path rejected: ' + relative);
  const full = resolve(root, relative);
  if (!inside(root, full)) throw new Error('path escapes root: ' + relative);
  return full;
}
