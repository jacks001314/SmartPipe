#!/usr/bin/env node
// Build a signed-by-digest component bundle for BundleSwapScenario.
//
//   node build-bundle.mjs --spec hotswap.json --out candidate.tar.gz [--root DIR]
//
// spec: { "version": "0.5.22-hotfix", "components": [
//   { "slot": "app-asar", "path": "dist/app.asar", "target": "resources/app.asar" },
//   { "slot": "nbot", "path": "dist/nbot-windows-amd64.exe",
//     "target": "resources/artifacts/nbot-windows-amd64.exe", "mode": 493 } ] }
import { mkdtemp, mkdir, copyFile, readFile, writeFile, rm, stat } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';

function arg(name, fallback) {
  const index = process.argv.indexOf('--' + name);
  return index >= 0 ? process.argv[index + 1] : fallback;
}
const specPath = arg('spec');
const outPath = arg('out', 'candidate.tar.gz');
const rootDir = resolve(arg('root', process.cwd()));
if (!specPath) throw new Error('Usage: build-bundle.mjs --spec SPEC.json --out BUNDLE.tar.gz [--root DIR]');

async function sha256(path) {
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(path)) hash.update(chunk);
  return hash.digest('hex');
}
const spec = JSON.parse(await readFile(specPath, 'utf8'));
if (!Array.isArray(spec.components) || spec.components.length < 1 || spec.components.length > 64) {
  throw new Error('spec.components must contain 1..64 entries');
}
const work = await mkdtemp(join(tmpdir(), 'sp-bundle-build-'));
try {
  await mkdir(join(work, 'payload'), { recursive: true });
  const components = [];
  for (const [index, item] of spec.components.entries()) {
    if (String(item.target).replace(/\\/g, '/').toLowerCase() === 'resources/app.asar') {
      console.error('warning: resources/app.asar is protected by Electron asar integrity validation; only bundles for test builds with the fuse disabled can swap it.');
    }
    const source = isAbsolute(item.path) ? item.path : resolve(rootDir, item.path);
    const info = await stat(source);
    if (!info.isFile()) throw new Error('not a file: ' + source);
    const relative = 'payload/' + String(index).padStart(2, '0') + '-' + basename(source);
    await copyFile(source, join(work, relative));
    const component = { slot: item.slot, source: relative, target: item.target, sha256: await sha256(source) };
    if (item.mode !== undefined) component.mode = item.mode;
    components.push(component);
  }
  const manifest = { format: 1, components };
  if (spec.version) manifest.version = spec.version;
  await writeFile(join(work, 'bundle.manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
  const tar = process.platform === 'win32'
    ? join(process.env.SystemRoot ?? 'C:\\Windows', 'System32', 'tar.exe')
    : '/usr/bin/tar';
  const result = spawnSync(tar, ['-czf', resolve(outPath), '-C', work, '.'], { encoding: 'utf8' });
  if (result.status !== 0) throw new Error('tar failed: ' + (result.stderr || result.stdout));
  const digest = await sha256(resolve(outPath));
  const size = (await stat(resolve(outPath))).size;
  console.log(JSON.stringify({ bundle: resolve(outPath), artifact_sha256: digest, bytes: size, version: manifest.version, components }, null, 2));
} finally {
  await rm(work, { recursive: true, force: true });
}
