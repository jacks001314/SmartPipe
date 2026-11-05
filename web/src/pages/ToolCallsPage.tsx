import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { MCPConfig, ToolCall } from "../types";
import type { Notify } from "../App";

function fmt(iso: string) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString("zh-CN", { hour12: false });
}

const STATUS_CLASS: Record<string, string> = {
  ok: "ok",
  denied: "bad",
  error: "bad",
  invalid_args: "warn",
  rate_limited: "warn",
};

export function ToolCallsPage({ notify }: { notify: Notify }) {
  const [items, setItems] = useState<ToolCall[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [tool, setTool] = useState("");
  const [busy, setBusy] = useState(false);
  const [config, setConfig] = useState<MCPConfig>({ url: "", pool_name: "" });
  const [draft, setDraft] = useState<MCPConfig>({ url: "", pool_name: "" });

  const load = useCallback(async () => {
    setLoading(true);
    const [calls, cfg] = await Promise.allSettled([api.listToolCalls(tool.trim(), 200), api.getMCPConfig()]);
    if (calls.status === "fulfilled") {
      setItems(calls.value);
      setError("");
    } else {
      setError(calls.reason instanceof ApiError ? calls.reason.message : "读取工具调用失败");
    }
    if (cfg.status === "fulfilled") {
      setConfig(cfg.value);
      setDraft(cfg.value);
    }
    setLoading(false);
  }, [tool]);

  useEffect(() => {
    void load();
  }, [load]);

  const saveConfig = async () => {
    setBusy(true);
    try {
      const next = await api.updateMCPConfig({ url: draft.url.trim(), pool_name: draft.pool_name.trim() });
      setConfig(next);
      setDraft(next);
      notify("MCP 配置已保存，新部署的 Agent 会使用该地址");
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "保存失败", "error");
    } finally {
      setBusy(false);
    }
  };

  const okCount = items.filter((i) => i.status === "ok").length;
  const deniedCount = items.filter((i) => i.status === "denied" || i.status === "rate_limited").length;
  const toolNames = Array.from(new Set(items.map((i) => i.tool)));

  return (
    <>
      <div className="notice">
        <span>i</span>
        <div>
          网关记录每次 <span className="mono">tools/call</span>：谁（令牌 / Agent / 角色）、调了什么工具、结果与耗时。
          只留存可审计的标量参数，补丁全文和评论正文不落库。
        </div>
      </div>

      <div className="stat-grid">
        <div className="stat"><div className="stat-label">最近调用</div><div className="stat-value">{items.length}</div><div className="stat-foot">最新在前，最多 200 条</div></div>
        <div className="stat"><div className="stat-label">成功</div><div className="stat-value">{okCount}</div><div className="stat-foot">status=ok</div></div>
        <div className="stat"><div className="stat-label">被拒 / 限流</div><div className="stat-value">{deniedCount}</div><div className="stat-foot">角色越权或超频</div></div>
        <div className="stat"><div className="stat-label">涉及工具</div><div className="stat-value">{toolNames.length}</div><div className="stat-foot">去重后</div></div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">MCP 接入配置</div>
            <div className="card-sub">写入数据库后覆盖环境变量，Agent 部署时注入到全局 MCP Pool</div>
          </div>
          <button className="btn" disabled={busy} onClick={() => void saveConfig()}>保存配置</button>
        </div>
        <div className="card-body">
          <div className="form-grid">
            <div className="field">
              <label>MCP 地址</label>
              <input
                value={draft.url}
                placeholder="http://127.0.0.1:8097/mcp"
                onChange={(e) => setDraft((p) => ({ ...p, url: e.target.value }))}
              />
              <div className="tiny">当前生效：<span className="mono">{config.url || "未配置"}</span></div>
            </div>
            <div className="field">
              <label>MCP Pool 名称</label>
              <input
                value={draft.pool_name}
                placeholder="smartpipe"
                onChange={(e) => setDraft((p) => ({ ...p, pool_name: e.target.value }))}
              />
              <div className="tiny">Syntropy 模板通过它引用工具网关，令牌走 <span className="mono">bearerTokenEnvVar</span> 注入</div>
            </div>
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">工具调用审计</div>
            <div className="card-sub">{items.length} 条 · 最新在前</div>
          </div>
          <div style={{ display: "flex", gap: 8 }}>
            <select
              value={tool}
              onChange={(e) => setTool(e.target.value)}
              style={{ border: "1px solid var(--line)", borderRadius: 8, padding: "7px 10px", fontSize: 12.5 }}
            >
              <option value="">全部工具</option>
              {toolNames.map((t) => <option key={t} value={t}>{t}</option>)}
            </select>
            <button className="btn" onClick={() => void load()}>刷新</button>
          </div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {loading ? (
            <div className="empty">加载中…</div>
          ) : error ? (
            <div className="empty"><h3>无法读取工具调用</h3><div className="tiny">{error}</div></div>
          ) : items.length === 0 ? (
            <div className="empty">
              <h3>暂无调用</h3>
              <div className="tiny">Agent 通过 MCP 网关调用任意工具后，这里会出现记录。</div>
            </div>
          ) : (
            <table className="table">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>Agent / 角色</th>
                  <th>工具</th>
                  <th>参数摘要</th>
                  <th>结果</th>
                  <th>耗时</th>
                </tr>
              </thead>
              <tbody>
                {items.map((i) => (
                  <tr key={i.id}>
                    <td className="tiny" style={{ whiteSpace: "nowrap" }}>{fmt(i.created_at)}</td>
                    <td>
                      <div className="strong">{i.agent_name || "—"}</div>
                      <div className="tiny mono">{i.role} · {i.token_id}</div>
                    </td>
                    <td><span className="chip">{i.tool}</span></td>
                    <td className="tiny mono" style={{ maxWidth: 320, wordBreak: "break-all" }}>
                      {JSON.stringify(i.args_summary)}
                    </td>
                    <td>
                      <span className={"chip " + (STATUS_CLASS[i.status] ?? "")}>
                        <span className="dot" />{i.status}
                      </span>
                      {i.error ? <div className="tiny" style={{ color: "var(--bad, #c33)" }}>{i.error}</div> : null}
                    </td>
                    <td className="tiny">{i.duration_ms} ms</td>
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
