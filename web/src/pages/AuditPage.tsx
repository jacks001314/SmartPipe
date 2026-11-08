import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { AuditEntry } from "../types";
import type { Notify } from "../App";

function fmt(iso: string) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString("zh-CN", { hour12: false });
}

export function AuditPage({ notify }: { notify: Notify }) {
  const [items, setItems] = useState<AuditEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [prefix, setPrefix] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setItems(await api.listAudit(150, prefix.trim()));
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载审计失败");
      notify("加载审计失败", "error");
    } finally {
      setLoading(false);
    }
  }, [prefix, notify]);

  useEffect(() => { void load(); }, [load]);

  return (
    <>
      <div className="notice">
        <span>i</span>
        <div>
          审计记录包含操作人、动作、目标与 <span className="mono">trace_id</span>，可用于把一次配置变更串到具体任务。
          凭据明文和密文都不会出现在这里。
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">最近操作</div>
            <div className="card-sub">{items.length} 条 · 最新在前</div>
          </div>
          <div style={{ display: "flex", gap: 8 }}>
            <select
              value={prefix}
              onChange={(e) => setPrefix(e.target.value)}
              style={{ border: "1px solid var(--line)", borderRadius: 8, padding: "7px 10px", fontSize: 12.5 }}
            >
              <option value="">全部动作</option>
              <option value="project">项目</option>
              <option value="credential">凭据</option>
              <option value="github">GitHub</option>
              <option value="issue">Issue</option>
              <option value="patch">补丁验证</option>
              <option value="release">发版</option>
              <option value="crash">崩溃上报</option>
              <option value="model">模型</option>
              <option value="lanxin">蓝信</option>
            </select>
            <button className="btn" onClick={() => void load()}>刷新</button>
          </div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {loading ? (
            <div className="empty">加载中…</div>
          ) : error ? (
            <div className="empty"><h3>无法读取审计</h3><div className="tiny">{error}</div></div>
          ) : items.length === 0 ? (
            <div className="empty">
              <h3>暂无记录</h3>
              <div className="tiny">在项目或凭据页做一次修改后，这里会出现对应条目。</div>
            </div>
          ) : (
            <table className="table">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>操作人</th>
                  <th>动作</th>
                  <th>目标</th>
                  <th>结果</th>
                  <th>trace</th>
                </tr>
              </thead>
              <tbody>
                {items.map((e) => (
                  <tr key={e.id}>
                    <td className="tiny" style={{ whiteSpace: "nowrap" }}>{fmt(e.at)}</td>
                    <td>{e.actor}</td>
                    <td><span className="chip">{e.action}</span></td>
                    <td>
                      <div className="mono">{e.target_type}</div>
                      <div className="tiny mono">{e.target_id}</div>
                    </td>
                    <td>
                      <span className={"chip " + (e.result === "ok" ? "ok" : "bad")}>
                        <span className="dot" />{e.result}
                      </span>
                    </td>
                    <td className="tiny mono">{e.trace_id}</td>
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
