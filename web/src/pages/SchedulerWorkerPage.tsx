import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { SchedulerEvent, SchedulerWorker, SchedulerWorkerDetail } from "../types";
import type { Notify } from "../App";
import { SchedulerOutputPanel } from "./SchedulerOutputPanel";

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
  return Number.isNaN(t) ? "—" : fmtDuration(Date.now() - t);
}

function when(value: string | null | undefined) {
  if (!value) return "—";
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

function actionLabel(action: string) {
  return { pause: "暂停", resume: "恢复", retry: "重试", stop: "停止" }[action] ?? action;
}

function eventTone(kind: string) {
  if (kind === "verify") return "ok";
  if (kind === "state") return "";
  if (kind === "resume_ignored") return "warn";
  return "";
}

function eventTitle(ev: SchedulerEvent) {
  if (ev.state_from && ev.state_to) return stateLabel(ev.state_from) + " → " + stateLabel(ev.state_to);
  if (ev.state_to) return stateLabel(ev.state_to);
  return ev.kind;
}

function currentText(w: SchedulerWorker) {
  if (w.blocked_reason && ["paused", "blocked", "awaiting_input"].includes(w.state)) return w.blocked_reason;
  if (w.last_error && ["blocked", "failed"].includes(w.state)) return w.last_error;
  switch (w.state) {
    case "queued": return "等待调度器空槽";
    case "preparing": return "准备工作区与分支";
    case "coding": return "Codex 正在修改代码";
    case "verifying": return "patch-verifier 正在执行验证";
    case "pushing": return "提交、推送并创建 PR";
    case "awaiting_review": return "PR 已就绪，等待 review";
    case "awaiting_input": return "等待发起人补充信息";
    case "paused": return "管理员已暂停当前 worker";
    case "blocked": return "需要人工处理";
    case "closed": return "任务已结束";
    case "failed": return "执行失败";
    default: return stateLabel(w.state);
  }
}

export function SchedulerWorkerPage({
  workerId, notify, onBack, onChanged,
}: { workerId: string; notify: Notify; onBack: () => void; onChanged: () => void }) {
  const [detail, setDetail] = useState<SchedulerWorkerDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [tab, setTab] = useState<"output" | "events">("output");

  const load = useCallback(async (initial = false) => {
    if (initial) setLoading(true);
    try {
      setDetail(await api.getSchedulerWorker(workerId));
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "读取 worker 详情失败");
    } finally {
      if (initial) setLoading(false);
    }
  }, [workerId]);

  useEffect(() => {
    void load(true);
    const timer = window.setInterval(() => {
      if (!document.hidden) void load(false);
    }, 3000);
    return () => window.clearInterval(timer);
  }, [load]);

  const runAction = async (action: "pause" | "resume" | "retry" | "stop") => {
    const w = detail?.worker;
    if (!w) return;
    if (action === "stop" && !window.confirm("停止「" + (w.issue_title || "#" + w.issue_number) + "」？当前执行会被取消，工作区将回收。")) return;
    setBusy(action);
    try {
      await api.schedulerWorkerAction(w.id, action);
      notify("已" + actionLabel(action) + " #" + w.issue_number);
      await load(false);
      onChanged();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : actionLabel(action) + "失败", "error");
    } finally {
      setBusy("");
    }
  };

  if (loading) return <div className="card"><div className="empty">正在读取 worker 详情…</div></div>;
  if (error || !detail) {
    return (
      <div className="card">
        <div className="empty">
          <h3>无法读取 worker</h3>
          <div className="tiny">{error}</div>
          <div style={{ display: "flex", gap: 8, justifyContent: "center", marginTop: 12 }}>
            <button className="btn" onClick={onBack}>返回列表</button>
            <button className="btn primary" onClick={() => void load(true)}>重试</button>
          </div>
        </div>
      </div>
    );
  }

  const w = detail.worker;
  const actions = w.allowed_actions ?? [];
  const stageIndex = PIPELINE.indexOf(w.state as typeof PIPELINE[number]);
  const terminal = w.state === "closed" || w.state === "failed";

  return (
    <>
      <div className="scheduler-detail-head">
        <button className="btn small" onClick={onBack}>← 调度器</button>
        <div className="scheduler-detail-title">
          <div className="scheduler-detail-name">{w.issue_title || "Issue #" + w.issue_number}</div>
          <div className="tiny">{w.project_name || w.project_id} · Issue #{w.issue_number} · {w.id}</div>
        </div>
        <div className="scheduler-detail-actions">
          {w.issue_url && <a className="btn small" href={w.issue_url} target="_blank" rel="noreferrer">打开 Issue</a>}
          {w.pr_url && <a className="btn small" href={w.pr_url} target="_blank" rel="noreferrer">打开 PR</a>}
          {actions.map((action) => (
            <button
              key={action}
              className={"btn small" + (action === "retry" ? " primary" : action === "stop" ? " danger" : "")}
              disabled={busy === action}
              onClick={() => void runAction(action as "pause" | "resume" | "retry" | "stop")}
            >
              {busy === action ? "处理中…" : actionLabel(action)}
            </button>
          ))}
        </div>
      </div>

      <div className="card">
        <div className="card-body">
          <div className="scheduler-status-line">
            <span className={"chip " + stateTone(w.state)}><span className="dot" />{stateLabel(w.state)}</span>
            <span className="strong">{currentText(w)}</span>
            <span className="tiny">已运行 {elapsed(w.state === "queued" ? w.queued_at : (w.started_at || w.state_changed_at))} · 第 {w.round} 轮 · 尝试 {w.attempts} 次</span>
          </div>
          <div className="stage-strip">
            {PIPELINE.map((step, i) => {
              const done = terminal || (stageIndex >= 0 && i < stageIndex);
              const active = !terminal && stageIndex === i;
              return (
                <div key={step} className={"stage-step" + (done ? " done" : "") + (active ? " active" : "")}>
                  <span className="stage-dot">{done ? "✓" : active ? "●" : i + 1}</span>
                  <span>{stateLabel(step)}</span>
                  {i < PIPELINE.length - 1 && <span className="stage-line" />}
                </div>
              );
            })}
          </div>
          {w.running && (
            <div className="tiny" style={{ marginTop: 10 }}>
              心跳 {w.heartbeat_at ? when(w.heartbeat_at) : "尚未上报"} · 最近更新 {when(w.updated_at)}
            </div>
          )}
        </div>
      </div>

      <div className="scheduler-workbench">
        <div className="card scheduler-main-card">
          <div className="card-head scheduler-main-head">
            <div className="scheduler-tabs" role="tablist" aria-label="worker 详情">
              <button className={tab === "output" ? "active" : ""} onClick={() => setTab("output")} role="tab" aria-selected={tab === "output"}>实时输出</button>
              <button className={tab === "events" ? "active" : ""} onClick={() => setTab("events")} role="tab" aria-selected={tab === "events"}>事件时间线</button>
            </div>
            <div className="scheduler-main-tools">
              <span className="tiny">{tab === "output" ? "同一条消息和命令会原位更新" : `${detail.events.length} 个状态与验证事件`}</span>
              <button className="btn small" onClick={() => void load(false)}>刷新</button>
            </div>
          </div>
          <div className={"card-body scheduler-main-body " + tab}>
            {tab === "output" ? (
              <SchedulerOutputPanel terminal={terminal} workerId={w.id} />
            ) : (
              <div className="timeline scheduler-event-scroll">
                {detail.events.map((ev) => (
                  <div className="timeline-item" key={ev.id}>
                    <span className={"timeline-dot " + eventTone(ev.kind)} />
                    <div className="timeline-main">
                      <div className="timeline-head">
                        <span className="strong">{eventTitle(ev)}</span>
                        <span className="tiny">{when(ev.at)}</span>
                      </div>
                      <div className="timeline-summary">{ev.summary}</div>
                      <div className="tiny">{ev.actor}</div>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>

        <div className="card scheduler-info-card">
          <div className="card-head">
            <div className="card-title">任务信息</div>
          </div>
          <div className="card-body">
            <div className="kv"><span className="k">项目</span><span>{w.project_name || w.project_id}</span></div>
            <div className="kv"><span className="k">来源会话</span><span className="mono tiny">{w.chat_id || "—"}</span></div>
            <div className="kv"><span className="k">分支</span><span className="mono tiny">{w.branch || "—"}</span></div>
            <div className="kv"><span className="k">工作区</span><span className="mono tiny" title={w.worktree}>{w.worktree || "—"}</span></div>
            <div className="kv"><span className="k">模型</span><span>{w.model || "—"}{w.provider_id ? " · " + w.provider_id : ""}</span></div>
            <div className="kv"><span className="k">PR</span><span>{w.pr_number ? "#" + w.pr_number : "—"}</span></div>
            <div className="kv"><span className="k">创建</span><span>{when(w.created_at)}</span></div>
            <div className="kv"><span className="k">最近状态</span><span>{when(w.state_changed_at)}</span></div>
            {w.started_at && <div className="kv"><span className="k">开始执行</span><span>{when(w.started_at)}</span></div>}
            {w.finished_at && <div className="kv"><span className="k">结束</span><span>{when(w.finished_at)}</span></div>}
            {w.last_error && <div className="kv"><span className="k">最近错误</span><span className="bad-note">{w.last_error}</span></div>}
          </div>
        </div>
      </div>
    </>
  );
}
