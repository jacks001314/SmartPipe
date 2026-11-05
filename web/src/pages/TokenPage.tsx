import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { AgentToken, Project } from "../types";
import type { Notify } from "../App";

function when(value: string | null | undefined) {
  if (!value) return "—";
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

export function TokenPage({ notify }: { notify: Notify }) {
  const [tokens, setTokens] = useState<AgentToken[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [issued, setIssued] = useState<{ token: string; record: AgentToken } | null>(null);
  const [form, setForm] = useState({ project_id: "", role: "lead", name: "", ttl: "0" });

  const load = useCallback(async () => {
    setLoading(true);
    const [t, p] = await Promise.allSettled([api.listAgentTokens(), api.listProjects()]);
    if (t.status === "fulfilled") {
      setTokens(t.value);
      setError("");
    } else {
      setError(t.reason instanceof ApiError ? t.reason.message : "读取令牌失败");
    }
    if (p.status === "fulfilled") {
      setProjects(p.value);
      setForm((prev) => (prev.project_id ? prev : { ...prev, project_id: p.value[0]?.id ?? "" }));
    }
    setLoading(false);
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const create = async () => {
    if (!form.name.trim()) {
      notify("名称必填", "error");
      return;
    }
    setBusy(true);
    try {
      const res = await api.createAgentToken({
        project_id: form.project_id || undefined,
        role: form.role,
        name: form.name.trim(),
        expires_in_hours: Number(form.ttl) || 0,
      });
      setIssued(res);
      notify("已签发令牌（明文只显示一次）");
      setForm((prev) => ({ ...prev, name: "" }));
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "签发失败", "error");
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (t: AgentToken) => {
    if (!window.confirm("撤销 " + t.name + "（" + t.role + "）？撤销后该 Agent 立即无法调用工具。")) return;
    setBusy(true);
    try {
      await api.revokeAgentToken(t.id);
      notify("已撤销 " + t.name);
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "撤销失败", "error");
    } finally {
      setBusy(false);
    }
  };

  const active = tokens.filter((t) => t.enabled).length;
  const projectName = (id: string | null) => projects.find((p) => p.id === id)?.name ?? (id || "全局");

  return (
    <>
      <div className="notice">
        <span>i</span>
        <div>
          <strong>MCP 令牌按项目与角色签发：</strong>lead 可用 issue / verify / derivation / release 工具，dev/test 只能读项目信息，
          release 可执行发版与回滚。网关用服务端管理员令牌调用管理接口，Agent 永远拿不到 GitHub / 蓝信凭据。
        </div>
      </div>

      <div className="stat-grid">
        <div className="stat"><div className="stat-label">令牌总数</div><div className="stat-value">{tokens.length}</div><div className="stat-foot">最近 200 条</div></div>
        <div className="stat"><div className="stat-label">生效中</div><div className="stat-value">{active}</div><div className="stat-foot">已撤销 {tokens.length - active}</div></div>
        <div className="stat"><div className="stat-label">角色</div><div className="stat-value" style={{ fontSize: 16 }}>lead / dev / test / release</div><div className="stat-foot">工具集按角色裁剪</div></div>
        <div className="stat"><div className="stat-label">MCP 入口</div><div className="stat-value" style={{ fontSize: 16 }}>:8097/mcp</div><div className="stat-foot">Authorization: Bearer spt_…</div></div>
      </div>

      <div className="card">
        <div className="card-head">
          <div><div className="card-title">签发令牌</div><div className="card-sub">明文只在签发后显示一次，服务端只保存哈希</div></div>
          <button className="btn" onClick={() => void load()}>刷新</button>
        </div>
        <div className="card-body">
          <div className="form-grid">
            <div className="field">
              <label>项目</label>
              <select value={form.project_id} onChange={(e) => setForm((p) => ({ ...p, project_id: e.target.value }))}>
                <option value="">不绑定项目</option>
                {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
              </select>
            </div>
            <div className="field">
              <label>角色</label>
              <select value={form.role} onChange={(e) => setForm((p) => ({ ...p, role: e.target.value }))}>
                <option value="lead">lead</option>
                <option value="dev">dev</option>
                <option value="test">test</option>
                <option value="release">release</option>
              </select>
            </div>
            <div className="field">
              <label>名称</label>
              <input value={form.name} onChange={(e) => setForm((p) => ({ ...p, name: e.target.value }))} placeholder="lead-issue-123" />
            </div>
            <div className="field">
              <label>有效期</label>
              <select value={form.ttl} onChange={(e) => setForm((p) => ({ ...p, ttl: e.target.value }))}>
                <option value="0">长期有效</option>
                <option value="24">24 小时</option>
                <option value="168">7 天</option>
                <option value="720">30 天</option>
                <option value="2160">90 天</option>
              </select>
            </div>
          </div>
          <div style={{ display: "flex", justifyContent: "flex-end", marginTop: 12 }}>
            <button className="btn primary" disabled={busy} onClick={() => void create()}>{busy ? "签发中…" : "签发令牌"}</button>
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div><div className="card-title">令牌列表</div><div className="card-sub">只显示前缀；撤销后立即失效</div></div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {loading ? (
            <div className="empty">加载中…</div>
          ) : error ? (
            <div className="empty"><h3>无法读取令牌</h3><div className="tiny">{error}</div></div>
          ) : tokens.length === 0 ? (
            <div className="empty"><h3>还没有令牌</h3><div className="tiny">签发一个 lead 令牌后，用它连接 :8097/mcp。</div></div>
          ) : (
            <table className="table">
              <thead><tr><th>名称</th><th>项目</th><th>角色</th><th>前缀</th><th>最近使用</th><th>到期</th><th>状态</th><th></th></tr></thead>
              <tbody>
                {tokens.map((t) => (
                  <tr key={t.id}>
                    <td className="strong">{t.name}</td>
                    <td className="tiny">{projectName(t.project_id)}</td>
                    <td className="tiny">{t.role}</td>
                    <td className="mono tiny">{t.prefix}…</td>
                    <td className="tiny">{when(t.last_used_at)}</td>
                    <td className="tiny">{t.expires_at ? when(t.expires_at) : "长期"}</td>
                    <td>
                      {!t.enabled ? (
                        <span className="chip bad"><span className="dot" />revoked</span>
                      ) : t.expires_at && new Date(t.expires_at).getTime() < Date.now() ? (
                        <span className="chip bad"><span className="dot" />expired</span>
                      ) : (
                        <span className="chip ok"><span className="dot" />active</span>
                      )}
                    </td>
                    <td>{t.enabled ? <button className="btn small" disabled={busy} onClick={() => void revoke(t)}>撤销</button> : "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className={"modal-backdrop" + (issued ? " open" : "")}>
        <div className="modal" style={{ width: "min(720px, 100%)" }}>
          <div className="modal-head">
            <div><div className="modal-title">令牌已签发</div><div className="modal-sub">{issued?.record.name} · {issued?.record.role} · {issued ? projectName(issued.record.project_id) : ""}</div></div>
            <button className="icon-btn" onClick={() => setIssued(null)}>✕</button>
          </div>
          <div className="modal-body">
            <div className="tiny" style={{ marginBottom: 6 }}>复制后妥善保存；关闭后无法再次查看明文。</div>
            <pre className="mono" style={{ background: "#0f172a", color: "#e2e8f0", padding: 12, borderRadius: 8, whiteSpace: "pre-wrap", wordBreak: "break-all" }}>{issued?.token}</pre>
            <div className="tiny" style={{ marginTop: 10 }}>
              连接方式：<span className="mono">POST http://127.0.0.1:8097/mcp</span>，Header <span className="mono">Authorization: Bearer &lt;token&gt;</span>。
            </div>
          </div>
          <div className="modal-foot"><div className="tiny">服务端只保存 sha256 哈希。</div><button className="btn" onClick={() => setIssued(null)}>我已保存</button></div>
        </div>
      </div>
    </>
  );
}