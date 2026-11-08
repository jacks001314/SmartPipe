import type { AgentItem, AgentToken, AppServer, AuditEntry, AuthConfig, AuthSession, BridgeLink, CrashReport, CrashRetention, Credential, LanxinBot, LanxinBotDetail, GitHubDeviceFlow, GitHubOAuthPoll, GitHubOAuthView, LanxinGroup, LanxinMessage, LanxinTestResult, ModelConfigImportPreview, ModelConfigImportResult, ModelDefault, ModelProvider, ProviderModel, ProviderTestResult, Derivation, DispatchRecord, Health, IssueAttempt, IssuePlan, Project, ProjectBinding, ReleaseCandidate, ReleaseRecord, ReleaseRun, RepoOp, MCPConfig, TestResult, ToolCall, UserIdentity, VerificationRecord, WebhookDelivery, GitHubAppSetup, GitHubAppSetupStatus } from "./types";

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
  listProjects: () => request<Project[]>("/projects"),
  createProject: (body: Partial<Project>) => request<Project>("/projects", { method: "POST", body }),
  updateProject: (id: string, body: Partial<Project>) => request<Project>("/projects/" + id, { method: "PATCH", body }),
  setProjectEnabled: (id: string, enabled: boolean) =>
    request<Project>("/projects/" + id + "/enabled", { method: "POST", body: { enabled } }),
  listCredentials: (projectId?: string) =>
    request<Credential[]>("/credentials" + (projectId ? "?project_id=" + encodeURIComponent(projectId) : "")),
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
  previewModelConfigImport: (source: string, content: string) =>
    request<ModelConfigImportPreview>("/model-config/import/preview", { method: "POST", body: { source, content } }),
  importModelConfig: (source: string, content: string) =>
    request<ModelConfigImportResult>("/model-config/import", { method: "POST", body: { source, content } }),
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
  listLanxinMessages: (limit = 100, botId = "") =>
    request<LanxinMessage[]>("/lanxin/messages?limit=" + limit + (botId ? "&bot_id=" + encodeURIComponent(botId) : "")),
  listAudit: (limit = 100, action = "") =>
    request<AuditEntry[]>("/audit?limit=" + limit + (action ? "&action=" + encodeURIComponent(action) : "")),

  listAppServers: () => request<AppServer[]>("/runtime/app-servers"),
  startAppServer: (body: { name?: string; addr?: string; binary?: string; token?: string; data_dir?: string }) =>
    request<AppServer>("/runtime/app-servers", { method: "POST", body }),
  stopAppServer: (id: string) => request<AppServer>("/runtime/app-servers/" + id + "/stop", { method: "POST" }),
  removeAppServer: (id: string) => request<void>("/runtime/app-servers/" + id, { method: "DELETE" }),
  restartAppServer: (id: string) => request<AppServer>("/runtime/app-servers/" + id + "/restart", { method: "POST" }),
  appServerLogs: (id: string, lines = 200) =>
    request<{ id: string; lines: number; log: string }>("/runtime/app-servers/" + id + "/logs?lines=" + lines),
  masterStatus: (id: string) => request<Record<string, unknown>>("/runtime/app-servers/" + id + "/master/status"),
  masterRegistry: (id: string) => request<Record<string, unknown>>("/runtime/app-servers/" + id + "/master/registry"),
  deployMaster: (id: string, body: { name: string; target?: string }) =>
    request<Record<string, unknown>>("/runtime/app-servers/" + id + "/master/deploy", { method: "POST", body }),
  nodeList: (id: string) => request<Record<string, unknown>>("/runtime/app-servers/" + id + "/nodes"),
  listAgents: (id: string) => request<AgentItem[]>("/runtime/app-servers/" + id + "/agents"),
  deployAgent: (id: string, body: { project_id?: string; name: string; template_name?: string; target_node_id?: string; bind_lead?: boolean }) =>
    request<Record<string, unknown>>("/runtime/app-servers/" + id + "/agents", { method: "POST", body }),
  agentOperation: (id: string, op: string) =>
    request<Record<string, unknown>>("/runtime/app-servers/" + id + "/agent-operations/" + op),

  listIdentities: (platform = "", projectId = "") =>
    request<UserIdentity[]>("/identities?platform=" + encodeURIComponent(platform) + (projectId ? "&project_id=" + encodeURIComponent(projectId) : "")),
  upsertIdentity: (body: { platform?: string; platform_user_id: string; project_id?: string | null; github_login: string; display_name?: string; enabled?: boolean }) =>
    request<UserIdentity>("/identities", { method: "POST", body }),
  deleteIdentity: (id: number) => request<void>("/identities/" + id, { method: "DELETE" }),

  listBridgeLinks: (limit = 100) => request<BridgeLink[]>("/bridge/links?limit=" + limit),
  listWebhookDeliveries: (limit = 50) => request<WebhookDelivery[]>("/bridge/deliveries?limit=" + limit),
  listDispatches: (limit = 50) => request<DispatchRecord[]>("/bridge/dispatches?limit=" + limit),
  listBindings: () => request<ProjectBinding[]>("/bridge/bindings"),
  listPlans: (limit = 50) => request<IssuePlan[]>("/bridge/plans?limit=" + limit),
  upsertBinding: (projectId: string, body: { app_server_id: string; lead_agent_id: string; lead_agent_name?: string; app_server_addr?: string; agent_project_id?: string }) =>
    request<ProjectBinding>("/bridge/bindings/" + encodeURIComponent(projectId), { method: "PUT", body }),
  deleteBinding: (projectId: string) =>
    request<void>("/bridge/bindings/" + encodeURIComponent(projectId), { method: "DELETE" }),
  redispatch: (linkId: number) =>
    request<BridgeLink>("/bridge/links/" + linkId + "/redispatch", { method: "POST" }),

  listVerifications: (limit = 50) => request<VerificationRecord[]>("/verifications?limit=" + limit),
  listAttempts: (limit = 50) => request<IssueAttempt[]>("/attempts?limit=" + limit),
  listDerivations: (limit = 50) => request<Derivation[]>("/derivations?limit=" + limit),
  listReleases: (limit = 50) => request<ReleaseRecord[]>("/releases?limit=" + limit),
  createRelease: (body: { project_id?: string; repo?: string; version: string; commit: string; artifact_globs?: string[]; notes?: string }) =>
    request<{ record: ReleaseRecord; report: Record<string, unknown> }>("/releases", { method: "POST", body }),
  listReleaseRuns: (limit = 50) => request<ReleaseRun[]>("/releases/runs?limit=" + limit),
  listAgentTokens: (projectId?: string) =>
    request<AgentToken[]>("/agent-tokens" + (projectId ? "?project_id=" + encodeURIComponent(projectId) : "")),
  createAgentToken: (body: { project_id?: string; role: string; name: string; expires_in_hours?: number }) =>
    request<{ token: string; record: AgentToken }>("/agent-tokens", { method: "POST", body }),
  revokeAgentToken: (id: string) => request<{ id: string; enabled: boolean }>("/agent-tokens/" + id + "/revoke", { method: "POST" }),
  listToolCalls: (tool = "", limit = 100) =>
    request<ToolCall[]>("/tool-calls?limit=" + limit + (tool ? "&tool=" + encodeURIComponent(tool) : "")),
  listRepoOps: (limit = 100) => request<RepoOp[]>("/repo-ops?limit=" + limit),
  getMCPConfig: () => request<MCPConfig>("/runtime/mcp-config"),
  updateMCPConfig: (body: { url: string; pool_name: string }) =>
    request<MCPConfig>("/runtime/mcp-config", { method: "PUT", body }),

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
