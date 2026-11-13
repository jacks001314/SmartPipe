import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { LanxinBot, LanxinBotDetail, LanxinGroup } from "../types";
import type { Notify } from "../App";

type GroupPolicyForm = {
  response_mode: "at_only" | "smart" | "all";
  shadow_mode: boolean;
  reply_cooldown_seconds: number;
  hourly_reply_limit: number;
  auto_issue_enabled: boolean;
  auto_issue_confidence: number;
  daily_auto_issue_limit: number;
  answer_confidence: number;
};

const MODE_LABEL: Record<string, string> = {
  at_only: "@ 或命令",
  smart: "智能观察",
  all: "全部处理",
};

type EditForm = {
  name: string;
  app_id: string;
  app_secret: string;
  api_base: string;
  require_at_me: boolean;
};

const statusLabel = (status: string) => ({
  connected: "已连接",
  connecting: "连接中",
  disconnected: "未连接",
  disabled: "已停用",
  error: "连接异常",
}[status] ?? status);

const messageStatus = (status: string) => ({
  issue_created: "已建单",
  issue_updated: "已补充",
  answered: "已回答",
  need_info: "待补充",
  awaiting_confirmation: "待确认建单",
  processing: "处理中",
  ignored: "已忽略",
  silent: "判断沉默",
  shadow_silent: "影子-沉默",
  shadow_answer: "影子-候选回答",
  shadow_triage: "影子-候选建单",
  triage_candidate: "待评估候选",
  rate_limited: "限额跳过",
  deduped_bot: "其他机器人已处理",
  control_blocked: "控制命令已拦截",
  filtered: "已过滤",
  unbound: "未绑定项目",
  failed: "处理失败",
  received: "已接收",
}[status] ?? status);

export function LanxinBotDetailPage({
  botId,
  notify,
  onBack,
  onDeleted,
}: {
  botId: string;
  notify: Notify;
  onBack: () => void;
  onDeleted: () => void;
}) {
  const [detail, setDetail] = useState<LanxinBotDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [syncing, setSyncing] = useState(false);
  const [testing, setTesting] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [form, setForm] = useState<EditForm>({ name: "", app_id: "", app_secret: "", api_base: "", require_at_me: true });
  const [saving, setSaving] = useState(false);
  const [policyGroup, setPolicyGroup] = useState<LanxinGroup | null>(null);
  const [policyForm, setPolicyForm] = useState<GroupPolicyForm>({
    response_mode: "smart", shadow_mode: false, reply_cooldown_seconds: 30, hourly_reply_limit: 6,
    auto_issue_enabled: false, auto_issue_confidence: 0.85, daily_auto_issue_limit: 3, answer_confidence: 0.55,
  });
  const [savingPolicy, setSavingPolicy] = useState(false);

  const load = useCallback(async (sync = false) => {
    setLoading(true);
    try {
      const data = await api.getLanxinBot(botId, sync);
      setDetail(data);
      setForm({
        name: data.bot.name,
        app_id: data.bot.app_id,
        app_secret: "",
        api_base: data.bot.api_base,
        require_at_me: data.bot.require_at_me,
      });
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载机器人详情失败");
    } finally {
      setLoading(false);
    }
  }, [botId]);

  useEffect(() => { void load(true); }, [load]);

  const syncGroups = async () => {
    setSyncing(true);
    try {
      const res = await api.syncLanxinBotGroups(botId);
      notify("群信息已同步，共 " + res.groups.length + " 个群");
      await load(false);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "同步群失败", "error");
    } finally {
      setSyncing(false);
    }
  };

  const test = async () => {
    setTesting(true);
    try {
      const res = await api.testLanxinBot(botId);
      notify((res.ok ? "连接正常：" : "连接失败：") + res.message + "（" + res.latency_ms + "ms）", res.ok ? "ok" : "error");
      await load(false);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "连接测试失败", "error");
    } finally {
      setTesting(false);
    }
  };

  const save = async () => {
    if (form.name.trim() === "" || form.app_id.trim() === "") {
      notify("机器人名称和 App ID 不能为空", "error");
      return;
    }
    setSaving(true);
    try {
      await api.updateLanxinBot(botId, {
        name: form.name.trim(),
        app_id: form.app_id.trim(),
        app_secret: form.app_secret,
        api_base: form.api_base.trim(),
        require_at_me: form.require_at_me,
      });
      notify("机器人配置已保存");
      setEditOpen(false);
      await load(true);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "保存失败", "error");
    } finally {
      setSaving(false);
    }
  };

  const openPolicy = (g: LanxinGroup) => {
    setPolicyGroup(g);
    setPolicyForm({
      response_mode: g.response_mode || "smart",
      shadow_mode: g.shadow_mode,
      reply_cooldown_seconds: g.reply_cooldown_seconds,
      hourly_reply_limit: g.hourly_reply_limit,
      auto_issue_enabled: g.auto_issue_enabled,
      auto_issue_confidence: g.auto_issue_confidence,
      daily_auto_issue_limit: g.daily_auto_issue_limit,
      answer_confidence: g.answer_confidence,
    });
  };

  const savePolicy = async () => {
    if (!policyGroup) return;
    setSavingPolicy(true);
    try {
      const updated = await api.updateLanxinGroupPolicy({
        group_id: policyGroup.group_id,
        ...policyForm,
      });
      setDetail((prev) => prev ? { ...prev, groups: prev.groups.map((g) => g.group_id === updated.group_id ? { ...g, ...updated } : g) } : prev);
      setPolicyGroup(null);
      notify("群响应策略已保存");
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "保存群策略失败", "error");
    } finally {
      setSavingPolicy(false);
    }
  };

  const toggle = async () => {
    if (!detail) return;
    try {
      await api.setLanxinBotEnabled(botId, !detail.bot.enabled);
      notify(detail.bot.enabled ? "机器人已停用" : "机器人已启用");
      await load(false);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "操作失败", "error");
    }
  };

  const remove = async () => {
    if (!detail || !window.confirm("删除机器人「" + detail.bot.name + "」？凭据、群快照和消息归属会一并移除。")) return;
    try {
      await api.deleteLanxinBot(botId);
      notify("机器人已删除");
      onDeleted();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "删除失败", "error");
    }
  };

  if (loading && !detail) return <div className="card"><div className="empty">正在加载机器人并同步群信息…</div></div>;
  if (error || !detail) {
    return (
      <div className="card">
        <div className="empty">
          <h3>无法读取机器人</h3>
          <div className="tiny">{error || "机器人不存在"}</div>
          <button className="btn" style={{ marginTop: 12 }} onClick={() => void load(false)}>重试</button>
          <button className="btn" style={{ marginLeft: 8 }} onClick={onBack}>返回列表</button>
        </div>
      </div>
    );
  }

  const bot: LanxinBot = detail.bot;
  const statusTone = bot.status === "connected" ? "ok" : bot.status === "error" ? "bad" : "warn";

  return (
    <>
      <div style={{ display: "flex", alignItems: "center", gap: 10, marginBottom: 14 }}>
        <button className="btn" onClick={onBack}>← 机器人列表</button>
        <span className="tiny">蓝信配置 / 机器人详情</span>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">{bot.name}</div>
            <div className="card-sub mono">{bot.id} · {bot.app_id}</div>
          </div>
          <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
            <span className={"chip " + statusTone}><span className="dot" />{statusLabel(bot.status)}</span>
            <button className="btn" disabled={testing} onClick={() => void test()}>{testing ? "测试中…" : "测试连接"}</button>
            <button className="btn" disabled={syncing} onClick={() => void syncGroups()}>{syncing ? "同步中…" : "同步群信息"}</button>
            <button className="btn" onClick={() => setEditOpen(true)}>编辑</button>
            <button className="btn" onClick={() => void toggle()}>{bot.enabled ? "停用" : "启用"}</button>
            <button className="btn danger" onClick={() => void remove()}>删除</button>
          </div>
        </div>
        <div className="card-body">
          <div className="stat-grid" style={{ marginBottom: 12 }}>
            <div className="stat"><div className="stat-label">所在群</div><div className="stat-value">{detail.groups.length}</div><div className="stat-foot">自动从蓝信同步</div></div>
            <div className="stat"><div className="stat-label">消息记录</div><div className="stat-value">{detail.messages.length}</div><div className="stat-foot">最近 100 条</div></div>
            <div className="stat"><div className="stat-label">群响应策略</div><div className="stat-value" style={{ fontSize: 18 }}>{detail.groups.filter((g) => g.response_mode !== "at_only").length} 个群观察</div><div className="stat-foot">按群配置，见下方策略列</div></div>
            <div className="stat"><div className="stat-label">最近同步</div><div className="stat-value" style={{ fontSize: 16 }}>{bot.last_group_sync_at ? new Date(bot.last_group_sync_at).toLocaleString() : "—"}</div><div className="stat-foot">打开详情时自动刷新</div></div>
          </div>
          <div className="tiny mono">API Base：{bot.api_base}</div>
          {bot.last_error && <div className="tiny" style={{ color: "var(--danger)", marginTop: 6 }}>连接错误：{bot.last_error}</div>}
          {bot.last_sync_error && <div className="tiny" style={{ color: "var(--danger)", marginTop: 6 }}>群同步：{bot.last_sync_error}</div>}
          {bot.last_tested_at && (
            <div className="tiny" style={{ marginTop: 6 }}>
              最近测试：{bot.last_test_ok === false ? "失败" : "通过"} · {new Date(bot.last_tested_at).toLocaleString()} · {bot.last_test_message}
            </div>
          )}
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">机器人所在群</div>
            <div className="card-sub">每次打开页面或点击同步都会从蓝信重新拉取，不需要手工填写群 ID。</div>
          </div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {detail.groups.length === 0 ? (
            <div className="empty">当前没有同步到群。确认机器人已加入群，并检查群列表接口权限。</div>
          ) : (
            <table className="table">
              <thead><tr><th>群名称</th><th>群 ID</th><th>成员</th><th>项目绑定</th><th>响应策略</th><th>限额</th><th>同步时间</th></tr></thead>
              <tbody>
                {detail.groups.map((g) => (
                  <tr key={g.id}>
                    <td><div className="strong">{g.name || "(未命名群)"}</div>{g.description && <div className="tiny">{g.description}</div>}</td>
                    <td className="mono tiny">{g.group_id}</td>
                    <td>{g.member_count || "—"}</td>
                    <td>{g.project_id ? <span className="chip ok">{g.project_name || g.project_id}</span> : <span className="muted">未绑定项目</span>}</td>
                    <td>
                      <div>{MODE_LABEL[g.response_mode] || g.response_mode}{g.shadow_mode && <span className="chip warn" style={{ marginLeft: 6 }}>影子</span>}</div>
                      <button className="btn" style={{ marginTop: 4 }} onClick={() => openPolicy(g)}>编辑策略</button>
                    </td>
                    <td className="tiny">{g.hourly_reply_limit} 条/小时<br />冷却 {g.reply_cooldown_seconds}s<br />自动建单 {g.daily_auto_issue_limit}/日</td>
                    <td className="tiny">{new Date(g.synced_at).toLocaleString()}</td>
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
            <div className="card-title">聊天消息</div>
            <div className="card-sub">该机器人收到的最近消息及建单结果。</div>
          </div>
          <button className="btn" onClick={() => void load(false)}>刷新消息</button>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {detail.messages.length === 0 ? (
            <div className="empty">还没有消息记录</div>
          ) : (
            <table className="table">
              <thead><tr><th>时间</th><th>群 / 会话</th><th>发送人</th><th>内容</th><th>项目</th><th>Issue</th><th>状态</th></tr></thead>
              <tbody>
                {detail.messages.map((m) => (
                  <tr key={m.id}>
                    <td className="tiny">{new Date(m.created_at).toLocaleString()}</td>
                    <td><div>{m.group_name || m.chat_id}</div><div className="tiny mono">{m.chat_id}</div></td>
                    <td>{m.sender_name || <span className="mono tiny">{m.user_id}</span>}</td>
                    <td className="tiny" title={m.text}>{m.text ? (m.text.length > 80 ? m.text.slice(0, 80) + "…" : m.text) : "—"}</td>
                    <td>{m.project_id || <span className="muted">未匹配</span>}</td>
                    <td>{m.issue_number ? <a href={m.issue_url} target="_blank" rel="noreferrer">#{m.issue_number}</a> : "—"}</td>
                    <td><span className={"chip " + (m.status === "issue_created" ? "ok" : m.status === "failed" ? "bad" : "warn")}><span className="dot" />{messageStatus(m.status)}</span>{m.reason && <div className="tiny">{m.reason}</div>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className={"modal-backdrop" + (editOpen ? " open" : "")}>
        <div className="modal" style={{ width: "min(620px, 100%)" }}>
          <div className="modal-head">
            <div><div className="modal-title">编辑机器人</div><div className="modal-sub">修改凭据后会重启对应 WebSocket 并自动刷新群列表。</div></div>
            <button className="icon-btn" onClick={() => setEditOpen(false)}>✕</button>
          </div>
          <div className="modal-body">
            <div className="form-grid">
              <div className="field"><label>机器人名称</label><input value={form.name} onChange={(e) => setForm((p) => ({ ...p, name: e.target.value }))} /></div>
              <div className="field"><label>App ID</label><input value={form.app_id} onChange={(e) => setForm((p) => ({ ...p, app_id: e.target.value }))} /></div>
              <div className="field full"><label>App Secret</label><input type="password" value={form.app_secret} onChange={(e) => setForm((p) => ({ ...p, app_secret: e.target.value }))} placeholder={"已保存（" + bot.secret_mask + "），留空不修改"} /></div>
              <div className="field full"><label>API Base</label><input value={form.api_base} onChange={(e) => setForm((p) => ({ ...p, api_base: e.target.value }))} /></div>
              <div className="field full"><label style={{ display: "flex", gap: 8, alignItems: "center", fontWeight: 400 }}><input type="checkbox" checked={form.require_at_me} onChange={(e) => setForm((p) => ({ ...p, require_at_me: e.target.checked }))} />未配置 Scheduler 时的兜底：群聊仅响应 @机器人 的消息</label><div className="tiny">启用 Scheduler 后由各群自己的响应策略决定，默认 smart + 影子模式。</div></div>
            </div>
          </div>
          <div className="modal-foot">
            <div className="tiny">凭据只以密文保存。</div>
            <div><button className="btn" onClick={() => setEditOpen(false)}>取消</button>{" "}<button className="btn primary" disabled={saving} onClick={() => void save()}>{saving ? "保存中…" : "保存"}</button></div>
          </div>
        </div>
      </div>

      <div className={"modal-backdrop" + (policyGroup ? " open" : "")}>
        <div className="modal" style={{ width: "min(680px, 100%)" }}>
          <div className="modal-head">
            <div><div className="modal-title">群响应策略</div><div className="modal-sub">{policyGroup?.name || policyGroup?.group_id}</div></div>
            <button className="icon-btn" onClick={() => setPolicyGroup(null)}>✕</button>
          </div>
          <div className="modal-body">
            <div className="form-grid">
              <div className="field"><label>响应模式</label>
                <select value={policyForm.response_mode} onChange={(e) => setPolicyForm((p) => ({ ...p, response_mode: e.target.value as GroupPolicyForm["response_mode"] }))}>
                  <option value="at_only">at_only：只处理 @ 和明确命令</option>
                  <option value="smart">smart：观察全部消息，按置信度响应</option>
                  <option value="all">all：全部消息进入 agent</option>
                </select>
              </div>
              <div className="field"><label style={{ display: "flex", gap: 8, alignItems: "center", fontWeight: 400 }}><input type="checkbox" checked={policyForm.shadow_mode} onChange={(e) => setPolicyForm((p) => ({ ...p, shadow_mode: e.target.checked }))} />影子模式：只记录判断，不回复</label></div>
              <div className="field"><label>回答置信度阈值</label><input type="number" min="0" max="1" step="0.05" value={policyForm.answer_confidence} onChange={(e) => setPolicyForm((p) => ({ ...p, answer_confidence: Number(e.target.value) }))} /></div>
              <div className="field"><label>自动建单置信度阈值</label><input type="number" min="0" max="1" step="0.05" value={policyForm.auto_issue_confidence} onChange={(e) => setPolicyForm((p) => ({ ...p, auto_issue_confidence: Number(e.target.value) }))} /></div>
              <div className="field"><label>每小时回复上限</label><input type="number" min="1" value={policyForm.hourly_reply_limit} onChange={(e) => setPolicyForm((p) => ({ ...p, hourly_reply_limit: Number(e.target.value) }))} /></div>
              <div className="field"><label>回复冷却（秒）</label><input type="number" min="0" value={policyForm.reply_cooldown_seconds} onChange={(e) => setPolicyForm((p) => ({ ...p, reply_cooldown_seconds: Number(e.target.value) }))} /></div>
              <div className="field"><label>每日自动建单草稿上限</label><input type="number" min="0" value={policyForm.daily_auto_issue_limit} onChange={(e) => setPolicyForm((p) => ({ ...p, daily_auto_issue_limit: Number(e.target.value) }))} /></div>
              <div className="field"><label style={{ display: "flex", gap: 8, alignItems: "center", fontWeight: 400 }}><input type="checkbox" checked={policyForm.auto_issue_enabled} onChange={(e) => setPolicyForm((p) => ({ ...p, auto_issue_enabled: e.target.checked }))} />允许高置信度非 @ 消息自动建单草稿</label><div className="tiny">开启后，高置信度的非 @ 消息会直接建单并派发 worker；建单失败时草稿保留，可在群里回复“确认建单”重试。</div></div>
            </div>
          </div>
          <div className="modal-foot">
            <div className="tiny">影子模式适合上线初期观察误判率。</div>
            <div><button className="btn" onClick={() => setPolicyGroup(null)}>取消</button>{" "}<button className="btn primary" disabled={savingPolicy} onClick={() => void savePolicy()}>{savingPolicy ? "保存中…" : "保存"}</button></div>
          </div>
        </div>
      </div>
    </>
  );
}
