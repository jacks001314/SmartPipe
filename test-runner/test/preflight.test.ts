import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import { preflight } from '../src/preflight.js';

test('preflight rejects relative executable without starting a process',async()=>{
 const r=await preflight({command:'node',args:[]});
 assert.equal(r.status,'BLOCKED');
 assert.equal(r.checks.find(c=>c.id==='absolute_executable')?.ok,false);
});
test('unrelated MCP server is not ready for Qwen desktop sessions',async()=>{
 const r=await preflight({command:process.execPath,args:[fileURLToPath(new URL('./upstream-fixture.js',import.meta.url))]});
 assert.equal(r.checks.find(c=>c.id==='mcp_connect')?.ok,true);
 assert.equal(r.status,'BLOCKED');
});
test('preflight bounds an upstream that never answers initialization',async()=>{
 const start=Date.now();
 const r=await preflight({command:process.execPath,args:['-e','setInterval(()=>{},1000)'],timeoutMs:150});
 assert.equal(r.status,'BLOCKED');
 assert.ok(Date.now()-start<8000);
});
