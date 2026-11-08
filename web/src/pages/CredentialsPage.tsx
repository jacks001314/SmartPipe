import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import { CREDENTIAL_KINDS, type Credential, type GitHubAppSetup, type GitHubAppSetupStatus, type Project } from "../types";
import type { Notify } from "../App";

const KIND_LABEL: Record<string, string> = {
  github_app: "GitHub App",
  github_pat: "GitHub PAT",
  github_webhook_secret: "Webhook Secret",
  lanxin: "蓝信 Secret",
  git_ssh: "Git 只读",
  signing_key: "签名密钥",
};

type CreateForm = {
  kind: string;
  name: string;
  value: string;
  project_id: string;
  api_base: string;
  app_id: string;
  installation_id: string;
  base_url: string;
  repo_url: string;
};

const EMPTY: CreateForm = {
  kind: "github_app",
  name: "",
  value: "",
  project_id: "",
  api_base: "",
  app_id: "",
  installation_id: "",
  base_url: "http://127.0.0.1:8765",
  repo_url: "",
};

export function CredentialsPage({ notify }: { notify: Notify }) {
  const [items, setItems] = useState<Credential[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState<CreateForm>(EMPTY);
  const [busy, setBusy] = useState("");
  const [rotateTarget, setRotateTarget] = useState<Credential | null>(null);
  const [rotateValue, setRotateValue] = useState("");
  const [appSetup, setAppSetup] = useState<GitHubAppSetup | null>(null);
  const [appStatus, setAppStatus] = useState<GitHubAppSetupStatus | null>(null);
  const [appBusy, setAppBusy] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [creds, projs] = await Promise.all([api.listCredentials(), api.listProjects()]);
      setItems(creds);
      setProjects(projs);
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载凭据失败");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  useEffect(() => {
    if (!appSetup) return;
    let stopped = false;
    const tick = async () => {
      try {
        const res = await api.getGithubAppStatus(appSetup.setup_id);
        if (stopped) return;
        setAppStatus(res);
        if (res.status === "installed") {
          setAppSetup(null);
          notify("GitHub 已连接" + (res.app_slug ? "：" + res.app_slug : ""), "ok");
          await load();
        } else if (res.status === "expired" || res.status === "error") {
          setAppSetup(null);
          notify(res.message || (res.status === "expired" ? "连接已过期，请重新发起" : "GitHub 连接失败"), "error");
        }
      } catch (err) {
        if (stopped) return;
        setAppSetup(null);
        notify(err instanceof ApiError ? err.message : "查询 GitHub 连接状态失败", "error");
      }
    };
    void tick();
    const timer = window.setInterval(() => void tick(), Math.max(appSetup.poll_interval || 2, 2) * 1000);
    return () => {
      stopped = true;
      window.clearInterval(timer);
    };
  }, [appSetup, load, notify]);

  const connectGithub = async () => {
    const authWindow = window.open("about:blank", "_blank");
    setAppBusy(true);
    setAppStatus({ status: "pending", message: "正在创建 GitHub 连接" });
    try {
      const setup = await api.startGithubApp();
      setAppSetup(setup);
      if (authWindow) {
        authWindow.location.href = setup.launch_url;
      } else {
        notify("浏览器拦截了 GitHub 窗口，请点击页面里的连接链接继续", "error");
      }
    } catch (err) {
      authWindow?.close();
      setAppStatus(null);
      notify(err instanceof ApiError ? err.message : "发起 GitHub 连接失败", "error");
    } finally {
      setAppBusy(false);
    }
  };

  const githubAppCred = items.find((item) => item.kind === "github_app" && item.enabled);
  const githubAppSlug = typeof githubAppCred?.metadata.slug === "string" ? githubAppCred.metadata.slug : "";
  const githubAppOwner = typeof githubAppCred?.metadata.owner === "string" ? githubAppCred.metadata.owner : "";
  const appReopenUrl = appStatus?.status === "created" && appStatus.app_slug
    ? "https://github.com/apps/" + encodeURIComponent(appStatus.app_slug) + "/installations/new"
    : appSetup?.launch_url || "";

  const projectName = (id: string | null) => {
    if (!id) return "全局";
    return projects.find((p) => p.id === id)?.name ?? id;
  };

  const buildMetadata = () => {
    const meta: Record<string, unknown> = {};
    if (form.api_base.trim()) meta.api_base = form.api_base.trim();
    if (form.app_id.trim()) meta.app_id = form.app_id.trim();
    if (form.installation_id.trim()) meta.installation_id = form.installation_id.trim();
    if (form.base_url.trim()) meta.base_url = form.base_url.trim();
    if (form.repo_url.trim()) meta.repo_url = form.repo_url.trim();
    if (form.kind === "model_provider" && form.api_base.trim()) meta.base_url = form.api_base.trim();
    return meta;
  };

  const submit = async () => {
    if (form.name.trim() === "" || form.value === "") {
      notify("名称和凭据值都不能为空", "error");
      return;
    }
    setBusy("create");
    try {
      await api.createCredential({
        kind: form.kind,
        name: form.name.trim(),
        value: form.value,
        project_id: form.project_id || null,
        metadata: buildMetadata(),
      });
      notify("凭据已保存（密文）");
      setOpen(false);
      setForm(EMPTY);
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "保存失败", "error");
    } finally {
      setBusy("");
    }
  };

  const test = async (cred: Credential) => {
    setBusy(cred.id);
    try {
      const res = await api.testCredential(cred.id);
      notify((res.ok ? "测试通过：" : "测试失败：") + res.message + "（" + res.latency_ms + "ms）", res.ok ? "ok" : "error");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "测试失败", "error");
    } finally {
      setBusy("");
    }
  };

  const toggle = async (cred: Credential) => {
    try {
      await api.setCredentialEnabled(cred.id, !cred.enabled);
      notify(cred.enabled ? "凭据已停用" : "凭据已启用");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "操作失败", "error");
    }
  };

  const remove = async (cred: Credential) => {
    if (!window.confirm("删除凭据 " + cred.name + "？使用它的任务会立即失败。")) return;
    try {
      await api.deleteCredential(cred.id);
      notify("凭据已删除");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "删除失败", "error");
    }
  };

  const doRotate = async () => {
    if (!rotateTarget || rotateValue === "") return;
    setBusy(rotateTarget.id);
    try {
      await api.rotateCredential(rotateTarget.id, rotateValue);
      notify("凭据已轮换，旧值立即失效");
      setRotateTarget(null);
      setRotateValue("");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "轮换失败", "error");
    } finally {
      setBusy("");
    }
  };

  const kindHint = CREDENTIAL_KINDS.find((k) => k.value === form.kind)?.hint ?? "";
  const needApp = form.kind === "github_app" || form.kind === "lanxin";

  return (
    <>
      <div className="notice">
        <span>i</span>
        <div>
          <strong>凭据只以密文保存。</strong>
          保存后接口只返回 <span className="mono">****</span> 掩码；连接测试由服务端发起，错误信息会脱敏。
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">GitHub 接入</div>
            <div className="card-sub">在控制台发起连接，浏览器完成 GitHub 登录确认后自动回到这里；应用凭据由后台加密保存。</div>
          </div>
          {githubAppCred && (
            <span className="chip ok"><span className="dot" />已连接{githubAppSlug ? " · " + githubAppSlug : ""}</span>
          )}
        </div>
        <div className="card-body">
          {appSetup ? (
            <>
              <div>已打开 GitHub 连接流程。请在弹出窗口登录并确认创建、安装 SmartPipe GitHub App。</div>
              <div className="tiny" style={{ marginTop: 8 }}>
                {appStatus?.message || "等待 GitHub 返回…"}
              </div>
              <div className="row-actions" style={{ marginTop: 12 }}>
                <a className="btn" href={appReopenUrl} target="_blank" rel="noreferrer">重新打开 GitHub</a>
                <button className="btn" onClick={() => { setAppSetup(null); setAppStatus(null); }}>取消</button>
              </div>
            </>
          ) : githubAppCred ? (
            <>
              <div>
                {githubAppOwner ? <><b>{githubAppOwner}</b> / </> : null}
                <b>{githubAppSlug || "GitHub App"}</b> 已安装。后台会按需自动换取 installation token，不需要手工维护 PAT。
              </div>
              <div className="row-actions" style={{ marginTop: 12 }}>
                <button className="btn" disabled={appBusy} onClick={() => void connectGithub()}>重新连接</button>
              </div>
            </>
          ) : (
            <>
              <div>
                点“连接 GitHub”后，系统会打开 GitHub，由你完成登录、创建应用和选择仓库。确认完成后自动返回产品，App ID、私钥和安装编号会加密写入凭据库。
              </div>
              <div className="tiny" style={{ marginTop: 8 }}>
                不需要填写 Client ID、安装编号，也不需要手工创建 PAT。
              </div>
              <div className="row-actions" style={{ marginTop: 12 }}>
                <button className="btn primary" disabled={appBusy} onClick={() => void connectGithub()}>
                  {appBusy ? "正在准备…" : "连接 GitHub"}
                </button>
              </div>
            </>
          )}
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">凭据列表</div>
            <div className="card-sub">{items.length} 条记录 · 掩码展示 · 支持轮换与停用</div>
          </div>
          <div style={{ display: "flex", gap: 8 }}>
            <button className="btn" onClick={() => void load()}>刷新</button>
            <button className="btn primary" onClick={() => { setForm(EMPTY); setOpen(true); }}>＋ 新增凭据</button>
          </div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {loading ? (
            <div className="empty">加载中…</div>
          ) : error ? (
            <div className="empty">
              <h3>无法读取凭据</h3>
              <div className="tiny">{error}</div>
            </div>
          ) : items.length === 0 ? (
            <div className="empty">
              <h3>还没有凭据</h3>
              <div className="tiny">先登记 GitHub App、蓝信 Secret 等凭据，连接测试通过后再接入项目。</div>
              <button className="btn primary" style={{ marginTop: 14 }} onClick={() => setOpen(true)}>＋ 新增凭据</button>
            </div>
          ) : (
            <table className="table">
              <thead>
                <tr>
                  <th>凭据</th>
                  <th>类型</th>
                  <th>作用域</th>
                  <th>掩码</th>
                  <th>最近测试</th>
                  <th>状态</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {items.map((c) => (
                  <tr key={c.id}>
                    <td>
                      <div className="strong">{c.name}</div>
                      <div className="tiny mono">{c.id}</div>
                    </td>
                    <td><span className="chip">{KIND_LABEL[c.kind] ?? c.kind}</span></td>
                    <td>{projectName(c.project_id)}</td>
                    <td className="mono">{c.mask || "****"}</td>
                    <td>
                      {c.last_tested_at ? (
                        <>
                          <span className={"chip " + (c.last_test_ok ? "ok" : "bad")}>
                            <span className="dot" />{c.last_test_ok ? "通过" : "失败"}
                          </span>
                          <div className="tiny">{c.last_test_message}</div>
                        </>
                      ) : (
                        <span className="muted">未测试</span>
                      )}
                    </td>
                    <td>
                      <span className={"chip " + (c.enabled ? "ok" : "bad")}>
                        <span className="dot" />{c.enabled ? "启用" : "停用"}
                      </span>
                    </td>
                    <td>
                      <div className="row-actions">
                        <button className="btn small" disabled={busy === c.id} onClick={() => void test(c)}>测试</button>
                        <button className="btn small" onClick={() => { setRotateTarget(c); setRotateValue(""); }}>轮换</button>
                        <button className="btn small" onClick={() => void toggle(c)}>{c.enabled ? "停用" : "启用"}</button>
                        <button className="btn small danger" onClick={() => void remove(c)}>删除</button>
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
              <div className="modal-title">新增凭据</div>
              <div className="modal-sub">明文只在本次请求中传输，落库前加密。{kindHint}</div>
            </div>
            <button className="icon-btn" onClick={() => setOpen(false)}>✕</button>
          </div>
          <div className="modal-body">
            <div className="form-grid">
              <div className="field">
                <label>类型</label>
                <select value={form.kind} onChange={(e) => setForm((p) => ({ ...p, kind: e.target.value }))}>
                  {CREDENTIAL_KINDS.map((k) => (
                    <option key={k.value} value={k.value}>{k.label}</option>
                  ))}
                </select>
              </div>
              <div className="field">
                <label>作用域</label>
                <select value={form.project_id} onChange={(e) => setForm((p) => ({ ...p, project_id: e.target.value }))}>
                  <option value="">全局</option>
                  {projects.map((p) => (
                    <option key={p.id} value={p.id}>{p.name}</option>
                  ))}
                </select>
              </div>
              <div className="field">
                <label>名称</label>
                <input value={form.name} onChange={(e) => setForm((p) => ({ ...p, name: e.target.value }))} placeholder="core-github-app" />
              </div>
              <div className="field">
                <label>凭据值</label>
                <input
                  type="password"
                  value={form.value}
                  onChange={(e) => setForm((p) => ({ ...p, value: e.target.value }))}
                  placeholder="粘贴密钥 / 私钥 / token"
                />
              </div>
              {needApp && (
                <>
                  <div className="field">
                    <label>App ID</label>
                    <input value={form.app_id} onChange={(e) => setForm((p) => ({ ...p, app_id: e.target.value }))} placeholder="123456" />
                  </div>
                  <div className="field">
                    <label>{form.kind === "github_app" ? "Installation ID" : "API Base"}</label>
                    {form.kind === "github_app" ? (
                      <input value={form.installation_id} onChange={(e) => setForm((p) => ({ ...p, installation_id: e.target.value }))} placeholder="78910" />
                    ) : (
                      <input value={form.api_base} onChange={(e) => setForm((p) => ({ ...p, api_base: e.target.value }))} placeholder="https://open.e.lanxin.cn/open/apigw" />
                    )}
                  </div>
                </>
              )}
              {form.kind === "model_provider" && (
                <div className="field full">
                  <label>API Base URL</label>
                  <input value={form.api_base} onChange={(e) => setForm((p) => ({ ...p, api_base: e.target.value }))} placeholder="https://api.openai.com/v1" />
                </div>
              )}
              {form.kind === "github_app" && (
                <div className="field full">
                  <label>API Base（GitHub Enterprise 才需要改）</label>
                  <input value={form.api_base} onChange={(e) => setForm((p) => ({ ...p, api_base: e.target.value }))} placeholder="https://api.github.com" />
                </div>
              )}
              {form.kind === "git_ssh" && (
                <div className="field full">
                  <label>仓库地址（用于 ls-remote 探测）</label>
                  <input value={form.repo_url} onChange={(e) => setForm((p) => ({ ...p, repo_url: e.target.value }))} placeholder="git@github.com:org/repo.git" />
                </div>
              )}
            </div>
          </div>
          <div className="modal-foot">
            <div className="tiny">保存后只显示末 4 位掩码，无法读回明文。</div>
            <div>
              <button className="btn" onClick={() => setOpen(false)}>取消</button>{" "}
              <button className="btn primary" disabled={busy === "create"} onClick={() => void submit()}>
                {busy === "create" ? "保存中…" : "保存"}
              </button>
            </div>
          </div>
        </div>
      </div>

      <div className={"modal-backdrop" + (rotateTarget ? " open" : "")}>
        <div className="modal" style={{ width: "min(480px, 100%)" }}>
          <div className="modal-head">
            <div>
              <div className="modal-title">轮换凭据</div>
              <div className="modal-sub">新值立即生效，旧值即刻失效。</div>
            </div>
            <button className="icon-btn" onClick={() => setRotateTarget(null)}>✕</button>
          </div>
          <div className="modal-body">
            <div className="field full">
              <label>{rotateTarget?.name}</label>
              <input type="password" value={rotateValue} onChange={(e) => setRotateValue(e.target.value)} placeholder="新的凭据值" />
            </div>
          </div>
          <div className="modal-foot">
            <div className="tiny">该操作会写入审计。</div>
            <div>
              <button className="btn" onClick={() => setRotateTarget(null)}>取消</button>{" "}
              <button className="btn primary" disabled={rotateValue === ""} onClick={() => void doRotate()}>确认轮换</button>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}
