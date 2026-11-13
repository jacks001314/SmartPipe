import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readdir, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';

test('real stdio MCP gateway forwards image and closes upstream lease', {timeout:15000}, async () => {
 const root = await mkdtemp(join(tmpdir(),'smartpipe-mcp-'));
 const client = new Client({name:'test',version:'1'});
 try {
  await client.connect(new StdioClientTransport({
   command:process.execPath,
   args:[fileURLToPath(new URL('../src/cli.js',import.meta.url)), root, '--',
    process.execPath,fileURLToPath(new URL('./upstream-fixture.js',import.meta.url))],
   stderr:'pipe'
  }));
  assert.equal((await client.listTools()).tools[0]?.name,'observe');
  const result = await client.callTool({name:'observe',arguments:{}});
  assert.equal((result.content as Array<{type:string}>)[0]?.type,'image');
  const runs = await readdir(join(root,'runs'));
  const log = await readFile(join(root,'runs',runs[0]!, 'actions.jsonl'),'utf8');
  assert.equal(JSON.parse(log).images.length,1);
 } finally {
  await client.close();
  await rm(root,{recursive:true,force:true});
 }
});
