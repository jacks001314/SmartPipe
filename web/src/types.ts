export type Project = {
  id: string;
  name: string;
  description: string;
  github_provider: string;
  github_repo_url: string;
  github_default_branch: string;
  github_issue_repo: string;
  labels_prefix: string;
  codeowners_required: boolean;
  approver_teams: string;
  verify_profile_path: string;
  release_profile: string;
  bluex_group_ids: string;
  lanxin_bot_id: string;
  lead_agent_template: string;
  codex_model_provider: string;
  codex_model: string;
  codex_reasoning_effort: string;
  tool_profile: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
};

export type GitHubAppSetup = {
  setup_id: string;
  launch_url: string;
  expires_in: number;
  poll_interval: number;
};

export type GitHubAppSetupStatus = {
  status: "pending" | "created" | "installed" | "expired" | "error";
  app_slug?: string;
  owner?: string;
  installation_id?: string;
  credential_id?: string;
  message?: string;
};

export type GitHubRepo = {
  id: number;
  full_name: string;
  name: string;
  owner: string;
  private: boolean;
  description: string;
  default_branch: string;
  language: string;
  stars: number;
  open_issues: number;
  updated_at: string;
  html_url: string;
  archived: boolean;
  fork: boolean;
};

export type GitHubRepoList = {
  connected: boolean;
  account: string;
  app_slug: string;
  source: string;
  repos: GitHubRepo[];
  error?: string;
  fetched_at: string;
};

export type GitHubOAuthView = {
  configured: boolean;
  client_id: string;
  scope: string;
  authorized: boolean;
  login: string;
  credential_id: string;
};

export type GitHubDeviceFlow = {
  flow_id: string;
  user_code: string;
  verification_uri: string;
  expires_in: number;
  interval: number;
};

export type GitHubOAuthPoll = {
  status: "pending" | "authorized" | "expired" | "denied" | "error";
  login?: string;
  message?: string;
  credential_id?: string;
};

export type Credential = {
  id: string;
  project_id: string | null;
  kind: string;
  name: string;
  mask: string;
  metadata: Record<string, unknown>;
  enabled: boolean;
  last_tested_at: string | null;
  last_test_ok: boolean | null;
  last_test_message: string;
  created_at: string;
  updated_at: string;
};

export type AuditEntry = {
  id: number;
  at: string;
  actor: string;
  action: string;
  target_type: string;
  target_id: string;
  project_id: string | null;
  result: string;
  detail: Record<string, unknown>;
  trace_id: string;
};

export type AuthConfig = {
  login_enabled: boolean;
  dev_open: boolean;
  token_required: boolean;
};

export type AuthSession = {
  username: string;
  display_name: string;
  mode: string;
  expires_at?: string;
};

export type Health = {
  status: string;
  database: boolean;
  version: string;
  auth: string;
  checked_at: string;
};

export type TestResult = {
  ok: boolean;
  message: string;
  latency_ms: number;
  credential: Credential | null;
};

export const CREDENTIAL_KINDS: { value: string; label: string; hint: string }[] = [
  { value: "github_app", label: "GitHub App 私钥", hint: "需同时填 app_id 与 installation_id" },
  { value: "github_pat", label: "GitHub PAT", hint: "用于快速验证仓库读取权限" },
  { value: "github_webhook_secret", label: "GitHub Webhook Secret", hint: "仅做长度校验，至少 16 位" },
  { value: "lanxin", label: "蓝信 App Secret", hint: "需填 app_id 与 api_base" },
  { value: "git_ssh", label: "Git 只读凭据", hint: "需填 repo_url，用于 ls-remote 探测" },
  { value: "signing_key", label: "发版签名密钥", hint: "仅登记，联网校验在 M4" },
  { value: "model_provider", label: "模型 API Key", hint: "需填 API Base（通常到 /v1 结尾），供模型档案引用" },
];

export type ProviderModel = {
  provider_id: string;
  model_id: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
};

export type ModelProvider = {
  id: string;
  name: string;
  base_url: string;
  wire_api: string;
  credential_id: string;
  enabled: boolean;
  last_tested_at: string | null;
  last_test_ok: boolean | null;
  last_test_message: string;
  models: ProviderModel[];
  created_at: string;
  updated_at: string;
};

export type LanxinBot = {
  id: string;
  name: string;
  credential_id: string;
  app_id: string;
  api_base: string;
  require_at_me: boolean;
  enabled: boolean;
  status: string;
  last_error: string;
  last_connected_at: string | null;
  last_group_sync_at: string | null;
  last_sync_error: string;
  group_count: number;
  message_count: number;
  secret_mask: string;
  has_secret: boolean;
  last_tested_at: string | null;
  last_test_ok: boolean | null;
  last_test_message: string;
  created_at: string;
  updated_at: string;
};

export type LanxinGroup = {
  id: string;
  bot_id: string;
  bot_name: string;
  group_id: string;
  name: string;
  avatar_url: string;
  description: string;
  owner_id: string;
  owner_name: string;
  member_count: number;
  send_msg_status: boolean;
  active: boolean;
  synced_at: string;
  project_id?: string | null;
  project_name?: string;
};

export type LanxinBotDetail = {
  bot: LanxinBot;
  groups: LanxinGroup[];
  messages: LanxinMessage[];
};

export type LanxinTestResult = {
  ok: boolean;
  message: string;
  latency_ms: number;
  bot: LanxinBot;
};

export type ModelDefault = {
  provider_id: string;
  model: string;
  reasoning_effort: string;
};

export type ModelProfile = {
  id: string;
  name: string;
  provider_id: string;
  provider_name: string;
  model: string;
  reasoning_effort: string;
  enabled: boolean;
  source: string;
  created_at: string;
  updated_at: string;
};

export type ModelConfigImportProvider = {
  key: string;
  name: string;
  base_url: string;
  wire_api: string;
  api_key_source: "toml" | "saved" | "env" | "missing";
  has_api_key: boolean;
  has_existing_credential: boolean;
  env_key: string;
  requires_openai_auth: boolean;
  action: "create" | "update";
};

export type ModelConfigImportProfile = {
  key: string;
  model: string;
  provider_key: string;
  provider_name: string;
  reasoning_effort: string;
  api_key_source: "toml" | "saved" | "env" | "missing";
  action: "create" | "update";
  provider_missing: boolean;
};

export type ModelConfigImportPreview = {
  profile_count: number;
  provider_count: number;
  default_profile_key: string;
  profiles: ModelConfigImportProfile[];
  providers: ModelConfigImportProvider[];
  warnings: string[];
};

export type ModelLocalConfigResult = {
  found: boolean;
  source: string;
  candidates: string[];
  error?: string;
  preview?: ModelConfigImportPreview;
};

export type ModelConfigImportResult = {
  source: string;
  imported_profiles: number;
  updated_profiles: number;
  imported_providers: number;
  updated_providers: number;
  profiles: ModelProfile[];
  providers: ModelProvider[];
  default: ModelDefault;
  warnings: string[];
};

export type ProviderTestResult = {
  ok: boolean;
  message: string;
  latency_ms: number;
  model: string;
  provider: ModelProvider | null;
};

export type AppServer = {
  id: string;
  name: string;
  addr: string;
  binary: string;
  pid: number;
  status: string;
  health: string;
  version: string;
  started_at: string;
  exit_code: number | null;
  last_error: string;
  log_path: string;
  data_dir: string;
};

export type AgentItem = {
  id: string;
  name?: string;
  displayName?: string;
  projectId?: string;
  phase?: string;
  desiredState?: string;
  templateName?: string;
  nodeId?: string;
  [key: string]: unknown;
};

export type BridgeLink = {
  id: number;
  source_message_id: string;
  channel: string;
  chat_id: string;
  user_id: string;
  project_id: string | null;
  repo: string;
  issue_number: number;
  issue_url: string;
  lead_agent_id: string;
  dispatch_status: string;
  dispatch_error: string;
  dispatched_at: string | null;
  receipt_status: string;
  receipt_error: string;
  created_at: string;
};

export type LanxinMessage = {
  id: number;
  platform_message_id: string;
  bot_id: string;
  bot_name: string;
  chat_type: string;
  chat_id: string;
  user_id: string;
  sender_name: string;
  group_name: string;
  msg_type: string;
  text: string;
  is_at_me: boolean;
  project_id: string | null;
  status: string;
  reason: string;
  issue_number: number;
  issue_url: string;
  created_at: string;
  updated_at: string;
};

export type ProjectBinding = {
  project_id: string;
  app_server_id: string;
  app_server_addr: string;
  agent_project_id: string;
  lead_agent_id: string;
  lead_agent_name: string;
  updated_at: string;
};

export type DispatchRecord = {
  id: number;
  link_id: number;
  agent_id: string;
  status: string;
  message: string;
  at: string;
};

export type WebhookDelivery = {
  delivery_id: string;
  event: string;
  repo: string;
  received_at: string;
};

export type IssuePlan = {
  id: number;
  repo: string;
  issue_number: number;
  version: number;
  body: string;
  content_hash: string;
  author: string;
  created_at: string;
  approved: boolean;
  approver: string;
  approved_at: string | null;
};

export type VerifyCheck = {
  name: string;
  command: string;
  ok: boolean;
  exit_code: number;
  duration_ms: number;
  output: string;
  timed_out: boolean;
};

export type VerificationRecord = {
  id: number;
  project_id: string | null;
  issue_ref: string;
  repo: string;
  base_commit: string;
  head_commit: string;
  patch_hash: string;
  ok: boolean;
  files: string[];
  changed_lines: number;
  checks: VerifyCheck[];
  error: string;
  duration_ms: number;
  report: Record<string, unknown>;
  created_at: string;
};

export type IssueAttempt = {
  issue_ref: string;
  project_id: string | null;
  attempts: number;
  state: string;
  last_verification_id: number | null;
  updated_at: string;
};

export type Derivation = {
  id: number;
  project_id: string | null;
  issue_ref: string;
  agent_name: string;
  agent_id: string;
  operation_id: string;
  approval_version: number;
  approval_hash: string;
  status: string;
  created_at: string;
};

export type ReleaseArtifact = {
  path: string;
  size: number;
  sha256: string;
  download_url?: string;
  upload_error?: string;
};

export type ReleaseRecord = {
  id: number;
  project_id: string | null;
  version: string;
  commit_sha: string;
  ok: boolean;
  status: string;
  manifest: Record<string, unknown>;
  manifest_sha256: string;
  signature: string;
  artifacts: ReleaseArtifact[];
  output_dir: string;
  error: string;
  duration_ms: number;
  created_at: string;
};

export type ReleaseCandidate = {
  number: number;
  title: string;
  url: string;
  author: string;
};

export type ReleaseRun = {
  id: number;
  project_id: string | null;
  version: string;
  commit_sha: string;
  branch: string;
  status: string;
  trigger: string;
  candidates: ReleaseCandidate[];
  notes: string;
  release_url: string;
  tag: string;
  release_id: number;
  manifest_sha256: string;
  output_dir: string;
  artifacts: ReleaseArtifact[];
  build_output: string;
  error: string;
  notify_status: string;
  notify_error: string;
  started_at: string;
  finished_at: string | null;
  duration_ms: number;
};

export type CrashReport = {
  id: number;
  project_id: string | null;
  fingerprint: string;
  app: string;
  version: string;
  os: string;
  arch: string;
  user_id: string;
  device_id: string;
  source: string;
  repo: string;
  issue_number: number;
  issue_url: string;
  status: string;
  reject_reason: string;
  occurrences: number;
  size_bytes: number;
  bundle: Record<string, unknown>;
  similar?: CrashSimilar[];
  occurred_at: string | null;
  created_at: string;
  updated_at: string;
};

export type AgentToken = {
  id: string;
  project_id: string | null;
  role: string;
  name: string;
  prefix: string;
  enabled: boolean;
  last_used_at: string | null;
  expires_at: string | null;
  created_at: string;
};

export type ToolCall = {
  id: number;
  project_id: string | null;
  token_id: string;
  agent_name: string;
  role: string;
  tool: string;
  status: string;
  duration_ms: number;
  args_summary: Record<string, unknown>;
  error: string;
  created_at: string;
};

export type MCPConfig = {
  url: string;
  pool_name: string;
};

export type RepoOp = {
  id: number;
  project_id: string | null;
  kind: string;
  issue_ref: string;
  repo: string;
  branch: string;
  target_branch: string;
  commit: string;
  ok: boolean;
  error: string;
  created_at: string;
};

export type UserIdentity = {
  id: number;
  platform: string;
  platform_user_id: string;
  project_id: string | null;
  github_login: string;
  display_name: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
};

export type CrashSimilar = {
  report_id: number;
  issue_number: number;
  issue_url: string;
  fingerprint: string;
  version: string;
  score: number;
};

export type CrashRetention = {
  rows: number;
  bundle_bytes: number;
  bundle_days: number;
  rejected_days: number;
  bundles_cleared?: number;
  rejected_deleted?: number;
  ran_at?: string;
};
