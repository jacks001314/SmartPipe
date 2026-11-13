import type { AuditEntry, AuthConfig, AuthSession, CrashReport, CrashRetention, Credential, LanxinBot, LanxinBotDetail, GitHubDeviceFlow, GitHubOAuthPoll, GitHubOAuthView, LanxinGroup, LanxinMessage, LanxinTestResult, ModelConfigImportPreview, ModelConfigImportResult, ModelLocalConfigResult, ModelDefault, ModelProfile, ModelProvider, ProviderModel, ProviderTestResult, Health, IssueAttempt, Project, ReleaseCandidate, ReleaseRecord, ReleaseRun, ReleaseV2AgentView, ReleaseV2AgentSession, ReleaseV2Approval, ReleaseV2Bundle, ReleaseV2Executor, ReleaseV2Plan, ReleaseV2Run, ReleaseV2Settings, ReleaseV2Probe, ReleaseV2ExecutorBootstrap, ReleaseV2ExecutorDeployResult, ReleaseV2ExecutorRemoveResult, RepoOp, TestResult, UserIdentity, VerificationRecord, GitHubAppSetup, GitHubAppSetupStatus, GitHubRepoList, GitHubProxyView, GitHubTunnelView, GitHubPATView, GitHubRepoCatalog, GitHubRepo, SchedulerOverview, SchedulerWorkerDetail, SchedulerWorker, SchedulerMessage, SchedulerOutputFrame, SchedulerOutputRawPage } from "./types";
import type { ResourceInspectionResult, ResourceProbeResult, RuntimeResource } from "./types";

const TOKEN_KEY = "smartpipe.adminToken";

let unauthorizedHandler: (() => void) | null = null;

export function setUnauthorizedHandler(fn: (() => void) | null) {
  unauthorizedHandler = fn;
}

export function getToken(): string {
  return window.localStorage.getItem(TOKEN_KEY) ?? "";
}

export function setToken(value: string) {
  if (value.trim() === "") {
    window.localStorage.removeItem(TOKEN_KEY);
  } else {
    window.localStorage.setItem(TOKEN_KEY, value.trim());
  }
}

export class ApiError extends Error {
  status: number;
  code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

type RequestOptions = {
  method?: string;
  body?: unknown;
};

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (options.body !== undefined) headers["Content-Type"] = "application/json";
  const token = getToken();
  if (token) headers["Authorization"] = "Bearer " + token;

  const res = await fetch("/admin" + path, {
    method: options.method ?? "GET",
    headers,
    credentials: "same-origin",
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });

  if (res.status === 204) {
    return undefined as T;
  }

  let payload: unknown = null;
  try {
    payload = await res.json();
  } catch {
    payload = null;
  }

  if (res.status === 401 && path !== "/login" && path !== "/session" && path !== "/auth-config") {
    unauthorizedHandler?.();
  }
  if (!res.ok) {
    const err = (payload as { error?: { code?: string; message?: string } })?.error;
    throw new ApiError(res.status, err?.code ?? "http_error", err?.message ?? "请求失败（HTTP " + res.status + "）");
  }

  return (payload as { data: T }).data;
}

export const api = {
  health: () => request<Health>("/health"),
  authConfig: () => request<AuthConfig>("/auth-config"),
  session: () => request<AuthSession>("/session"),
  login: (username: string, password: string) =>
    request<AuthSession>("/login", { method: "POST", body: { username, password } }),
  logout: () => request<void>("/logout", { method: "POST" }),
  schedulerOverview: () => request<SchedulerOverview>("/scheduler/overview"),
  schedulerMessages: (limit = 100) => request<SchedulerMessage[]>("/scheduler/messages?limit=" + limit),
  getSchedulerWorker: (id: string) => request<SchedulerWorkerDetail>("/scheduler/workers/" + encodeURIComponent(id)),
  schedulerOutput: (id: string, after = 0, limit = 500) =>
    request<SchedulerOutputFrame>("/scheduler/workers/" + encodeURIComponent(id) + "/output?after=" + after + "&limit=" + limit),
  schedulerOutputRaw: (id: string, itemId: string, offset = 0, limit = 256 * 1024) =>
    request<SchedulerOutputRawPage>("/scheduler/workers/" + encodeURIComponent(id) + "/output/" + encodeURIComponent(itemId) + "/raw?offset=" + offset + "&limit=" + limit),
  schedulerWorkerAction: (id: string, action: "pause" | "resume" | "retry" | "stop") =>
    request<{ worker: SchedulerWorker }>("/scheduler/workers/" + encodeURIComponent(id) + "/action", { method: "POST", body: { action } }),
  listProjects: () => request<Project[]>("/projects"),
  createProject: (body: Partial<Project>) => request<Project>("/projects", { method: "POST", body }),
  updateProject: (id: string, body: Partial<Project>) => request<Project>("/projects/" + id, { method: "PATCH", body }),
  deleteProject: (id: string) => request<void>("/projects/" + id, { method: "DELETE" }),
  setProjectEnabled: (id: string, enabled: boolean) =>
    request<Project>("/projects/" + id + "/enabled", { method: "POST", body: { enabled } }),
  listCredentials: (projectId?: string) =>
    request<Credential[]>("/credentials" + (projectId ? "?project_id=" + encodeURIComponent(projectId) : "")),
  listGitHubRepos: () => request<GitHubRepoList>("/github/repositories"),
  listGitHubRepoCatalog: () => request<GitHubRepoCatalog>("/github/repositories/catalog"),
  syncGitHubRepoCatalog: () => request<GitHubRepoCatalog>("/github/repositories/sync", { method: "POST" }),
  getGithubCloneRoot: () => request<{ root: string; exists: boolean }>("/github/clone-root"),
  setGithubCloneRoot: (root: string) =>
    request<{ root: string; exists: boolean }>("/github/clone-root", { method: "PUT", body: { root } }),
  cloneGithubRepo: (body: { full_name: string; target_dir?: string }) =>
    request<{ full_name: string; path: string; status: string }>(
      "/github/repositories/clone",
      { method: "POST", body },
    ),
  getGithubProxy: () => request<GitHubProxyView>("/github/proxy"),
  setGithubProxy: (url: string) => request<GitHubProxyView>("/github/proxy", { method: "PUT", body: { url } }),
  testGithubProxy: (url: string) => request<{ ok: boolean; message: string; latency_ms: number }>("/github/proxy/test", { method: "POST", body: { url } }),
  getGithubTunnel: () => request<GitHubTunnelView>("/github/tunnel"),
  setGithubTunnel: (body: {
    enabled: boolean;
    server: string;
    server_port: number;
    remote_port: number;
    proxy_name: string;
    token?: string;
  }) => request<GitHubTunnelView>("/github/tunnel", { method: "PUT", body }),
  startGithubApp: () => request<GitHubAppSetup>("/github/app/start", { method: "POST" }),
  getGithubAppStatus: (setup_id: string) =>
    request<GitHubAppSetupStatus>("/github/app/status/" + encodeURIComponent(setup_id)),
  getGithubOAuth: () => request<GitHubOAuthView>("/github/oauth"),
  saveGithubOAuth: (body: { client_id: string; scope: string }) =>
    request<GitHubOAuthView>("/github/oauth", { method: "PUT", body }),
  startGithubOAuth: () => request<GitHubDeviceFlow>("/github/oauth/device", { method: "POST" }),
  pollGithubOAuth: (flow_id: string) =>
    request<GitHubOAuthPoll>("/github/oauth/poll", { method: "POST", body: { flow_id } }),
  deleteGithubOAuth: () => request<void>("/github/oauth", { method: "DELETE" }),
  saveGithubToken: (token: string) =>
    request<GitHubPATView>("/github/token", { method: "POST", body: { token } }),
  deleteGithubToken: () => request<void>("/github/token", { method: "DELETE" }),
  getReleaseV2Settings: () => request<ReleaseV2Settings>("/releases/v2/settings"),
  saveReleaseV2Settings: (body: ReleaseV2Settings) => request<ReleaseV2Settings>("/releases/v2/settings", { method: "PUT", body }),
  probeReleaseV2Settings: (service: "update" | "github", target = "") =>
    request<ReleaseV2Probe>("/releases/v2/settings/probe", { method: "POST", body: { service, target } }),
  createCredential: (body: { project_id?: string | null; kind: string; name: string; value: string; metadata?: Record<string, unknown> }) =>
    request<Credential>("/credentials", { method: "POST", body }),
  rotateCredential: (id: string, value: string) =>
    request<Credential>("/credentials/" + id + "/rotate", { method: "POST", body: { value } }),
  setCredentialEnabled: (id: string, enabled: boolean) =>
    request<Credential>("/credentials/" + id + "/enabled", { method: "POST", body: { enabled } }),
  deleteCredential: (id: string) => request<void>("/credentials/" + id, { method: "DELETE" }),
  testCredential: (id: string) => request<TestResult>("/credentials/" + id + "/test", { method: "POST" }),
  listModelProviders: () => request<ModelProvider[]>("/model-providers"),
  createModelProvider: (body: { name: string; base_url: string; wire_api?: string; api_key?: string; enabled?: boolean; models?: string[] }) =>
    request<ModelProvider>("/model-providers", { method: "POST", body }),
  updateModelProvider: (id: string, body: { name?: string; base_url?: string; wire_api?: string; api_key?: string; enabled?: boolean }) =>
    request<ModelProvider>("/model-providers/" + id, { method: "PATCH", body }),
  setModelProviderEnabled: (id: string, enabled: boolean) =>
    request<ModelProvider>("/model-providers/" + id + "/enabled", { method: "POST", body: { enabled } }),
  testModelProvider: (id: string, model?: string) =>
    request<ProviderTestResult>("/model-providers/" + id + "/test", { method: "POST", body: { model: model ?? "" } }),
  deleteModelProvider: (id: string) => request<void>("/model-providers/" + id, { method: "DELETE" }),
  addProviderModel: (id: string, modelId: string) =>
    request<ProviderModel>("/model-providers/" + id + "/models", { method: "POST", body: { model_id: modelId } }),
  setProviderModelEnabled: (id: string, modelId: string, enabled: boolean) =>
    request<ProviderModel>("/model-providers/" + id + "/models/" + encodeURIComponent(modelId) + "/enabled", { method: "POST", body: { enabled } }),
  deleteProviderModel: (id: string, modelId: string) =>
    request<void>("/model-providers/" + id + "/models/" + encodeURIComponent(modelId), { method: "DELETE" }),
  refreshProviderModels: (id: string) =>
    request<{ provider: ModelProvider; count: number }>("/model-providers/" + id + "/models/refresh", { method: "POST" }),
  getModelDefault: () => request<ModelDefault>("/model-default"),
  putModelDefault: (body: ModelDefault) => request<ModelDefault>("/model-default", { method: "PUT", body }),
  previewLocalModelConfig: () => request<ModelLocalConfigResult>("/model-config/local/preview"),
  importLocalModelConfig: (profileKeys: string[], providerKeys: string[]) =>
    request<ModelConfigImportResult>("/model-config/local/import", { method: "POST", body: { profile_keys: profileKeys, provider_keys: providerKeys } }),
  previewModelConfigImport: (source: string, content: string) =>
    request<ModelConfigImportPreview>("/model-config/import/preview", { method: "POST", body: { source, content } }),
  importModelConfig: (source: string, content: string, profileKeys: string[], providerKeys: string[]) =>
    request<ModelConfigImportResult>("/model-config/import", { method: "POST", body: { source, content, profile_keys: profileKeys, provider_keys: providerKeys } }),
  listModelProfiles: () => request<ModelProfile[]>("/model-profiles"),
  setModelProfileEnabled: (id: string, enabled: boolean) =>
    request<ModelProfile>("/model-profiles/" + id + "/enabled", { method: "POST", body: { enabled } }),
  setModelProfileDefault: (id: string) =>
    request<ModelDefault>("/model-profiles/" + id + "/default", { method: "POST" }),
  deleteModelProfile: (id: string) => request<void>("/model-profiles/" + id, { method: "DELETE" }),
  listLanxinBots: (sync = false) => request<LanxinBot[]>("/lanxin/bots" + (sync ? "?sync=1" : "")),
  createLanxinBot: (body: { name: string; app_id: string; app_secret: string; api_base?: string; require_at_me?: boolean }) =>
    request<LanxinBot>("/lanxin/bots", { method: "POST", body }),
  getLanxinBot: (id: string, sync = false) =>
    request<LanxinBotDetail>("/lanxin/bots/" + encodeURIComponent(id) + (sync ? "?sync=1" : "")),
  updateLanxinBot: (id: string, body: { name?: string; app_id?: string; app_secret?: string; api_base?: string; require_at_me?: boolean; enabled?: boolean }) =>
    request<LanxinBot>("/lanxin/bots/" + encodeURIComponent(id), { method: "PATCH", body }),
  deleteLanxinBot: (id: string) => request<void>("/lanxin/bots/" + encodeURIComponent(id), { method: "DELETE" }),
  setLanxinBotEnabled: (id: string, enabled: boolean) =>
    request<LanxinBot>("/lanxin/bots/" + encodeURIComponent(id) + "/enabled", { method: "POST", body: { enabled } }),
  testLanxinBot: (id: string) => request<LanxinTestResult>("/lanxin/bots/" + encodeURIComponent(id) + "/test", { method: "POST" }),
  syncLanxinBotGroups: (id: string) =>
    request<{ bot: LanxinBot; groups: LanxinGroup[] }>("/lanxin/bots/" + encodeURIComponent(id) + "/sync-groups", { method: "POST" }),
  listLanxinBotMessages: (id: string, limit = 100) =>
    request<LanxinMessage[]>("/lanxin/bots/" + encodeURIComponent(id) + "/messages?limit=" + limit),
  listLanxinGroups: (refresh = false, botId = "") =>
    request<LanxinGroup[]>("/lanxin/groups?" + (refresh ? "refresh=1" : "") + (botId ? "&bot_id=" + encodeURIComponent(botId) : "")),
  bindLanxinGroup: (groupId: string, projectId: string, botId = "") =>
    request<{ group_id: string; project_id: string }>("/lanxin/groups/bind", { method: "POST", body: { group_id: groupId, project_id: projectId, bot_id: botId } }),
  unbindLanxinGroup: (groupId: string) => request<void>("/lanxin/groups/" + encodeURIComponent(groupId), { method: "DELETE" }),
  updateLanxinGroupPolicy: (body: {
    group_id: string;
    response_mode?: "at_only" | "smart" | "all";
    shadow_mode?: boolean;
    reply_cooldown_seconds?: number;
    hourly_reply_limit?: number;
    auto_issue_enabled?: boolean;
    auto_issue_confidence?: number;
    daily_auto_issue_limit?: number;
    answer_confidence?: number;
  }) => request<LanxinGroup>("/lanxin/groups/policy", { method: "POST", body }),
  listLanxinMessages: (limit = 100, botId = "") =>
    request<LanxinMessage[]>("/lanxin/messages?limit=" + limit + (botId ? "&bot_id=" + encodeURIComponent(botId) : "")),
  listAudit: (limit = 100, action = "") =>
    request<AuditEntry[]>("/audit?limit=" + limit + (action ? "&action=" + encodeURIComponent(action) : "")),

  listResources: () => request<RuntimeResource[]>("/resources"),
  listAvailableResources: (tag = "") =>
    request<RuntimeResource[]>("/resources/available" + (tag ? "?tag=" + encodeURIComponent(tag) : "")),
  getResource: (id: string) => request<RuntimeResource>("/resources/" + encodeURIComponent(id)),
  createResource: (body: {
    name: string;
    description?: string;
    host: string;
    port: number;
    username: string;
    auth_type: "password" | "private_key";
    secret: string;
    passphrase?: string;
    host_key_fingerprint?: string;
    tags?: string[];
    enabled?: boolean;
  }) => request<RuntimeResource>("/resources", { method: "POST", body }),
  updateResource: (id: string, body: {
    name: string;
    description?: string;
    host: string;
    port: number;
    username: string;
    auth_type: "password" | "private_key";
    secret?: string;
    passphrase?: string;
    host_key_fingerprint?: string;
    tags?: string[];
    enabled?: boolean;
  }) => request<RuntimeResource>("/resources/" + encodeURIComponent(id), { method: "PATCH", body }),
  setResourceEnabled: (id: string, enabled: boolean) =>
    request<RuntimeResource>("/resources/" + encodeURIComponent(id) + "/enabled", { method: "POST", body: { enabled } }),
  testResource: (id: string) =>
    request<ResourceProbeResult>("/resources/" + encodeURIComponent(id) + "/test", { method: "POST" }),
  inspectResource: (id: string) =>
    request<ResourceInspectionResult>("/resources/" + encodeURIComponent(id) + "/inspect", { method: "POST" }),
  deleteResource: (id: string) => request<void>("/resources/" + encodeURIComponent(id), { method: "DELETE" }),

  listIdentities: (platform = "", projectId = "") =>
    request<UserIdentity[]>("/identities?platform=" + encodeURIComponent(platform) + (projectId ? "&project_id=" + encodeURIComponent(projectId) : "")),
  upsertIdentity: (body: { platform?: string; platform_user_id: string; project_id?: string | null; github_login: string; display_name?: string; enabled?: boolean }) =>
    request<UserIdentity>("/identities", { method: "POST", body }),
  deleteIdentity: (id: number) => request<void>("/identities/" + id, { method: "DELETE" }),

  listVerifications: (limit = 50) => request<VerificationRecord[]>("/verifications?limit=" + limit),
  listAttempts: (limit = 50) => request<IssueAttempt[]>("/attempts?limit=" + limit),
  listReleases: (limit = 50) => request<ReleaseRecord[]>("/releases?limit=" + limit),
  createRelease: (body: { project_id?: string; repo?: string; version: string; commit: string; artifact_globs?: string[]; notes?: string }) =>
    request<{ record: ReleaseRecord; report: Record<string, unknown> }>("/releases", { method: "POST", body }),
  listReleaseRuns: (limit = 50) => request<ReleaseRun[]>("/releases/runs?limit=" + limit),
  listRepoOps: (limit = 100) => request<RepoOp[]>("/repo-ops?limit=" + limit),

  crashRetention: () => request<CrashRetention>("/crashes/retention"),
  runCrashRetention: () => request<CrashRetention>("/crashes/retention/run", { method: "POST" }),
  listCrashes: (limit = 100) => request<CrashReport[]>("/crashes?limit=" + limit),
  getCrash: (id: number) => request<CrashReport>("/crashes/" + id),
  listReleaseCandidates: (projectId: string, label?: string) =>
    request<{ repo: string; label: string; candidates: ReleaseCandidate[] }>(
      "/releases/candidates?project_id=" + encodeURIComponent(projectId) + (label ? "&label=" + encodeURIComponent(label) : "")),
  createReleaseRun: (body: { project_id: string; version: string; commit?: string; branch?: string; notes?: string; dry_run?: boolean; artifact_globs?: string[]; confirm_major?: boolean }) =>
    request<{ run: ReleaseRun; record: ReleaseRun }>("/releases/runs", { method: "POST", body }),
  rollbackReleaseRun: (id: number) => request<ReleaseRun>("/releases/runs/" + id + "/rollback", { method: "POST" }),
  listReleaseV2Runs: (limit = 50) => request<ReleaseV2Run[]>("/releases/v2/runs?limit=" + limit),
  getReleaseV2Run: (id: string, after = 0) =>
    request<ReleaseV2Bundle>("/releases/v2/runs/" + encodeURIComponent(id) + "?after=" + after),
  createReleaseV2EventTicket: (id: string) =>
    request<{ ticket: string; expires_at: string }>("/releases/v2/runs/" + encodeURIComponent(id) + "/events/ticket", { method: "POST", body: {} }),
  getReleaseV2Agent: (id: string) => request<ReleaseV2AgentView>("/releases/v2/runs/" + encodeURIComponent(id) + "/agent"),
  startReleaseV2Agent: (id: string, body: { scopes?: string[]; model?: string; effort?: string }) =>
    request<ReleaseV2AgentSession>("/releases/v2/runs/" + encodeURIComponent(id) + "/agent", { method: "POST", body }),
  createReleaseV2Plan: (body: { project_id: string; version: string; build_version?: string; channel?: string; mode: string; branch?: string; commit?: string; targets?: string[]; executor_bindings?: Record<string, string>; notes?: string }) =>
    request<ReleaseV2Plan>("/releases/v2/plans", { method: "POST", body }),
  createReleaseV2Run: (body: { plan_id: string; idempotency_key?: string }) =>
    request<ReleaseV2Run>("/releases/v2/runs", { method: "POST", body }),
  cancelReleaseV2Run: (id: string, reason?: string) =>
    request<ReleaseV2Run>("/releases/v2/runs/" + encodeURIComponent(id) + "/cancel", { method: "POST", body: { reason } }),
  issueReleaseV2Token: (id: string, scopes: string[], ttlSeconds = 14400) =>
    request<{ token: string; token_id: string; expires_at: string; scopes: string[] }>("/releases/v2/runs/" + encodeURIComponent(id) + "/agent-token", { method: "POST", body: { scopes, ttl_seconds: ttlSeconds } }),
  listReleaseV2Executors: () => request<ReleaseV2Executor[]>("/releases/v2/executors"),
  releaseV2ExecutorBootstrap: (os: string, arch: string, id: string) =>
    request<ReleaseV2ExecutorBootstrap>(
      "/releases/v2/executors/bootstrap?os=" + encodeURIComponent(os) + "&arch=" + encodeURIComponent(arch) + "&id=" + encodeURIComponent(id),
    ),
  deployReleaseV2Executor: (body: {
    id: string; os: string; arch: string;
    host?: string; port?: number; username?: string;
    auth_type?: string; secret?: string; passphrase?: string;
    resource_id?: string; install_dir?: string; save_resource?: boolean;
  }) => request<ReleaseV2ExecutorDeployResult>("/releases/v2/executors/deploy", { method: "POST", body }),
  deleteReleaseV2Executor: (id: string, body?: { force?: boolean; resource_id?: string; install_dir?: string }) =>
    request<ReleaseV2ExecutorRemoveResult>("/releases/v2/executors/" + encodeURIComponent(id), { method: "DELETE", body: body ?? {} }),
  listReleaseV2Approvals: (id: string) => request<ReleaseV2Approval[]>("/releases/v2/runs/" + encodeURIComponent(id) + "/approvals"),
  approveReleaseV2Run: (id: string, reason: string, ttlSeconds = 7200) =>
    request<ReleaseV2Approval>("/releases/v2/runs/" + encodeURIComponent(id) + "/approvals", { method: "POST", body: { reason, ttl_seconds: ttlSeconds } }),
  createVerification: (body: {
    project_id?: string;
    issue_ref: string;
    repo: string;
    base_commit: string;
    patch: string;
    test_log?: string;
    author?: string;
  }) => request<{ record: VerificationRecord; report: Record<string, unknown> }>("/verifications", { method: "POST", body }),
};

export type ToolCallApi = typeof api;


