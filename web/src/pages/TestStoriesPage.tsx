import { useCallback, useEffect, useRef, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeSanitize from "rehype-sanitize";
import { api, getToken } from "../api";
import type { Notify } from "../App";
import type { Project } from "../types";
import type {
  SessionEvent,
  SessionEvidence,
  SessionFinding,
  TestSession,
  TestSessionInput,
  TestStoryEvent,
  TestStory,
  TestMode,
  StoryTriggerKind,
} from "../testlab-types";

type DraftEvent = {
  id: string;
  title: string;
  trigger_kind: StoryTriggerKind;
  trigger: string;
  method: string;
  expected: string;
};

type Draft = {
  id?: string;
  project_id: string;
  slug: string;
  title: string;
  body: string;
  mode: TestMode;
  environment: string;
  platform: string;
  checks: string;
  tags: string;
  events: DraftEvent[];
  timeout_seconds: number;
};

const MODES: TestMode[] = ["story", "bug", "explore", "hotswap", "gate"];
const PLATFORMS = ["windows-amd64", "darwin-arm64", "darwin-amd64", "linux-amd64"];
const TRIGGER_KINDS: StoryTriggerKind[] = ["ui", "shell", "http", "manual"];

function toDraftEvent(event: TestStoryEvent): DraftEvent {
  return {
    id: event.id,
    title: event.title,
    trigger_kind: event.trigger_kind ?? "ui",
    trigger: event.trigger ?? "",
    method: event.method,
    expected: event.expected ?? "",
  };
}

function emptyEvent(): DraftEvent {
  return { id: "", title: "", trigger_kind: "ui", trigger: "", method: "", expected: "" };
}

function slugify(value: string): string {
  return value.toLowerCase().trim().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 64);
}

function toDraft(story: TestStory): Draft {
  return {
    id: story.id,
    project_id: story.project_id,
    slug: story.slug,
    title: story.title,
    body: story.body,
    mode: story.mode,
    environment: story.environment,
    platform: story.platform,
    checks: story.required_checks.join(", "),
    tags: story.tags.join(", "),
    events: (story.events ?? []).map(toDraftEvent),
    timeout_seconds: story.timeout_seconds || 1800,
  };
}

function emptyDraft(projectId: string): Draft {
  return {
    project_id: projectId, slug: "", title: "", body: "",
    mode: "story", environment: "syntropy", platform: "windows-amd64",
    checks: "", tags: "", events: [], timeout_seconds: 1800,
  };
}

function splitList(value: string): string[] {
  return value.split(/[\s,，]+/).map(item => item.trim()).filter(Boolean);
}

function statusClass(status?: string): string {
  switch (status) {
    case "PASS": return "chip ok";
    case "FAIL": return "chip bad";
    case "BLOCKED": return "chip warn";
    case "RUNNING": return "chip";
    case "QUEUED": return "chip";
    default: return "chip";
  }
}

function severityClass(severity: string): string {
  if (severity === "blocker" || severity === "critical") return "chip bad";
  if (severity === "major") return "chip warn";
  return "chip";
}

export function TestStoriesPage({ notify }: { notify: Notify }) {
  const [projects, setProjects] = useState<Project[]>([]);
  const [projectId, setProjectId] = useState("");
  const [query, setQuery] = useState("");
  const [stories, setStories] = useState<TestStory[]>([]);
  const [sessions, setSessions] = useState<TestSession[]>([]);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [preview, setPreview] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [detail, setDetail] = useState<TestSession | null>(null);
  const [detailInputs, setDetailInputs] = useState<TestSessionInput[]>([]);
  const [detailEvents, setDetailEvents] = useState<SessionEvent[]>([]);
  const [detailEvidence, setDetailEvidence] = useState<SessionEvidence[]>([]);
  const [evidencePreview, setEvidencePreview] = useState<{ url: string; label: string } | null>(null);
  const [viewing, setViewing] = useState<TestStory | null>(null);
  const [runTarget, setRunTarget] = useState<TestStory | null>(null);
  const [runSelection, setRunSelection] = useState<string[]>([]);
  const [importResult, setImportResult] = useState<Array<{ slug: string; action: string; error?: string }>>([]);
  const [selectedStoryIDs, setSelectedStoryIDs] = useState<string[]>([]);
  const [batchResult, setBatchResult] = useState<Array<{ story_id: string; session_id?: string; error?: string }>>([]);
  const fileRef = useRef<HTMLInputElement | null>(null);
  const dirRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    dirRef.current?.setAttribute("webkitdirectory", "");
  }, []);

  const loadStories = useCallback(async (project = projectId, q = query) => {
    const items = await api.listTestStories(project, q);
    setStories(items);
    setSelectedStoryIDs(current => current.filter(id => items.some(story => story.id === id)));
  }, [projectId, query]);

  const loadSessions = useCallback(async (project = projectId) => {
    const items = await api.listTestSessions(project);
    setSessions(items);
  }, [projectId]);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const list = await api.listProjects();
        if (cancelled) return;
        setProjects(list);
        setProjectId(current => current || list[0]?.id || "");
      } catch (e) {
        setError(e instanceof Error ? e.message : "读取项目失败");
      }
    })();
    return () => { cancelled = true; };
  }, []);

  useEffect(() => {
    if (!projectId) return;
    setBusy(true);
    void (async () => {
      try {
        await Promise.all([loadStories(projectId, ""), loadSessions(projectId)]);
        setError("");
      } catch (e) {
        setError(e instanceof Error ? e.message : "读取故事失败");
      } finally {
        setBusy(false);
      }
    })();
  }, [projectId, loadStories, loadSessions]);

  useEffect(() => {
    if (!projectId) return;
    const timer = setInterval(() => { void loadSessions(projectId).catch(() => undefined); }, 8000);
    return () => clearInterval(timer);
  }, [projectId, loadSessions]);

  async function refresh() {
    setBusy(true);
    try {
      await Promise.all([loadStories(projectId, query), loadSessions(projectId)]);
    } catch (e) {
      notify(e instanceof Error ? e.message : "刷新失败", "error");
    } finally {
      setBusy(false);
    }
  }

  async function save() {
    if (!draft) return;
    if (!draft.title.trim() || !draft.body.trim()) {
      notify("标题和故事正文不能为空", "error");
      return;
    }
    setBusy(true);
    try {
      const events = draft.events
        .map((event, index) => ({
          id: event.id.trim() || slugify(event.title) || "event-" + (index + 1),
          title: event.title.trim(),
          trigger_kind: event.trigger_kind,
          trigger: event.trigger.trim(),
          method: event.method.trim() || event.id.trim() || slugify(event.title) || "event-" + (index + 1),
          expected: event.expected.trim(),
        }))
        .filter(event => event.title || event.trigger || event.id || event.method);
      for (const event of events) {
        if (!event.title) {
          notify("每个事件都需要标题", "error");
          return;
        }
        if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(event.id)) {
          notify("事件 ID 只能用小写字母、数字、- 和 _：" + (event.id || event.title), "error");
          return;
        }
        if (!/^[a-zA-Z0-9_.-]{1,80}$/.test(event.method)) {
          notify("测试方法 ID 只能包含字母、数字、_ . -：" + event.method, "error");
          return;
        }
      }
      const eventIDs = events.map(event => event.id);
      const eventMethods = events.map(event => event.method);
      if (new Set(eventIDs).size !== eventIDs.length) {
        notify("事件 ID 不能重复", "error");
        return;
      }
      if (new Set(eventMethods).size !== eventMethods.length) {
        notify("测试方法 ID 不能重复", "error");
        return;
      }
      const body = {
        project_id: draft.project_id,
        slug: draft.slug.trim() || draft.title.trim(),
        title: draft.title.trim(),
        body: draft.body,
        mode: draft.mode,
        environment: draft.environment.trim() || "syntropy",
        platform: draft.platform,
        required_checks: [...new Set([...splitList(draft.checks), ...eventMethods])],
        tags: splitList(draft.tags),
        events,
        timeout_seconds: draft.timeout_seconds,
      };
      const saved = draft.id
        ? await api.updateTestStory(draft.id, body)
        : await api.createTestStory(body);
      setDraft(toDraft(saved));
      await loadStories();
      notify("故事已保存");
    } catch (e) {
      notify(e instanceof Error ? e.message : "保存失败", "error");
    } finally {
      setBusy(false);
    }
  }

  function openRun(story: TestStory) {
    setRunTarget(story);
    setRunSelection((story.events ?? []).map(event => event.id));
  }

  function updateDraftEvent(index: number, patch: Partial<DraftEvent>) {
    setDraft(current => current
      ? { ...current, events: current.events.map((event, i) => i === index ? { ...event, ...patch } : event) }
      : current);
  }

  function removeDraftEvent(index: number) {
    setDraft(current => current ? { ...current, events: current.events.filter((_, i) => i !== index) } : current);
  }

  async function remove(story: TestStory) {
    if (!window.confirm("删除故事「" + story.title + "」？已触发的测试记录不受影响。")) return;
    setBusy(true);
    try {
      await api.deleteTestStory(story.id);
      if (draft?.id === story.id) setDraft(null);
      await loadStories();
      notify("故事已删除");
    } catch (e) {
      notify(e instanceof Error ? e.message : "删除失败", "error");
    } finally {
      setBusy(false);
    }
  }

  async function triggerRun(story: TestStory, eventIDs: string[]) {
    setBusy(true);
    try {
      const result = await api.runTestStory(story.id, {
        environment: story.environment,
        platform: story.platform,
        timeout_seconds: story.timeout_seconds,
        event_ids: eventIDs.length > 0 && eventIDs.length < (story.events ?? []).length ? eventIDs : undefined,
      });
      await loadSessions();
      setRunTarget(null);
      notify("已排队：" + result.session.id + (eventIDs.length ? "（" + eventIDs.length + " 个事件）" : "") + "，等待测试机领取");
    } catch (e) {
      notify(e instanceof Error ? e.message : "触发失败", "error");
    } finally {
      setBusy(false);
    }
  }

  async function importFiles(files: FileList | null) {
    if (!files?.length || !projectId) return;
    setBusy(true);
    try {
      const storiesToImport: Array<{ filename: string; content: string }> = [];
      for (const file of Array.from(files)) {
        if (!/\.md$/i.test(file.name)) continue;
        const relative = (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name;
        storiesToImport.push({ filename: relative, content: await file.text() });
      }
      if (!storiesToImport.length) {
        notify("请选择 Markdown（.md）文件", "error");
        return;
      }
      const result = await api.importTestStories({ project_id: projectId, stories: storiesToImport });
      const ok = result.items.filter(item => item.action === "imported").length;
      setImportResult(result.items);
      await loadStories();
      notify("已导入 " + ok + " 个故事" + (ok < result.items.length ? "，其余被拒绝（见导入结果）" : ""));
    } catch (e) {
      notify(e instanceof Error ? e.message : "导入失败", "error");
    } finally {
      setBusy(false);
      if (fileRef.current) fileRef.current.value = "";
      if (dirRef.current) dirRef.current.value = "";
    }
  }

  async function runBatch() {
    if (!selectedStoryIDs.length) return;
    if (!window.confirm("为选中的 " + selectedStoryIDs.length + " 个故事各创建一个测试会话？每个故事按定义执行全部事件。")) return;
    setBusy(true);
    try {
      const result = await api.runTestStoriesBatch({ story_ids: selectedStoryIDs });
      setBatchResult(result.items.map(item => ({
        story_id: item.story_id, session_id: item.session?.id, error: item.error,
      })));
      setSelectedStoryIDs([]);
      await loadSessions();
      const failed = result.items.filter(item => item.error).length;
      notify("已排队 " + result.queued + " 个故事" + (failed ? "，" + failed + " 个失败（见批量结果）" : ""));
    } catch (e) {
      notify(e instanceof Error ? e.message : "批量触发失败", "error");
    } finally {
      setBusy(false);
    }
  }

  async function openSession(session: TestSession) {
    setBusy(true);
    try {
      const [fresh, inputs, events, evidence] = await Promise.all([
        api.getTestSession(session.id),
        api.listTestSessionInputs(session.id),
        api.listTestSessionEvents(session.id),
        api.listTestSessionEvidence(session.id),
      ]);
      setDetail(fresh);
      setDetailInputs(inputs);
      setDetailEvents(events);
      setDetailEvidence(evidence);
    } catch (e) {
      notify(e instanceof Error ? e.message : "读取测试详情失败", "error");
    } finally {
      setBusy(false);
    }
  }

  const detailID = detail?.id;
  const detailRunning = detail?.status === "RUNNING" || detail?.status === "QUEUED";
  useEffect(() => {
    if (!detailID || !detailRunning) return;
    let cancelled = false;
    const tick = async () => {
      try {
        const [fresh, events, evidence] = await Promise.all([
          api.getTestSession(detailID),
          api.listTestSessionEvents(detailID),
          api.listTestSessionEvidence(detailID),
        ]);
        if (cancelled) return;
        setDetail(current => (current?.id === detailID ? fresh : current));
        setDetailEvents(events);
        setDetailEvidence(evidence);
      } catch {
        // The next tick retries; a transient poll failure must not close the panel.
      }
    };
    const timer = setInterval(() => void tick(), 5_000);
    return () => { cancelled = true; clearInterval(timer); };
  }, [detailID, detailRunning]);

  async function downloadEvidence(sessionID: string, reference?: string) {
    if (!reference || !/^sha256:[a-f0-9]{64}$/.test(reference)) {
      notify("该检查项没有可下载的证据", "error");
      return;
    }
    try {
      const headers: Record<string, string> = {};
      const token = getToken();
      if (token) headers.Authorization = "Bearer " + token;
      const response = await fetch("/admin/test-sessions/" + encodeURIComponent(sessionID) + "/evidence/" + reference.slice(7),
        { headers, credentials: "same-origin" });
      if (!response.ok) throw new Error("证据下载失败（" + response.status + "）");
      const url = URL.createObjectURL(await response.blob());
      const link = document.createElement("a");
      link.href = url;
      link.download = reference.slice(7);
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (e) {
      notify(e instanceof Error ? e.message : "下载失败", "error");
    }
  }

  async function previewEvidence(sessionID: string, reference?: string) {
    if (!reference || !/^sha256:[a-f0-9]{64}$/.test(reference)) {
      notify("该检查项没有可预览的证据", "error");
      return;
    }
    try {
      const headers: Record<string, string> = {};
      const token = getToken();
      if (token) headers.Authorization = "Bearer " + token;
      const response = await fetch("/admin/test-sessions/" + encodeURIComponent(sessionID) + "/evidence/" + reference.slice(7),
        { headers, credentials: "same-origin" });
      if (!response.ok) throw new Error("证据读取失败（" + response.status + "）");
      const blob = await response.blob();
      if (!blob.type.startsWith("image/")) {
        notify("该证据不是图片（" + (blob.type || "未知类型") + "），请下载查看", "error");
        return;
      }
      setEvidencePreview(current => {
        if (current) URL.revokeObjectURL(current.url);
        return { url: URL.createObjectURL(blob), label: reference.slice(7, 15) };
      });
    } catch (e) {
      notify(e instanceof Error ? e.message : "预览失败", "error");
    }
  }

  function closeEvidencePreview() {
    setEvidencePreview(current => {
      if (current) URL.revokeObjectURL(current.url);
      return null;
    });
  }

  return <>
    <div className="notice">
      故事库是测试的编写入口：导入或编辑 Markdown，选择模式后触发测试。触发时会快照正文，后续修改不会改变历史测试记录。
      引擎只消费 SmartPipe 调度准备好的输入包，不会自己拉取源码。
    </div>
    {error && <div role="alert" className="notice">{error}</div>}

    <div className="card" style={{ marginBottom: 16 }}>
      <div className="card-head">
        <div>
          <div className="card-title">故事库</div>
          <div className="card-sub">项目、标签与必测项会随故事一起保存</div>
        </div>
        <div className="row-actions">
          <button className="btn small" disabled={busy} onClick={() => void refresh()}>刷新</button>
          <button className="btn small" disabled={!projectId} onClick={() => fileRef.current?.click()}>导入 Markdown</button>
          <button className="btn small" disabled={!projectId} onClick={() => dirRef.current?.click()}>导入目录</button>
          <button className="btn small" disabled={busy || !selectedStoryIDs.length} onClick={() => void runBatch()}>
            批量触发（{selectedStoryIDs.length}）
          </button>
          <button className="btn small primary" disabled={!projectId} onClick={() => setDraft(emptyDraft(projectId))}>新建故事</button>
          <input ref={fileRef} type="file" multiple accept=".md,text/markdown" style={{ display: "none" }}
            onChange={e => void importFiles(e.target.files)} />
          <input ref={dirRef} type="file" multiple accept=".md,text/markdown" style={{ display: "none" }}
            onChange={e => void importFiles(e.target.files)} />
        </div>
      </div>
      <div className="card-body">
        <div className="grid two">
          <label>项目
            <select value={projectId} onChange={e => setProjectId(e.target.value)}>
              {projects.map(project => <option key={project.id} value={project.id}>{project.name}</option>)}
            </select>
          </label>
          <label>搜索
            <input value={query} placeholder="标题或 slug" onChange={e => setQuery(e.target.value)}
              onKeyDown={e => { if (e.key === "Enter") void refresh(); }} />
          </label>
        </div>
        <table className="table">
          <thead><tr>
            <th style={{ width: 32 }}><input type="checkbox" aria-label="选择全部故事"
              checked={stories.length > 0 && selectedStoryIDs.length === stories.length}
              onChange={e => setSelectedStoryIDs(e.target.checked ? stories.map(story => story.id) : [])} /></th>
            <th>标题</th><th>模式</th><th>事件 / 测试方法</th><th>必测项</th><th>更新时间</th><th /></tr></thead>
          <tbody>
            {stories.map(story => <tr key={story.id}>
              <td><input type="checkbox" aria-label={"选择故事 " + story.title}
                checked={selectedStoryIDs.includes(story.id)}
                onChange={e => setSelectedStoryIDs(current => e.target.checked
                  ? [...current, story.id]
                  : current.filter(id => id !== story.id))} /></td>
              <td>
                <button className="btn small" style={{ padding: 0, border: "none", background: "none", fontWeight: 600 }}
                  onClick={() => setViewing(story)}>{story.title}</button>
                <div className="mono muted">{story.slug}</div>
              </td>
              <td><span className="chip">{story.mode}</span></td>
              <td>
                {(story.events ?? []).length
                  ? <div className="mono" title={(story.events ?? []).map(event => event.id + " → " + event.method).join("\n")}>
                      {(story.events ?? []).length} 个事件
                      <div className="muted" style={{ maxWidth: 260, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                        {(story.events ?? []).map(event => event.method).join(", ")}
                      </div>
                    </div>
                  : <span className="muted">未定义</span>}
              </td>
              <td className="mono">{story.required_checks.join(", ") || "—"}</td>
              <td className="muted">{story.updated_at ? new Date(story.updated_at).toLocaleString() : "—"}</td>
              <td>
                <div className="row-actions">
                  <button className="btn small" onClick={() => setViewing(story)}>查看</button>
                  <button className="btn small" onClick={() => { setDraft(toDraft(story)); setPreview(false); }}>编辑</button>
                  <button className="btn small primary" disabled={busy} onClick={() => openRun(story)}>触发测试</button>
                  <button className="btn small danger" disabled={busy} onClick={() => void remove(story)}>删除</button>
                </div>
              </td>
            </tr>)}
            {!stories.length && <tr><td colSpan={7} className="muted">还没有故事。点击「新建故事」或导入 .md 文件。</td></tr>}
          </tbody>
        </table>
        {batchResult.length > 0 && <div style={{ marginTop: 12 }}>
          <div className="row-actions" style={{ justifyContent: "space-between" }}>
            <strong>批量触发结果</strong>
            <button className="btn small" onClick={() => setBatchResult([])}>清空</button>
          </div>
          <table className="table">
            <thead><tr><th>故事</th><th>会话</th><th>结果</th></tr></thead>
            <tbody>
              {batchResult.map((item, index) => <tr key={item.story_id + "-" + index}>
                <td className="mono">{item.story_id}</td>
                <td className="mono muted">{item.session_id || "—"}</td>
                <td>{item.error ? <span className="chip bad">{item.error}</span> : <span className="chip ok">queued</span>}</td>
              </tr>)}
            </tbody>
          </table>
        </div>}
        {importResult.length > 0 && <div style={{ marginTop: 12 }}>
          <div className="row-actions" style={{ justifyContent: "space-between" }}>
            <strong>导入结果</strong>
            <button className="btn small" onClick={() => setImportResult([])}>清空</button>
          </div>
          <table className="table">
            <thead><tr><th>Slug</th><th>结果</th><th>原因</th></tr></thead>
            <tbody>
              {importResult.map((item, index) => <tr key={item.slug + "-" + index}>
                <td className="mono">{item.slug}</td>
                <td><span className={item.action === "imported" ? "chip ok" : "chip bad"}>{item.action}</span></td>
                <td className="muted">{item.error || "—"}</td>
              </tr>)}
            </tbody>
          </table>
        </div>}
      </div>
    </div>

    {draft && <div className="card" style={{ marginBottom: 16 }}>
      <div className="card-head">
        <div>
          <div className="card-title">{draft.id ? "编辑故事" : "新建故事"}</div>
          <div className="card-sub">{draft.id ? draft.id : "保存后生成 ID"}</div>
        </div>
        <div className="row-actions">
          <button className="btn small" onClick={() => setPreview(value => !value)}>{preview ? "编辑" : "预览"}</button>
          <button className="btn small" onClick={() => setDraft(null)}>关闭</button>
          <button className="btn small primary" disabled={busy} onClick={() => void save()}>保存</button>
        </div>
      </div>
      <div className="card-body">
        <div className="grid two">
          <label>标题<input value={draft.title} onChange={e => setDraft({ ...draft, title: e.target.value })} /></label>
          <label>Slug<input value={draft.slug} placeholder="留空则按标题生成" onChange={e => setDraft({ ...draft, slug: e.target.value })} /></label>
          <label>模式<select value={draft.mode} onChange={e => setDraft({ ...draft, mode: e.target.value as TestMode })}>
            {MODES.map(mode => <option key={mode} value={mode}>{mode}</option>)}
          </select></label>
          <label>平台<select value={draft.platform} onChange={e => setDraft({ ...draft, platform: e.target.value })}>
            {PLATFORMS.map(platform => <option key={platform} value={platform}>{platform}</option>)}
          </select></label>
          <label>环境<input value={draft.environment} onChange={e => setDraft({ ...draft, environment: e.target.value })} /></label>
          <label>超时（秒）<input type="number" min={60} max={14400} value={draft.timeout_seconds}
            onChange={e => setDraft({ ...draft, timeout_seconds: Number(e.target.value) || 1800 })} /></label>
          <label>必测项<input value={draft.checks} placeholder="app_installed, app_window"
            onChange={e => setDraft({ ...draft, checks: e.target.value })} /></label>
          <label>标签<input value={draft.tags} placeholder="smoke, ui" onChange={e => setDraft({ ...draft, tags: e.target.value })} /></label>
        </div>
        <div style={{ marginTop: 16 }}>
          <div className="row-actions" style={{ justifyContent: "space-between", alignItems: "flex-start" }}>
            <div>
              <strong>事件与测试方法</strong>
              <div className="muted">
                每行是一个可单独触发的测试事件；方法 ID 是 agent 记录检查项时使用的 ID，也是触发时的筛选键。
                保存时会自动并入必测项。
              </div>
            </div>
            <button className="btn small" onClick={() => setDraft({ ...draft, events: [...draft.events, emptyEvent()] })}>
              添加事件
            </button>
          </div>
          <table className="table" style={{ marginTop: 8 }}>
            <thead><tr><th>事件 ID</th><th>标题</th><th>触发方式</th><th>触发内容</th><th>测试方法</th><th>预期</th><th /></tr></thead>
            <tbody>
              {draft.events.map((event, index) => <tr key={index}>
                <td><input className="mono" style={{ minWidth: 130 }} value={event.id}
                  placeholder="open-settings"
                  onChange={e => updateDraftEvent(index, { id: e.target.value })} /></td>
                <td><input style={{ minWidth: 140 }} value={event.title} placeholder="打开设置页"
                  onChange={e => updateDraftEvent(index, { title: e.target.value })}
                  onBlur={e => {
                    if (!event.id.trim() && e.target.value.trim()) updateDraftEvent(index, { id: slugify(e.target.value) });
                  }} /></td>
                <td><select value={event.trigger_kind}
                  onChange={e => updateDraftEvent(index, { trigger_kind: e.target.value as StoryTriggerKind })}>
                  {TRIGGER_KINDS.map(kind => <option key={kind} value={kind}>{kind}</option>)}
                </select></td>
                <td><input className="mono" style={{ minWidth: 180 }} value={event.trigger}
                  placeholder={event.trigger_kind === "shell" ? "powershell -File ..." : "点击 设置 → 关于"}
                  onChange={e => updateDraftEvent(index, { trigger: e.target.value })} /></td>
                <td><input className="mono" style={{ minWidth: 130 }} value={event.method}
                  placeholder="settings_open"
                  onChange={e => updateDraftEvent(index, { method: e.target.value })} /></td>
                <td><input style={{ minWidth: 170 }} value={event.expected} placeholder="设置页可见且可交互"
                  onChange={e => updateDraftEvent(index, { expected: e.target.value })} /></td>
                <td><button className="btn small danger" onClick={() => removeDraftEvent(index)}>删除</button></td>
              </tr>)}
              {!draft.events.length && <tr>
                <td colSpan={7} className="muted">未定义事件时，触发测试会执行整篇故事。也可以从 Markdown 的 events 段导入。</td>
              </tr>}
            </tbody>
          </table>
        </div>
        <div style={{ marginTop: 12 }}>
          {preview
            ? <div className="markdown-body"><ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeSanitize]}>{draft.body}</ReactMarkdown></div>
            : <textarea className="mono" style={{ width: "100%", minHeight: 320 }} value={draft.body}
              placeholder={"# 测试故事\n\n1. 打开产品\n2. 执行操作\n3. 验证界面和日志\n"}
              onChange={e => setDraft({ ...draft, body: e.target.value })} />}
        </div>
      </div>
    </div>}

    <div className="card">
      <div className="card-head">
        <div>
          <div className="card-title">最近测试</div>
          <div className="card-sub">点击一行查看检查项、问题列表与输入包</div>
        </div>
        <button className="btn small" disabled={busy} onClick={() => void refresh()}>刷新</button>
      </div>
      <div className="card-body">
        <table className="table">
          <thead><tr><th>会话</th><th>标题</th><th>状态</th><th>判定</th><th>机器</th><th>创建时间</th><th /></tr></thead>
          <tbody>
            {sessions.map(session => <tr key={session.id}>
              <td className="mono">{session.id.slice(0, 18)}…</td>
              <td>{session.spec.title}</td>
              <td><span className={statusClass(session.status)}>{session.status}</span></td>
              <td>{session.verdict ? <span className={statusClass(session.verdict)}>{session.verdict}</span> : <span className="muted">—</span>}</td>
              <td className="mono muted" style={{ maxWidth: 200, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}
                title={session.machine_id}>{session.machine_id ? session.machine_id.slice(0, 24) + "…" : "—"}</td>
              <td className="muted">{new Date(session.created_at).toLocaleString()}</td>
              <td><button className="btn small" onClick={() => void openSession(session)}>详情</button></td>
            </tr>)}
            {!sessions.length && <tr><td colSpan={7} className="muted">还没有测试记录。</td></tr>}
          </tbody>
        </table>
      </div>
    </div>

    {detail && <div className="card" style={{ marginTop: 16 }}>
      <div className="card-head">
        <div>
          <div className="card-title">{detail.spec.title}</div>
          <div className="card-sub mono">{detail.id}</div>
        </div>
        <div className="row-actions">
          <span className={statusClass(detail.status)}>{detail.status}</span>
          {detail.verdict && <span className={statusClass(detail.verdict)}>{detail.verdict}</span>}
          <button className="btn small" onClick={() => setDetail(null)}>关闭</button>
        </div>
      </div>
      <div className="card-body">
        {detail.summary && <div className="notice">{detail.summary}</div>}
        <div className="grid two">
          <div><div className="muted">模式</div><div>{detail.spec.mode || detail.spec.kind}</div></div>
          <div><div className="muted">环境 / 平台</div><div>{detail.spec.environment} / {detail.spec.platform}</div></div>
          <div><div className="muted">开始</div><div>{detail.started_at ? new Date(detail.started_at).toLocaleString() : "—"}</div></div>
          <div><div className="muted">截止</div><div>{detail.deadline ? new Date(detail.deadline).toLocaleString() : "—"}</div></div>
        </div>
        {(detail.spec.events ?? []).length > 0 && <>
          <h4 style={{ marginTop: 16 }}>本次触发的事件与测试方法</h4>
          <div className="muted" style={{ marginBottom: 6 }}>
            事件来自触发时的故事快照；方法 ID 与下方检查项按 ID 对应，agent 每执行一个事件就回填一条检查结果。
          </div>
          <table className="table">
            <thead><tr><th>事件</th><th>触发</th><th>测试方法</th><th>结果</th><th>说明</th><th>证据</th></tr></thead>
            <tbody>
              {(detail.spec.events ?? []).map(event => {
                const check = (detail.checks ?? []).find(item => item.id === event.method);
                return <tr key={event.id}>
                  <td><div>{event.title}</div><div className="mono muted">{event.id}</div></td>
                  <td><span className="chip">{event.trigger_kind || "manual"}</span>
                    {event.trigger && <div className="mono muted" style={{ maxWidth: 220, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}
                      title={event.trigger}>{event.trigger}</div>}</td>
                  <td className="mono">{event.method}</td>
                  <td>{check ? <span className={statusClass(check.status)}>{check.status}</span> : <span className="muted">未记录</span>}</td>
                  <td>{check?.note
                    ? <div style={{ maxWidth: 360, maxHeight: 120, overflow: "auto", whiteSpace: "pre-wrap", wordBreak: "break-word" }}>{check.note}</div>
                    : "—"}</td>
                  <td>{check?.evidence
                    ? <div className="row-actions">
                        <button className="btn small" onClick={() => void previewEvidence(detail.id, check.evidence)}>预览</button>
                        <button className="btn small" onClick={() => void downloadEvidence(detail.id, check.evidence)}>下载</button>
                      </div>
                    : <span className="muted">无</span>}</td>
                </tr>;
              })}
            </tbody>
          </table>
        </>}
        <h4 style={{ marginTop: 16 }}>执行事件</h4>
        <div className="muted" style={{ marginBottom: 6 }}>
          引擎在测试机上执行的步骤；测试方法（检查项）由 agent 在这些事件中记录并附带证据。
        </div>
        <table className="table">
          <thead><tr><th>#</th><th>时间</th><th>类型</th><th>内容</th></tr></thead>
          <tbody>
            {detailEvents.map(event => <tr key={event.seq}>
              <td className="mono muted">{event.seq}</td>
              <td className="muted">{new Date(event.at).toLocaleTimeString()}</td>
              <td><span className="chip">{event.kind}</span></td>
              <td className="mono" style={{ whiteSpace: "pre-wrap", wordBreak: "break-word" }}>{event.message}</td>
            </tr>)}
            {!detailEvents.length && <tr><td colSpan={4} className="muted">暂无事件（队列中或引擎未上报）</td></tr>}
          </tbody>
        </table>
        <h4 style={{ marginTop: 16 }}>测试方法（检查项）</h4>
        {(detail.spec.required_checks ?? []).length > 0 && <div className="muted" style={{ marginBottom: 6 }}>
          必测方法：{(detail.spec.required_checks ?? []).map(id => {
            const recorded = (detail.checks ?? []).find(check => check.id === id);
            return <span key={id} className={recorded ? statusClass(recorded.status) : "chip"} style={{ marginRight: 6 }}>{id}</span>;
          })}
        </div>}
        <table className="table">
          <thead><tr><th>ID</th><th>状态</th><th>说明</th><th>证据</th></tr></thead>
          <tbody>
            {(detail.checks ?? []).map(check => <tr key={check.id}>
              <td className="mono">{check.id}</td>
              <td><span className={statusClass(check.status)}>{check.status}</span></td>
              <td>{check.note
                ? <div style={{ maxWidth: 520, maxHeight: 180, overflow: "auto", whiteSpace: "pre-wrap", wordBreak: "break-word" }}>{check.note}</div>
                : "—"}</td>
              <td>{check.evidence
                ? <div className="row-actions">
                    <button className="btn small" onClick={() => void previewEvidence(detail.id, check.evidence)}>预览</button>
                    <button className="btn small" onClick={() => void downloadEvidence(detail.id, check.evidence)}>下载</button>
                  </div>
                : <span className="muted">无</span>}</td>
            </tr>)}
            {!(detail.checks ?? []).length && <tr><td colSpan={4} className="muted">暂无检查项</td></tr>}
          </tbody>
        </table>
        <h4 style={{ marginTop: 16 }}>问题列表</h4>
        {(detail.findings ?? []).map((finding: SessionFinding) => <div key={finding.id} className="card" style={{ marginBottom: 8 }}>
          <div className="card-body">
            <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
              <span className={severityClass(finding.severity)}>{finding.severity}</span>
              <strong>{finding.title}</strong>
              <span className="mono muted">{finding.id}</span>
            </div>
            {finding.detail && <p>{finding.detail}</p>}
            {finding.repro && <p><span className="muted">复现：</span>{finding.repro}</p>}
            {finding.expected && <p><span className="muted">预期：</span>{finding.expected}</p>}
            {finding.actual && <p><span className="muted">实际：</span>{finding.actual}</p>}
            {(finding.evidence ?? []).length > 0 && <div className="row-actions">
              {(finding.evidence ?? []).map(ref => <span key={ref} style={{ display: "inline-flex", gap: 4 }}>
                <button className="btn small" onClick={() => void previewEvidence(detail.id, ref)}>预览 {ref.slice(7, 11)}</button>
                <button className="btn small" onClick={() => void downloadEvidence(detail.id, ref)}>下载</button>
              </span>)}
            </div>}
          </div>
        </div>)}
        {!(detail.findings ?? []).length && <div className="muted">暂无问题记录</div>}
        <h4 style={{ marginTop: 16 }}>输入包</h4>
        <table className="table">
          <thead><tr><th>名称</th><th>类型</th><th>SHA-256</th><th>大小</th></tr></thead>
          <tbody>
            {detailInputs.map(input => <tr key={input.name}>
              <td className="mono">{input.name}</td>
              <td>{input.kind}</td>
              <td className="mono">{input.sha256.slice(0, 16)}…</td>
              <td>{input.bytes} B</td>
            </tr>)}
            {!detailInputs.length && <tr><td colSpan={4} className="muted">该会话没有输入包（纯故事模式）</td></tr>}
          </tbody>
        </table>
        <h4 style={{ marginTop: 16 }}>证据包</h4>
        <div className="muted" style={{ marginBottom: 6 }}>
          引擎与 agent 上传到 SmartPipe 的全部证据；图片可直接预览（悬浮窗内可看截图）。
        </div>
        <table className="table">
          <thead><tr><th>SHA-256</th><th>类型</th><th>大小</th><th /></tr></thead>
          <tbody>
            {detailEvidence.map(item => {
              const ref = "sha256:" + item.sha256;
              return <tr key={item.sha256}>
                <td className="mono">{item.sha256.slice(0, 16)}…</td>
                <td>{item.mime_type}</td>
                <td>{item.bytes} B</td>
                <td><div className="row-actions">
                  <button className="btn small" onClick={() => void previewEvidence(detail.id, ref)}>预览</button>
                  <button className="btn small" onClick={() => void downloadEvidence(detail.id, ref)}>下载</button>
                </div></td>
              </tr>;
            })}
            {!detailEvidence.length && <tr><td colSpan={4} className="muted">暂无证据</td></tr>}
          </tbody>
        </table>
      </div>
    </div>}
    {viewing && <div role="dialog" aria-modal="true" onClick={() => setViewing(null)}
      style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,.6)", zIndex: 1000,
        display: "flex", alignItems: "center", justifyContent: "center", padding: 24 }}>
      <div onClick={e => e.stopPropagation()}
        style={{ width: 880, maxWidth: "92vw", maxHeight: "90vh", overflow: "auto", background: "#fff", borderRadius: 8, padding: 16 }}>
        <div className="row-actions" style={{ justifyContent: "space-between", alignItems: "flex-start" }}>
          <div>
            <div className="card-title">{viewing.title}</div>
            <div className="mono muted">{viewing.id} · {viewing.slug}</div>
          </div>
          <div className="row-actions">
            <button className="btn small" onClick={() => { setDraft(toDraft(viewing)); setViewing(null); setPreview(false); }}>编辑</button>
            <button className="btn small primary" onClick={() => { openRun(viewing); setViewing(null); }}>触发测试</button>
            <button className="btn small" onClick={() => setViewing(null)}>关闭</button>
          </div>
        </div>
        <div className="grid two" style={{ marginTop: 12 }}>
          <div><div className="muted">模式 / 环境</div><div>{viewing.mode} / {viewing.environment}</div></div>
          <div><div className="muted">平台 / 超时</div><div>{viewing.platform} / {viewing.timeout_seconds}s</div></div>
          <div><div className="muted">标签</div><div>{viewing.tags.join(", ") || "—"}</div></div>
          <div><div className="muted">必测方法</div><div className="mono">{viewing.required_checks.join(", ") || "—"}</div></div>
        </div>
        <h4 style={{ marginTop: 16 }}>事件与测试方法</h4>
        <table className="table">
          <thead><tr><th>事件 ID</th><th>标题</th><th>触发</th><th>测试方法</th><th>预期</th></tr></thead>
          <tbody>
            {(viewing.events ?? []).map(event => <tr key={event.id}>
              <td className="mono">{event.id}</td>
              <td>{event.title}</td>
              <td><span className="chip">{event.trigger_kind || "manual"}</span>
                {event.trigger && <div className="mono muted" style={{ maxWidth: 240, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}
                  title={event.trigger}>{event.trigger}</div>}</td>
              <td className="mono">{event.method}</td>
              <td className="muted">{event.expected || "—"}</td>
            </tr>)}
            {!(viewing.events ?? []).length && <tr><td colSpan={5} className="muted">未定义事件</td></tr>}
          </tbody>
        </table>
        <h4 style={{ marginTop: 16 }}>故事正文</h4>
        <div className="markdown-body">
          <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeSanitize]}>{viewing.body}</ReactMarkdown>
        </div>
      </div>
    </div>}
    {runTarget && <div role="dialog" aria-modal="true" onClick={() => setRunTarget(null)}
      style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,.6)", zIndex: 1000,
        display: "flex", alignItems: "center", justifyContent: "center", padding: 24 }}>
      <div onClick={e => e.stopPropagation()}
        style={{ width: 880, maxWidth: "92vw", maxHeight: "90vh", overflow: "auto", background: "#fff", borderRadius: 8, padding: 16 }}>
        <div className="row-actions" style={{ justifyContent: "space-between" }}>
          <div>
            <div className="card-title">触发测试：{runTarget.title}</div>
            <div className="muted">{runTarget.environment} / {runTarget.platform} / 超时 {runTarget.timeout_seconds}s</div>
          </div>
          <button className="btn small" onClick={() => setRunTarget(null)}>关闭</button>
        </div>
        {(runTarget.events ?? []).length > 0 ? <>
          <div className="muted" style={{ marginTop: 12 }}>
            选择要执行的事件。只选部分事件时，本次会话会把所选事件的方法作为必测项；全选则包含故事的其它必测项。
          </div>
          <div className="row-actions" style={{ marginTop: 8 }}>
            <button className="btn small" onClick={() => setRunSelection((runTarget.events ?? []).map(event => event.id))}>全选</button>
            <button className="btn small" onClick={() => setRunSelection([])}>清空</button>
            <span className="muted">已选 {runSelection.length} / {(runTarget.events ?? []).length}</span>
          </div>
          <table className="table" style={{ marginTop: 8 }}>
            <thead><tr><th /><th>事件</th><th>触发</th><th>方法</th><th>预期</th></tr></thead>
            <tbody>
              {(runTarget.events ?? []).map(event => <tr key={event.id}>
                <td><input type="checkbox" checked={runSelection.includes(event.id)}
                  onChange={e => setRunSelection(current => e.target.checked
                    ? [...current, event.id]
                    : current.filter(id => id !== event.id))} /></td>
                <td><div>{event.title}</div><div className="mono muted">{event.id}</div></td>
                <td><span className="chip">{event.trigger_kind || "manual"}</span>
                  {event.trigger && <div className="mono muted" style={{ maxWidth: 180, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}
                    title={event.trigger}>{event.trigger}</div>}</td>
                <td className="mono">{event.method}</td>
                <td className="muted" style={{ maxWidth: 200 }}>{event.expected || "—"}</td>
              </tr>)}
            </tbody>
          </table>
        </> : <div className="notice" style={{ marginTop: 12 }}>
          该故事未定义事件，将按正文执行整篇故事，并由 agent 自行记录检查项。
        </div>}
        <div className="row-actions" style={{ justifyContent: "flex-end", marginTop: 16 }}>
          <button className="btn small" onClick={() => setRunTarget(null)}>取消</button>
          <button className="btn small primary" disabled={busy || ((runTarget.events ?? []).length > 0 && runSelection.length === 0)}
            onClick={() => void triggerRun(runTarget, runSelection)}>
            {(runTarget.events ?? []).length === 0 ? "触发整篇故事" : "触发选中事件（" + runSelection.length + "）"}
          </button>
        </div>
      </div>
    </div>}
    {evidencePreview && <div role="dialog" aria-modal="true" onClick={closeEvidencePreview}
      style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,.6)", zIndex: 1000,
        display: "flex", alignItems: "center", justifyContent: "center", padding: 24 }}>
      <div onClick={e => e.stopPropagation()}
        style={{ maxWidth: "92vw", maxHeight: "92vh", overflow: "auto", background: "#fff", borderRadius: 8, padding: 12 }}>
        <div className="row-actions" style={{ justifyContent: "space-between", marginBottom: 8 }}>
          <span className="mono muted">证据 {evidencePreview.label}</span>
          <button className="btn small" onClick={closeEvidencePreview}>关闭</button>
        </div>
        <img src={evidencePreview.url} alt="测试证据截图" style={{ maxWidth: "86vw", maxHeight: "78vh", display: "block" }} />
      </div>
    </div>}
  </>;
}
