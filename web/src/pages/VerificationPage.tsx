import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { IssueAttempt, Project, RepoOp, VerificationRecord } from "../types";
import type { Notify } from "../App";

function when(value: string | null | undefined) {
  if (!value) return "—";
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

function shortHash(hash: string) {
  const h = (hash || "").replace("sha256:", "");
  return h ? h.slice(0, 12) : "—";
}

export function VerificationPage({ notify }: { notify: Notify }) {
  const [records, setRecords] = useState<VerificationRecord[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [attempts, setAttempts] = useState<IssueAttempt[]>([]);
  const [ops, setOps] = useState<RepoOp[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [detail, setDetail] = useState<VerificationRecord | null>(null);
  const [form, setForm] = useState({
    project_id: "",
    issue_ref: "",
    repo: "",
    base_commit: "",
    patch: "",
  });

  const load = useCallback(async () => {
    setLoading(true);
    const [v, p, a, o] = await Promise.allSettled([api.listVerifications(50), api.listProjects(), api.listAttempts(100), api.listRepoOps(50)]);
    if (v.status === "fulfilled") {
      setRecords(v.value);
      setError("");
    } else {
      setError(v.reason instanceof ApiError ? v.reason.message : "读取验证记录失败");
    }
    if (p.status === "fulfilled") setProjects(p.value);
    if (a.status === "fulfilled") setAttempts(a.value);
    if (o.status === "fulfilled") setOps(o.value);
    setLoading(false);
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const submit = async () => {
    if (!form.repo || !form.base_commit || !form.patch.trim()) {
      notify("仓库、base commit、补丁都必填", "error");
      return;
    }
    setBusy(true);
    try {
      const res = await api.createVerification({
        project_id: form.project_id || undefined,
        issue_ref: form.issue_ref,
        repo: form.repo,
        base_commit: form.base_commit.trim(),
        patch: form.patch,
        author: "console",
      });
      const attempt = (res as unknown as { attempt?: IssueAttempt }).attempt;
      if (attempt?.state === "blocked") {
        notify("验证失败已达 3 次，Issue 已置 blocked，请人工介入", "error");
      } else {
        notify(res.report.ok ? "验证通过" : "验证未通过：" + String(res.report.error ?? ""), res.report.ok ? "ok" : "error");
      }
      setForm((p) => ({ ...p, patch: "" }));
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "提交验证失败", "error");
    } finally {
      setBusy(false);
    }
  };

  const passed = records.filter((r) => r.ok).length;
  const last = records[0];

  return (
    <>
      <div className="notice">
        <span>i</span>
        <div>
          <strong>手动验证：</strong>本页仅用于在干净副本上手动执行仓库
          <span className="mono">smartpipe.yaml</span> 声明的验证命令，并查看历史验证记录。
          提交 <span className="mono">base_commit + unified diff</span> 后，verifier 会克隆干净副本、
          校验路径/大小/密钥，<span className="mono">git apply</span> 后执行验证命令。
          补丁验证不再作为 Issue 修复的合并门禁：Issue 修复由 Codex 在 worker 内自测后提交；
          patch-verifier 仍保留在 worker 中，用于手动验证与发版构建。
        </div>
      </div>

      <div className="stat-grid">
        <div className="stat">
          <div className="stat-label">验证记录</div>
          <div className="stat-value">{records.length}</div>
          <div className="stat-foot">最近 50 次</div>
        </div>
        <div className="stat">
          <div className="stat-label">通过</div>
          <div className="stat-value">{passed}</div>
          <div className="stat-foot">命令全部通过</div>
        </div>
        <div className="stat">
          <div className="stat-label">未通过</div>
          <div className="stat-value">{records.length - passed}</div>
          <div className="stat-foot">命令未全部通过</div>
        </div>
        <div className="stat">
          <div className="stat-label">最近耗时</div>
          <div className="stat-value" style={{ fontSize: 20 }}>{last ? Math.round(last.duration_ms / 1000) + "s" : "—"}</div>
          <div className="stat-foot">{last ? when(last.created_at) : "尚无记录"}</div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">提交补丁验证</div>
            <div className="card-sub">repo 支持本地路径或 git URL；验证过程不会改动源仓库</div>
          </div>
          <button className="btn" onClick={() => void load()}>刷新</button>
        </div>
        <div className="card-body">
          <div className="form-grid">
            <div className="field">
              <label>项目（可选）</label>
              <select value={form.project_id} onChange={(e) => setForm((p) => ({ ...p, project_id: e.target.value }))}>
                <option value="">不关联项目</option>
                {projects.map((prj) => <option key={prj.id} value={prj.id}>{prj.name}</option>)}
              </select>
            </div>
            <div className="field">
              <label>Issue</label>
              <input value={form.issue_ref} onChange={(e) => setForm((p) => ({ ...p, issue_ref: e.target.value }))} placeholder="owner/repo#12" />
            </div>
            <div className="field">
              <label>仓库路径或 URL</label>
              <input value={form.repo} onChange={(e) => setForm((p) => ({ ...p, repo: e.target.value }))} placeholder="C:\work\repo 或 git@github.com:org/repo.git" />
            </div>
            <div className="field">
              <label>base commit</label>
              <input value={form.base_commit} onChange={(e) => setForm((p) => ({ ...p, base_commit: e.target.value }))} placeholder="完整或 7 位以上 sha" />
            </div>
            <div className="field full">
              <label>unified diff</label>
              <textarea
                rows={10}
                value={form.patch}
                onChange={(e) => setForm((p) => ({ ...p, patch: e.target.value }))}
                placeholder={"diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ ..."}
                style={{ fontFamily: "var(--mono, monospace)", fontSize: 12 }}
              />
            </div>
          </div>
          <div style={{ display: "flex", justifyContent: "flex-end", marginTop: 12 }}>
            <button className="btn primary" disabled={busy} onClick={() => void submit()}>
              {busy ? "验证中…（克隆并执行命令）" : "开始验证"}
            </button>
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">验证记录</div>
            <div className="card-sub">每次验证保存补丁指纹、文件清单、命令输出与耗时</div>
          </div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {loading ? (
            <div className="empty">加载中…</div>
          ) : error ? (
            <div className="empty"><h3>无法读取记录</h3><div className="tiny">{error}</div></div>
          ) : records.length === 0 ? (
            <div className="empty">
              <h3>还没有验证记录</h3>
              <div className="tiny">提交一个补丁，verifier 会在临时目录克隆仓库、应用补丁并执行 smartpipe.yaml 声明的验证命令。</div>
            </div>
          ) : (
            <table className="table">
              <thead>
                <tr><th>时间</th><th>项目 / Issue</th><th>尝试</th><th>base</th><th>补丁指纹</th><th>结果</th><th>耗时</th><th></th></tr>
              </thead>
              <tbody>
                {records.map((r) => (
                  <tr key={r.id}>
                    <td className="tiny">{when(r.created_at)}</td>
                    <td>
                      <div className="strong">{projects.find((p) => p.id === r.project_id)?.name ?? r.project_id ?? "—"}</div>
                      <div className="tiny mono">{r.issue_ref || "—"}</div>
                    </td>
                    <td>
                      {(() => {
                        const at = attempts.find((x) => x.issue_ref === r.issue_ref);
                        if (!at) return <span className="tiny">—</span>;
                        if (at.last_verification_id !== r.id) {
                          return <span className="tiny">{at.attempts} 次</span>;
                        }
                        const cls = at.state === "blocked" ? "bad" : at.state === "passed" ? "ok" : "warn";
                        return <span className={"chip " + cls}><span className="dot" />{at.attempts} 次 · {at.state}</span>;
                      })()}
                    </td>
                    <td className="mono tiny">{(r.base_commit || "").slice(0, 10)}</td>
                    <td className="mono tiny">{shortHash(r.patch_hash)}</td>
                    <td>
                      {r.ok ? <span className="chip ok"><span className="dot" />通过</span> : <span className="chip bad"><span className="dot" />未通过</span>}
                      <div className="tiny">{(r.files ?? []).length} 个文件 · {r.changed_lines} 行</div>
                    </td>
                    <td className="tiny">{Math.round(r.duration_ms / 1000)}s</td>
                    <td>
                      <button className="btn small" onClick={() => setDetail(r)}>详情</button>
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
            <div className="card-title">提交与推送记录</div>
            <div className="card-sub">仅展示 repo.commit / repo.push 历史；手动验证不会自动提交或合并，远端地址与 Git 凭据由服务端注入</div>
          </div>
          <button className="btn" onClick={() => void load()}>刷新</button>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {ops.length === 0 ? (
            <div className="empty">
              <h3>还没有提交记录</h3>
              <div className="tiny">提交后这里会显示分支、commit 与目标分支。</div>
            </div>
          ) : (
            <table className="table">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>类型</th>
                  <th>Issue</th>
                  <th>分支 → 目标</th>
                  <th>commit</th>
                  <th>结果</th>
                </tr>
              </thead>
              <tbody>
                {ops.map((o) => (
                  <tr key={o.id}>
                    <td className="tiny" style={{ whiteSpace: "nowrap" }}>{when(o.created_at)}</td>
                    <td><span className="chip">{o.kind}</span></td>
                    <td className="mono tiny">{o.issue_ref || "—"}</td>
                    <td className="mono tiny">{o.branch || "—"}{o.target_branch ? " → " + o.target_branch : ""}</td>
                    <td className="mono tiny">{(o.commit || "").slice(0, 12) || "—"}</td>
                    <td>
                      {o.ok ? <span className="chip ok"><span className="dot" />成功</span> : <span className="chip bad"><span className="dot" />失败</span>}
                      {o.error ? <div className="tiny">{o.error}</div> : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
      <div className={"modal-backdrop" + (detail ? " open" : "")}>
        <div className="modal" style={{ width: "min(860px, 100%)" }}>
          <div className="modal-head">
            <div>
              <div className="modal-title">验证 #{detail?.id}</div>
              <div className="modal-sub">{detail?.repo} · base {(detail?.base_commit ?? "").slice(0, 12)} · {detail ? Math.round(detail.duration_ms / 1000) + "s" : ""}</div>
            </div>
            <button className="icon-btn" onClick={() => setDetail(null)}>✕</button>
          </div>
          <div className="modal-body">
            {detail?.error ? <div className="chip bad" style={{ marginBottom: 12 }}><span className="dot" />{detail.error}</div> : null}
            <div className="tiny" style={{ marginBottom: 8 }}>文件：{(detail?.files ?? []).join("、") || "—"}</div>
            {detail?.checks?.length ? (
              detail.checks.map((chk, i) => (
                <div key={i} style={{ marginBottom: 14 }}>
                  <div className="strong">{chk.name} · {chk.ok ? "通过" : chk.timed_out ? "超时" : "失败"} · {Math.round(chk.duration_ms / 1000)}s</div>
                  <div className="tiny mono">{chk.command}</div>
                  <pre className="mono" style={{ maxHeight: 180, overflow: "auto", background: "#0f172a", color: "#cbd5e1", padding: 10, borderRadius: 8, fontSize: 11 }}>{chk.output || "（无输出）"}</pre>
                </div>
              ))
            ) : (
              <div className="tiny">没有执行验证命令。</div>
            )}
          </div>
          <div className="modal-foot">
            <div className="tiny">证据保存在 patch_verifications 表，补丁指纹用于核对本次验证的补丁内容是否一致。</div>
            <button className="btn" onClick={() => setDetail(null)}>关闭</button>
          </div>
        </div>
      </div>
    </>
  );
}
