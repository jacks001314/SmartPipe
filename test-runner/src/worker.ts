export interface Spec {
 project_id: string; source_sha: string; artifact_sha256: string;
 scenario: string; scenario_version: string; platform: string; required_checks: string[];
}
export interface Report {
 source_sha: string; artifact_sha256: string; scenario_version: string; platform: string;
 checks: Array<{id: string; status: 'PASS'|'FAIL'|'BLOCKED'|'SKIPPED'; evidence: string}>;
}
export interface Assignment {
 machine_id: string; generation: number; expires_at: string;
 run: {id: string; spec: Spec};
}
export interface Scenario {
 capabilities?: string[];
 execute(assignment: Assignment, signal: AbortSignal): Promise<Report>;
 // Must resolve only after all desktop input stopped. On failure machine stays busy.
 cleanup(): Promise<void>;
 uploadEvidence?(upload: (mimeType: string, bytes: Buffer) => Promise<string>): Promise<void>;
}
export class RunnerClient {
 private readonly base: URL;
 constructor(base: string, private machineID: string, private token: string) {
  this.base = new URL(base);
  if (this.base.username || this.base.password || this.base.search || this.base.hash) throw new Error('Invalid API URL');
  if (this.base.protocol !== 'https:' &&
      !(this.base.protocol === 'http:' && ['localhost','127.0.0.1','[::1]'].includes(this.base.hostname))) {
   throw new Error('Remote runner requires HTTPS');
  }
 }
 async request<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T | undefined> {
  const response = await fetch(new URL('/test-runner/' + path, this.base), {
   method:'POST', headers:{'Content-Type':'application/json','Authorization':'Bearer '+this.token,'X-Test-Machine-ID':this.machineID},
   body:JSON.stringify(body), redirect:'error',
   signal:signal ? AbortSignal.any([signal, AbortSignal.timeout(10_000)]) : AbortSignal.timeout(10_000),
  });
  if (!response.ok) throw new Error('Runner API ' + path + ' returned ' + response.status);
  if (response.status === 204) return undefined;
  const result = await response.json() as {data: T};
  if (!result.data) throw new Error('Runner API omitted data');
  return result.data;
 }
 /** One assignment per invocation. No input or report is automatically retried. */
 async runOnce(scenario: Scenario, signal?: AbortSignal, heartbeatMs = 15_000, maxRunMs = 20*60_000): Promise<boolean> {
  if(!Number.isFinite(maxRunMs)||maxRunMs<1||maxRunMs>20*60_000)throw new Error('Invalid run deadline');
  if (signal?.aborted) throw signal.reason;
  const assignment = await this.request<Assignment>('claim', {scenarios:scenario.capabilities ?? ['desktop-observe@1']}, signal);
  if (!assignment) return false;
  if (assignment.machine_id !== this.machineID || !Number.isSafeInteger(assignment.generation) || assignment.generation < 1) {
   throw new Error('Invalid assignment identity');
  }
  const abort = new AbortController();
  const runDeadline=setTimeout(()=>abort.abort(new Error('Run deadline exceeded')),maxRunMs);
  const onAbort = () => abort.abort(signal?.reason);
  signal?.addEventListener('abort',onAbort,{once:true});
  if (signal?.aborted) onAbort();
  const identity = {run_id:assignment.run.id,generation:assignment.generation};
  let active = true;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let heartbeat: Promise<void> = Promise.resolve();
  let deadline: ReturnType<typeof setTimeout> | undefined;
  const setDeadline = (expiresAt: string) => {
   if (deadline) clearTimeout(deadline);
   const ms = Date.parse(expiresAt) - Date.now() - 2_000;
   if (!Number.isFinite(ms) || ms <= 0) {abort.abort(new Error('Lease expired'));return;}
   deadline = setTimeout(() => abort.abort(new Error('Lease expired')), Math.min(ms,60_000));
  };
  setDeadline(assignment.expires_at);
  const schedule = () => {
   timer = setTimeout(() => {
    heartbeat = (async () => {
     try {
      const updated = await this.request<{expires_at:string}>('heartbeat',identity,abort.signal);
      if (!updated) throw new Error('Missing heartbeat');
      setDeadline(updated.expires_at);
      if (active && !abort.signal.aborted) schedule();
     } catch (error) { abort.abort(error); }
    })();
   },heartbeatMs);
  };
  schedule();
  let cleaned = false;
  try {
   abort.signal.throwIfAborted();
   const report = await scenario.execute(assignment,abort.signal);
   abort.signal.throwIfAborted();
   await scenario.cleanup(); cleaned=true;
   abort.signal.throwIfAborted();
   if (scenario.uploadEvidence) await scenario.uploadEvidence(async (mimeType, bytes) => {
    if (!bytes.length || bytes.length > 8 * 1024 * 1024) throw new Error('Evidence size limit');
    const { createHash } = await import('node:crypto');
    const digest = createHash('sha256').update(bytes).digest('hex');
    const result = await this.request<{reference:string}>('evidence', {...identity,evidence:{
     sha256:digest,mime_type:mimeType,payload:bytes.toString('base64')
    }},abort.signal);
    if (result?.reference !== 'sha256:' + digest) throw new Error('Invalid evidence receipt');
    return result.reference;
   });
   abort.signal.throwIfAborted();
   // Stop heartbeat before report changes task state to terminal.
   active=false;if(timer)clearTimeout(timer);await heartbeat;
   abort.signal.throwIfAborted();
   await this.request('report',{...identity,report},abort.signal);
   await this.request('release',identity);
   return true;
  } finally {
   clearTimeout(runDeadline);
   active=false;if(timer)clearTimeout(timer);if(deadline)clearTimeout(deadline);
   abort.abort(new Error('Assignment ended'));
   await heartbeat;
   signal?.removeEventListener('abort',onAbort);
   if (!cleaned) await scenario.cleanup();
   // Failure leaves the machine quarantined/busy. Admin cancels the run and a
   // controlled recovery acknowledges release after independently checking cleanup.
  }
 }
}
