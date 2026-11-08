import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { LanxinBot } from "../types";
import type { Notify } from "../App";
import { LanxinBotDetailPage } from "./LanxinBotDetailPage";

type CreateForm = {
  name: string;
  app_id: string;
  app_secret: string;
  api_base: string;
  require_at_me: boolean;
};

const EMPTY: CreateForm = {
  name: "",
  app_id: "",
  app_secret: "",
  api_base: "https://apigw.lx.qianxin.com",
  require_at_me: true,
};

const statusLabel = (status: string) => ({
  connected: "已连接",
  connecting: "连接中",
  disconnected: "未连接",
  disabled: "已停用",
  error: "连接异常",
}[status] ?? status);

export function LanxinPage({ notify }: { notify: Notify }) {
  const [bots, setBots] = useState<LanxinBot[]>([]);
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [error, setError] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [form, setForm] = useState<CreateForm>(EMPTY);
  const [saving, setSaving] = useState(false);
  const [selected, setSelected] = useState("");

  const load = useCallback(async (refreshGroups = false) => {
    setLoading(true);
    if (refreshGroups) setSyncing(true);
    try {
      setBots(await api.listLanxinBots(refreshGroups));
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载蓝信机器人失败");
    } finally {
      setLoading(false);
      setSyncing(false);
    }
  }, []);

  useEffect(() => { void load(true); }, [load]);

  const syncGroups = async () => {
    setSyncing(true);
    try {
      await api.listLanxinBots(true);
      notify("所有机器人的群信息已刷新");
      await load(false);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "同步群信息失败", "error");
    } finally {
      setSyncing(false);
    }
  };

  const create = async () => {
    if (!form.name.trim() || !form.app_id.trim() || !form.app_secret.trim()) {
      notify("机器人名称、App ID 和 App Secret 都不能为空", "error");
      return;
    }
    setSaving(true);
    try {
      const bot = await api.createLanxinBot({
        name: form.name.trim(), app_id: form.app_id.trim(), app_secret: form.app_secret.trim(),
        api_base: form.api_base.trim(), require_at_me: form.require_at_me,
      });
      notify("机器人已添加，正在同步所在群");
      setCreateOpen(false);
      setForm(EMPTY);
      await load(false);
      setSelected(bot.id);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "添加机器人失败", "error");
    } finally {
      setSaving(false);
    }
  };

  const toggle = async (bot: LanxinBot) => {
    try {
      await api.setLanxinBotEnabled(bot.id, !bot.enabled);
      notify(bot.enabled ? "机器人已停用" : "机器人已启用");
      await load(false);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "操作失败", "error");
    }
  };

  if (selected) {
    return (
      <LanxinBotDetailPage
        botId={selected}
        notify={notify}
        onBack={() => setSelected("")}
        onDeleted={() => { setSelected(""); void load(false); }}
      />
    );
  }

  if (loading) return <div className="card"><div className="empty">{syncing ? "正在自动同步机器人所在群…" : "加载蓝信机器人…"}</div></div>;
  if (error) {
    return <div className="card"><div className="empty"><h3>无法读取蓝信机器人</h3><div className="tiny">{error}</div><button className="btn" style={{ marginTop: 12 }} onClick={() => void load(false)}>重试</button></div></div>;
  }

  return (
    <>
      <div className="notice">
        <span>i</span>
        <div><strong>这里管理蓝信机器人，不再维护群到项目绑定。</strong>添加机器人或打开页面时会自动拉取机器人所在群；群绑定在创建或编辑项目时选择。</div>
      </div>

      <div className="stat-grid">
        <div className="stat"><div className="stat-label">机器人总数</div><div className="stat-value">{bots.length}</div><div className="stat-foot">{bots.filter((b) => b.enabled).length} 个启用</div></div>
        <div className="stat"><div className="stat-label">在线连接</div><div className="stat-value">{bots.filter((b) => b.status === "connected").length}</div><div className="stat-foot">每 30 秒自动重连/应用配置</div></div>
        <div className="stat"><div className="stat-label">已发现群</div><div className="stat-value">{bots.reduce((n, b) => n + b.group_count, 0)}</div><div className="stat-foot">打开页面自动同步</div></div>
        <div className="stat"><div className="stat-label">收到消息</div><div className="stat-value">{bots.reduce((n, b) => n + b.message_count, 0)}</div><div className="stat-foot">按机器人归档</div></div>
      </div>

      <div className="card">
        <div className="card-head">
          <div><div className="card-title">机器人列表</div><div className="card-sub">点击机器人名称查看连接详情、所在群与聊天消息。</div></div>
          <div style={{ display: "flex", gap: 8 }}>
            <button className="btn" disabled={syncing} onClick={() => void syncGroups()}>{syncing ? "同步中…" : "刷新群信息"}</button>
            <button className="btn primary" onClick={() => { setForm(EMPTY); setCreateOpen(true); }}>＋ 添加机器人</button>
          </div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {bots.length === 0 ? (
            <div className="empty"><h3>还没有蓝信机器人</h3><div className="tiny">添加个人机器人后，系统会自动连接 WebSocket 并同步所在群。</div><button className="btn primary" style={{ marginTop: 14 }} onClick={() => { setForm(EMPTY); setCreateOpen(true); }}>＋ 添加机器人</button></div>
          ) : (
            <table className="table">
              <thead><tr><th>机器人</th><th>App ID</th><th>连接状态</th><th>所在群</th><th>消息</th><th>最近群同步</th><th></th></tr></thead>
              <tbody>
                {bots.map((bot) => (
                  <tr key={bot.id}>
                    <td><button className="btn small" style={{ fontWeight: 600 }} onClick={() => setSelected(bot.id)}>{bot.name}</button><div className="tiny mono">{bot.id}</div></td>
                    <td className="mono tiny">{bot.app_id}</td>
                    <td><span className={"chip " + (bot.status === "connected" ? "ok" : bot.status === "error" ? "bad" : "warn")}><span className="dot" />{statusLabel(bot.status)}</span>{bot.last_error && <div className="tiny" style={{ color: "var(--danger)" }}>{bot.last_error}</div>}</td>
                    <td>{bot.group_count}</td>
                    <td>{bot.message_count}</td>
                    <td className="tiny">{bot.last_group_sync_at ? new Date(bot.last_group_sync_at).toLocaleString() : "尚未同步"}{bot.last_sync_error && <div style={{ color: "var(--danger)" }}>{bot.last_sync_error}</div>}</td>
                    <td><div className="row-actions"><button className="btn small" onClick={() => setSelected(bot.id)}>详情</button><button className="btn small" onClick={() => void toggle(bot)}>{bot.enabled ? "停用" : "启用"}</button></div></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className={"modal-backdrop" + (createOpen ? " open" : "")}>
        <div className="modal" style={{ width: "min(640px, 100%)" }}>
          <div className="modal-head"><div><div className="modal-title">添加蓝信机器人</div><div className="modal-sub">保存后立即测试连接、同步所在群，并启动独立 WebSocket。</div></div><button className="icon-btn" onClick={() => setCreateOpen(false)}>✕</button></div>
          <div className="modal-body">
            <div className="form-grid">
              <div className="field"><label>机器人名称</label><input value={form.name} onChange={(e) => setForm((p) => ({ ...p, name: e.target.value }))} placeholder="研发群机器人" /></div>
              <div className="field"><label>App ID</label><input value={form.app_id} onChange={(e) => setForm((p) => ({ ...p, app_id: e.target.value }))} placeholder="orgId-appId" /></div>
              <div className="field full"><label>App Secret</label><input type="password" value={form.app_secret} onChange={(e) => setForm((p) => ({ ...p, app_secret: e.target.value }))} placeholder="粘贴机器人 App Secret" /></div>
              <div className="field full"><label>API Base</label><input value={form.api_base} onChange={(e) => setForm((p) => ({ ...p, api_base: e.target.value }))} placeholder="https://apigw.lx.qianxin.com" /><div className="tiny">无需填写 /v1；系统会自动读取群列表和群详情。</div></div>
              <div className="field full"><label style={{ display: "flex", gap: 8, alignItems: "center", fontWeight: 400 }}><input type="checkbox" checked={form.require_at_me} onChange={(e) => setForm((p) => ({ ...p, require_at_me: e.target.checked }))} />群聊仅响应 @机器人 的消息</label></div>
            </div>
          </div>
          <div className="modal-foot"><div className="tiny">群 ID 由系统自动同步，用户无需填写。</div><div><button className="btn" onClick={() => setCreateOpen(false)}>取消</button>{" "}<button className="btn primary" disabled={saving} onClick={() => void create()}>{saving ? "添加中…" : "添加机器人"}</button></div></div>
        </div>
      </div>
    </>
  );
}
