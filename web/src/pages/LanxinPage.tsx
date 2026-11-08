import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { LanxinConfig, LanxinGroup, LanxinMessage, Project } from "../types";
import type { Notify } from "../App";

type FormState = {
  app_id: string;
  app_secret: string;
  api_base: string;
  require_at_me: boolean;
};

const EMPTY: FormState = {
  app_id: "",
  app_secret: "",
  api_base: "https://open.e.lanxin.cn/open/apigw",
  require_at_me: true,
};

export function LanxinPage({ notify }: { notify: Notify }) {
  const [config, setConfig] = useState<LanxinConfig | null>(null);
  const [form, setForm] = useState<FormState>(EMPTY);
  const [projects, setProjects] = useState<Project[]>([]);
  const [groups, setGroups] = useState<LanxinGroup[]>([]);
  const [messages, setMessages] = useState<LanxinMessage[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [bindOpen, setBindOpen] = useState(false);
  const [bindGroup, setBindGroup] = useState("");
  const [bindProject, setBindProject] = useState("");
  const [busyGroup, setBusyGroup] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [cfg, projs, grps, msgs] = await Promise.all([
        api.getLanxinConfig(),
        api.listProjects(),
        api.listLanxinGroups(),
        api.listLanxinMessages(30),
      ]);
      setConfig(cfg);
      setProjects(projs);
      setGroups(grps);
      setMessages(msgs);
      setForm({
        app_id: cfg.app_id,
        app_secret: "",
        api_base: cfg.api_base || "https://open.e.lanxin.cn/open/apigw",
        require_at_me: cfg.require_at_me,
      });
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载蓝信配置失败");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const save = async () => {
    if (form.app_id.trim() === "") {
      notify("App ID 不能为空", "error");
      return;
    }
    setSaving(true);
    try {
      const updated = await api.saveLanxinConfig({
        app_id: form.app_id.trim(),
        app_secret: form.app_secret,
        api_base: form.api_base.trim(),
        require_at_me: form.require_at_me,
      });
      setConfig(updated);
      setForm((prev) => ({ ...prev, app_secret: "" }));
      notify("蓝信配置已保存，bot 会在 30 秒内自动重连");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "保存失败", "error");
    } finally {
      setSaving(false);
    }
  };

  const test = async () => {
    if (dirty) {
      notify("当前有未保存的修改，请先点「保存」再测试连接", "error");
      return;
    }
    setTesting(true);
    try {
      const res = await api.testLanxin();
      notify((res.ok ? "测试通过：" : "测试失败：") + res.message + "（" + res.latency_ms + "ms）", res.ok ? "ok" : "error");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "测试失败", "error");
    } finally {
      setTesting(false);
    }
  };

  const bind = async () => {
    if (bindGroup.trim() === "" || bindProject === "") {
      notify("群 ID 与项目都不能为空", "error");
      return;
    }
    setSaving(true);
    try {
      await api.bindLanxinGroup(bindGroup.trim(), bindProject);
      notify("群已绑定到项目");
      setBindOpen(false);
      setBindGroup("");
      setBindProject("");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "绑定失败", "error");
    } finally {
      setSaving(false);
    }
  };

  const unbind = async (group: string) => {
    if (!window.confirm("解绑群 " + group + "？解绑后该群消息不再路由到项目。")) return;
    setBusyGroup(group);
    try {
      await api.unbindLanxinGroup(group);
      notify("群已解绑");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "解绑失败", "error");
    } finally {
      setBusyGroup("");
    }
  };

  const removeConfig = async () => {
    if (!window.confirm("清除已保存的蓝信应用配置？bot 会回落到环境变量。")) return;
    try {
      await api.deleteLanxinConfig();
      notify("蓝信应用配置已清除");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "清除失败", "error");
    }
  };

  const configured = !!config?.has_secret;
  const dirty =
    form.app_id.trim() !== (config?.app_id ?? "") ||
    form.api_base.trim() !== (config?.api_base ?? "") ||
    form.app_secret !== "";
  const projectName = (id: string) => projects.find((p) => p.id === id)?.name ?? id;
  const statusLabel = (status: string) =>
    ({
      issue_created: "已建单",
      filtered: "已过滤",
      unbound: "未绑定项目",
      failed: "处理失败",
      received: "已接收",
    }[status] ?? status);

  if (loading) {
    return <div className="card"><div className="empty">加载蓝信配置…</div></div>;
  }
  if (error) {
    return (
      <div className="card">
        <div className="empty">
          <h3>无法读取蓝信配置</h3>
          <div className="tiny">{error}</div>
          <button className="btn" style={{ marginTop: 12 }} onClick={() => void load()}>重试</button>
        </div>
      </div>
    );
  }

  return (
    <>
      <div className="notice">
        <span>i</span>
        <div>
          <strong>蓝信机器人配置集中在这里。</strong>
          应用连接保存后会同步给 bot 并自动重连 WebSocket；群绑定决定消息路由到哪个项目，未绑定的群会回执提示。
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">应用连接</div>
            <div className="card-sub">
              {configured ? "已配置 · 密钥只显示掩码 · 修改后 30 秒内生效" : "未配置 · 先在蓝信桌面端创建个人机器人"}
            </div>
          </div>
          <div style={{ display: "flex", gap: 8 }}>
            <button className="btn" disabled={testing || !configured} onClick={() => void test()}>
              {testing ? "测试中…" : "测试连接"}
            </button>
            {configured && <button className="btn danger" onClick={() => void removeConfig()}>清除配置</button>}
            <button className="btn primary" disabled={saving} onClick={() => void save()}>
              {saving ? "保存中…" : dirty ? "保存修改" : "保存"}
            </button>
          </div>
        </div>
        <div className="card-body">
          {config?.last_tested_at && (
            <div
              style={{
                marginBottom: 14,
                padding: "10px 12px",
                borderRadius: 10,
                border: "1px solid " + (config.last_test_ok ? "#bfe6d0" : "#f6c7c7"),
                background: config.last_test_ok ? "var(--ok-soft)" : "var(--danger-soft)",
              }}
            >
              <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 4 }}>
                <span className={"chip " + (config.last_test_ok ? "ok" : "bad")}>
                  <span className="dot" />{config.last_test_ok ? "最近测试通过" : "最近测试失败"}
                </span>
                <span className="tiny">{new Date(config.last_tested_at).toLocaleString()}</span>
                <button className="btn small" style={{ marginLeft: "auto" }} disabled={testing || !configured} onClick={() => void test()}>
                  {testing ? "测试中…" : "重新测试"}
                </button>
              </div>
              <div style={{ color: config.last_test_ok ? "var(--ok)" : "var(--danger)", wordBreak: "break-word" }}>
                {config.last_test_message}
              </div>
              {!config.last_test_ok && (
                <div className="tiny" style={{ marginTop: 4 }}>
                  可按提示核对 App ID、App Secret 与 API Base；保存修改后再测试。
                </div>
              )}
            </div>
          )}
          <div className="form-grid">
            <div className="field">
              <label>App ID</label>
              <input value={form.app_id} onChange={(e) => setForm((p) => ({ ...p, app_id: e.target.value }))} placeholder="orgId-appId" />
            </div>
            <div className="field">
              <label>App Secret</label>
              <input
                type="password"
                value={form.app_secret}
                onChange={(e) => setForm((p) => ({ ...p, app_secret: e.target.value }))}
                placeholder={configured ? "已保存（" + config?.secret_mask + "），留空不修改" : "粘贴 App Secret"}
              />
            </div>
            <div className="field full">
              <label>API Base（网关地址，填到 /open/apigw 为止）</label>
              <input value={form.api_base} onChange={(e) => setForm((p) => ({ ...p, api_base: e.target.value }))} placeholder="https://open.e.lanxin.cn/open/apigw" />
              <div className="tiny" style={{ marginTop: 4 }}>
                公有云填 https://open.e.lanxin.cn/open/apigw；企业私有部署填管理员提供的网关地址（如 https://apigw.lx.qianxin.com）。无需填写 /v1 版本路径。
              </div>
            </div>
            <div className="field full">
              <label>群聊响应规则</label>
              <label style={{ display: "flex", alignItems: "center", gap: 8, fontWeight: 400, color: "var(--ink)" }}>
                <input
                  type="checkbox"
                  checked={form.require_at_me}
                  onChange={(e) => setForm((p) => ({ ...p, require_at_me: e.target.checked }))}
                />
                仅响应 @机器人 的群消息（关闭后群里每条消息都会触发）
              </label>
            </div>
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">群到项目绑定</div>
            <div className="card-sub">{groups.length} 个群已绑定 · 同一个群只能绑定一个项目</div>
          </div>
          <button className="btn primary" onClick={() => setBindOpen(true)}>＋ 绑定群</button>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {groups.length === 0 ? (
            <div className="empty">
              <h3>还没有绑定群</h3>
              <div className="tiny">绑定后，群里的消息会带上对应 project_id 创建 Issue 或崩溃单。</div>
              <button className="btn primary" style={{ marginTop: 14 }} onClick={() => setBindOpen(true)}>＋ 绑定群</button>
            </div>
          ) : (
            <table className="table">
              <thead>
                <tr>
                  <th>群 ID</th>
                  <th>项目</th>
                  <th>状态</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {groups.map((g) => (
                  <tr key={g.group_id + g.project_id}>
                    <td className="mono">{g.group_id}</td>
                    <td>
                      <div className="strong">{g.project_name}</div>
                      <div className="tiny mono">{projectName(g.project_id)}</div>
                    </td>
                    <td>
                      <span className={"chip " + (g.enabled ? "ok" : "bad")}>
                        <span className="dot" />{g.enabled ? "启用" : "项目停用"}
                      </span>
                    </td>
                    <td>
                      <div className="row-actions">
                        <button className="btn small" disabled={busyGroup === g.group_id} onClick={() => void unbind(g.group_id)}>解绑</button>
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
            <div className="card-title">最近消息</div>
            <div className="card-sub">蓝信入站消息与建单结果，最近 30 条</div>
          </div>
          <button className="btn" onClick={() => void load()}>刷新</button>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {messages.length === 0 ? (
            <div className="empty">还没有蓝信消息记录</div>
          ) : (
            <table className="table">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>会话</th>
                  <th>发送人</th>
                  <th>内容</th>
                  <th>项目</th>
                  <th>Issue</th>
                  <th>状态</th>
                </tr>
              </thead>
              <tbody>
                {messages.map((m) => (
                  <tr key={m.id}>
                    <td className="tiny">{new Date(m.created_at).toLocaleString()}</td>
                    <td className="mono">{m.group_name || m.chat_id}</td>
                    <td>{m.sender_name || <span className="mono">{m.user_id}</span>}</td>
                    <td className="tiny" title={m.text}>{m.text ? (m.text.length > 60 ? m.text.slice(0, 60) + "…" : m.text) : "—"}</td>
                    <td>{m.project_id ? projectName(m.project_id) : <span className="muted">未匹配</span>}</td>
                    <td>{m.issue_number ? "#" + m.issue_number : "—"}</td>
                    <td>
                      <span className={"chip " + (m.status === "issue_created" ? "ok" : m.status === "failed" ? "bad" : "warn")}>
                        <span className="dot" />{statusLabel(m.status)}
                      </span>
                      {m.reason && <div className="tiny">{m.reason}</div>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className={"modal-backdrop" + (bindOpen ? " open" : "")}>
        <div className="modal" style={{ width: "min(520px, 100%)" }}>
          <div className="modal-head">
            <div>
              <div className="modal-title">绑定群到项目</div>
              <div className="modal-sub">群 ID 来自蓝信会话；保存后会从其他项目移除同名群。</div>
            </div>
            <button className="icon-btn" onClick={() => setBindOpen(false)}>✕</button>
          </div>
          <div className="modal-body">
            <div className="field full" style={{ marginBottom: 12 }}>
              <label>群 ID</label>
              <input value={bindGroup} onChange={(e) => setBindGroup(e.target.value)} placeholder="grp-xxxxxxxx" />
            </div>
            <div className="field full">
              <label>项目</label>
              <select value={bindProject} onChange={(e) => setBindProject(e.target.value)}>
                <option value="">请选择项目</option>
                {projects.map((p) => (
                  <option key={p.id} value={p.id}>{p.name}</option>
                ))}
              </select>
            </div>
          </div>
          <div className="modal-foot">
            <div className="tiny">绑定写入项目配置，无需重启服务。</div>
            <div>
              <button className="btn" onClick={() => setBindOpen(false)}>取消</button>{" "}
              <button className="btn primary" disabled={saving} onClick={() => void bind()}>绑定</button>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}