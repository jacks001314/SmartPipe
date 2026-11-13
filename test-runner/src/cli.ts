import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { CallToolRequestSchema, ListToolsRequestSchema } from '@modelcontextprotocol/sdk/types.js';
import { isAbsolute, join } from 'node:path';
import { randomUUID } from 'node:crypto';
import { DesktopLease, Evidence, SerialDesktop } from './session.js';

const args = process.argv.slice(2);
const separator = args.indexOf('--');
const root = args[0];
const command = args[separator + 1];
if (!root || separator !== 1 || !command || !isAbsolute(root) || !isAbsolute(command)) {
  throw new Error('Usage: runner ABSOLUTE_STATE_ROOT -- ABSOLUTE_MCP_EXECUTABLE [args...]');
}
// This gateway wraps a separately provisioned upstream MCP executable.
// It never downloads or installs code during an agent session.
const lease = await DesktopLease.acquire(root, 20 * 60 * 1000);
const evidence = await Evidence.create(join(root, 'runs'), {
  kind: 'desktop-tool-session', verdict: 'NOT_EVALUATED',
  upstream: command,
});
const queue = new SerialDesktop(lease);
const client = new Client({ name: 'smartpipe-desktop-client', version: '0.1.0' });
const server = new Server({ name: 'smartpipe-desktop', version: '0.1.0' }, { capabilities: { tools: {} } });
let closing: Promise<void> | undefined;
function close(): Promise<void> {
  return closing ??= (async () => {
    try { await client.close(); } finally {
      try { await server.close(); } finally { await lease.close(); }
    }
  })();
}
process.once('SIGINT', () => { void close(); });
process.once('SIGTERM', () => { void close(); });
process.stdin.once('end', () => { void close(); });
server.onclose = () => { void close(); };
const deadline = setTimeout(() => { void close(); }, 20 * 60 * 1000);
deadline.unref();
try {
  await client.connect(new StdioClientTransport({ command, args: args.slice(separator + 2), stderr: 'inherit' }));
  const tools: Awaited<ReturnType<Client['listTools']>>['tools'] = [];
  let cursor: string | undefined;
  do {
    const page = await client.listTools(cursor ? { cursor } : {});
    tools.push(...page.tools);
    cursor = page.nextCursor;
  } while (cursor);
  const names = new Set(tools.map(t => t.name));
  server.setRequestHandler(ListToolsRequestSchema, async () => ({ tools }));
  server.setRequestHandler(CallToolRequestSchema, async request => queue.run(async () => {
    if (!names.has(request.params.name)) throw new Error('Unknown upstream tool');
    const actionId = randomUUID();
    try {
      const result = await client.callTool(request.params, undefined, { timeout: 60_000 });
      await evidence.record(request.params.name, result, actionId);
      return result;
    } catch (error) {
      await evidence.record(request.params.name, { isError: true }, actionId);
      // An upstream call may have performed input before failing. Do not replay.
      throw error;
    }
  }));
  console.error('SmartPipe desktop evidence: ' + evidence.directory);
  await server.connect(new StdioServerTransport());
} catch (error) {
  await close();
  throw error;
}
