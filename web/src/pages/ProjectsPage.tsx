import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { GitHubRepo, LanxinBot, LanxinGroup, ModelProfile, ModelProvider, Project } from "../types";
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
  const [repoSearch, setRepoSearch] = useState("");
  const [modelProfiles, setModelProfiles] = useState<ModelProfile[]>([]);
  const [modelProfileId, setModelProfileId] = useState("");
  const [nameTouched, setNameTouched] = useState(false);

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
    void api.listModelProfiles().then(setModelProfiles).catch(() => setModelProfiles([]));
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
    setNameTouched(false);
    setModelProfileId("");
    setRepoSearch("");
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
    setNameTouched(true);
    const matched = modelProfiles.find(
      (profile) => profile.provider_id === p.codex_model_provider && profile.model === p.codex_model,
    );
    setModelProfileId(matched?.id ?? "");
    setRepoSearch("");
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

  const removeProject = async (p: Project) => {
    if (!window.confirm(`删除项目「${p.name}」？会同时清除它的凭据、蓝信群绑定和运行记录，无法恢复。`)) return;
    try {
      await api.deleteProject(p.id);
      notify("项目已删除");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "删除项目失败", "error");
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

  const groupLabel = (id: string) => {
    const g = lanxinGroups.find((item) => item.group_id === id);
    return g ? (g.name || g.group_id) : id;
  };

  const set = <K extends keyof FormState>(key: K, value: FormState[K]) =>
    setForm((prev) => ({ ...prev, [key]: value }));

  const clonedRepos = repoCatalog.filter((repo) => (repo.local_path || "").trim() !== "");

  const filteredCloned = clonedRepos.filter((repo) => {
    const q = repoSearch.trim().toLowerCase();
    if (!q) return true;
    return repo.full_name.toLowerCase().includes(q) || (repo.local_path || "").toLowerCase().includes(q);
  });
  const selectedGroupName = lanxinGroups.find((g) => g.group_id === form.bluex_group_ids)?.name || "";
  const selectedProfile = modelProfiles.find((p) => p.id === modelProfileId) ?? null;
  const providerName = providers.find((p) => p.id === form.codex_model_provider)?.name || form.codex_model_provider;
  const legacyModelLabel =
    !modelProfileId && form.codex_model_provider
      ? `${providerName}${form.codex_model ? " · " + form.codex_model : ""}`
      : "";


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
      name: !nameTouched || prev.name.trim() === "" ? repo.name : prev.name,
      github_repo_url: repo.html_url,
      local_repo_path: repo.local_path || "",
      github_default_branch: repo.default_branch || prev.github_default_branch,
      github_issue_repo: repo.full_name,
    }));
  };

  const pickModelProfile = (profileId: string) => {
    setModelProfileId(profileId);
    const profile = modelProfiles.find((item) => item.id === profileId);
    if (!profile) {
      setForm((prev) => ({ ...prev, codex_model_provider: "", codex_model: "", codex_reasoning_effort: "" }));
      return;
    }
    setForm((prev) => ({
      ...prev,
      codex_model_provider: profile.provider_id,
      codex_model: profile.model,
      codex_reasoning_effort: profile.reasoning_effort,
    }));
  };

  const pickGroup = (groupId: string) => {
    const group = lanxinGroups.find((item) => item.group_id === groupId);
    setForm((prev) => ({ ...prev, bluex_group_ids: groupId, lanxin_bot_id: group?.bot_id || prev.lanxin_bot_id }));
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
                        <button className="btn small danger" onClick={() => void removeProject(p)}>删除</button>
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
        <div className="modal" style={{ maxWidth: 820 }}>
          <div className="modal-head">
            <div>
              <div className="modal-title">{editing ? "编辑项目" : "新增项目"}</div>
              <div className="modal-sub">选一个已克隆的仓库，再绑定蓝信群和模型就能创建。</div>
            </div>
            <button className="icon-btn" onClick={() => setOpen(false)}>✕</button>
          </div>
          <div className="modal-body">
            <div className="field full">
              <label>
                代码仓库
                <span className="proj-req">*</span>
                <span className="tiny" style={{ marginLeft: 8 }}>只列出已 clone 到本地的仓库（{clonedRepos.length}）</span>
              </label>
              <div className="proj-repo-tools">
                <input
                  value={repoSearch}
                  onChange={(e) => setRepoSearch(e.target.value)}
                  placeholder="搜索仓库名或路径，例如 syntropy"
                />
                <button className="btn small" disabled={repoSyncing} onClick={() => void loadRepoCatalog(true)}>
                  {repoSyncing ? "同步中…" : "同步仓库库"}
                </button>
                {catalogSyncedLabel && <span className="tiny">更新于 {catalogSyncedLabel}</span>}
              </div>
              <div className="proj-repo-list">
                {filteredCloned.map((repo) => (
                  <div
                    key={repo.full_name}
                    className={"proj-repo" + (form.local_repo_path === repo.local_path ? " active" : "")}
                    onClick={() => pickRepo(repo.full_name)}
                  >
                    <span className="proj-radio" />
                    <div className="proj-repo-main">
                      <div className="proj-repo-name">{repo.full_name}</div>
                      <div className="proj-repo-meta">
                        <span className={"chip " + (repo.private ? "warn" : "")}>{repo.private ? "私有" : "公开"}</span>
                        {repo.fork && <span className="chip">Fork</span>}
                        {repo.language && <span className="chip">{repo.language}</span>}
                        <span className="chip">分支 {repo.default_branch || "main"}</span>
                        <span className="chip ok">已克隆</span>
                      </div>
                      <div className="proj-repo-path">{repo.local_path}</div>
                    </div>
                  </div>
                ))}
                {filteredCloned.length === 0 && (
                  <div className="tiny" style={{ padding: "14px 4px" }}>
                    {clonedRepos.length === 0
                      ? "还没有已克隆的仓库：到「GitHub 配置」页点仓库行的 Clone，克隆完成后这里就能选。"
                      : "没有匹配的仓库，换个关键词试试。"}
                  </div>
                )}
              </div>
            </div>

            <div className="form-grid">
              <div className="field">
                <label>项目名称</label>
                <input
                  value={form.name}
                  onChange={(e) => {
                    setNameTouched(e.target.value.trim() !== "");
                    set("name", e.target.value);
                  }}
                  placeholder="选择仓库后自动填写"
                />
              </div>
              <div className="field">
                <label>蓝信群</label>
                <select value={form.bluex_group_ids} onChange={(e) => pickGroup(e.target.value)}>
                  <option value="">暂不绑定（可稍后配置）</option>
                  {lanxinBots.map((bot) => {
                    const groups = lanxinGroups.filter((g) => g.bot_id === bot.id && g.active);
                    if (groups.length === 0) return null;
                    return (
                      <optgroup key={bot.id} label={"蓝信机器人 · " + bot.name}>
                        {groups.map((g) => {
                          const occupied = Boolean(g.project_id) && g.project_id !== editing?.id;
                          return (
                            <option key={g.id} value={g.group_id} disabled={occupied}>
                              {(g.name || g.group_id) + "（" + g.member_count + " 人）" + (occupied ? " — 已被其他项目绑定" : "")}
                            </option>
                          );
                        })}
                      </optgroup>
                    );
                  })}
                </select>
                <div className="tiny">按群名选择；已被其他项目占用的群会置灰。</div>
              </div>
              <div className="field">
                <label>模型</label>
                <select
                  value={modelProfileId}
                  onChange={(e) => {
                    if (e.target.value === "__current") return;
                    pickModelProfile(e.target.value);
                  }}
                >
                  <option value="">跟随系统默认</option>
                  {legacyModelLabel && <option value="__current">当前：{legacyModelLabel}</option>}
                  {modelProfiles.filter((p) => p.enabled).map((profile) => (
                    <option key={profile.id} value={profile.id}>
                      {profile.name + " · " + profile.model + (profile.reasoning_effort ? " · 推理 " + profile.reasoning_effort : "")}
                    </option>
                  ))}
                </select>
                <div className="tiny">只保留一个模型入口，不再分别选供应商、模型名和推理强度。</div>
              </div>
              <div className="field">
                <label>描述</label>
                <input value={form.description} onChange={(e) => set("description", e.target.value)} placeholder="可选" />
              </div>
            </div>

            <div className={"proj-summary" + (selectedRepoInfo || selectedGroupName || selectedProfile ? " show" : "")}>
              将创建项目 <b>{form.name || selectedRepoInfo?.name || "—"}</b>
              {selectedRepoInfo ? (
                <>
                  ：仓库 <b>{selectedRepoInfo.full_name}</b>（{selectedRepoInfo.default_branch || "main"}）
                </>
              ) : null}
              ，蓝信群 <b>{selectedGroupName || "未绑定"}</b>，模型 <b>{selectedProfile ? selectedProfile.name : legacyModelLabel || "系统默认"}</b>。
            </div>

            <details className="proj-adv">
              <summary>高级设置</summary>
              <div className="form-grid" style={{ marginTop: 12 }}>
                <div className="field">
                  <label>验证契约路径</label>
                  <input value={form.verify_profile_path} onChange={(e) => set("verify_profile_path", e.target.value)} />
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
            </details>
          </div>
          <div className="modal-foot">
            <div className="tiny">仓库路径、分支和 Issue 仓库都从本地克隆自动带出；保存会写入审计日志。</div>
            <div>
              <button className="btn" onClick={() => setOpen(false)}>取消</button>{" "}
              <button className="btn primary" disabled={saving || !form.local_repo_path} onClick={() => void submit()}>
                {saving ? "保存中…" : editing ? "保存" : "创建项目"}
              </button>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}
