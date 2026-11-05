import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { Project, ReleaseCandidate, ReleaseRecord, ReleaseRun } from "../types";
import type { Notify } from "../App";

function when(value: string | null | undefined) {
  if (!value) return "—";
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

function shortHash(hash: string) {
  return (hash || "").replace("sha256:", "").slice(0, 12) || "—";
}

function statusChip(status: string) {
  const ok = status === "released" || status === "built";
  const warn = status === "released_with_warnings" || status === "skipped";
  return <span className={"chip " + (ok ? "ok" : warn ? "warn" : "bad")}><span className="dot" />{status}</span>;
}

function todayVersion() {
  const d = new Date();
  const mm = String(d.getMonth() + 1).padStart(2, "0");
  const dd = String(d.getDate()).padStart(2, "0");
  return "v" + d.getFullYear() + "." + mm + "." + dd;
}

export function ReleasePage({ notify }: { notify: Notify }) {
  const [records, setRecords] = useState<ReleaseRecord[]>([]);
  const [runs, setRuns] = useState<ReleaseRun[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [detail, setDetail] = useState<ReleaseRecord | null>(null);
  const [runDetail, setRunDetail] = useState<ReleaseRun | null>(null);
  const [candidates, setCandidates] = useState<ReleaseCandidate[]>([]);
  const [candidateError, setCandidateError] = useState("");
  const [candidateLabel, setCandidateLabel] = useState("");
  const [form, setForm] = useState({ project_id: "", repo: "", version: "", commit: "", globs: "dist/**", notes: "" });
  const [runForm, setRunForm] = useState({ project_id: "", version: todayVersion(), commit: "", branch: "", notes: "", globs: "dist/**" });

  const load = useCallback(async () => {
    setLoading(true);
    const [r, p, n] = await Promise.allSettled([api.listReleases(50), api.listProjects(), api.listReleaseRuns(50)]);
    if (r.status === "fulfilled") {
      setRecords(r.value);
      setError("");
    } else {
      setError(r.reason instanceof ApiError ? r.reason.message : "读取发版记录失败");
    }
    if (p.status === "fulfilled") {
      setProjects(p.value);
      setRunForm((prev) => (prev.project_id ? prev : { ...prev, project_id: p.value[0]?.id ?? "" }));
    }
    if (n.status === "fulfilled") setRuns(n.value);
    setLoading(false);
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const loadCandidates = useCallback(async (projectId: string) => {
    if (!projectId) {
      setCandidates([]);
      setCandidateLabel("");
      setCandidateError("");
      return;
    }
    try {
      const res = await api.listReleaseCandidates(projectId);
      setCandidates(res.candidates);
      setCandidateLabel(res.label);
      setCandidateError("");
    } catch (err) {
      setCandidates([]);
      setCandidateError(err instanceof ApiError ? err.message : "读取候选 Issue 失败");
    }
  }, []);

  useEffect(() => {
    void loadCandidates(runForm.project_id);
  }, [runForm.project_id, loadCandidates]);

  const submit = async () => {
    if (!form.version || !form.commit) {
      notify("version 与 commit 必填", "error");
      return;
    }
    if (!form.project_id && !form.repo) {
      notify("选择项目或填写仓库路径", "error");
      return;
    }
    setBusy(true);
    try {
      const res = await api.createRelease({
        project_id: form.project_id || undefined,
        repo: form.repo || undefined,
        version: form.version.trim(),
        commit: form.commit.trim(),
        artifact_globs: form.globs.split(",").map((x) => x.trim()).filter(Boolean),
        notes: form.notes,
      });
      const report = res.report as { ok?: boolean; error?: string };
      notify(report.ok ? "构建完成：" + form.version : "构建失败：" + String(report.error ?? ""), report.ok ? "ok" : "error");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "发版构建失败", "error");
    } finally {
      setBusy(false);
    }
  };

  const submitRun = async (dryRun: boolean) => {
    if (!runForm.project_id) {
      notify("先选择项目", "error");
      return;
    }
    if (!runForm.version.trim()) {
      notify("版本号必填", "error");
      return;
    }
    if (!dryRun && !window.confirm("将创建 tag 与 GitHub Release，并给 " + candidates.length + " 个 Issue 回写标签。继续？")) {
      return;
    }
    setBusy(true);
    try {
      const res = await api.createReleaseRun({
        project_id: runForm.project_id,
        version: runForm.version.trim(),
        commit: runForm.commit.trim() || undefined,
        branch: runForm.branch.trim() || undefined,
        notes: runForm.notes,
        dry_run: dryRun,
        artifact_globs: runForm.globs.split(",").map((x) => x.trim()).filter(Boolean),
      });
      const run = res.run;
      notify(
        run.status === "released" || run.status === "built"
          ? run.status + "：" + run.version + "（" + run.candidates.length + " 个 Issue）"
          : run.status + "：" + (run.error || run.status),
        run.status === "released" || run.status === "built" ? "ok" : "error",
      );
      setRunDetail(run);
      await load();
      await loadCandidates(runForm.project_id);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "发版运行失败", "error");
    } finally {
      setBusy(false);
    }
  };

  const rollback = async (run: ReleaseRun) => {
    if (!window.confirm("回滚 " + run.version + "？将删除 GitHub Release 与 tag，并把 " + (run.candidates ?? []).length + " 个 Issue 退回待发版。")) return;
    setBusy(true);
    try {
      const updated = await api.rollbackReleaseRun(run.id);
      notify("已回滚 " + run.version);
      setRunDetail(updated);
      await load();
      await loadCandidates(run.project_id ?? "");
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "回滚失败", "error");
    } finally {
      setBusy(false);
    }
  };

  const passed = records.filter((r) => r.ok).length;
  const last = records[0];
  const released = runs.filter((r) => r.status === "released" || r.status === "released_with_warnings").length;

  return (
    <>
      <div className="notice">
        <span>i</span>
        <div>
          <strong>无 Actions 的发版链路：</strong>读取 <span className="mono">smartpipe:ready</span> 候选 Issue →
          从目标分支 HEAD 干净构建 → 创建 tag 与 GitHub Release → 回写 <span className="mono">smartpipe:released</span> →
          通知蓝信群。失败时保留制品与日志，候选 Issue 不会被消费。
        </div>
      </div>

      <div className="stat-grid">
        <div className="stat"><div className="stat-label">发版运行</div><div className="stat-value">{runs.length}</div><div className="stat-foot">最近 50 次</div></div>
        <div className="stat"><div className="stat-label">成功发布</div><div className="stat-value">{released}</div><div className="stat-foot">含带告警的发布</div></div>
        <div className="stat"><div className="stat-label">独立构建</div><div className="stat-value">{records.length}</div><div className="stat-foot">成功 {passed} 次</div></div>
        <div className="stat"><div className="stat-label">最近版本</div><div className="stat-value" style={{ fontSize: 18 }}>{runs[0]?.version ?? last?.version ?? "—"}</div><div className="stat-foot">{runs[0] ? when(runs[0].started_at) : last ? when(last.created_at) : "尚无记录"}</div></div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">每日发版</div>
            <div className="card-sub">候选集来自 GitHub 标签；commit 留空时取目标分支 HEAD</div>
          </div>
          <div>
            <button className="btn" onClick={() => void loadCandidates(runForm.project_id)}>刷新候选</button>{" "}
            <button className="btn" onClick={() => void load()}>刷新记录</button>
          </div>
        </div>
        <div className="card-body">
          <div className="form-grid">
            <div className="field">
              <label>项目</label>
              <select value={runForm.project_id} onChange={(e) => setRunForm((p) => ({ ...p, project_id: e.target.value }))}>
                <option value="">选择项目</option>
                {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
              </select>
            </div>
            <div className="field">
              <label>版本</label>
              <input value={runForm.version} onChange={(e) => setRunForm((p) => ({ ...p, version: e.target.value }))} placeholder="v2026.11.05" />
            </div>
            <div className="field">
              <label>commit（可留空）</label>
              <input value={runForm.commit} onChange={(e) => setRunForm((p) => ({ ...p, commit: e.target.value }))} placeholder="留空取分支 HEAD" />
            </div>
            <div className="field">
              <label>目标分支（可留空）</label>
              <input value={runForm.branch} onChange={(e) => setRunForm((p) => ({ ...p, branch: e.target.value }))} placeholder="项目默认分支" />
            </div>
            <div className="field">
              <label>制品 glob</label>
              <input value={runForm.globs} onChange={(e) => setRunForm((p) => ({ ...p, globs: e.target.value }))} placeholder="dist/**,bin/**" />
            </div>
            <div className="field">
              <label>说明</label>
              <input value={runForm.notes} onChange={(e) => setRunForm((p) => ({ ...p, notes: e.target.value }))} placeholder="版本说明或升级提示" />
            </div>
          </div>

          <div style={{ marginTop: 14 }}>
            <div className="tiny" style={{ marginBottom: 6 }}>
              候选 Issue（{candidateLabel || "smartpipe:ready"}）：{candidates.length} 个
            </div>
            {candidateError ? (
              <div className="chip bad"><span className="dot" />{candidateError}</div>
            ) : candidates.length === 0 ? (
              <div className="tiny">当前没有待发版 Issue。给 Issue 打上 {candidateLabel || "smartpipe:ready"} 标签后会出现在这里。</div>
            ) : (
              <table className="table">
                <thead><tr><th>Issue</th><th>标题</th><th>作者</th><th>链接</th></tr></thead>
                <tbody>
                  {candidates.map((c) => (
                    <tr key={c.number}>
                      <td className="mono">#{c.number}</td>
                      <td>{c.title}</td>
                      <td className="tiny">{c.author || "—"}</td>
                      <td className="tiny">{c.url}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>

          <div style={{ display: "flex", justifyContent: "flex-end", gap: 8, marginTop: 12 }}>
            <button className="btn" disabled={busy} onClick={() => void submitRun(true)}>{busy ? "执行中…" : "仅构建（Dry Run）"}</button>
            <button className="btn primary" disabled={busy || candidates.length === 0} onClick={() => void submitRun(false)}>
              {busy ? "发布中…" : "构建并发布"}
            </button>
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div><div className="card-title">发版运行记录</div><div className="card-sub">Release Note、制品指纹、标签回写与通知结果</div></div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {loading ? (
            <div className="empty">加载中…</div>
          ) : runs.length === 0 ? (
            <div className="empty"><h3>还没有发版运行</h3><div className="tiny">选择项目并执行一次发版后会显示在这里。</div></div>
          ) : (
            <table className="table">
              <thead><tr><th>时间</th><th>版本</th><th>候选</th><th>commit</th><th>状态</th><th>通知</th><th>Release</th><th></th></tr></thead>
              <tbody>
                {runs.map((r) => (
                  <tr key={r.id}>
                    <td className="tiny">{when(r.started_at)}</td>
                    <td className="strong">{r.version}</td>
                    <td className="tiny">{(r.candidates ?? []).length} 个</td>
                    <td className="mono tiny">{(r.commit_sha || "").slice(0, 10)}</td>
                    <td>{statusChip(r.status)}</td>
                    <td className="tiny">{r.notify_status || "—"}</td>
                    <td className="tiny">{r.release_url ? <a href={r.release_url} target="_blank" rel="noreferrer">打开</a> : "—"}</td>
                    <td style={{ whiteSpace: "nowrap" }}>
                      <button className="btn small" onClick={() => setRunDetail(r)}>详情</button>{" "}
                      {(r.status === "released" || r.status === "released_with_warnings") ? (
                        <button className="btn small" disabled={busy} onClick={() => void rollback(r)}>回滚</button>
                      ) : null}
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
            <div className="card-title">仅构建（不发布）</div>
            <div className="card-sub">用于验证某个 commit 的制品，不创建 tag、不改 Issue 标签</div>
          </div>
        </div>
        <div className="card-body">
          <div className="form-grid">
            <div className="field">
              <label>项目</label>
              <select value={form.project_id} onChange={(e) => setForm((p) => ({ ...p, project_id: e.target.value }))}>
                <option value="">不关联项目</option>
                {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
              </select>
            </div>
            <div className="field">
              <label>版本</label>
              <input value={form.version} onChange={(e) => setForm((p) => ({ ...p, version: e.target.value }))} placeholder="v0.1.0" />
            </div>
            <div className="field">
              <label>commit</label>
              <input value={form.commit} onChange={(e) => setForm((p) => ({ ...p, commit: e.target.value }))} placeholder="完整 sha" />
            </div>
            <div className="field">
              <label>制品 glob</label>
              <input value={form.globs} onChange={(e) => setForm((p) => ({ ...p, globs: e.target.value }))} placeholder="dist/**,bin/**" />
            </div>
            <div className="field full">
              <label>仓库路径或 URL（项目已配置时可留空）</label>
              <input value={form.repo} onChange={(e) => setForm((p) => ({ ...p, repo: e.target.value }))} />
            </div>
            <div className="field full">
              <label>备注</label>
              <input value={form.notes} onChange={(e) => setForm((p) => ({ ...p, notes: e.target.value }))} placeholder="本次构建用途" />
            </div>
          </div>
          <div style={{ display: "flex", justifyContent: "flex-end", marginTop: 12 }}>
            <button className="btn" disabled={busy} onClick={() => void submit()}>{busy ? "构建中…" : "开始构建"}</button>
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div><div className="card-title">构建记录</div><div className="card-sub">manifest 与制品校验和用于分发前复核</div></div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {loading ? (
            <div className="empty">加载中…</div>
          ) : error ? (
            <div className="empty"><h3>无法读取记录</h3><div className="tiny">{error}</div></div>
          ) : records.length === 0 ? (
            <div className="empty"><h3>还没有构建记录</h3><div className="tiny">提交一次构建后会显示版本、commit、manifest 指纹与制品。</div></div>
          ) : (
            <table className="table">
              <thead><tr><th>时间</th><th>版本</th><th>commit</th><th>manifest</th><th>制品</th><th>状态</th><th></th></tr></thead>
              <tbody>
                {records.map((r) => (
                  <tr key={r.id}>
                    <td className="tiny">{when(r.created_at)}</td>
                    <td className="strong">{r.version}</td>
                    <td className="mono tiny">{(r.commit_sha || "").slice(0, 10)}</td>
                    <td className="mono tiny">{shortHash(r.manifest_sha256)}</td>
                    <td className="tiny">{(r.artifacts ?? []).length} 个</td>
                    <td>{r.ok ? <span className="chip ok"><span className="dot" />{r.status}</span> : <span className="chip bad"><span className="dot" />{r.status}</span>}</td>
                    <td><button className="btn small" onClick={() => setDetail(r)}>详情</button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className={"modal-backdrop" + (runDetail ? " open" : "")}>
        <div className="modal" style={{ width: "min(900px, 100%)" }}>
          <div className="modal-head">
            <div>
              <div className="modal-title">{runDetail?.version} · {runDetail?.status}</div>
              <div className="modal-sub">commit {runDetail?.commit_sha} · tag {runDetail?.tag || "—"} · 通知 {runDetail?.notify_status || "—"}</div>
            </div>
            <button className="icon-btn" onClick={() => setRunDetail(null)}>✕</button>
          </div>
          <div className="modal-body">
            {runDetail?.error ? <div className="chip bad" style={{ marginBottom: 10 }}><span className="dot" />{runDetail.error}</div> : null}
            {runDetail?.notify_error ? <div className="chip bad" style={{ marginBottom: 10 }}><span className="dot" />通知失败：{runDetail.notify_error}</div> : null}
            <div className="tiny" style={{ marginBottom: 8 }}>
              Release：{runDetail?.release_url || "—"} · manifest {runDetail?.manifest_sha256 || "—"} · 耗时 {runDetail?.duration_ms ?? 0} ms
            </div>
            <div className="tiny" style={{ marginBottom: 6 }}>候选 Issue：{(runDetail?.candidates ?? []).map((c) => "#" + c.number).join("、") || "—"}</div>
            <pre className="mono" style={{ maxHeight: 300, overflow: "auto", background: "#0f172a", color: "#cbd5e1", padding: 10, borderRadius: 8, fontSize: 11, whiteSpace: "pre-wrap" }}>
              {runDetail?.notes || "（无 Release Note）"}
            </pre>
            {runDetail?.build_output ? (
              <details>
                <summary className="tiny">构建输出</summary>
                <pre className="mono" style={{ maxHeight: 200, overflow: "auto", background: "#111827", color: "#cbd5e1", padding: 10, borderRadius: 8, fontSize: 11 }}>
                  {runDetail.build_output}
                </pre>
              </details>
            ) : null}
          </div>
          <div className="modal-foot">
            <div className="tiny">失败时制品与日志保留在输出目录：{runDetail?.output_dir || "—"}</div>
            <button className="btn" onClick={() => setRunDetail(null)}>关闭</button>
          </div>
        </div>
      </div>

      <div className={"modal-backdrop" + (detail ? " open" : "")}>
        <div className="modal" style={{ width: "min(860px, 100%)" }}>
          <div className="modal-head">
            <div><div className="modal-title">{detail?.version}</div><div className="modal-sub">commit {detail?.commit_sha} · manifest {detail?.manifest_sha256}</div></div>
            <button className="icon-btn" onClick={() => setDetail(null)}>✕</button>
          </div>
          <div className="modal-body">
            {detail?.error ? <div className="chip bad" style={{ marginBottom: 10 }}><span className="dot" />{detail.error}</div> : null}
            <div className="tiny" style={{ marginBottom: 8 }}>输出目录：{detail?.output_dir || "—"} · 签名：{detail?.signature || "未签名"}</div>
            <table className="table">
              <thead><tr><th>制品</th><th>大小</th><th>sha256</th></tr></thead>
              <tbody>
                {(detail?.artifacts ?? []).map((a) => (
                  <tr key={a.path}><td className="mono tiny">{a.path}</td><td className="tiny">{a.size}</td><td className="mono tiny">{a.sha256}</td></tr>
                ))}
              </tbody>
            </table>
            <pre className="mono" style={{ maxHeight: 220, overflow: "auto", background: "#0f172a", color: "#cbd5e1", padding: 10, borderRadius: 8, fontSize: 11 }}>
              {JSON.stringify(detail?.manifest ?? {}, null, 2)}
            </pre>
          </div>
          <div className="modal-foot"><div className="tiny">制品与 manifest 保存在服务端输出目录。</div><button className="btn" onClick={() => setDetail(null)}>关闭</button></div>
        </div>
      </div>
    </>
  );
}