import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import type { CallToolResult } from '@modelcontextprotocol/sdk/types.js';
import { z } from 'zod';
import { SessionAgentClient, type SessionInfo } from './session-client.js';

const apiURL = process.env.SMARTPIPE_API_URL ?? '';
const sessionID = process.env.SMARTPIPE_TEST_SESSION_ID ?? '';
const token = process.env.SMARTPIPE_TEST_SESSION_TOKEN ?? '';
if (!apiURL || !sessionID || !token) {
  throw new Error('SMARTPIPE_API_URL, SMARTPIPE_TEST_SESSION_ID and SMARTPIPE_TEST_SESSION_TOKEN are required');
}
const client = new SessionAgentClient(apiURL, sessionID, token);
const session: SessionInfo = await client.info();

function text(value: unknown, isError = false): CallToolResult {
  return {content:[{type:'text', text: typeof value === 'string' ? value : JSON.stringify(value, null, 2)}], ...(isError ? {isError:true} : {})};
}

async function run(tool: () => Promise<Record<string,unknown>>): Promise<CallToolResult> {
  try {
    const result = await tool();
    const failed = result.status === 'FAILED';
    return text(result, failed);
  } catch (error) {
    return text({error: error instanceof Error ? error.message : String(error)}, true);
  }
}

const server = new McpServer(
  {name:'smartpipe-test', version:'0.1.0'},
  {instructions: `You are executing one SmartPipe test session on a real ${session.spec.platform} machine.
Read the story with test_session or story_read. Act on the real environment through the env_* and ui_* tools.
Record every acceptance check with check_record and attach evidence (screenshot/upload_file/test evidence reference).
Call test_finish only after you have recorded at least one PASS check with evidence, or when the story is genuinely blocked.
Never claim PASS from a command exit code alone; verify the product state that the story describes.`},
);

server.registerTool('test_session', {
  title:'Test session context',
  description:'Return the session metadata, inputs and the full Markdown story. Call this first.',
  inputSchema:{},
  annotations:{readOnlyHint:true, openWorldHint:false},
}, async () => text({
  id: session.id,
  title: session.spec.title,
  kind: session.spec.kind,
  platform: session.spec.platform,
  environment: session.spec.environment,
  status: session.status,
  deadline: session.deadline,
  work_dir: session.spec.work_dir ?? '',
  inputs: session.spec.inputs ?? {},
  required_checks: session.spec.required_checks ?? [],
  story_ref: session.spec.story_ref ?? '',
  story: session.spec.story,
  checks: session.checks ?? [],
}));

server.registerTool('story_read', {
  title:'Read the story',
  description:'Return only the Markdown test story assigned to this session.',
  inputSchema:{},
  annotations:{readOnlyHint:true, openWorldHint:false},
}, async () => text(session.spec.story));

server.registerTool('env_exec', {
  title:'Run an executable',
  description:'Run an absolute executable on the test machine. Non-zero exit codes are returned, not thrown.',
  inputSchema:{
    executable:z.string().describe('Absolute path of the executable.'),
    args:z.array(z.string()).default([]).describe('Arguments, passed without a shell.'),
    cwd:z.string().optional().describe('Absolute working directory.'),
    timeout_ms:z.number().int().min(100).max(600_000).optional(),
    env:z.record(z.string(), z.string()).optional().describe('Extra environment variables.'),
  },
  annotations:{readOnlyHint:false, destructiveHint:true, openWorldHint:true},
}, async ({executable,args,cwd,timeout_ms,env}) => run(() =>
  client.run('exec', {executable,args,cwd,timeout_ms,env}, (timeout_ms ?? 120_000) + 60_000)));

server.registerTool('env_shell', {
  title:'Run a shell script',
  description:'Run a PowerShell (Windows) or bash (POSIX) script in the test environment.',
  inputSchema:{
    script:z.string().min(1).max(32_000),
    shell:z.enum(['powershell','cmd']).optional().describe('Windows shell; defaults to PowerShell.'),
    cwd:z.string().optional(),
    timeout_ms:z.number().int().min(100).max(600_000).optional(),
  },
  annotations:{readOnlyHint:false, destructiveHint:true, openWorldHint:true},
}, async ({script,shell,cwd,timeout_ms}) => run(() =>
  client.run('shell', {script,shell,cwd,timeout_ms}, (timeout_ms ?? 120_000) + 60_000)));

server.registerTool('env_read_file', {
  title:'Read a file',
  description:'Read a text or binary file on the test machine. Binary content is returned as base64.',
  inputSchema:{
    path:z.string().describe('Absolute path under an allowed root.'),
    max_bytes:z.number().int().min(1).max(8_388_608).default(262_144),
    encoding:z.enum(['auto','utf8','base64']).default('auto'),
  },
  annotations:{readOnlyHint:true, openWorldHint:false},
}, async ({path,max_bytes,encoding}) => run(() => client.run('read_file', {path,max_bytes,encoding})));

server.registerTool('env_write_file', {
  title:'Write a file',
  description:'Write a file on the test machine. Parent directories are created by default.',
  inputSchema:{
    path:z.string(),
    content:z.string(),
    encoding:z.enum(['utf8','base64']).default('utf8'),
    mkdir:z.boolean().default(true),
  },
  annotations:{readOnlyHint:false, destructiveHint:true, openWorldHint:false},
}, async ({path,content,encoding,mkdir}) => run(() => client.run('write_file', {path,content,encoding,mkdir})));

server.registerTool('env_list', {
  title:'List a directory',
  description:'List directory entries on the test machine (depth 1..3, at most 500 entries).',
  inputSchema:{
    path:z.string(),
    depth:z.number().int().min(1).max(3).default(1),
    limit:z.number().int().min(1).max(500).default(200),
  },
  annotations:{readOnlyHint:true, openWorldHint:false},
}, async ({path,depth,limit}) => run(() => client.run('list', {path,depth,limit})));

server.registerTool('env_http', {
  title:'HTTP request from the test machine',
  description:'Call a local HTTP API from inside the test machine (for example the Syntropy app-server).',
  inputSchema:{
    method:z.string().default('GET'),
    url:z.string(),
    headers:z.record(z.string(), z.string()).optional(),
    body:z.string().optional(),
    timeout_ms:z.number().int().min(100).max(60_000).default(20_000),
    max_bytes:z.number().int().min(1).max(1_048_576).default(262_144),
  },
  annotations:{readOnlyHint:false, openWorldHint:true},
}, async ({method,url,headers,body,timeout_ms,max_bytes}) => run(() =>
  client.run('http', {method,url,headers,body,timeout_ms,max_bytes}, timeout_ms + 30_000)));

server.registerTool('env_processes', {
  title:'List processes',
  description:'List processes on the test machine, optionally filtered by name or command line substring.',
  inputSchema:{name:z.string().optional(), limit:z.number().int().min(1).max(500).default(200)},
  annotations:{readOnlyHint:true, openWorldHint:false},
}, async ({name,limit}) => run(() => client.run('processes', {name,limit})));

server.registerTool('env_start', {
  title:'Start a process',
  description:'Start a detached process on the test machine and return its PID.',
  inputSchema:{
    executable:z.string(),
    args:z.array(z.string()).default([]),
    cwd:z.string().optional(),
    env:z.record(z.string(), z.string()).optional(),
  },
  annotations:{readOnlyHint:false, destructiveHint:true, openWorldHint:true},
}, async ({executable,args,cwd,env}) => run(() => client.run('start_process', {executable,args,cwd,env})));

server.registerTool('env_stop', {
  title:'Stop a process',
  description:'Stop a process on the test machine, including its child tree by default.',
  inputSchema:{pid:z.number().int().min(1), tree:z.boolean().default(true)},
  annotations:{readOnlyHint:false, destructiveHint:true, openWorldHint:false},
}, async ({pid,tree}) => run(() => client.run('stop_process', {pid,tree})));

server.registerTool('ui_screenshot', {
  title:'Capture the desktop',
  description:'Capture a window (by title) or the full screen and return the PNG plus an evidence reference.',
  inputSchema:{
    title:z.string().optional().describe('Window title substring; omit to capture the full screen.'),
    full_screen:z.boolean().default(false),
    timeout_ms:z.number().int().min(100).max(120_000).default(30_000),
  },
  annotations:{readOnlyHint:true, openWorldHint:false},
}, async ({title,full_screen,timeout_ms}) => {
  try {
    const result = await client.run('screenshot', {title,full_screen,timeout_ms}, timeout_ms + 60_000);
    const reference = String(result.reference ?? '');
    const sha = reference.replace(/^sha256:/, '');
    if (!sha) return text(result);
    const image = await client.download(sha);
    return {content:[
      {type:'text', text:JSON.stringify({reference, path:result.path, bytes:result.bytes})},
      {type:'image', data:image.bytes.toString('base64'), mimeType:image.mimeType.split(';')[0] || 'image/png'},
    ]};
  } catch (error) {
    return text({error: error instanceof Error ? error.message : String(error)}, true);
  }
});

server.registerTool('upload_file', {
  title:'Upload a file as evidence',
  description:'Upload an existing file from the test machine as evidence and return its reference.',
  inputSchema:{path:z.string(), mime_type:z.string().optional()},
  annotations:{readOnlyHint:true, openWorldHint:false},
}, async ({path,mime_type}) => run(() => client.run('upload_file', {path,mime_type})));

server.registerTool('check_record', {
  title:'Record a check',
  description:'Record or replace one acceptance check. PASS checks must reference evidence (sha256:...).',
  inputSchema:{
    id:z.string().min(1).max(80),
    status:z.enum(['PASS','FAIL','BLOCKED','SKIPPED']),
    evidence:z.string().optional().describe('Evidence reference, normally sha256:<digest>.'),
    note:z.string().max(4000).optional().describe('What was observed and why the status is justified.'),
  },
  annotations:{readOnlyHint:false, destructiveHint:false, openWorldHint:false},
}, async ({id,status,evidence,note}) => run(async () => {
  const updated = await client.check({id,status,evidence,note});
  return {checks: updated.checks ?? []};
}));

server.registerTool('evidence_upload', {
  title:'Upload inline evidence',
  description:'Upload small evidence bytes (base64) and return a sha256 reference.',
  inputSchema:{
    base64:z.string(),
    mime_type:z.enum(['image/png','image/jpeg','application/json','text/plain']),
  },
  annotations:{readOnlyHint:false, destructiveHint:false, openWorldHint:false},
}, async ({base64,mime_type}) => {
  try {
    const {createHash} = await import('node:crypto');
    const bytes = Buffer.from(base64, 'base64');
    if (!bytes.length || bytes.length > 8 * 1024 * 1024) throw new Error('Evidence must be 1 byte..8 MiB');
    const sha256 = createHash('sha256').update(bytes).digest('hex');
    const reference = await client.upload({sha256, mime_type, payload:bytes.toString('base64')});
    return text({reference, bytes:bytes.length});
  } catch (error) {
    return text({error: error instanceof Error ? error.message : String(error)}, true);
  }
});

server.registerTool('test_finish', {
  title:'Finish the session',
  description:'Ask the runner to clean up and close the session with this verdict. Required checks and evidence are enforced server-side.',
  inputSchema:{
    verdict:z.enum(['PASS','FAIL','BLOCKED']),
    summary:z.string().min(1).max(4000),
  },
  annotations:{readOnlyHint:false, destructiveHint:false, openWorldHint:false},
}, async ({verdict,summary}) => {
  try {
    await client.finish(verdict, summary);
    return text({status:'finishing', verdict, summary});
  } catch (error) {
    return text({error: error instanceof Error ? error.message : String(error)}, true);
  }
});

process.once('SIGINT', () => { void server.close(); });
process.once('SIGTERM', () => { void server.close(); });
await server.connect(new StdioServerTransport());
