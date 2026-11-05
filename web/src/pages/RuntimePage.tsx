import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { AgentItem, AppServer, Project } from "../types";
import type { Notify } from "../App";

const PHASE_NAMES: Record<string, string> = {
  "0": "UNSPECIFIED",
  "1": "PENDING",
  "2": "DEPLOYING",
  "3": "STARTING",
  "4": "RUNNING",
  "5": "STOPPING",
  "6": "STOPPED",
  "7": "RESTARTING",
  "8": "DELETING",
  "9": "FAILED",
  "10": "UNKNOWN",
  AGENT_PHASE_UNSPECIFIED: "UNSPECIFIED",
  AGENT_PHASE_PENDING: "PENDING",
  AGENT_PHASE_DEPLOYING: "DEPLOYING",
  AGENT_PHASE_STARTING: "STARTING",
  AGENT_PHASE_RUNNING: "RUNNING",
  AGENT_PHASE_STOPPING: "STOPPING",
  AGENT_PHASE_STOPPED: "STOPPED",
  AGENT_PHASE_RESTARTING: "RESTARTING",
  AGENT_PHASE_DELETING: "DELETING",
  AGENT_PHASE_FAILED: "FAILED",
  AGENT_PHASE_UNKNOWN: "UNKNOWN",
};

function phaseName(phase: unknown): string {
  const raw = String(phase ?? "");
  return PHASE_NAMES[raw] ?? raw ?? "—";
}

function phaseChip(phase: unknown) {
  const name = phaseName(phase);
  const cls = name === "RUNNING" ? "ok" : name === "FAILED" ? "bad" : "warn";
  return <span className={"chip " + cls}><span className="dot" />{name}</span>;
}

function healthChip(health: string) {
  if (health === "ok") return <span className="chip ok"><span className="dot" />健康</span>;
  if (health === "degraded") return <span className="chip warn"><span className="dot" />降级（Master 未配置）</span>;
  if (health === "down") return <span className="chip bad"><span className="dot" />不可达</span>;
  return <span className="chip">未知</span>;
}

export function RuntimePage({ notify }: { notify: Notify }) {
  const [servers, setServers] = useState<AppServer[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [agents, setAgents] = useState<AgentItem[]>([]);
  const [agentError, setAgentError] = useState("");
  const [master, setMaster] = useState<Record<string, unknown> | null>(null);
  const [masterError, setMasterError] = useState("");
  const [busy, setBusy] = useState("");
  const [open, setOpen] = useState(false);
  const [logOpen, setLogOpen] = useState(false);
  const [logText, setLogText] = useState("");
  const [form, setForm] = useState({ name: "app-01", addr: "127.0.0.1:8765", binary: "", token: "" });
  const [leadForm, setLeadForm] = useState({ project_id: "", name: "lead-core-01", template_name: "pipeline-lead-v3", target_node_id: "", bind_lead: true });
  const [projects, setProjects] = useState<Project[]>([]);
  const [leadOpen, setLeadOpen] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const list = await api.listAppServers();
      setServers(list);
      setError("");
      const running = list.find((s) => s.status === "running");
      if (running) {
        const [agentRes, masterRes] = await Promise.allSettled([api.listAgents(running.id), api.masterStatus(running.id)]);
        if (agentRes.status === "fulfilled") {
          setAgents(agentRes.value);
          setAgentError("");
        } else {
          setAgents([]);
          setAgentError(agentRes.reason instanceof ApiError ? agentRes.reason.message : "读取 Agent 失败");
        }
        if (masterRes.status === "fulfilled") {
          setMaster(masterRes.value);
          setMasterError("");
        } else {
          setMaster(null);
          setMasterError(masterRes.reason instanceof ApiError ? masterRes.reason.message : "读取 Master 状态失败");
        }
      } else {
        setAgents([]);
        setAgentError("");
        setMaster(null);
        setMasterError("");
      }
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载运行时失败");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 15000);
    return () => window.clearInterval(timer);
  }, [load]);

  useEffect(() => {
    void api.listProjects().then(setProjects).catch(() => setProjects([]));
  }, []);

  const start = async () => {
    setBusy("start");
    try {
      await api.startAppServer({
        name: form.name,
        addr: form.addr,
        binary: form.binary || undefined,
        token: form.token || undefined,
      });
      notify("App Server 已启动");
      setOpen(false);
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "启动失败", "error");
    } finally {
      setBusy("");
    }
  };

  const act = async (server: AppServer, action: "stop" | "restart") => {
    setBusy(server.id + action);
    try {
      if (action === "stop") await api.stopAppServer(server.id);
      else await api.restartAppServer(server.id);
      notify(action === "stop" ? "已停止" : "已重启");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "操作失败", "error");
    } finally {
      setBusy("");
    }
  };

  const showLogs = async (server: AppServer) => {
    try {
      const res = await api.appServerLogs(server.id, 120);
      setLogText(res.log || "（日志为空）");
      setLogOpen(true);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "读取日志失败", "error");
    }
  };

  const deployLead = async () => {
    const running = servers.find((s) => s.status === "running");
    if (!running) {
      notify("先启动一个 App Server", "error");
      return;
    }
    setBusy("lead");
    try {
      const op = await api.deployAgent(running.id, leadForm);
      notify("已提交 AgentOperation：" + String(op.id ?? ""));
      setLeadOpen(false);
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "部署失败", "error");
    } finally {
      setBusy("");
    }
  };

  const running = servers.filter((s) => s.status === "running").length;

  return (
    <>
      <div className="notice">
        <span>i</span>
        <div>
          <strong>三层能力分开执行：</strong>App Server 进程由 Runtime Manager 启动守护，Master/Worker 通过 App Server 的
          <span className="mono"> /api/master </span>与<span className="mono"> /api/node </span>，pipeline-lead 通过
          <span className="mono"> POST /api/agents </span>部署并轮询 AgentOperation。
        </div>
      </div>

      <div className="stat-grid">
        <div className="stat">
          <div className="stat-label">App Server</div>
          <div className="stat-value">{servers.length}</div>
          <div className="stat-foot">{running} 个运行中</div>
        </div>
        <div className="stat">
          <div className="stat-label">Master</div>
          <div className="stat-value" style={{ fontSize: 15 }}>
            {master ? String((master as Record<string, unknown>).masterAddr ?? "未配置") : "—"}
          </div>
          <div className="stat-foot">{masterError ? "未部署或不可达" : "控制面地址"}</div>
        </div>
        <div className="stat">
          <div className="stat-label">Agent</div>
          <div className="stat-value">{agents.length}</div>
          <div className="stat-foot">pipeline-lead 实例</div>
        </div>
        <div className="stat">
          <div className="stat-label">子 Agent</div>
          <div className="stat-value" style={{ fontSize: 15 }}>由 lead 派生</div>
          <div className="stat-foot">管理台不提供创建入口</div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">App Server 进程</div>
            <div className="card-sub">Runtime Manager 启动并守护；健康检查走 /api/health</div>
          </div>
          <div style={{ display: "flex", gap: 8 }}>
            <button className="btn" onClick={() => void load()}>刷新</button>
            <button className="btn primary" onClick={() => setOpen(true)}>＋ 启动 App Server</button>
          </div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {loading ? (
            <div className="empty">加载中…</div>
          ) : error ? (
            <div className="empty"><h3>无法读取运行时</h3><div className="tiny">{error}</div></div>
          ) : servers.length === 0 ? (
            <div className="empty">
              <h3>还没有运行中的 App Server</h3>
              <div className="tiny">启动后这里会显示 PID、健康状态和版本；Master 与 Agent 都在它之上运行。</div>
              <button className="btn primary" style={{ marginTop: 14 }} onClick={() => setOpen(true)}>＋ 启动 App Server</button>
            </div>
          ) : (
            <table className="table">
              <thead>
                <tr><th>实例</th><th>地址</th><th>PID</th><th>状态</th><th>健康</th><th>版本</th><th></th></tr>
              </thead>
              <tbody>
                {servers.map((s) => (
                  <tr key={s.id}>
                    <td><div className="strong">{s.name}</div><div className="tiny mono">{s.id}</div></td>
                    <td className="mono">{s.addr}</td>
                    <td className="mono">{s.pid || "—"}</td>
                    <td>
                      <span className={"chip " + (s.status === "running" ? "ok" : s.status === "failed" ? "bad" : "")}>
                        <span className="dot" />{s.status}
                      </span>
                    </td>
                    <td>{healthChip(s.health)}</td>
                    <td className="mono">{s.version || "—"}</td>
                    <td>
                      <div className="row-actions">
                        <button className="btn small" onClick={() => void showLogs(s)}>日志</button>
                        <button className="btn small" disabled={busy === s.id + "restart"} onClick={() => void act(s, "restart")}>重启</button>
                        <button className="btn small danger" disabled={busy === s.id + "stop"} onClick={() => void act(s, "stop")}>停止</button>
                        {s.status !== "running" && (
                          <button
                            className="btn small"
                            onClick={async () => {
                              try {
                                await api.removeAppServer(s.id);
                                notify("实例记录已删除");
                                await load();
                              } catch (err) {
                                notify(err instanceof ApiError ? err.message : "删除失败", "error");
                              }
                            }}
                          >
                            移除
                          </button>
                        )}
                      </div>
                    </td>
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
            <div>
              <div className="card-title">Master 控制面</div>
              <div className="card-sub">/api/master/status</div>
            </div>
            <button
              className="btn small"
              disabled={!servers.some((s) => s.status === "running")}
              onClick={async () => {
                const running = servers.find((s) => s.status === "running");
                if (!running) return;
                try {
                  const out = await api.deployMaster(running.id, { name: "master-01", target: "local" });
                  notify("已提交 Master 部署：" + JSON.stringify(out).slice(0, 80));
                  await load();
                } catch (err) {
                  notify(err instanceof ApiError ? err.message : "部署失败", "error");
                }
              }}
            >
              部署本地 Master
            </button>
          </div>
          <div className="card-body">
            {masterError ? (
              <div className="empty" style={{ padding: "18px 0" }}>
                <h3>Master 未就绪</h3>
                <div className="tiny">{masterError}</div>
              </div>
            ) : master ? (
              <pre className="mono" style={{ margin: 0, whiteSpace: "pre-wrap", fontSize: 11.5 }}>
                {JSON.stringify(master, null, 2)}
              </pre>
            ) : (
              <div className="empty" style={{ padding: "18px 0" }}><div className="tiny">启动 App Server 后显示</div></div>
            )}
          </div>
        </div>

        <div className="card">
          <div className="card-head">
            <div>
              <div className="card-title">pipeline-lead</div>
              <div className="card-sub">POST /api/agents · 返回 AgentOperation</div>
            </div>
            <button
              className="btn small primary"
              disabled={!servers.some((s) => s.status === "running")}
              onClick={() => setLeadOpen(true)}
            >
              ＋ 部署 lead
            </button>
          </div>
          <div className="card-body" style={{ padding: 0 }}>
            {agentError ? (
              <div className="empty" style={{ padding: "18px 0" }}>
                <h3>Agent 列表不可用</h3>
                <div className="tiny">{agentError}</div>
              </div>
            ) : agents.length === 0 ? (
              <div className="empty" style={{ padding: "18px 0" }}>
                <div className="tiny">还没有 pipeline-lead。Master 就绪后即可部署。</div>
              </div>
            ) : (
              <table className="table">
                <thead><tr><th>Agent</th><th>Phase</th></tr></thead>
                <tbody>
                  {agents.map((a) => {
                    const spec = (a.spec ?? {}) as Record<string, unknown>;
                    const status = (a.status ?? {}) as Record<string, unknown>;
                    const label = String(spec.name ?? a.name ?? a.displayName ?? a.id);
                    return (
                      <tr key={a.id}>
                        <td>
                          <div className="strong">{label}</div>
                          <div className="tiny mono">{a.id} · {String(spec.template_name ?? spec.templateName ?? "—")}</div>
                        </td>
                        <td>{phaseChip(status.phase ?? a.phase)}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            )}
          </div>
        </div>
      </div>

      <div className={"modal-backdrop" + (open ? " open" : "")}>
        <div className="modal" style={{ width: "min(620px, 100%)" }}>
          <div className="modal-head">
            <div><div className="modal-title">启动 App Server</div><div className="modal-sub">由 Runtime Manager 启动进程，SDK 只是它的客户端。</div></div>
            <button className="icon-btn" onClick={() => setOpen(false)}>✕</button>
          </div>
          <div className="modal-body">
            <div className="form-grid">
              <div className="field"><label>实例名称</label><input value={form.name} onChange={(e) => setForm((p) => ({ ...p, name: e.target.value }))} /></div>
              <div className="field"><label>监听地址</label><input value={form.addr} onChange={(e) => setForm((p) => ({ ...p, addr: e.target.value }))} /></div>
              <div className="field full"><label>二进制路径（留空用服务端默认）</label><input value={form.binary} onChange={(e) => setForm((p) => ({ ...p, binary: e.target.value }))} placeholder="backend\bin\syntropy-app-server.exe" /></div>
              <div className="field full"><label>会话令牌（留空用服务端默认）</label><input type="password" value={form.token} onChange={(e) => setForm((p) => ({ ...p, token: e.target.value }))} /></div>
            </div>
          </div>
          <div className="modal-foot">
            <div className="tiny">token 只写入进程参数，不返回给浏览器。</div>
            <div>
              <button className="btn" onClick={() => setOpen(false)}>取消</button>{" "}
              <button className="btn primary" disabled={busy === "start"} onClick={() => void start()}>{busy === "start" ? "启动中…" : "启动"}</button>
            </div>
          </div>
        </div>
      </div>

      <div className={"modal-backdrop" + (leadOpen ? " open" : "")}>
        <div className="modal" style={{ width: "min(620px, 100%)" }}>
          <div className="modal-head">
            <div><div className="modal-title">部署 pipeline-lead</div><div className="modal-sub">提交到 /api/agents，成功后由 lead 自己派生 dev/test/release。</div></div>
            <button className="icon-btn" onClick={() => setLeadOpen(false)}>✕</button>
          </div>
          <div className="modal-body">
            <div className="form-grid">
              <div className="field">
                <label>项目 project_id</label>
                <select value={leadForm.project_id} onChange={(e) => setLeadForm((p) => ({ ...p, project_id: e.target.value }))}>
                  <option value="">选择项目</option>
                  {projects.map((prj) => <option key={prj.id} value={prj.id}>{prj.name}（{prj.id}）</option>)}
                </select>
              </div>
              <div className="field"><label>Agent 名称</label><input value={leadForm.name} onChange={(e) => setLeadForm((p) => ({ ...p, name: e.target.value }))} /></div>
              <div className="field"><label>模板</label><input value={leadForm.template_name} onChange={(e) => setLeadForm((p) => ({ ...p, template_name: e.target.value }))} /></div>
              <div className="field"><label>目标节点</label><input value={leadForm.target_node_id} onChange={(e) => setLeadForm((p) => ({ ...p, target_node_id: e.target.value }))} placeholder="留空由 Master 调度" /></div>
              <label className="field full" style={{ flexDirection: "row", alignItems: "center", gap: 8 }}>
                <input type="checkbox" style={{ width: 16 }} checked={leadForm.bind_lead} onChange={(e) => setLeadForm((p) => ({ ...p, bind_lead: e.target.checked }))} />
                <span>部署成功后自动绑定为该项目唯一的 pipeline-lead（桥接按此派单）</span>
              </label>
            </div>
          </div>
          <div className="modal-foot">
            <div className="tiny">子 Agent 由 lead 创建，管理台不提供入口。</div>
            <div>
              <button className="btn" onClick={() => setLeadOpen(false)}>取消</button>{" "}
              <button className="btn primary" disabled={busy === "lead"} onClick={() => void deployLead()}>{busy === "lead" ? "提交中…" : "提交部署"}</button>
            </div>
          </div>
        </div>
      </div>

      <div className={"modal-backdrop" + (logOpen ? " open" : "")}>
        <div className="modal" style={{ width: "min(900px, 100%)" }}>
          <div className="modal-head">
            <div><div className="modal-title">App Server 日志</div><div className="modal-sub">最后 120 行</div></div>
            <button className="icon-btn" onClick={() => setLogOpen(false)}>✕</button>
          </div>
          <div className="modal-body">
            <pre className="mono" style={{ maxHeight: 460, overflow: "auto", background: "#0f172a", color: "#cbd5e1", padding: 14, borderRadius: 10, fontSize: 11.5 }}>
              {logText}
            </pre>
          </div>
          <div className="modal-foot"><div className="tiny">日志可能包含路径，请勿写入凭据。</div><button className="btn" onClick={() => setLogOpen(false)}>关闭</button></div>
        </div>
      </div>
    </>
  );
}
