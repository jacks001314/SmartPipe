import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { DesktopLease, Evidence, SerialDesktop } from '../src/session.js';

test('desktop lease excludes another owner and can be reacquired after close', async () => {
  const root = await mkdtemp(join(tmpdir(), 'smartpipe-test-'));
  try {
    const lease = await DesktopLease.acquire(root, 5000);
    await assert.rejects(DesktopLease.acquire(root, 5000), { code: 'EEXIST' });
    await lease.close();
    assert.throws(() => lease.assertActive());
    const next = await DesktopLease.acquire(root, 5000);
    await lease.close(); // old close must not delete the new lease
    await assert.rejects(DesktopLease.acquire(root, 5000));
    await next.close();
  } finally { await rm(root, { recursive: true, force: true }); }
});

test('desktop serializes actions and never retries failed input', async () => {
  const root = await mkdtemp(join(tmpdir(), 'smartpipe-test-'));
  const lease = await DesktopLease.acquire(root, 5000);
  try {
    const queue = new SerialDesktop(lease);
    const calls: number[] = [];
    const first = queue.run(async () => {
      calls.push(1);
      await new Promise(r => setTimeout(r, 15));
      calls.push(2);
      throw new Error('uncertain input outcome');
    });
    const failure = assert.rejects(first, /uncertain/);
    const second = queue.run(async () => { calls.push(3); });
    await Promise.all([failure, second]);
    assert.deepEqual(calls, [1, 2, 3]);
    await lease.close();
    await assert.rejects(queue.run(async () => calls.push(4)), /closed/);
  } finally { await lease.close(); await rm(root, { recursive: true, force: true }); }
});

test('evidence preserves image bytes and hashes without logging typed text', async () => {
  const root = await mkdtemp(join(tmpdir(), 'smartpipe-test-'));
  try {
    const evidence = await Evidence.create(root, { verdict: 'NOT_EVALUATED' });
    await evidence.record('js', { content: [
      { type: 'text', text: 'private typed text' },
      { type: 'image', mimeType: 'image/png', data: Buffer.from('fixture').toString('base64') },
    ] }, 'action-1');
    const log = await readFile(join(evidence.directory, 'actions.jsonl'), 'utf8');
    assert.equal(log.includes('private typed text'), false);
    const row = JSON.parse(log);
    assert.match(row.images[0].sha256, /^[a-f0-9]{64}$/);
    assert.equal(await readFile(join(evidence.directory, row.images[0].path), 'utf8'), 'fixture');
    assert.equal((await readdir(evidence.directory)).length, 3);
  } finally { await rm(root, { recursive: true, force: true }); }
});
