import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError } from "../api";
import type { SchedulerMessage, SchedulerOverview, SchedulerWorker } from "../types";
import type { Notify } from "../App";
import { SchedulerWorkerPage } from "./SchedulerWorkerPage";

type Filter = "active" | "queued" | "human" | "history" | "all" | "preparing" | "coding" | "verifying" | "pushing" | "awaiting_review";

const FILTERS: { key: Filter; label: string }[] = [
  { key: "active", label: "进行中" },
  { key: "queued", label: "排队" },
  { key: "human", label: "待人工" },
  { key: "history", label: "最近结束" },
  { key: "all", label: "全部" },
];

const PIPELINE = ["queued", "preparing", "coding", "verifying", "pushing", "awaiting_review"] as const;

const STATE_LABEL: Record<string, string> = {
  queued: "排队",
  preparing: "准备",
  coding: "编码",
  verifying: "验证",
  pushing: "推送",
  awaiting_review: "待评审",
  awaiting_input: "待补充",
  paused: "已暂停",
  blocked: "待人工",
  closed: "已结束",
  failed: "失败",
};

const STATE_TONE: Record<string, string> = {
  queued: "warn",
  preparing: "warn",
  coding: "",
  verifying: "",
  pushing: "",
  awaiting_review: "ok",
  awaiting_input: "warn",
  paused: "warn",
  blocked: "bad",
  closed: "",
  failed: "bad",
};

const MESSAGE_LABEL: Record<string, string> = {
  received: "已收到",
  processing: "处理中",
  answered: "已回答",
  need_info: "待补充",
  awaiting_confirmation: "待确认建单",
  ignored: "已忽略",
  silent: "判断沉默",
  shadow_silent: "影子-沉默",
  shadow_answer: "影子-候选回答",
  shadow_triage: "影子-候选建单",
  triage_candidate: "待评估候选",
  rate_limited: "限额跳过",
  deduped_bot: "其他机器人已处理",
  control_blocked: "控制命令已拦截",
  issue_created: "已建单",
  issue_updated: "已补充",
  unbound: "未绑定",
  filtered: "已过滤",
  failed: "失败",
};

const ACTIVE = new Set(["preparing", "coding", "verifying", "pushing"]);
const HUMAN = new Set(["paused", "blocked", "awaiting_input"]);

function stateLabel(state: string) {
  return STATE_LABEL[state] ?? state;
}

function stateTone(state: string) {
  return STATE_TONE[state] ?? "";
}

function fmtDuration(ms: number) {
  if (!Number.isFinite(ms) || ms < 0) return "—";
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return seconds + "s";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return minutes + "m" + String(seconds % 60).padStart(2, "0") + "s";
  const hours = Math.floor(minutes / 60);
  return hours + "h" + String(minutes % 60).padStart(2, "0") + "m";
}

function elapsed(from: string | null | undefined) {
  if (!from) return "—";
  const t = new Date(from).getTime();
  if (Number.isNaN(t)) return "—";
  return fmtDuration(Date.now() - t);
}

function relative(from: string | null | undefined) {
  if (!from) return "—";
  const t = new Date(from).getTime();
  if (Number.isNaN(t)) return "—";
  const ms = Date.now() - t;
  if (ms < 60_000) return Math.max(0, Math.floor(ms / 1000)) + "s 前";
  if (ms < 3_600_000) return Math.floor(ms / 60_000) + "m 前";
  return Math.floor(ms / 3_600_000) + "h 前";
}

function currentText(w: SchedulerWorker, queuePosition?: number) {
  if (w.blocked_reason && ["paused", "blocked", "awaiting_input"].includes(w.state)) return w.blocked_reason;
  if (w.last_error && ["blocked", "failed"].includes(w.state)) return w.last_error;
  switch (w.state) {
    case "queued":
      return queuePosition ? "排队第 " + queuePosition + " 位，等待空槽" : "等待空槽";
    case "preparing":
      return "准备工作区与分支";
    case "coding":
      return "Codex 正在修改代码";
    case "verifying":
      return "patch-verifier 正在执行验证";
    case "pushing":
      return "提交、推送并创建 PR";
    case "awaiting_review":
      return "PR 已就绪，等待 review";
    case "awaiting_input":
      return "等待发起人补充信息";
    case "paused":
      return "管理员已暂停当前 worker";
    case "blocked":
      return "需要人工处理";
    case "closed":
      return "任务已结束";
    case "failed":
      return "执行失败";
    default:
      return stateLabel(w.state);
  }
}

function actionLabel(action: string) {
  return { pause: "暂停", resume: "恢复", retry: "重试", stop: "停止" }[action] ?? action;
}

export function SchedulerPage({ notify }: { notify: Notify }) {
  const [overview, setOverview] = useState<SchedulerOverview | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [filter, setFilter] = useState<Filter>("active");
  const [selected, setSelected] = useState("");
  const [busy, setBusy] = useState("");
  const [updatedAt, setUpdatedAt] = useState("");
  const [messages, setMessages] = useState<SchedulerMessage[]>([]);
  const [messageError, setMessageError] = useState("");

  const load = useCallback(async (initial = false) => {
    if (initial) setLoading(true);
    const [ov, msgs] = await Promise.allSettled([api.schedulerOverview(), api.schedulerMessages(100)]);
    if (ov.status === "fulfilled") {
      setOverview(ov.value);
      setError("");
    } else {
      setError(ov.reason instanceof ApiError ? ov.reason.message : "读取调度器状态失败");
    }
    if (msgs.status === "fulfilled") {
      setMessages(msgs.value);
      setMessageError("");
    } else {
      setMessageError(msgs.reason instanceof ApiError ? msgs.reason.message : "读取消息流水失败");
    }
    setUpdatedAt(new Date().toLocaleTimeString());
    if (initial) setLoading(false);
  }, []);

  useEffect(() => {
    void load(true);
    const timer = window.setInterval(() => {
      if (!document.hidden) void load(false);
    }, 5000);
    return () => window.clearInterval(timer);
  }, [load]);

  const workers = overview?.workers ?? [];
  const counts = overview?.counts ?? {};
  const activeCount = workers.filter((w) => ACTIVE.has(w.state)).length;
  const queued = useMemo(
    () => [...workers].filter((w) => w.state === "queued").sort((a, b) => (b.priority - a.priority) || a.queued_at.localeCompare(b.queued_at)),
    [workers],
  );
  const queuePositions = useMemo(() => {
    const map = new Map<string, number>();
    queued.forEach((w, i) => map.set(w.id, i + 1));
    return map;
  }, [queued]);
  const humanCount = workers.filter((w) => HUMAN.has(w.state)).length;
  const reviewCount = workers.filter((w) => w.state === "awaiting_review").length;
  const oldestWait = queued.length ? elapsed(queued[0].queued_at) : "—";

  const filtered = useMemo(() => {
    const list = [...workers].sort((a, b) => {
      const order: Record<string, number> = {
        coding: 0, verifying: 1, pushing: 2, preparing: 3, queued: 4, paused: 5, blocked: 6, awaiting_input: 7, awaiting_review: 8, failed: 9, closed: 10,
      };
      const d = (order[a.state] ?? 20) - (order[b.state] ?? 20);
      return d !== 0 ? d : b.updated_at.localeCompare(a.updated_at);
    });
    switch (filter) {
      case "active":
        return list.filter((w) => !["closed", "failed", "paused", "blocked", "awaiting_input"].includes(w.state));
      case "queued":
        return list.filter((w) => w.state === "queued");
      case "human":
        return list.filter((w) => HUMAN.has(w.state));
      case "history":
        return list.filter((w) => w.state === "closed" || w.state === "failed");
      case "preparing":
      case "coding":
      case "verifying":
      case "pushing":
      case "awaiting_review":
        return list.filter((w) => w.state === filter);
      default:
        return list;
    }
  }, [workers, filter]);

  const runAction = async (w: SchedulerWorker, action: "pause" | "resume" | "retry" | "stop") => {
    if (action === "stop" && !window.confirm("停止「" + (w.issue_title || "#" + w.issue_number) + "」？当前执行会被取消，工作区将回收。")) return;
    setBusy(w.id + action);
    try {
      await api.schedulerWorkerAction(w.id, action);
      notify("已" + actionLabel(action) + " #" + w.issue_number);
      await load(false);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : actionLabel(action) + "失败", "error");
    } finally {
      setBusy("");
    }
  };

  if (selected) {
    return (
      <SchedulerWorkerPage
        workerId={selected}
        notify={notify}
        onBack={() => setSelected("")}
        onChanged={() => void load(false)}
      />
    );
  }

  if (loading) {
    return <div className="card"><div className="empty">正在读取调度器状态…</div></div>;
  }

  if (error) {
    return (
      <div className="card">
        <div className="empty">
          <h3>无法读取调度器</h3>
          <div className="tiny">{error}</div>
          <div className="tiny">检查调度器是否在运行，以及 admin-api 的 SMARTPIPE_SCHEDULER_URL / SMARTPIPE_SCHEDULER_TOKEN 是否配置正确。</div>
          <button className="btn" style={{ marginTop: 12 }} onClick={() => void load(true)}>重试</button>
        </div>
      </div>
    );
  }

  return (
    <>
      <div className="notice">
        <span>i</span>
        <div><strong>暂停只作用于当前 worker。</strong>点击暂停会取消这一轮 Codex 执行并保留分支和工作区；恢复后重新排队。需要停止全部新任务时，请逐个项目调整或先停用项目。</div>
      </div>

      <div className="stat-grid">
        <div className="stat">
          <div className="stat-label">调度器</div>
          <div className="stat-value" style={{ fontSize: 20 }}>运行中</div>
          <div className="stat-foot">最近刷新 {updatedAt || "—"} · 全局并发 {overview?.max_global ?? "—"}</div>
        </div>
        <div className="stat">
          <div className="stat-label">活跃 worker</div>
          <div className="stat-value">{activeCount}<span className="tiny" style={{ fontSize: 13 }}> / {overview?.max_global ?? "—"}</span></div>
          <div className="stat-foot">单项目上限 {overview?.max_per_project ?? "—"}</div>
        </div>
        <div className="stat">
          <div className="stat-label">排队</div>
          <div className="stat-value">{queued.length}</div>
          <div className="stat-foot">最久等待 {oldestWait}</div>
        </div>
        <div className="stat">
          <div className="stat-label">待人工</div>
          <div className="stat-value">{humanCount}</div>
          <div className="stat-foot">待评审 {reviewCount} · 已暂停/阻塞/待补充</div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">流水线概览</div>
            <div className="card-sub">点击阶段可只看该状态任务；状态计数来自调度器实时数据。</div>
          </div>
          <div className="scheduler-filter">
            {FILTERS.map((f) => (
              <button key={f.key} className={"btn small" + (filter === f.key ? " primary" : "")} onClick={() => setFilter(f.key)}>
                {f.label}
              </button>
            ))}
            <button className="btn small" onClick={() => void load(true)}>刷新</button>
          </div>
        </div>
        <div className="card-body">
          <div className="scheduler-pipeline">
            {PIPELINE.map((state, i) => (
              <button key={state} className={"scheduler-stage" + (filter === state ? " active" : "")} onClick={() => setFilter(state)}>
                <span className={"chip " + stateTone(state)}><span className="dot" />{stateLabel(state)}</span>
                <strong>{counts[state] ?? 0}</strong>
                {i < PIPELINE.length - 1 && <span className="scheduler-stage-arrow">→</span>}
              </button>
            ))}
            <button className="scheduler-stage" onClick={() => setFilter("human")}>
              <span className="chip warn"><span className="dot" />待人工</span>
              <strong>{humanCount}</strong>
            </button>
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">任务</div>
            <div className="card-sub">“当前动作”会直接说明正在做什么、为什么停着。</div>
          </div>
          <div className="tiny">每 5 秒自动刷新</div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {filtered.length === 0 ? (
            <div className="empty"><h3>没有匹配的任务</h3><div className="tiny">切换筛选条件，或等待新的蓝信消息进入调度器。</div></div>
          ) : (
            <table className="table scheduler-table">
              <thead>
                <tr><th>任务</th><th>状态</th><th>当前动作</th><th>已运行</th><th>心跳 / 更新</th><th></th></tr>
              </thead>
              <tbody>
                {filtered.map((w) => (
                  <tr key={w.id}>
                    <td>
                      <div className="scheduler-task-title">
                        <span className="strong">{w.issue_title || "Issue #" + w.issue_number}</span>
                        {w.issue_url && <a href={w.issue_url} target="_blank" rel="noreferrer" className="tiny">#{w.issue_number} ↗</a>}
                      </div>
                      <div className="tiny">{w.project_name || w.project_id} · {w.chat_id || "—"}</div>
                    </td>
                    <td><span className={"chip " + stateTone(w.state)}><span className="dot" />{stateLabel(w.state)}</span></td>
                    <td>
                      <div className="scheduler-current">{currentText(w, queuePositions.get(w.id))}</div>
                      {w.running && <div className="tiny">heartbeat {relative(w.heartbeat_at || w.updated_at)}</div>}
                    </td>
                    <td className="mono tiny">{elapsed(w.state === "queued" ? w.queued_at : (w.started_at || w.state_changed_at))}</td>
                    <td className="tiny">{relative(w.heartbeat_at || w.updated_at)}</td>
                    <td>
                      <div className="row-actions">
                        <button className="btn small" onClick={() => setSelected(w.id)}>详情</button>
                        {(w.allowed_actions ?? []).filter((a) => a === "pause" || a === "resume" || a === "retry").map((action) => (
                          <button
                            key={action}
                            className={"btn small" + (action === "retry" ? " primary" : "")}
                            disabled={busy === w.id + action}
                            onClick={() => void runAction(w, action as "pause" | "resume" | "retry")}
                          >
                            {busy === w.id + action ? "处理中…" : actionLabel(action)}
                          </button>
                        ))}
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
            <div className="card-title">消息流水</div>
            <div className="card-sub">最近蓝信入站消息及调度结果；失败消息也会保留原因。</div>
          </div>
          <div className="tiny">最近 100 条</div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {messageError ? (
            <div className="empty"><div className="tiny">{messageError}</div></div>
          ) : messages.length === 0 ? (
            <div className="empty"><h3>还没有消息记录</h3><div className="tiny">蓝信消息进入调度器后会出现在这里。</div></div>
          ) : (
            <table className="table scheduler-message-table">
              <thead><tr><th>时间</th><th>会话 / 发送人</th><th>内容</th><th>处理结果</th><th>Issue</th></tr></thead>
              <tbody>
                {messages.slice(0, 50).map((m) => (
                  <tr key={m.id}>
                    <td className="tiny">{new Date(m.created_at).toLocaleString()}</td>
                    <td><div>{m.group_name || m.chat_id}</div><div className="tiny">{m.sender_name || m.user_id}</div></td>
                    <td className="tiny" title={m.text}>{m.text ? (m.text.length > 90 ? m.text.slice(0, 90) + "…" : m.text) : "—"}</td>
                    <td>
                      <span className={"chip " + (m.status === "issue_created" || m.status === "answered" ? "ok" : m.status === "failed" || m.status === "unbound" ? "bad" : "warn")}>
                        <span className="dot" />{MESSAGE_LABEL[m.status] ?? m.status}
                      </span>
                      {m.reason && <div className="tiny" title={m.reason}>{m.reason.length > 80 ? m.reason.slice(0, 80) + "…" : m.reason}</div>}
                    </td>
                    <td>{m.issue_number ? <a href={m.issue_url} target="_blank" rel="noreferrer">#{m.issue_number}</a> : "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </>
  );
}
