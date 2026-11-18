// Deterministic operator smoke for a windowed product session. It exercises the
// same MCP tools an LLM agent uses, so it verifies the whole path without a model.
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const mcp = join(here, '..', 'dist', 'src', 'test-mcp.js');
const required = ['SMARTPIPE_API_URL','SMARTPIPE_TEST_SESSION_ID','SMARTPIPE_TEST_SESSION_TOKEN'];
for (const key of required) if (!process.env[key]) throw new Error(key + ' is required');

const client = new Client({name:'smartpipe-session-smoke', version:'0.1.0'});
await client.connect(new StdioClientTransport({command:process.execPath, args:[mcp], env:{...process.env}, stderr:'inherit'}));

async function call(name, args = {}) {
  const result = await client.callTool({name, arguments:args}, undefined, {timeout:180_000});
  const textPart = (result.content ?? []).find(c => c.type === 'text');
  const text = textPart?.text ?? '';
  console.error('[' + name + '] ' + text.slice(0, 400));
  if (result.isError) throw new Error(name + ' failed: ' + text);
  try { return JSON.parse(text); } catch { return text; }
}

const context = await call('test_session');
const root = context.inputs?.install_root;
if (!root) throw new Error('story did not provide install_root');
const output = join('C:\\SmartPipeTest\\work', 'session-' + Date.now());
await call('env_shell', {script:'New-Item -ItemType Directory -Force -Path \'' + output + '\' | Out-Null'});

const listing = await call('env_shell', {script:
  '$root=' + JSON.stringify(root) + ';' +
  '$items=@(\'Syntropy.exe\',\'resources\\bin\\win32-x64\\syntropy-app-server.exe\') | ForEach-Object {' +
  ' $p=Join-Path $root $_; $e=Test-Path -LiteralPath $p;' +
  ' [pscustomobject]@{path=$p;exists=$e;length=$(if($e){(Get-Item -LiteralPath $p).Length}else{$null})} };' +
  '$items | ConvertTo-Json -Compress'});
if (listing.exit_code !== 0) throw new Error('file listing failed: ' + listing.stderr);
const files = JSON.parse(listing.stdout.trim());
if (!files.every(f => f.exists)) throw new Error('installation is incomplete: ' + listing.stdout);
const installPath = join(output, 'files.json');
await call('env_write_file', {path:installPath, content:listing.stdout.trim()});
const installEvidence = await call('upload_file', {path:installPath, mime_type:'application/json'});
await call('check_record', {id:'app_installed', status:'PASS', evidence:installEvidence.reference,
  note:'Syntropy.exe and app-server are present'});

let processes = await call('env_processes', {name:'Syntropy'});
if (!processes.processes?.some(p => /Syntropy\.exe/i.test(String(p.name)))) {
  await call('env_start', {executable:join(root, 'Syntropy.exe'), cwd:root});
}
const window = await call('env_shell', {timeout_ms:150000, script:
  '$deadline=(Get-Date).AddSeconds(120);' +
  'do { $p=Get-Process | Where-Object { $_.MainWindowHandle -ne 0 -and $_.MainWindowTitle -match \'Syntropy\' } | Select-Object -First 1;' +
  ' if($p){ [pscustomobject]@{pid=$p.Id;title=$p.MainWindowTitle} | ConvertTo-Json -Compress; exit 0 };' +
  ' Start-Sleep -Milliseconds 500 } while((Get-Date) -lt $deadline); exit 3'});
if (window.exit_code !== 0) throw new Error('Syntropy window did not appear: ' + window.stderr);
const windowPath = join(output, 'window.json');
await call('env_write_file', {path:windowPath, content:window.stdout.trim()});
const windowEvidence = await call('upload_file', {path:windowPath, mime_type:'application/json'});
await call('check_record', {id:'app_window', status:'PASS', evidence:windowEvidence.reference,
  note:'main window observed: ' + window.stdout.trim()});

const shot = await call('ui_screenshot', {title:'Syntropy', timeout_ms:60000});
if (!shot.reference) throw new Error('screenshot did not produce evidence');
await call('check_record', {id:'screenshot', status:'PASS', evidence:shot.reference,
  note:'captured main window ' + shot.reference});

await call('test_finish', {verdict:'PASS', summary:'Syntropy installed, main window shown and captured; checks recorded with evidence.'});
await client.close();
console.log(JSON.stringify({status:'PASS', session:context.id, install_evidence:installEvidence.reference, screenshot:shot.reference}));
