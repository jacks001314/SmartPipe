/** HTTP clients for agent-driven test sessions. */
export interface SessionSpec {
  project_id: string; title: string; kind: string; environment: string; platform: string;
  story_ref?: string; story: string; inputs?: Record<string,string>;
  required_checks?: string[]; timeout_seconds: number; work_dir?: string;
}
export interface SessionCheck {
  id: string; status: 'PASS'|'FAIL'|'BLOCKED'|'SKIPPED'; evidence?: string; note?: string; updated_at?: string;
}
export interface SessionInfo {
  id: string; spec: SessionSpec; status: string; machine_id?: string; verdict?: string; summary?: string;
  checks?: SessionCheck[]; created_at: string; started_at?: string; finished_at?: string; deadline?: string;
}
export interface SessionCommand {
  id: string; seq: number; kind: string; args: Record<string,unknown>; status: string;
  result?: Record<string,unknown>; created_at: string; started_at?: string; finished_at?: string;
}
export interface SessionLease {
  session_id: string; machine_id: string; generation: number;
  expires_at: string; deadline: string; session: SessionInfo;
}

export interface SessionInputInfo {
  name: string; kind: string; sha256: string; mime_type: string; bytes: number;
}

function endpoint(base: string): URL {
  const url = new URL(base);
  if (url.username || url.password || url.search || url.hash) throw new Error('Invalid API URL');
  if (url.protocol !== 'https:' && !(url.protocol === 'http:' && ['localhost','127.0.0.1','[::1]'].includes(url.hostname))) {
    throw new Error('Remote runner requires HTTPS');
  }
  return url;
}

async function call<T>(base: URL, path: string, init: RequestInit, signal?: AbortSignal): Promise<T|undefined> {
  const response = await fetch(new URL(path, base), {
    ...init, redirect: 'error',
    signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(35_000)]) : AbortSignal.timeout(35_000),
  });
  if (response.status === 204) return undefined;
  const text = await response.text();
  if (!response.ok) throw new Error('Test session API ' + path + ' returned ' + response.status + (text ? ': ' + text.slice(0,500) : ''));
  const parsed = JSON.parse(text) as {data?:T};
  if (!parsed.data) throw new Error('Test session API omitted data');
  return parsed.data;
}

export class SessionAgentClient {
  private readonly base: URL;
  constructor(base: string, readonly sessionID: string, private readonly token: string) {
    this.base = endpoint(base);
  }
  private headers(): Record<string,string> {
    return {'Content-Type':'application/json','Authorization':'Bearer '+this.token};
  }
  async info(signal?: AbortSignal): Promise<SessionInfo> {
    const data = await call<SessionInfo>(this.base, '/test-session/' + encodeURIComponent(this.sessionID), {method:'GET',headers:this.headers()}, signal);
    if (!data) throw new Error('Missing session');
    return data;
  }
  async command(kind: string, args: Record<string,unknown>, signal?: AbortSignal): Promise<SessionCommand> {
    const data = await call<SessionCommand>(this.base, '/test-session/' + encodeURIComponent(this.sessionID) + '/command',
      {method:'POST',headers:this.headers(),body:JSON.stringify({kind,args})}, signal);
    if (!data) throw new Error('Missing command');
    return data;
  }
  async result(commandID: string, waitMs: number, signal?: AbortSignal): Promise<SessionCommand> {
    const data = await call<SessionCommand>(this.base, '/test-session/' + encodeURIComponent(this.sessionID) +
      '/command/' + encodeURIComponent(commandID) + '?wait_ms=' + Math.max(0, Math.min(30_000, waitMs)),
      {method:'GET',headers:this.headers()}, signal);
    if (!data) throw new Error('Missing command result');
    return data;
  }
  /** Run one command to a terminal state. */
  async run(kind: string, args: Record<string,unknown>, timeoutMs = 120_000, signal?: AbortSignal): Promise<Record<string,unknown>> {
    const started = Date.now();
    const command = await this.command(kind, args, signal);
    let current = command;
    while (current.status === 'PENDING' || current.status === 'RUNNING') {
      if (Date.now() - started > timeoutMs) throw new Error('Command ' + command.id + ' timed out locally');
      current = await this.result(command.id, 15_000, signal);
    }
    return {command_id: current.id, status: current.status, ...(current.result ?? {})};
  }
  async check(check: {id:string;status:string;evidence?:string;note?:string}, signal?: AbortSignal): Promise<SessionInfo> {
    const data = await call<SessionInfo>(this.base, '/test-session/' + encodeURIComponent(this.sessionID) + '/check',
      {method:'POST',headers:this.headers(),body:JSON.stringify(check)}, signal);
    if (!data) throw new Error('Missing session');
    return data;
  }
  async upload(evidence: {sha256:string;mime_type:string;payload:string}, signal?: AbortSignal): Promise<string> {
    const data = await call<{reference:string}>(this.base, '/test-session/' + encodeURIComponent(this.sessionID) + '/evidence',
      {method:'POST',headers:this.headers(),body:JSON.stringify(evidence)}, signal);
    if (!data?.reference) throw new Error('Missing evidence receipt');
    return data.reference;
  }
  async finish(verdict: string, summary: string, signal?: AbortSignal): Promise<void> {
    await call<unknown>(this.base, '/test-session/' + encodeURIComponent(this.sessionID) + '/finish',
      {method:'POST',headers:this.headers(),body:JSON.stringify({verdict,summary})}, signal);
  }
  async download(sha256: string, signal?: AbortSignal): Promise<{mimeType:string;bytes:Buffer}> {
    const response = await fetch(new URL('/test-session/' + encodeURIComponent(this.sessionID) + '/evidence/' + encodeURIComponent(sha256), this.base), {
      method:'GET', headers:{'Authorization':'Bearer '+this.token}, redirect:'error',
      signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(20_000)]) : AbortSignal.timeout(20_000),
    });
    if (!response.ok) throw new Error('Evidence download returned ' + response.status);
    return {mimeType: response.headers.get('content-type') ?? 'application/octet-stream', bytes: Buffer.from(await response.arrayBuffer())};
  }
}

export class SessionMachineClient {
  private readonly base: URL;
  constructor(base: string, readonly machineID: string, private readonly token: string) {
    this.base = endpoint(base);
  }
  private headers(): Record<string,string> {
    return {'Content-Type':'application/json','Authorization':'Bearer '+this.token,'X-Test-Machine-ID':this.machineID};
  }
  async claim(environments: string[], signal?: AbortSignal): Promise<SessionLease|undefined> {
    return call<SessionLease>(this.base, '/test-runner/session/claim', {method:'POST',headers:this.headers(),body:JSON.stringify({environments})}, signal);
  }
  async heartbeat(sessionID: string, generation: number, signal?: AbortSignal): Promise<{expires_at:string;deadline:string}> {
    const data = await call<{expires_at:string;deadline:string}>(this.base, '/test-runner/session/heartbeat',
      {method:'POST',headers:this.headers(),body:JSON.stringify({session_id:sessionID,generation})}, signal);
    if (!data) throw new Error('Missing heartbeat');
    return data;
  }
  async next(sessionID: string, generation: number, waitSeconds = 25, signal?: AbortSignal): Promise<SessionCommand|'terminal'|undefined> {
    const response = await fetch(new URL('/test-runner/session/next', this.base), {
      method:'POST', headers:this.headers(), redirect:'error',
      body:JSON.stringify({session_id:sessionID,generation,wait_seconds:waitSeconds}),
      signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(35_000)]) : AbortSignal.timeout(35_000),
    });
    if (response.status === 204) return undefined;
    if (response.status === 409) return 'terminal';
    const text = await response.text();
    if (!response.ok) throw new Error('next returned ' + response.status + ': ' + text.slice(0,300));
    return (JSON.parse(text) as {data:SessionCommand}).data;
  }
  async result(sessionID: string, generation: number, commandID: string, status: string, result: Record<string,unknown>, signal?: AbortSignal): Promise<void> {
    await call<unknown>(this.base, '/test-runner/session/result', {method:'POST',headers:this.headers(),
      body:JSON.stringify({session_id:sessionID,generation,command_id:commandID,status,result})}, signal);
  }
  async evidence(sessionID: string, generation: number, evidence: {sha256:string;mime_type:string;payload:string}, signal?: AbortSignal): Promise<string> {
    const data = await call<{reference:string}>(this.base, '/test-runner/session/evidence', {method:'POST',headers:this.headers(),
      body:JSON.stringify({session_id:sessionID,generation,evidence})}, signal);
    if (!data?.reference) throw new Error('Missing evidence receipt');
    return data.reference;
  }
  async report(sessionID: string, generation: number, verdict: string, summary: string, signal?: AbortSignal): Promise<void> {
    await call<unknown>(this.base, '/test-runner/session/report', {method:'POST',headers:this.headers(),
      body:JSON.stringify({session_id:sessionID,generation,verdict,summary})}, signal);
  }
  async reportWithFindings(sessionID: string, generation: number, report: {
    verdict: string; summary: string;
    checks?: Array<{id:string;status:string;evidence?:string;note?:string}>;
    findings?: Array<Record<string,unknown>>;
  }, signal?: AbortSignal): Promise<void> {
    await call<unknown>(this.base, '/test-runner/session/report', {method:'POST',headers:this.headers(),
      body:JSON.stringify({session_id:sessionID,generation,...report})}, signal);
  }
  async check(sessionID: string, generation: number, check: {id:string;status:string;evidence?:string;note?:string}, signal?: AbortSignal): Promise<void> {
    await call<unknown>(this.base, '/test-runner/session/check', {method:'POST',headers:this.headers(),
      body:JSON.stringify({session_id:sessionID,generation,check})}, signal);
  }
  async finding(sessionID: string, generation: number, finding: Record<string,unknown>, signal?: AbortSignal): Promise<void> {
    await call<unknown>(this.base, '/test-runner/session/finding', {method:'POST',headers:this.headers(),
      body:JSON.stringify({session_id:sessionID,generation,finding})}, signal);
  }
  async events(sessionID: string, generation: number,
    events: Array<{seq:number;kind:string;message:string;at:string}>, signal?: AbortSignal): Promise<void> {
    await call<unknown>(this.base, '/test-runner/session/events', {method:'POST',headers:this.headers(),
      body:JSON.stringify({session_id:sessionID,generation,events})}, signal);
  }
  async listInputs(sessionID: string, generation: number, signal?: AbortSignal): Promise<SessionInputInfo[]> {
    const data = await call<SessionInputInfo[]>(this.base,
      '/test-runner/session/inputs/' + encodeURIComponent(sessionID) + '?generation=' + generation,
      {method:'GET',headers:this.headers()}, signal);
    return data ?? [];
  }
  async input(sessionID: string, generation: number, name: string, signal?: AbortSignal): Promise<Buffer> {
    const response = await fetch(new URL('/test-runner/session/inputs/' + encodeURIComponent(sessionID) + '/' +
      encodeURIComponent(name) + '?generation=' + generation, this.base), {
      method: 'GET', headers: {'Authorization': 'Bearer ' + this.token, 'X-Test-Machine-ID': this.machineID},
      redirect: 'error',
      signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(180_000)]) : AbortSignal.timeout(180_000),
    });
    if (!response.ok) throw new Error('input download ' + name + ' returned ' + response.status);
    return Buffer.from(await response.arrayBuffer());
  }
  async release(sessionID: string, generation: number, signal?: AbortSignal): Promise<void> {
    await call<unknown>(this.base, '/test-runner/session/release', {method:'POST',headers:this.headers(),
      body:JSON.stringify({session_id:sessionID,generation})}, signal);
  }
}
