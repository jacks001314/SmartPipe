import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { Project } from "../types";
import type { Notify } from "../App";

type FormState = {
  name: string;
  description: string;
  github_repo_url: string;
  github_default_branch: string;
  github_issue_repo: string;
  bluex_group_ids: string;
  verify_profile_path: string;
  lead_agent_template: string;
  tool_profile: string;
  codeowners_required: boolean;
};

const EMPTY: FormState = {
  name: "",
  description: "",
  github_repo_url: "",
  github_default_branch: "main",
  github_issue_repo: "",
  bluex_group_ids: "",
  verify_profile_path: "smartpipe.yaml",
  lead_agent_template: "pipeline-lead-v3",
  tool_profile: "lead",
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

  const startCreate = () => {
    setEditing(null);
    setForm(EMPTY);
    setOpen(true);
  };

  const startEdit = (p: Project) => {
    setEditing(p);
    setForm({
      name: p.name,
      description: p.description,
      github_repo_url: p.github_repo_url,
      github_default_branch: p.github_default_branch,
      github_issue_repo: p.github_issue_repo,
      bluex_group_ids: p.bluex_group_ids,
      verify_profile_path: p.verify_profile_path,
      lead_agent_template: p.lead_agent_template,
      tool_profile: p.tool_profile,
      codeowners_required: p.codeowners_required,
    });
    setOpen(true);
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

  const set = <K extends keyof FormState>(key: K, value: FormState[K]) =>
    setForm((prev) => ({ ...prev, [key]: value }));

  return (
    <>
      <div className="notice">
        <span>i</span>
        <div>
          <strong>项目是配置的最小作用域。</strong>
          Agent 只接收 <span className="mono">project_id</span>，由服务端解析仓库地址、审批人和凭据；停用项目后新消息会被拒绝。
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
          <div className="stat-label">默认模板</div>
          <div className="stat-value" style={{ fontSize: 15 }}>{items[0]?.lead_agent_template ?? "pipeline-lead-v3"}</div>
          <div className="stat-foot">每个 Issue 一条 lead</div>
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
                      <div className="tiny">{p.github_provider} · 契约 {p.verify_profile_path}</div>
                    </td>
                    <td className="mono">{p.github_default_branch}</td>
                    <td>
                      {p.bluex_group_ids ? p.bluex_group_ids.split(",").map((g) => <span key={g} className="chip" style={{ marginRight: 4 }}>{g.trim()}</span>) : <span className="muted">未绑定</span>}
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
                <label>GitHub 仓库地址</label>
                <input value={form.github_repo_url} onChange={(e) => set("github_repo_url", e.target.value)} placeholder="https://github.com/org/repo" />
              </div>
              <div className="field">
                <label>Issue 仓库</label>
                <input value={form.github_issue_repo} onChange={(e) => set("github_issue_repo", e.target.value)} placeholder="org/repo" />
              </div>
              <div className="field">
                <label>蓝信群 ID（逗号分隔）</label>
                <input value={form.bluex_group_ids} onChange={(e) => set("bluex_group_ids", e.target.value)} placeholder="grp-1,grp-2" />
              </div>
              <div className="field">
                <label>验证契约路径</label>
                <input value={form.verify_profile_path} onChange={(e) => set("verify_profile_path", e.target.value)} />
              </div>
              <div className="field">
                <label>Lead 模板</label>
                <input value={form.lead_agent_template} onChange={(e) => set("lead_agent_template", e.target.value)} />
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
              <div className="field">
                <label>工具权限集</label>
                <select value={form.tool_profile} onChange={(e) => set("tool_profile", e.target.value)}>
                  <option value="lead">lead（可对外通信）</option>
                  <option value="lead-readonly">lead（只读仓库）</option>
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
