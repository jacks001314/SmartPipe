import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError } from "../api";
import type { Project, ReleaseV2AgentView, ReleaseV2Bundle, ReleaseV2Event, ReleaseV2Executor, ReleaseV2ExecutorDeployResult, ReleaseV2Preview, ReleaseV2Run } from "../types";
import type { Notify } from "../App";
import { ReleaseV2ConfigModal } from "./ReleaseV2ConfigModal";

function when(value: string | null | undefined) {
  if (!value) return "—";
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

function shortHash(value: string) {
  return (value || "").replace("sha256:", "").slice(0, 10) || "—";
}

function bytes(size: number) {
  if (!size) return "—";
  if (size > 1024 * 1024 * 1024) return (size / (1024 * 1024 * 1024)).toFixed(2) + " GiB";
  if (size > 1024 * 1024) return (size / (1024 * 1024)).toFixed(1) + " MiB";
  if (size > 1024) return (size / 1024).toFixed(1) + " KiB";
  return size + " B";
}

function statusChip(status: string) {
  const ok = ["published", "built", "ready"].includes(status);
  const warn = ["staged", "publishing", "verifying", "artifacts_ready", "building", "preparing", "queued", "leased", "uploaded", "partially_published"].includes(status);
  return <span className={"chip " + (ok ? "ok" : warn ? "warn" : "bad")}><span className="dot" />{status}</span>;
}

const ALL_SCOPES = ["release.read", "executor.read", "artifact.read", "build.read", "release.write", "artifact.write", "update.read", "update.write", "update.publish", "github.write", "release.publish"];

export type ReleaseV2View = "create" | "runs" | "executors";

export function ReleaseV2Page({ notify, view = "runs" }: { notify: Notify; view?: ReleaseV2View }) {
  const [runs, setRuns] = useState<ReleaseV2Run[]>([]);
  const [executors, setExecutors] = useState<ReleaseV2Executor[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [error, setError] = useState("");
  const [detail, setDetail] = useState<ReleaseV2Bundle | null>(null);
  const [preview, setPreview] = useState<ReleaseV2Preview | null>(null);
  const [previewError, setPreviewError] = useState("");
  const [selectedServers, setSelectedServers] = useState<string[]>([]);
  const [selectedExecutors, setSelectedExecutors] = useState<string[]>([]);
  const [token, setToken] = useState("");
  const [agent, setAgent] = useState<ReleaseV2AgentView | null>(null);
  const streamRef = useRef<{ es: EventSource | null; timer: number | null; cursor: number }>({ es: null, timer: null, cursor: 0 });
  const [busy, setBusy] = useState(false);
  const [bootOpen, setBootOpen] = useState(false);
  const [bootForm, setBootForm] = useState({
    id: "build-01", os: "auto", arch: "auto",
    host: "", port: "22", username: "", auth_type: "password",
    secret: "", passphrase: "", install_dir: "", save_resource: true,
  });
  const [bootCommand, setBootCommand] = useState("");
  const [bootBusy, setBootBusy] = useState(false);
  const [bootResult, setBootResult] = useState<ReleaseV2ExecutorDeployResult | null>(null);
  const [removing, setRemoving] = useState("");
  const [form, setForm] = useState({ project_id: "" });
  const [configOpen, setConfigOpen] = useState(false);

  const generateBootstrap = async () => {
    setBootBusy(true);
    try {
      const result = await api.releaseV2ExecutorBootstrap(bootForm.os, bootForm.arch, bootForm.id.trim() || "build-01");
      setBootCommand(result.command);
    } catch (e) {
      notify(e instanceof ApiError ? e.message : String(e), "error");
    } finally {
      setBootBusy(false);
    }
  };

  const copyBootstrap = async () => {
    try {
      await navigator.clipboard.writeText(bootCommand);
      notify("接入命令已复制", "ok");
    } catch {
      notify("复制失败，请手动选择命令文本", "error");
    }
  };

  const load = useCallback(async () => {
    const [r, e, p] = await Promise.allSettled([api.listReleaseV2Runs(50), api.listReleaseV2Executors(), api.listProjects()]);
    if (r.status === "fulfilled") {
      setRuns(r.value);
      setError("");
    } else {
      setError(r.reason instanceof ApiError ? r.reason.message : "读取发版运行失败");
    }
    if (e.status === "fulfilled") setExecutors(e.value);
    if (p.status === "fulfilled") {
      setProjects(p.value);
      setForm((prev) => (prev.project_id ? prev : { ...prev, project_id: p.value[0]?.id ?? "" }));
    }
  }, []);

  const deployExecutor = async () => {
    if (!bootForm.host.trim() || !bootForm.username.trim() || !bootForm.secret.trim()) {
      notify("请填写 SSH 主机、用户名和密码/私钥", "error");
      return;
    }
    setBootBusy(true);
    setBootResult(null);
    try {
      const result = await api.deployReleaseV2Executor({
        id: bootForm.id.trim() || "build-01",
        os: bootForm.os, arch: bootForm.arch,
        host: bootForm.host.trim(), port: Number(bootForm.port) || 22,
        username: bootForm.username.trim(), auth_type: bootForm.auth_type,
        secret: bootForm.secret, passphrase: bootForm.passphrase,
        install_dir: bootForm.install_dir.trim(), save_resource: bootForm.save_resource,
      });
      setBootResult(result);
      notify(result.ok ? `构建机 ${result.executor_id} 已部署并注册` : (result.error || "部署失败"), result.ok ? "ok" : "error");
      if (result.ok) {
        setBootForm((f) => ({ ...f, secret: "", passphrase: "" }));
        void load();
      }
    } catch (e) {
      notify(e instanceof ApiError ? e.message : String(e), "error");
    } finally {
      setBootBusy(false);
    }
  };

  const removeExecutor = async (id: string, force = false) => {
    if (removing) return;
    if (!force && !window.confirm(`删除构建机 ${id}？会在目标机器上停止执行器、删除安装目录，并移除注册记录。`)) return;
    setRemoving(id);
    try {
      const result = await api.deleteReleaseV2Executor(id, { force });
      if (result.ok) {
        notify(`构建机 ${id} 已在目标机停止并卸载`, "ok");
        setBootResult(null);
        void load();
        return;
      }
      if (!force && window.confirm(`${result.error || "远程卸载失败"}。仍然只删除注册记录吗？`)) {
        setRemoving("");
        await removeExecutor(id, true);
        return;
      }
      notify(result.error || "删除失败", "error");
    } catch (e) {
      const message = e instanceof ApiError ? e.message : String(e);
      if (!force && message.includes("正在使用") && window.confirm(message + "。强制删除？")) {
        setRemoving("");
        await removeExecutor(id, true);
        return;
      }
      notify(message, "error");
    } finally {
      setRemoving("");
    }
  };

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 8000);
    return () => window.clearInterval(timer);
  }, [load]);

  const stopStream = useCallback(() => {
    const s = streamRef.current;
    if (s.es) {
      s.es.close();
      s.es = null;
    }
    if (s.timer !== null) {
      window.clearTimeout(s.timer);
      s.timer = null;
    }
  }, []);

  // 事件流：先用一次性票据换 SSE 连接，断线后带最新游标自动重连。
  const startStream = useCallback((runId: string, after: number) => {
    stopStream();
    streamRef.current.cursor = after;
    const connect = async () => {
      try {
        const issued = await api.createReleaseV2EventTicket(runId);
        const url = "/admin/releases/v2/runs/" + encodeURIComponent(runId) + "/events/stream"
          + "?ticket=" + encodeURIComponent(issued.ticket) + "&after=" + streamRef.current.cursor;
        const es = new EventSource(url);
        streamRef.current.es = es;
        es.onmessage = (msg) => {
          try {
            const ev = JSON.parse(msg.data) as ReleaseV2Event;
            streamRef.current.cursor = Math.max(streamRef.current.cursor, ev.id);
            setDetail((prev) => (prev && prev.run.id === runId ? { ...prev, events: mergeEvents(prev.events, ev) } : prev));
          } catch {
            /* 忽略单条坏消息 */
          }
        };
        es.onerror = () => {
          es.close();
          streamRef.current.es = null;
          if (streamRef.current.timer !== null) window.clearTimeout(streamRef.current.timer);
          streamRef.current.timer = window.setTimeout(() => void connect(), 3000);
        };
      } catch {
        // 换票失败（例如会话过期）就退回 8 秒轮询。
        if (streamRef.current.timer !== null) window.clearTimeout(streamRef.current.timer);
        streamRef.current.timer = window.setTimeout(() => void openRunRef.current?.(runId), 8000);
      }
    };
    void connect();
  }, [stopStream]);

  const openRunRef = useRef<((id: string) => Promise<void>) | null>(null);

  const openRun = useCallback(async (id: string) => {
    try {
      const [bundle, view] = await Promise.all([
        api.getReleaseV2Run(id),
        api.getReleaseV2Agent(id).catch(() => null),
      ]);
      setDetail(bundle);
      setAgent(view);
      setToken("");
      const lastId = bundle.events.reduce((max, ev) => Math.max(max, ev.id), 0);
      startStream(id, lastId);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "读取运行详情失败", "error");
    }
  }, [notify, startStream]);

  useEffect(() => {
    openRunRef.current = openRun;
  }, [openRun]);

  useEffect(() => () => stopStream(), [stopStream]);

  async function startAgent(id: string) {
    setBusy(true);
    try {
      const session = await api.startReleaseV2Agent(id, {});
      notify("发布 Agent 已接管运行（会话 " + session.id + "）", "ok");
      await openRun(id);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "启动发布 Agent 失败", "error");
    } finally {
      setBusy(false);
    }
  }

  const loadPreview = useCallback(async (projectId: string) => {
    if (!projectId) {
      setPreview(null);
      return;
    }
    setPreviewError("");
    try {
      const next = await api.releaseV2ProjectPreview(projectId);
      setPreview(next);
      setSelectedServers(next.update_servers.filter((s) => s.enabled).map((s) => s.id));
      setSelectedExecutors(next.builds.map((b) => b.executor_id).filter((v): v is string => Boolean(v)));
    } catch (err) {
      const message = err instanceof ApiError ? err.message : "读取发布预览失败";
      setPreview(null);
      setPreviewError(message);
    }
  }, []);

  useEffect(() => {
    void loadPreview(form.project_id);
  }, [form.project_id, loadPreview]);

  async function createRun() {
    if (!form.project_id) return;
    setBusy(true);
    try {
      const run = await api.createReleaseV2Run({
        project_id: form.project_id,
        update_server_ids: selectedServers,
        executor_ids: selectedExecutors,
      });
      notify("发布已创建，Agent 已接管 " + run.id, "ok");
      await load();
      await openRun(run.id);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "创建发布失败", "error");
    } finally {
      setBusy(false);
    }
  }

  function toggleServer(id: string) {
    setSelectedServers((prev) => prev.includes(id) ? prev.filter((item) => item !== id) : [...prev, id]);
  }

  function selectedExecutorFor(build: ReleaseV2Preview["builds"][number]) {
    const hostOS = build.host_os || build.os;
    const hostArch = build.host_arch || build.arch;
    return selectedExecutors.find((id) => {
      const executor = executors.find((item) => item.id === id);
      return executor?.os === hostOS && executor?.arch === hostArch;
    }) ?? "";
  }

  function chooseExecutor(build: ReleaseV2Preview["builds"][number], executorId: string) {
    const hostOS = build.host_os || build.os;
    const hostArch = build.host_arch || build.arch;
    const platformIDs = new Set(executors.filter((item) => item.os === hostOS && item.arch === hostArch).map((item) => item.id));
    setSelectedExecutors((prev) => [...prev.filter((id) => !platformIDs.has(id)), ...(executorId ? [executorId] : [])]);
  }

  async function cancelRun(id: string) {
    setBusy(true);
    try {
      await api.cancelReleaseV2Run(id, "从控制台取消");
      notify("已取消运行", "ok");
      await load();
      await openRun(id);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "取消失败", "error");
    } finally {
      setBusy(false);
    }
  }

  async function issueToken(id: string) {
    setBusy(true);
    try {
      const issued = await api.issueReleaseV2Token(id, ALL_SCOPES);
      setToken(issued.token);
      notify("已签发 Agent token（仅显示一次）", "ok");
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "签发 token 失败", "error");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="stack">
      <div className="card release-create" style={{ display: view === "create" ? undefined : "none" }}>
        <div className="card-head">
          <div>
            <div className="card-title">新建发布</div>
            <div className="card-sub">选择项目、升级服务器和构建机；版本、源码包、构建、灌库和发布都由 Agent 自动完成。</div>
          </div>
          <div className="btn-row">
            <button className="btn" disabled={!form.project_id} onClick={() => setConfigOpen(true)}>发版配置</button>
            <button className="btn" onClick={() => void load()}>刷新</button>
          </div>
        </div>
        <div className="card-body">
          <div className="form-grid">
            <label className="field">
              <span>项目</span>
              <select value={form.project_id} onChange={(e) => setForm({ project_id: e.target.value })}>
                {projects.map((p) => <option key={p.id} value={p.id}>{p.name || p.id}</option>)}
              </select>
            </label>
            <label className="field wide">
              <span>升级服务器</span>
              <div className="btn-row" style={{ flexWrap: "wrap", gap: 8 }}>
                {(preview?.update_servers ?? []).length === 0 && <span className="tiny">发版设置里还没有升级服务</span>}
                {(preview?.update_servers ?? []).map((server) => (
                  <label key={server.id} className="tiny" style={{ display: "flex", gap: 6, alignItems: "center" }}>
                    <input type="checkbox" checked={selectedServers.includes(server.id)} onChange={() => toggleServer(server.id)} />
                    {server.name} <span className="mono">{server.url}</span>
                  </label>
                ))}
              </div>
            </label>
            <label className="field wide">
              <span>构建机</span>
              <div className="btn-row" style={{ flexWrap: "wrap", gap: 8 }}>
                {(preview?.builds ?? []).map((build) => (
                  <label key={build.target_id} className="field" style={{ minWidth: 240 }}>
                    <span className="tiny">{build.target_id}</span>
                    <select value={selectedExecutorFor(build)} onChange={(e) => chooseExecutor(build, e.target.value)}>
                      <option value="">自动选择</option>
                      {executors.filter((item) => item.status === "online" && item.os === (build.host_os || build.os) && item.arch === (build.host_arch || build.arch)).map((item) => (
                        <option key={item.id} value={item.id}>{item.id}</option>
                      ))}
                    </select>
                  </label>
                ))}
              </div>
            </label>
          </div>
          {previewError && (
            <div className="notice bad" style={{ marginTop: 12 }}>
              <div><b>读取项目信息失败</b></div>
              <div className="tiny">{previewError}</div>
            </div>
          )}
          {preview && (
            <div className="notice" style={{ marginTop: 12 }}>
              <div><b>{preview.version}</b> · {preview.tag} · 提交 <span className="mono">{shortHash(preview.commit)}</span> · {preview.commit_date || "—"}</div>
              <div className="tiny">构建版本 {preview.build_version} · 仓库 {preview.repo || "—"} · 分支 {preview.branch}</div>
              <div className="tiny">平台：{preview.builds.map((build) => build.target_id + (build.host_os ? " ← " + build.host_os + "/" + build.host_arch : "") + (selectedExecutorFor(build) ? "@" + selectedExecutorFor(build) : "")).join("，") || "—"}</div>
              {(preview.warnings ?? []).length > 0 && (
                <div className="notice warn" style={{ marginTop: 10 }}>
                  {(preview.warnings ?? []).map((warning) => (
                    <div key={warning.code}>{warning.message}</div>
                  ))}
                </div>
              )}
              {preview.notes && <div className="tiny">发布说明：{preview.notes.slice(0, 180)}{preview.notes.length > 180 ? "…" : ""}</div>}
              {(preview.blockers ?? []).length > 0 && (
                <ul className="tiny">
                  {(preview.blockers ?? []).map((b) => <li key={b.code + (b.target_id ?? "")}>{b.code}：{b.message}</li>)}
                </ul>
              )}
            </div>
          )}
          <div className="btn-row" style={{ marginTop: 12 }}>
            <button className="btn primary" disabled={busy || !form.project_id || selectedServers.length === 0 || (preview?.blockers ?? []).length > 0} onClick={() => void createRun()}>
              {busy ? "处理中…" : "创建发布并由 Agent 接管"}
            </button>
          </div>
        </div>
      </div>

      <div className="card" style={{ display: view === "executors" ? undefined : "none" }}>
        <div className="card-head">
          <div><div className="card-title">构建执行器</div><div className="card-sub">填 SSH 连接信息直接部署：服务端上传 release-executor、写入令牌并在构建机上后台启动；也可以生成命令手动安装。</div></div>
          <button className="btn" onClick={() => setBootOpen((v) => !v)}>{bootOpen ? "收起" : "＋ 添加构建机"}</button>
        </div>
        {bootOpen && (
          <div className="card-body">
            <div className="grid two">
              <label>构建机 id<input value={bootForm.id} onChange={(e) => setBootForm((f) => ({ ...f, id: e.target.value }))} /></label>
              <label>系统
                <select value={bootForm.os} onChange={(e) => setBootForm((f) => ({ ...f, os: e.target.value }))}>
                  <option value="auto">自动检测</option>
                  <option value="darwin">macOS</option>
                  <option value="linux">Linux</option>
                  <option value="windows">Windows</option>
                </select>
              </label>
              <label>架构
                <select value={bootForm.arch} onChange={(e) => setBootForm((f) => ({ ...f, arch: e.target.value }))}>
                  <option value="auto">自动检测</option>
                  <option value="arm64">arm64</option>
                  <option value="amd64">amd64</option>
                </select>
              </label>
              <label>SSH 主机<input value={bootForm.host} onChange={(e) => setBootForm((f) => ({ ...f, host: e.target.value }))} placeholder="build-01.example.com" /></label>
              <label>SSH 端口<input inputMode="numeric" value={bootForm.port} onChange={(e) => setBootForm((f) => ({ ...f, port: e.target.value.replace(/[^0-9]/g, "") }))} placeholder="22" /></label>
              <label>用户名<input value={bootForm.username} onChange={(e) => setBootForm((f) => ({ ...f, username: e.target.value }))} placeholder="builder 或 root" autoComplete="username" /></label>
              <label>认证方式
                <select value={bootForm.auth_type} onChange={(e) => setBootForm((f) => ({ ...f, auth_type: e.target.value, secret: "", passphrase: "" }))}>
                  <option value="password">密码</option>
                  <option value="private_key">SSH 私钥</option>
                </select>
              </label>
              <label className="wide">{bootForm.auth_type === "password" ? "SSH 密码" : "SSH 私钥"}
                <textarea rows={bootForm.auth_type === "password" ? 1 : 5} value={bootForm.secret} onChange={(e) => setBootForm((f) => ({ ...f, secret: e.target.value }))}
                  placeholder={bootForm.auth_type === "password" ? "只保存在请求里，勾选保存才写入资源库" : "-----BEGIN OPENSSH PRIVATE KEY-----"} spellCheck={false} />
              </label>
              {bootForm.auth_type === "private_key" && (
                <label className="wide">私钥口令（可选）<input type="password" value={bootForm.passphrase} onChange={(e) => setBootForm((f) => ({ ...f, passphrase: e.target.value }))} placeholder="私钥未加密时留空" autoComplete="new-password" /></label>
              )}
              <label>安装目录（留空用 ~/.smartpipe/release-executor）<input value={bootForm.install_dir} onChange={(e) => setBootForm((f) => ({ ...f, install_dir: e.target.value }))} placeholder="默认即可" /></label>
              <label>保存连接
                <select value={bootForm.save_resource ? "1" : "0"} onChange={(e) => setBootForm((f) => ({ ...f, save_resource: e.target.value === "1" }))}>
                  <option value="1">保存为 SSH 资源（标签 build/release）</option>
                  <option value="0">仅本次部署，不保存</option>
                </select>
              </label>
            </div>
            <div className="actions">
              <button className="btn primary" disabled={bootBusy} onClick={() => void deployExecutor()}>{bootBusy ? "部署中…" : "SSH 部署并注册"}</button>
              <button className="btn" disabled={bootBusy} onClick={() => void generateBootstrap()}>{bootBusy ? "处理中…" : "生成手动接入命令"}</button>
              {bootCommand && <button className="btn" onClick={() => void copyBootstrap()}>复制命令</button>}
            </div>
            {bootResult && (
              <div className={"notice " + (bootResult.ok ? "" : "warn")} style={{ marginTop: 10 }}>
                <div><b>{bootResult.ok ? "部署成功" : "部署失败"}</b>
                  {bootResult.host ? ` · ${bootResult.username}@${bootResult.host}` : ""}
                  {bootResult.os && bootResult.os !== "auto" ? ` · ${bootResult.os}/${bootResult.arch}` : ""}
                  {bootResult.install_dir ? ` · ${bootResult.install_dir}` : ""}
                </div>
                {bootResult.error && <div className="tiny">{bootResult.error}</div>}
                <ul className="tiny">
                  {bootResult.steps.map((step) => <li key={step.name}>{step.ok ? "✓" : "✗"} {step.name}{step.detail ? "：" + step.detail : ""}</li>)}
                </ul>
                {bootResult.log_tail && <pre className="boot-command">{bootResult.log_tail}</pre>}
              </div>
            )}
            {bootCommand && <pre className="boot-command">{bootCommand}</pre>}
            <p className="muted small">SSH 部署会连接构建机、上传对应平台的 release-executor、写入服务地址与令牌，并在后台启动；令牌只发给已登录管理员，不会回显。</p>
          </div>
        )}
        <div className="card-body" style={{ padding: 0 }}>
          <table className="table">
            <thead><tr><th>执行器</th><th>平台</th><th>状态</th><th>版本</th><th>最近心跳</th><th /></tr></thead>
            <tbody>
              {executors.length === 0 && <tr><td colSpan={6} className="empty">还没有执行器注册；在上面填 SSH 信息部署一台。</td></tr>}
              {executors.map((x) => (
                <tr key={x.id}>
                  <td>{x.id}</td>
                  <td>{x.os}/{x.arch}</td>
                  <td>{statusChip(x.status)}</td>
                  <td>{x.version || "—"}</td>
                  <td>{when(x.last_heartbeat_at)}</td>
                  <td>
                    <button className="btn danger-ghost small" disabled={removing === x.id} onClick={() => void removeExecutor(x.id)}>
                      {removing === x.id ? "删除中…" : "删除"}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <div className="card" style={{ display: view === "runs" ? undefined : "none" }}>
        <div className="card-head">
          <div><div className="card-title">发版运行</div><div className="card-sub">{error || "每次构建、校验、暂存与渠道推进都在这里留痕。"}</div></div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          <table className="table">
            <thead><tr><th>版本</th><th>模式</th><th>状态</th><th>渠道</th><th>提交</th><th>创建时间</th><th /></tr></thead>
            <tbody>
              {runs.length === 0 && <tr><td colSpan={7} className="empty">还没有发版运行。</td></tr>}
              {runs.map((r) => (
                <tr key={r.id}>
                  <td>{r.version}</td>
                  <td>{r.mode}</td>
                  <td>{statusChip(r.status)}</td>
                  <td>{r.channel}</td>
                  <td className="mono tiny">{shortHash(r.resolved_commit)}</td>
                  <td className="tiny">{when(r.created_at)}</td>
                  <td><button className="btn small" onClick={() => void openRun(r.id)}>详情</button></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {detail && view === "runs" && (
        <div className="card">
          <div className="card-head">
            <div>
              <div className="card-title">运行 {detail.run.id}</div>
              <div className="card-sub">
                {detail.run.version} · {detail.run.mode} · {detail.run.channel} · 提交 {shortHash(detail.run.resolved_commit)}
              </div>
            </div>
            <div className="btn-row">
              <button className="btn" disabled={busy} onClick={() => void openRun(detail.run.id)}>刷新</button>
              <button className="btn" disabled={busy} onClick={() => void issueToken(detail.run.id)}>签发 Agent Token</button>
              <button className="btn danger" disabled={busy} onClick={() => void cancelRun(detail.run.id)}>取消</button>
              <button className="btn" onClick={() => { stopStream(); setDetail(null); }}>关闭</button>
            </div>
          </div>
          <div className="card-body">
            {token && (
              <div className="notice">
                <div className="tiny">Token 只显示一次，请交给 Release Agent 运行时使用；服务端只保存哈希。</div>
                <code style={{ wordBreak: "break-all" }}>{token}</code>
              </div>
            )}
            {(detail.run.error_code || detail.run.error_message) && (
              <div className="notice bad tiny">{detail.run.error_code}：{detail.run.error_message}</div>
            )}
            <h4>发布 Agent</h4>
            <div className="tiny" style={{ marginBottom: 8 }}>
              {agent === null
                ? "读取 Agent 状态…"
                : agent.enabled
                  ? "由独立的 Codex 会话通过 release MCP 工具推进：构建、制品、升级包与发布。会话使用运行级短期 token，控制台只记录工具调用与结论。"
                  : "服务端未开启发布 Agent（设置 SMARTPIPE_RELEASE_AGENT_MCP_URL 后重启 admin-api）。"}
            </div>
            {agent?.session && (
              <div className="notice">
                <div>
                  <b>会话 {agent.session.id}</b> · {statusChip(agent.session.status)} · 模型 {agent.session.model || "默认"}
                  {agent.running ? " · 正在执行" : ""}
                </div>
                <div className="tiny">开始 {when(agent.session.started_at)} · 结束 {when(agent.session.finished_at)}</div>
                {agent.session.summary && <div className="tiny">结论：{agent.session.summary}</div>}
                {agent.session.last_error && <div className="tiny">错误：{agent.session.last_error}</div>}
              </div>
            )}
            <div className="btn-row" style={{ marginTop: 8 }}>
              <button className="btn primary" disabled={busy || !agent?.enabled || agent?.running} onClick={() => void startAgent(detail.run.id)}>
                启动发布 Agent
              </button>
              <span className="tiny">Agent 自动推进构建、灌库、渠道和 GitHub；失败时自动诊断修复，无法恢复才终止并给出根因。</span>
            </div>
            <h4>目标平台</h4>
            <table className="table">
              <thead><tr><th>目标</th><th>执行器</th><th>状态</th><th>尝试</th><th>错误</th></tr></thead>
              <tbody>
                {detail.targets.map((t) => (
                  <tr key={t.target_id}>
                    <td>{t.target_id}</td>
                    <td>{t.executor_id || "—"}</td>
                    <td>{statusChip(t.status)}</td>
                    <td>{t.attempt}</td>
                    <td className="tiny">{t.error_code || t.error_message || "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <h4>制品</h4>
            <table className="table">
              <thead><tr><th>角色</th><th>文件</th><th>大小</th><th>SHA-256</th></tr></thead>
              <tbody>
                {detail.artifacts.length === 0 && <tr><td colSpan={4} className="empty">尚未登记制品。</td></tr>}
                {detail.artifacts.map((a) => (
                  <tr key={a.id}>
                    <td>{a.role}</td>
                    <td className="mono tiny">{a.name}</td>
                    <td>{bytes(a.size)}</td>
                    <td className="mono tiny">{shortHash(a.sha256)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <h4>发布目标</h4>
            <table className="table">
              <thead><tr><th>提供方</th><th>状态</th><th>外部 ID</th><th>链接</th></tr></thead>
              <tbody>
                {(detail.publications ?? []).length === 0 && <tr><td colSpan={4} className="empty">尚未调用外部发布。</td></tr>}
                {(detail.publications ?? []).map((p) => (
                  <tr key={p.id}>
                    <td>{p.provider}</td>
                    <td>{statusChip(p.state)}</td>
                    <td className="mono tiny">{p.external_id || "—"}</td>
                    <td className="tiny">{p.external_url ? <a href={p.external_url} target="_blank" rel="noreferrer">{p.external_url}</a> : "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <h4>事件</h4>
            <table className="table">
              <thead><tr><th>时间</th><th>执行者</th><th>类型</th><th>摘要</th></tr></thead>
              <tbody>
                {detail.events.slice(-40).reverse().map((e) => (
                  <tr key={e.id}>
                    <td className="tiny">{when(e.created_at)}</td>
                    <td className="tiny">{e.actor}</td>
                    <td className="mono tiny">{e.kind}</td>
                    <td className="tiny">{e.summary}{e.target_id ? " · " + e.target_id : ""}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
      {configOpen && (
        <ReleaseV2ConfigModal
          project={projects.find((project) => project.id === form.project_id) ?? null}
          onClose={() => setConfigOpen(false)}
          onSaved={() => { void loadPreview(form.project_id); }}
          notify={notify}
        />
      )}
    </div>
  );
}

function mergeEvents(events: ReleaseV2Event[], next: ReleaseV2Event): ReleaseV2Event[] {
  if (events.some((ev) => ev.id === next.id)) return events;
  const out = [...events, next];
  out.sort((a, b) => a.id - b.id);
  return out.slice(-800);
}
