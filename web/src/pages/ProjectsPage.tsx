import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { GitHubRepo, LanxinBot, LanxinGroup, ModelProvider, Project } from "../types";
import type { Notify } from "../App";

type FormState = {
  name: string;
  description: string;
  github_repo_url: string;
  local_repo_path: string;
  github_default_branch: string;
  github_issue_repo: string;
  bluex_group_ids: string;
  lanxin_bot_id: string;
  verify_profile_path: string;
  codex_model_provider: string;
  codex_model: string;
  codex_reasoning_effort: string;
  codeowners_required: boolean;
};

const EMPTY: FormState = {
  name: "",
  description: "",
  github_repo_url: "",
  local_repo_path: "",
  github_default_branch: "main",
  github_issue_repo: "",
  bluex_group_ids: "",
  lanxin_bot_id: "",
  verify_profile_path: "smartpipe.yaml",
  codex_model_provider: "",
  codex_model: "",
  codex_reasoning_effort: "",
  codeowners_required: false,
};

export function ProjectsPage({ notify }: { notify: Notify }) {
  const [items, setItems] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<Project | null>(null);
  const [form, setForm] = useState<FormState>(EMPTY);
  const [saving, setSaving] = useState(false);
  const [providers, setProviders] = useState<ModelProvider[]>([]);
  const [lanxinBots, setLanxinBots] = useState<LanxinBot[]>([]);
  const [lanxinGroups, setLanxinGroups] = useState<LanxinGroup[]>([]);
  const [groupsLoading, setGroupsLoading] = useState(false);
  const [repoCatalog, setRepoCatalog] = useState<GitHubRepo[]>([]);
  const [catalogSyncedAt, setCatalogSyncedAt] = useState("");
  const [repoSyncing, setRepoSyncing] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setItems(await api.listProjects());
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载项目失败");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  useEffect(() => {
    void api.listModelProviders().then(setProviders).catch(() => setProviders([]));
  }, []);

  const loadLanxinOptions = useCallback(async (refresh = false) => {
    setGroupsLoading(true);
    try {
      const bots = await api.listLanxinBots(refresh);
      const groups = await api.listLanxinGroups(false);
      setLanxinBots(bots.filter((b) => b.enabled));
      setLanxinGroups(groups.filter((g) => g.active));
    } catch {
      setLanxinBots([]);
      setLanxinGroups([]);
    } finally {
      setGroupsLoading(false);
    }
  }, []);

  useEffect(() => { void loadLanxinOptions(true); }, [loadLanxinOptions]);

  const loadRepoCatalog = useCallback(async (sync = false) => {
    if (sync) setRepoSyncing(true);
    try {
      const res = sync ? await api.syncGitHubRepoCatalog() : await api.listGitHubRepoCatalog();
      setRepoCatalog(res.repos ?? []);
      setCatalogSyncedAt(res.synced_at ?? "");
    } catch (err) {
      if (sync) notify(err instanceof ApiError ? err.message : "同步仓库库失败", "error");
    } finally {
      if (sync) setRepoSyncing(false);
    }
  }, [notify]);

  useEffect(() => { void loadRepoCatalog(); }, [loadRepoCatalog]);

  const startCreate = () => {
    setEditing(null);
    setForm(EMPTY);
    setOpen(true);
    void loadLanxinOptions(false);
    void loadRepoCatalog();
  };

  const startEdit = (p: Project) => {
    setEditing(p);
    setForm({
      name: p.name,
      description: p.description,
      github_repo_url: p.github_repo_url,
      local_repo_path: p.local_repo_path || "",
      github_default_branch: p.github_default_branch,
      github_issue_repo: p.github_issue_repo,
      bluex_group_ids: p.bluex_group_ids,
      lanxin_bot_id: p.lanxin_bot_id || lanxinGroups.find((g) => g.group_id === p.bluex_group_ids.split(",")[0])?.bot_id || "",
      verify_profile_path: p.verify_profile_path,
      codex_model_provider: p.codex_model_provider,
      codex_model: p.codex_model,
      codex_reasoning_effort: p.codex_reasoning_effort,
      codeowners_required: p.codeowners_required,
    });
    setOpen(true);
    void loadLanxinOptions(false);
    void loadRepoCatalog();
  };

  const submit = async () => {
    if (form.name.trim() === "") {
      notify("项目名称不能为空", "error");
      return;
    }
    setSaving(true);
    try {
      if (editing) {
        await api.updateProject(editing.id, form);
        notify("项目已更新");
      } else {
        await api.createProject(form);
        notify("项目已创建");
      }
      setOpen(false);
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "保存失败", "error");
    } finally {
      setSaving(false);
    }
  };

  const toggle = async (p: Project) => {
    try {
      await api.setProjectEnabled(p.id, !p.enabled);
      notify(p.enabled ? "项目已停用" : "项目已启用");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "操作失败", "error");
    }
  };

  const filtered = items.filter((p) => {
    const q = query.trim().toLowerCase();
    if (!q) return true;
    return p.name.toLowerCase().includes(q) || p.github_repo_url.toLowerCase().includes(q);
  });

  const modelLabel = (providerId: string, model: string) => {
    if (!providerId) return "未绑定（默认模型）";
    const p = providers.find((m) => m.id === providerId);
    return p ? (model ? p.name + " · " + model : p.name) : providerId;
  };

  const boundProvider = providers.find((p) => p.id === form.codex_model_provider) ?? null;
  const selectedGroup = form.bluex_group_ids.split(",")[0]?.trim() || "";
  const availableGroups = lanxinGroups.filter((g) =>
    g.bot_id === form.lanxin_bot_id && (!g.project_id || g.project_id === editing?.id)
  );
  const groupLabel = (id: string) => {
    const g = lanxinGroups.find((item) => item.group_id === id);
    return g ? (g.name || g.group_id) : id;
  };

  const set = <K extends keyof FormState>(key: K, value: FormState[K]) =>
    setForm((prev) => ({ ...prev, [key]: value }));

  const clonedRepos = repoCatalog.filter((repo) => (repo.local_path || "").trim() !== "");

  const selectedRepoInfo = repoCatalog.find((repo) => {
    const url = form.github_repo_url.trim().toLowerCase();
    if (!url) return false;
    return (
      repo.html_url.toLowerCase() === url ||
      repo.full_name.toLowerCase() === url ||
      url.endsWith("/" + repo.full_name.toLowerCase())
    );
  }) ?? null;

  const pickRepo = (fullName: string) => {
    const repo = repoCatalog.find((item) => item.full_name === fullName);
    if (!repo) return;
    setForm((prev) => ({
      ...prev,
      github_repo_url: repo.html_url,
      local_repo_path: repo.local_path || "",
      github_default_branch: repo.default_branch || prev.github_default_branch,
      github_issue_repo: repo.full_name,
    }));
  };

  const catalogSyncedLabel = catalogSyncedAt
    ? new Date(catalogSyncedAt).toLocaleString("zh-CN", { hour12: false })
    : "";

  return (
    <>
      <div className="notice">
        <span>i</span>
        <div>
          <strong>项目是配置的最小作用域。</strong>
          仓库地址、审批人、蓝信群和 Codex 模型都由项目解析；停用项目后新消息会被拒绝。
        </div>
      </div>

      <div className="stat-grid">
        <div className="stat">
          <div className="stat-label">项目总数</div>
          <div className="stat-value">{items.length}</div>
          <div className="stat-foot">{items.filter((p) => p.enabled).length} 个启用中</div>
        </div>
        <div className="stat">
          <div className="stat-label">已绑定蓝信群</div>
          <div className="stat-value">{items.filter((p) => p.bluex_group_ids.trim() !== "").length}</div>
          <div className="stat-foot">未绑定的项目无法从蓝信路由</div>
        </div>
        <div className="stat">
          <div className="stat-label">GitHub 仓库</div>
          <div className="stat-value">{items.filter((p) => p.github_repo_url.trim() !== "").length}</div>
          <div className="stat-foot">需在凭据页配置访问方式</div>
        </div>
        <div className="stat">
          <div className="stat-label">已绑定 Codex 模型</div>
          <div className="stat-value">{items.filter((p) => p.codex_model_provider).length}</div>
          <div className="stat-foot">未绑定的项目使用默认模型</div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">项目列表</div>
            <div className="card-sub">GitHub 地址、默认分支与蓝信群绑定</div>
          </div>
          <div style={{ display: "flex", gap: 8 }}>
            <input
              className="field"
              style={{ border: "1px solid var(--line)", borderRadius: 8, padding: "7px 10px", fontSize: 12.5 }}
              placeholder="搜索项目或仓库"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            <button className="btn primary" onClick={startCreate}>＋ 新增项目</button>
          </div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {loading ? (
            <div className="empty">加载中…</div>
          ) : error ? (
            <div className="empty">
              <h3>无法读取项目</h3>
              <div className="tiny">{error}</div>
              <button className="btn" style={{ marginTop: 12 }} onClick={() => void load()}>重试</button>
            </div>
          ) : filtered.length === 0 ? (
            <div className="empty">
              <h3>还没有项目</h3>
              <div className="tiny">先创建一个项目，填入仓库地址和蓝信群，后续 Issue 才能路由到它。</div>
              <button className="btn primary" style={{ marginTop: 14 }} onClick={startCreate}>＋ 新增项目</button>
            </div>
          ) : (
            <table className="table">
              <thead>
                <tr>
                  <th>项目</th>
                  <th>GitHub 仓库</th>
                  <th>默认分支</th>
                  <th>Codex 模型</th>
                  <th>蓝信群</th>
                  <th>状态</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((p) => (
                  <tr key={p.id}>
                    <td>
                      <div className="strong">{p.name}</div>
                      <div className="tiny mono">{p.id}</div>
                    </td>
                    <td>
                      <div className="mono">{p.github_repo_url || "—"}</div>
                      <div className="tiny">
                        {p.github_provider} · 契约 {p.verify_profile_path}
                        {p.local_repo_path ? " · 本地库 " + p.local_repo_path : ""}
                      </div>
                    </td>
                    <td className="mono">{p.github_default_branch}</td>
                    <td>{modelLabel(p.codex_model_provider, p.codex_model)}</td>
                    <td>
                      {p.bluex_group_ids ? p.bluex_group_ids.split(",").map((g) => <span key={g} className="chip" style={{ marginRight: 4 }}>{groupLabel(g.trim())}</span>) : <span className="muted">未绑定</span>}
                    </td>
                    <td>
                      <span className={"chip " + (p.enabled ? "ok" : "bad")}>
                        <span className="dot" />{p.enabled ? "启用" : "停用"}
                      </span>
                    </td>
                    <td>
                      <div className="row-actions">
                        <button className="btn small" onClick={() => startEdit(p)}>编辑</button>
                        <button className="btn small" onClick={() => void toggle(p)}>{p.enabled ? "停用" : "启用"}</button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className={"modal-backdrop" + (open ? " open" : "")}>
        <div className="modal">
          <div className="modal-head">
            <div>
              <div className="modal-title">{editing ? "编辑项目" : "新增项目"}</div>
              <div className="modal-sub">仓库地址与分支用于工作区检出；蓝信群用于入站路由。</div>
            </div>
            <button className="icon-btn" onClick={() => setOpen(false)}>✕</button>
          </div>
          <div className="modal-body">
            <div className="form-grid">
              <div className="field">
                <label>项目名称</label>
                <input value={form.name} onChange={(e) => set("name", e.target.value)} placeholder="SmartPipe Core" />
              </div>
              <div className="field">
                <label>默认分支</label>
                <input value={form.github_default_branch} onChange={(e) => set("github_default_branch", e.target.value)} />
              </div>
              <div className="field full">
                <label>GitHub 仓库</label>
                <div className="repo-picker">
                  <select value={selectedRepoInfo?.full_name ?? ""} onChange={(e) => pickRepo(e.target.value)}>
                    <option value="">
                      {clonedRepos.length > 0 ? `选择已克隆的本地库（${clonedRepos.length} 个）` : "还没有已克隆的仓库"}
                    </option>
                    {clonedRepos.map((repo) => (
                      <option key={repo.full_name} value={repo.full_name}>
                        {repo.full_name}{repo.private ? " · 私有" : ""}{repo.fork ? " · Fork" : ""}
                      </option>
                    ))}
                  </select>
                  <button className="btn small" disabled={repoSyncing} onClick={() => void loadRepoCatalog(true)}>
                    {repoSyncing ? "同步中…" : "同步仓库库"}
                  </button>
                  {catalogSyncedLabel && <span className="tiny">更新于 {catalogSyncedLabel}</span>}
                </div>
                <input value={form.github_repo_url} onChange={(e) => set("github_repo_url", e.target.value)} placeholder="https://github.com/org/repo" />
                <input value={form.local_repo_path} onChange={(e) => set("local_repo_path", e.target.value)} placeholder="本地仓库目录，例如 /data/repos/org/repo" spellCheck={false} />
                {clonedRepos.length === 0 && (
                  <div className="tiny">还没有克隆任何仓库：到「GitHub 配置」页点仓库行的 Clone，克隆完成后这里就能选本地库。</div>
                )}
                {selectedRepoInfo && (
                  <div className="tiny" style={{ display: "flex", gap: 6, flexWrap: "wrap", alignItems: "center" }}>
                    <span className={"chip " + (selectedRepoInfo.private ? "warn" : "")}>{selectedRepoInfo.private ? "私有" : "公开"}</span>
                    {selectedRepoInfo.fork && <span className="chip">Fork</span>}
                    <span>默认分支 {selectedRepoInfo.default_branch || "—"}</span>
                    {selectedRepoInfo.language && <span>· {selectedRepoInfo.language}</span>}
                    <span>· 最近更新 {selectedRepoInfo.updated_at ? new Date(selectedRepoInfo.updated_at).toLocaleDateString("zh-CN") : "—"}</span>
                  </div>
                )}
              </div>
              <div className="field">
                <label>Issue 仓库</label>
                <input value={form.github_issue_repo} onChange={(e) => set("github_issue_repo", e.target.value)} placeholder="org/repo" />
              </div>
              <div className="field">
                <label>蓝信机器人</label>
                <select
                  value={form.lanxin_bot_id}
                  onChange={(e) => setForm((prev) => ({ ...prev, lanxin_bot_id: e.target.value, bluex_group_ids: "" }))}
                >
                  <option value="">不绑定蓝信群</option>
                  {lanxinBots.map((b) => <option key={b.id} value={b.id}>{b.name}（{b.group_count} 个群）</option>)}
                </select>
                {groupsLoading && <div className="tiny">正在同步机器人所在群…</div>}
              </div>
              <div className="field">
                <label>蓝信群</label>
                <select
                  value={selectedGroup}
                  disabled={!form.lanxin_bot_id}
                  onChange={(e) => {
                    const group = lanxinGroups.find((g) => g.group_id === e.target.value);
                    setForm((prev) => ({ ...prev, bluex_group_ids: e.target.value, lanxin_bot_id: group?.bot_id || prev.lanxin_bot_id }));
                  }}
                >
                  <option value="">{form.lanxin_bot_id ? "请选择群" : "请先选择机器人"}</option>
                  {availableGroups.map((g) => (
                    <option key={g.id} value={g.group_id}>{g.name || g.group_id}（{g.member_count} 人）</option>
                  ))}
                </select>
                <div className="tiny">群列表由机器人自动同步；已被其他项目绑定的群不会出现。</div>
              </div>
              <div className="field">
                <label>验证契约路径</label>
                <input value={form.verify_profile_path} onChange={(e) => set("verify_profile_path", e.target.value)} />
              </div>
              <div className="field">
                <label>Codex 供应商</label>
                <select
                  value={form.codex_model_provider}
                  onChange={(e) => setForm((prev) => ({ ...prev, codex_model_provider: e.target.value, codex_model: "" }))}
                >
                  <option value="">未绑定（用默认模型）</option>
                  {providers.map((p) => (
                    <option key={p.id} value={p.id}>{p.name}</option>
                  ))}
                </select>
              </div>
              <div className="field">
                <label>Codex 模型</label>
                <select
                  value={form.codex_model}
                  disabled={!boundProvider}
                  onChange={(e) => set("codex_model", e.target.value)}
                >
                  <option value="">未选择</option>
                  {(boundProvider?.models ?? []).filter((m) => m.enabled).map((m) => (
                    <option key={m.model_id} value={m.model_id}>{m.model_id}</option>
                  ))}
                </select>
              </div>
              <div className="field">
                <label>推理强度</label>
                <select value={form.codex_reasoning_effort} onChange={(e) => set("codex_reasoning_effort", e.target.value)}>
                  <option value="">跟随模型默认</option>
                  <option value="minimal">minimal</option>
                  <option value="low">low</option>
                  <option value="medium">medium</option>
                  <option value="high">high</option>
                  <option value="xhigh">xhigh</option>
                </select>
              </div>
              <div className="field full">
                <label>描述</label>
                <input value={form.description} onChange={(e) => set("description", e.target.value)} placeholder="可选" />
              </div>
              <div className="field">
                <label>审批要求</label>
                <select
                  value={form.codeowners_required ? "yes" : "no"}
                  onChange={(e) => set("codeowners_required", e.target.value === "yes")}
                >
                  <option value="no">维护者批准即可</option>
                  <option value="yes">必须满足 CODEOWNERS</option>
                </select>
              </div>
            </div>
          </div>
          <div className="modal-foot">
            <div className="tiny">保存会写入审计日志，不涉及凭据明文。</div>
            <div>
              <button className="btn" onClick={() => setOpen(false)}>取消</button>{" "}
              <button className="btn primary" disabled={saving} onClick={() => void submit()}>
                {saving ? "保存中…" : "保存"}
              </button>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}
