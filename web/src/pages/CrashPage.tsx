import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { CrashReport, CrashRetention } from "../types";
import type { Notify } from "../App";

function when(value: string | null | undefined) {
  if (!value) return "—";
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

function shortFp(fp: string) {
  return (fp || "").replace("sha256:", "").slice(0, 12) || "—";
}

function statusChip(status: string) {
  const kind = status === "created" ? "ok" : status === "deduped" ? "warn" : "bad";
  return <span className={"chip " + kind}><span className="dot" />{status}</span>;
}

export function CrashPage({ notify }: { notify: Notify }) {
  const [items, setItems] = useState<CrashReport[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [detail, setDetail] = useState<CrashReport | null>(null);
  const [retention, setRetention] = useState<CrashRetention | null>(null);
  const [purging, setPurging] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [res, ret] = await Promise.all([api.listCrashes(100), api.crashRetention().catch(() => null)]);
      setItems(res);
      setRetention(ret);
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "读取崩溃上报失败");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const open = async (id: number) => {
    try {
      setDetail(await api.getCrash(id));
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "读取详情失败", "error");
    }
  };

  const created = items.filter((i) => i.status === "created").length;
  const deduped = Math.max(0, items.reduce((sum, i) => sum + (i.occurrences ?? 0), 0) - created);
  const rejected = items.filter((i) => i.status === "rejected" || i.status === "rate_limited").length;
  const occurrences = items.reduce((sum, i) => sum + (i.occurrences ?? 0), 0);

  return (
    <>
      <div className="notice">
        <span>i</span>
        <div>
          <strong>崩溃入口：</strong><span className="mono">POST /ingest/crash</span>（bridge，<span className="mono">X-Ingest-Token</span>）。
          服务端校验大小、脱敏凭据/邮箱/用户目录、按指纹 24h 去重、按用户每小时限流，然后建
          <span className="mono">crash_report</span> Issue。
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">原始包保留策略</div>
            <div className="card-sub">
              超过 {retention?.bundle_days ?? 30} 天清空包正文（保留指纹与次数），被拒绝的上报保留 {retention?.rejected_days ?? 7} 天
            </div>
          </div>
          <button
            className="btn"
            disabled={purging}
            onClick={async () => {
              setPurging(true);
              try {
                const res = await api.runCrashRetention();
                notify("已清理：" + res.bundles_cleared + " 个包 / " + res.rejected_deleted + " 条拒绝记录");
                await load();
              } catch (err) {
                notify(err instanceof ApiError ? err.message : "清理失败", "error");
              } finally {
                setPurging(false);
              }
            }}
          >
            立即清理
          </button>
        </div>
        <div className="card-body">
          <div className="form-grid">
            <div className="field">
              <label>记录总数</label>
              <input readOnly value={retention?.rows ?? "—"} />
            </div>
            <div className="field">
              <label>包体积</label>
              <input readOnly value={retention ? (retention.bundle_bytes / 1024).toFixed(1) + " KB" : "—"} />
            </div>
          </div>
        </div>
      </div>

      <div className="stat-grid">
        <div className="stat"><div className="stat-label">上报记录</div><div className="stat-value">{items.length}</div><div className="stat-foot">最近 100 条</div></div>
        <div className="stat"><div className="stat-label">已建单</div><div className="stat-value">{created}</div><div className="stat-foot">自动创建 Issue</div></div>
        <div className="stat"><div className="stat-label">重复出现</div><div className="stat-value">{deduped}</div><div className="stat-foot">同指纹 24h 内合并</div></div>
        <div className="stat"><div className="stat-label">拒绝 / 限流</div><div className="stat-value">{rejected}</div><div className="stat-foot">累计出现 {occurrences} 次</div></div>
      </div>

      <div className="card">
        <div className="card-head">
          <div><div className="card-title">崩溃上报</div><div className="card-sub">内容已脱敏；原始包仅在服务端保存</div></div>
          <button className="btn" onClick={() => void load()}>刷新</button>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {loading ? (
            <div className="empty">加载中…</div>
          ) : error ? (
            <div className="empty"><h3>无法读取记录</h3><div className="tiny">{error}</div></div>
          ) : items.length === 0 ? (
            <div className="empty"><h3>还没有崩溃上报</h3><div className="tiny">向 /ingest/crash 发一条 crash_bundle 后会显示在这里。</div></div>
          ) : (
            <table className="table">
              <thead><tr><th>时间</th><th>应用</th><th>版本</th><th>系统</th><th>指纹</th><th>次数</th><th>状态</th><th>Issue</th><th></th></tr></thead>
              <tbody>
                {items.map((i) => (
                  <tr key={i.id}>
                    <td className="tiny">{when(i.created_at)}</td>
                    <td className="strong">{i.app || "—"}</td>
                    <td className="tiny">{i.version || "—"}</td>
                    <td className="tiny">{i.os || "—"}</td>
                    <td className="mono tiny">{shortFp(i.fingerprint)}</td>
                    <td className="tiny">{i.occurrences}</td>
                    <td>{statusChip(i.status)}</td>
                    <td className="tiny">{i.issue_url ? <a href={i.issue_url} target="_blank" rel="noreferrer">#{i.issue_number}</a> : i.reject_reason || "—"}</td>
                    <td><button className="btn small" onClick={() => void open(i.id)}>详情</button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className={"modal-backdrop" + (detail ? " open" : "")}>
        <div className="modal" style={{ width: "min(900px, 100%)" }}>
          <div className="modal-head">
            <div>
              <div className="modal-title">崩溃 #{detail?.id} · {detail?.status}</div>
              <div className="modal-sub">{detail?.app} {detail?.version} · {detail?.os} / {detail?.arch}</div>
            </div>
            <button className="icon-btn" onClick={() => setDetail(null)}>✕</button>
          </div>
          <div className="modal-body">
            {detail?.reject_reason ? <div className="chip bad" style={{ marginBottom: 10 }}><span className="dot" />{detail.reject_reason}</div> : null}
            <div className="tiny" style={{ marginBottom: 8 }}>
              指纹 {detail?.fingerprint} · 用户 {detail?.user_id || "—"} · 设备 {detail?.device_id || "—"} ·
              出现 {detail?.occurrences} 次 · 大小 {detail?.size_bytes} B
            </div>
            {detail?.similar && detail.similar.length > 0 ? (
              <div style={{ marginBottom: 12 }}>
                <div className="strong" style={{ marginBottom: 6 }}>可能相关的问题（按调用栈相似度检索）</div>
                <table className="table">
                  <thead>
                    <tr><th>Issue</th><th>相似度</th><th>版本</th><th>指纹</th></tr>
                  </thead>
                  <tbody>
                    {detail.similar.map((it) => (
                      <tr key={it.report_id}>
                        <td>
                          {it.issue_url ? (
                            <a href={it.issue_url} target="_blank" rel="noreferrer">#{it.issue_number}</a>
                          ) : (
                            <span className="tiny">#{it.issue_number || "—"}</span>
                          )}
                        </td>
                        <td className="tiny">{Math.round(it.score * 100)}%</td>
                        <td className="tiny">{it.version || "—"}</td>
                        <td className="mono tiny">{(it.fingerprint || "").replace("sha256:", "").slice(0, 12)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : null}
            <pre className="mono" style={{ maxHeight: 420, overflow: "auto", background: "#0f172a", color: "#cbd5e1", padding: 10, borderRadius: 8, fontSize: 11, whiteSpace: "pre-wrap" }}>
              {JSON.stringify(detail?.bundle ?? {}, null, 2)}
            </pre>
          </div>
          <div className="modal-foot">
            <div className="tiny">{detail?.issue_url || "未建单"}</div>
            <button className="btn" onClick={() => setDetail(null)}>关闭</button>
          </div>
        </div>
      </div>
    </>
  );
}