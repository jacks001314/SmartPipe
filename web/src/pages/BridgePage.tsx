import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { AgentItem, AppServer, BridgeLink, Derivation, DispatchRecord, IssuePlan, Project, ProjectBinding, WebhookDelivery } from "../types";
import type { Notify } from "../App";

function when(value: string | null | undefined) {
  if (!value) return "—";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

function statusChip(status: string) {
  const map: Record<string, { cls: string; label: string }> = {
    sent: { cls: "ok", label: "已派单" },
    queued: { cls: "warn", label: "排队中" },
    unbound: { cls: "warn", label: "未绑定 lead" },
    failed: { cls: "bad", label: "派单失败" },
    skipped: { cls: "", label: "未派发" },
    pending: { cls: "warn", label: "待处理" },
  };
  const item = map[status] ?? { cls: "", label: status || "—" };
  return <span className={"chip " + item.cls}><span className="dot" />{item.label}</span>;
}

export function BridgePage({ notify }: { notify: Notify }) {
  const [links, setLinks] = useState<BridgeLink[]>([]);
  const [bindings, setBindings] = useState<ProjectBinding[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [servers, setServers] = useState<AppServer[]>([]);
  const [deliveries, setDeliveries] = useState<WebhookDelivery[]>([]);
  const [dispatches, setDispatches] = useState<DispatchRecord[]>([]);
  const [plans, setPlans] = useState<IssuePlan[]>([]);
  const [derivations, setDerivations] = useState<Derivation[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState({ project_id: "", app_server_id: "", lead_agent_id: "", lead_agent_name: "" });
  const [agents, setAgents] = useState<AgentItem[]>([]);
  const [agentError, setAgentError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    const [linkRes, bindRes, projRes, srvRes, delivRes, dispRes, planRes, derRes] = await Promise.allSettled([
      api.listBridgeLinks(100),
      api.listBindings(),
      api.listProjects(),
      api.listAppServers(),
      api.listWebhookDeliveries(30),
      api.listDispatches(30),
      api.listPlans(30),
      api.listDerivations(30),
    ]);
    if (linkRes.status === "fulfilled") setLinks(linkRes.value);
    if (bindRes.status === "fulfilled") setBindings(bindRes.value);
    if (projRes.status === "fulfilled") setProjects(projRes.value);
    if (srvRes.status === "fulfilled") setServers(srvRes.value);
    if (delivRes.status === "fulfilled") setDeliveries(delivRes.value);
    if (dispRes.status === "fulfilled") setDispatches(dispRes.value);
    if (planRes.status === "fulfilled") setPlans(planRes.value);
    if (derRes.status === "fulfilled") setDerivations(derRes.value);
    setError(linkRes.status === "rejected" ? (linkRes.reason instanceof ApiError ? linkRes.reason.message : "读取桥接数据失败") : "");
    setLoading(false);
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 15000);
    return () => window.clearInterval(timer);
  }, [load]);

  useEffect(() => {
    if (!form.app_server_id) {
      setAgents([]);
      return;
    }
    let alive = true;
    void api
      .listAgents(form.app_server_id)
      .then((list) => {
        if (alive) {
          setAgents(list);
          setAgentError("");
        }
      })
      .catch((err) => {
        if (alive) {
          setAgents([]);
          setAgentError(err instanceof ApiError ? err.message : "读取 Agent 失败");
        }
      });
    return () => {
      alive = false;
    };
  }, [form.app_server_id]);

  const projectName = (id: string | null) => {
    if (!id) return "—";
    return projects.find((p) => p.id === id)?.name ?? id;
  };

  const bindingFor = (projectId: string | null) => (projectId ? bindings.find((b) => b.project_id === projectId) : undefined);

  const startBind = (projectId?: string) => {
    const existing = projectId ? bindingFor(projectId) : undefined;
    const running = servers.find((s) => s.status === "running");
    setForm({
      project_id: projectId ?? "",
      app_server_id: existing?.app_server_id ?? running?.id ?? "",
      lead_agent_id: existing?.lead_agent_id ?? "",
      lead_agent_name: existing?.lead_agent_name ?? "",
    });
    setOpen(true);
  };

  const save = async () => {
    if (!form.project_id || !form.app_server_id || !form.lead_agent_id) {
      notify("项目、App Server、lead Agent 都需要选择", "error");
      return;
    }
    setBusy("save");
    try {
      await api.upsertBinding(form.project_id, {
        app_server_id: form.app_server_id,
        lead_agent_id: form.lead_agent_id,
        lead_agent_name: form.lead_agent_name,
      });
      notify("已绑定 pipeline-lead");
      setOpen(false);
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "绑定失败", "error");
    } finally {
      setBusy("");
    }
  };

  const unbind = async (projectId: string) => {
    setBusy("unbind-" + projectId);
    try {
      await api.deleteBinding(projectId);
      notify("已解除绑定");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "解绑失败", "error");
    } finally {
      setBusy("");
    }
  };

  const retry = async (link: BridgeLink) => {
    setBusy("retry-" + link.id);
    try {
      await api.redispatch(link.id);
      notify("已重新派发 #" + link.issue_number);
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "派发失败", "error");
    } finally {
      setBusy("");
    }
  };

  const sent = links.filter((l) => l.dispatch_status === "sent").length;
  const broken = links.filter((l) => l.dispatch_status === "failed" || l.dispatch_status === "unbound").length;

  return (
    <>
      <div className="notice">
        <span>i</span>
        <div>
          <strong>从反馈到工单：</strong>蓝信机器人收到问题后，桥接服务去重、在项目对应仓库建 GitHub Issue，并把工单投递到该项目的
          <span className="mono"> pipeline-lead </span>。每个项目只绑定一个 lead，子 Agent 由 lead 自己派生。
        </div>
      </div>

      <div className="stat-grid">
        <div className="stat">
          <div className="stat-label">收单映射</div>
          <div className="stat-value">{links.length}</div>
          <div className="stat-foot">最近 100 条蓝信消息</div>
        </div>
        <div className="stat">
          <div className="stat-label">已派给 lead</div>
          <div className="stat-value">{sent}</div>
          <div className="stat-foot">投递成功</div>
        </div>
        <div className="stat">
          <div className="stat-label">待处理</div>
          <div className="stat-value">{broken}</div>
          <div className="stat-foot">未绑定或投递失败</div>
        </div>
        <div className="stat">
          <div className="stat-label">Webhook 投递</div>
          <div className="stat-value">{deliveries.length}</div>
          <div className="stat-foot">已验签并去重</div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">pipeline-lead 绑定</div>
            <div className="card-sub">每个项目绑定一个 App Server 上的 lead Agent，桥接按这张表派单</div>
          </div>
          <div style={{ display: "flex", gap: 8 }}>
            <button className="btn" onClick={() => void load()}>刷新</button>
            <button className="btn primary" onClick={() => startBind()}>＋ 绑定 lead</button>
          </div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {bindings.length === 0 ? (
            <div className="empty">
              <h3>还没有项目绑定 lead</h3>
              <div className="tiny">绑定后，新 Issue 会自动投递给该 lead；也可以先在运行时页部署 lead 并勾选自动绑定。</div>
            </div>
          ) : (
            <table className="table">
              <thead>
                <tr><th>项目</th><th>lead Agent</th><th>App Server</th><th>更新时间</th><th></th></tr>
              </thead>
              <tbody>
                {bindings.map((b) => (
                  <tr key={b.project_id}>
                    <td>
                      <div className="strong">{projectName(b.project_id)}</div>
                      <div className="tiny mono">{b.project_id}</div>
                    </td>
                    <td>
                      <div className="strong">{b.lead_agent_name || "lead"}</div>
                      <div className="tiny mono">{b.lead_agent_id}</div>
                    </td>
                    <td>
                      <div className="mono">{b.app_server_addr || "—"}</div>
                      <div className="tiny mono">{b.app_server_id} · agent project {b.agent_project_id || "—"}</div>
                    </td>
                    <td className="tiny">{when(b.updated_at)}</td>
                    <td>
                      <div className="row-actions">
                        <button className="btn small" onClick={() => startBind(b.project_id)}>修改</button>
                        <button className="btn small danger" disabled={busy === "unbind-" + b.project_id} onClick={() => void unbind(b.project_id)}>解绑</button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">复杂 Issue 方案与批准</div>
            <div className="card-sub">lead 在 Issue 评论里 /plan 提交方案，维护者 /approve vN 后才开工；内容改动会使旧批准失效</div>
          </div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {plans.length === 0 ? (
            <div className="empty" style={{ padding: "24px 16px" }}>
              <div className="tiny">还没有登记方案。复杂任务由 lead 在 Issue 评论中发起 /plan。</div>
            </div>
          ) : (
            <table className="table">
              <thead><tr><th>Issue</th><th>版本</th><th>指纹</th><th>状态</th><th>时间</th></tr></thead>
              <tbody>
                {plans.map((p) => (
                  <tr key={p.id}>
                    <td>
                      <div className="strong">{p.repo}#{p.issue_number}</div>
                      <div className="tiny">提交人 {p.author || "—"}</div>
                    </td>
                    <td className="mono">v{p.version}</td>
                    <td className="tiny mono">{p.content_hash.replace("sha256:", "").slice(0, 12)}</td>
                    <td>
                      {p.approved ? <span className="chip ok"><span className="dot" />已批准 · {p.approver}</span> : <span className="chip warn"><span className="dot" />待批准</span>}
                    </td>
                    <td className="tiny">{when(p.created_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">蓝信消息 → GitHub Issue</div>
            <div className="card-sub">同一 message_id 只建一次单；派单失败可重试，Issue 不会被重复创建</div>
          </div>
          <button className="btn small" onClick={() => void load()}>刷新</button>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {loading ? (
            <div className="empty">加载中…</div>
          ) : error ? (
            <div className="empty"><h3>无法读取桥接数据</h3><div className="tiny">{error}</div></div>
          ) : links.length === 0 ? (
            <div className="empty">
              <h3>还没有收到反馈</h3>
              <div className="tiny">蓝信机器人消息会经 POST /bridge/messages 进入，创建 Issue 后显示在这里。</div>
            </div>
          ) : (
            <table className="table">
              <thead>
                <tr><th>时间</th><th>项目 / 来源</th><th>Issue</th><th>派单</th><th></th></tr>
              </thead>
              <tbody>
                {links.map((l) => (
                  <tr key={l.id}>
                    <td className="tiny">{when(l.created_at)}</td>
                    <td>
                      <div className="strong">{projectName(l.project_id)}</div>
                      <div className="tiny mono">{l.chat_id || "—"} · {l.user_id || "—"}</div>
                    </td>
                    <td>
                      <div className="strong">#{l.issue_number}</div>
                      <div className="tiny mono">{l.repo}</div>
                    </td>
                    <td>
                      {statusChip(l.dispatch_status)}
                      {l.dispatch_error ? <div className="tiny" style={{ color: "#b91c1c", marginTop: 4 }}>{l.dispatch_error}</div> : null}
                      {l.lead_agent_id ? <div className="tiny mono" style={{ marginTop: 4 }}>{l.lead_agent_id}</div> : null}
                      {l.receipt_status === "sent" ? <div className="tiny" style={{ marginTop: 4 }}>已回执蓝信群</div> : null}
                      {l.receipt_status === "failed" ? <div className="tiny" style={{ color: "#b91c1c", marginTop: 4 }}>回执失败：{l.receipt_error}</div> : null}
                    </td>
                    <td>
                      <div className="row-actions">
                        <a className="btn small" href={l.issue_url} target="_blank" rel="noreferrer">打开 Issue</a>
                        <button className="btn small" disabled={busy === "retry-" + l.id} onClick={() => void retry(l)}>重新派单</button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">子 Agent 派生记录</div>
            <div className="card-sub">lead 只能通过 /admin/derivations 派生；有方案记录的 Issue 必须先获得有效 /approve</div>
          </div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {derivations.length === 0 ? (
            <div className="empty" style={{ padding: "24px 16px" }}><div className="tiny">还没有派生记录</div></div>
          ) : (
            <table className="table">
              <thead><tr><th>时间</th><th>Issue</th><th>Agent</th><th>批准版本</th><th>Operation</th><th>状态</th></tr></thead>
              <tbody>
                {derivations.map((d) => (
                  <tr key={d.id}>
                    <td className="tiny">{when(d.created_at)}</td>
                    <td className="mono tiny">{d.issue_ref}</td>
                    <td>
                      <div className="strong">{d.agent_name}</div>
                      <div className="tiny mono">{d.agent_id || "—"}</div>
                    </td>
                    <td className="tiny">{d.approval_version ? "v" + d.approval_version : "简单任务"}</td>
                    <td className="tiny mono">{d.operation_id || "—"}</td>
                    <td><span className={"chip " + (d.status === "submitted" ? "ok" : "warn")}><span className="dot" />{d.status}</span></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className="split">
        <div className="card">
          <div className="card-head">
            <div><div className="card-title">最近派单记录</div><div className="card-sub">每次投递 lead 的结果</div></div>
          </div>
          <div className="card-body" style={{ padding: 0 }}>
            {dispatches.length === 0 ? (
              <div className="empty" style={{ padding: "24px 16px" }}><div className="tiny">暂无记录</div></div>
            ) : (
              <table className="table">
                <thead><tr><th>时间</th><th>Issue 映射</th><th>Agent</th><th>结果</th></tr></thead>
                <tbody>
                  {dispatches.map((d) => (
                    <tr key={d.id}>
                      <td className="tiny">{when(d.at)}</td>
                      <td className="mono">#{d.link_id}</td>
                      <td className="tiny mono">{d.agent_id || "—"}</td>
                      <td>{statusChip(d.status)}{d.message ? <div className="tiny">{d.message}</div> : null}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </div>

        <div className="card">
          <div className="card-head">
            <div><div className="card-title">GitHub Webhook</div><div className="card-sub">验签通过且未重复的投递</div></div>
          </div>
          <div className="card-body" style={{ padding: 0 }}>
            {deliveries.length === 0 ? (
              <div className="empty" style={{ padding: "24px 16px" }}><div className="tiny">暂无投递</div></div>
            ) : (
              <table className="table">
                <thead><tr><th>时间</th><th>事件</th><th>仓库</th><th>Delivery</th></tr></thead>
                <tbody>
                  {deliveries.map((d) => (
                    <tr key={d.delivery_id}>
                      <td className="tiny">{when(d.received_at)}</td>
                      <td><span className="chip">{d.event || "—"}</span></td>
                      <td className="tiny mono">{d.repo || "—"}</td>
                      <td className="tiny mono">{d.delivery_id.slice(0, 18)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </div>
      </div>

      <div className={"modal-backdrop" + (open ? " open" : "")}>
        <div className="modal" style={{ width: "min(620px, 100%)" }}>
          <div className="modal-head">
            <div><div className="modal-title">绑定 pipeline-lead</div><div className="modal-sub">一个项目只对应一个 lead；lead 再按需派生 doer/test Agent。</div></div>
            <button className="icon-btn" onClick={() => setOpen(false)}>✕</button>
          </div>
          <div className="modal-body">
            <div className="form-grid">
              <div className="field full">
                <label>项目</label>
                <select value={form.project_id} onChange={(e) => setForm((p) => ({ ...p, project_id: e.target.value }))}>
                  <option value="">选择项目</option>
                  {projects.map((p) => <option key={p.id} value={p.id}>{p.name}（{p.id}）</option>)}
                </select>
              </div>
              <div className="field full">
                <label>App Server</label>
                <select value={form.app_server_id} onChange={(e) => setForm((p) => ({ ...p, app_server_id: e.target.value, lead_agent_id: "" }))}>
                  <option value="">选择 App Server</option>
                  {servers.map((s) => <option key={s.id} value={s.id}>{s.name} · {s.addr} · {s.status}</option>)}
                </select>
              </div>
              <div className="field full">
                <label>lead Agent</label>
                <select
                  value={form.lead_agent_id}
                  onChange={(e) => {
                    const agent = agents.find((a) => a.id === e.target.value);
                    const spec = (agent?.spec ?? {}) as Record<string, unknown>;
                    setForm((p) => ({ ...p, lead_agent_id: e.target.value, lead_agent_name: String(spec.name ?? agent?.name ?? "") }));
                  }}
                >
                  <option value="">{agentError || "选择 Agent"}</option>
                  {agents.map((a) => {
                    const spec = (a.spec ?? {}) as Record<string, unknown>;
                    const label = String(spec.name ?? a.name ?? a.displayName ?? a.id);
                    return <option key={a.id} value={a.id}>{label}（{a.id}）</option>;
                  })}
                </select>
              </div>
            </div>
          </div>
          <div className="modal-foot">
            <div className="tiny">桥接只使用 project_id 与 lead agent id，不接触 GitHub / 蓝信凭据。</div>
            <div>
              <button className="btn" onClick={() => setOpen(false)}>取消</button>{" "}
              <button className="btn primary" disabled={busy === "save"} onClick={() => void save()}>{busy === "save" ? "保存中…" : "保存绑定"}</button>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}
