import { readFile } from 'node:fs/promises';
import { basename, extname, resolve, sep } from 'node:path';
import type { SessionSpec, StoryDocument } from './contract.js';

interface FrontMatter {
  id?: string;
  title?: string;
  required_checks?: string[];
}

function unquote(value: string): string {
  const trimmed = value.trim();
  if ((trimmed.startsWith('"') && trimmed.endsWith('"')) || (trimmed.startsWith("'") && trimmed.endsWith("'"))) {
    return trimmed.slice(1, -1);
  }
  return trimmed;
}

function parseList(value: string): string[] {
  const trimmed = value.trim();
  if (!trimmed) return [];
  if (trimmed.startsWith('[') && trimmed.endsWith(']')) {
    return trimmed.slice(1, -1).split(',').map(unquote).filter(Boolean);
  }
  return [unquote(trimmed)].filter(Boolean);
}

/** Parse the small YAML subset stories use: scalars, inline lists and block lists. */
export function parseFrontMatter(text: string): { meta: FrontMatter; body: string } {
  const normalized = text.replace(/^\uFEFF/, '').replace(/\r\n/g, '\n');
  if (!normalized.startsWith('---\n')) return { meta: {}, body: normalized };
  const end = normalized.indexOf('\n---', 4);
  if (end < 0) return { meta: {}, body: normalized };
  const bodyStart = normalized.indexOf('\n', end + 1);
  const header = normalized.slice(4, end);
  const body = bodyStart < 0 ? '' : normalized.slice(bodyStart + 1);
  const meta: FrontMatter = {};
  let listKey = '';
  for (const raw of header.split('\n')) {
    if (!raw.trim()) continue;
    const item = /^\s*-\s+(.*)$/.exec(raw);
    if (item && listKey) {
      const value = item[1];
      if (value !== undefined) (meta as unknown as Record<string, string[]>)[listKey]!.push(unquote(value));
      continue;
    }
    const pair = /^([A-Za-z0-9_-]+)\s*:\s*(.*)$/.exec(raw);
    if (!pair) continue;
    const [, key, value] = pair;
    if (key === undefined || value === undefined) continue;
    if (!value.trim()) {
      listKey = key;
      if (key === 'required_checks') meta.required_checks = [];
      continue;
    }
    listKey = '';
    if (key === 'id') meta.id = unquote(value);
    else if (key === 'title') meta.title = unquote(value);
    else if (key === 'required_checks') meta.required_checks = parseList(value);
  }
  return { meta, body };
}

export interface StoryLoadResult {
  stories: StoryDocument[];
  warnings: string[];
}

/**
 * Resolve every story the job names. Input directories are the already
 * materialised (hash-verified) engine inputs, keyed by input name.
 */
export async function loadStories(
  spec: SessionSpec,
  inputRoots: Map<string, string>,
): Promise<StoryLoadResult> {
  const stories: StoryDocument[] = [];
  const warnings: string[] = [];
  const engineStories = spec.engine?.stories ?? [];
  for (const ref of engineStories) {
    const root = inputRoots.get(ref.input);
    if (!root) {
      warnings.push('story input is missing: ' + ref.input);
      continue;
    }
    const full = resolve(root, ref.path);
    const boundary = resolve(root) + sep;
    if (full !== resolve(root) && !full.startsWith(boundary)) {
      warnings.push('story escapes its input: ' + ref.path);
      continue;
    }
    try {
      const text = await readFile(full, 'utf8');
      const { meta, body } = parseFrontMatter(text);
      const required = [...new Set([...(meta.required_checks ?? []), ...(ref.required_checks ?? [])])];
      stories.push({
        id: meta.id || basename(full, extname(full)),
        input: ref.input,
        path: ref.path,
        title: ref.title || meta.title || ref.path,
        required_checks: required,
        events: [],
        body,
      });
    } catch (error) {
      warnings.push('story read failed: ' + ref.path + ': ' + (error instanceof Error ? error.message : String(error)));
    }
  }
  if (typeof spec.story === 'string' && spec.story.trim()) {
    const { meta, body } = parseFrontMatter(spec.story);
    stories.push({
      id: meta.id || 'inline',
      input: 'session',
      path: 'inline',
      title: meta.title || spec.title,
      required_checks: [...new Set([...(meta.required_checks ?? []), ...(spec.required_checks ?? [])])],
      events: spec.events ?? [],
      body,
    });
  }
  return { stories, warnings };
}

/** Union of every story's required checks, used for the final evaluation hint. */
export function requiredChecks(stories: StoryDocument[]): string[] {
  return [...new Set(stories.flatMap(story => story.required_checks))];
}
