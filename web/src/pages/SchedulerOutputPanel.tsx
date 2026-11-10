import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import ReactMarkdown from "react-markdown";
import rehypeSanitize from "rehype-sanitize";
import remarkGfm from "remark-gfm";
import { api, ApiError } from "../api";
import { consumeSchedulerOutputStream } from "../scheduler-output-stream";
import type { SchedulerOutput, SchedulerOutputFrame, SchedulerOutputRawPage } from "../types";

type ConnectionState = "connecting" | "live" | "polling" | "done" | "error";
type OutputFile = { path: string; kind?: string; additions?: number; deletions?: number; diff?: string };
type PlanStep = { text: string; status?: string };

function asRecord(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {};
}

function textValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function numberValue(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function outputFiles(item: SchedulerOutput): OutputFile[] {
  const metadata = asRecord(item.metadata);
  const files = metadata.files;
  if (!Array.isArray(files)) return [];
  return files.flatMap((value) => {
    const file = asRecord(value);
    const path = textValue(file.path);
    if (!path) return [];
    return [{
      path,
      kind: textValue(file.kind),
      additions: numberValue(file.additions),
      deletions: numberValue(file.deletions),
      diff: textValue(file.diff),
    }];
  });
}

function outputPlan(item: SchedulerOutput): PlanStep[] {
  const plan = asRecord(item.metadata).plan;
  if (!Array.isArray(plan)) return [];
  return plan.flatMap((value) => {
    const step = asRecord(value);
    const text = textValue(step.text);
    return text ? [{ text, status: textValue(step.status) }] : [];
  });
}

function itemHaystack(item: SchedulerOutput): string {
  return [item.title, item.content, item.output, JSON.stringify(item.metadata ?? {})]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
}

function itemIsError(item: SchedulerOutput): boolean {
  const metadata = asRecord(item.metadata);
  const level = textValue(metadata.level).toLowerCase();
  return item.status === "failed" || level === "error" || level === "warning" ||
    /\b(error|failed|failure|panic|exception)\b|错误|失败|异常/i.test(itemHaystack(item));
}

function sortOutputs(items: SchedulerOutput[]): SchedulerOutput[] {
  return items.sort((a, b) => {
    const byTime = a.created_at.localeCompare(b.created_at);
    return byTime !== 0 ? byTime : a.revision - b.revision;
  });
}

function mergeFrame(previous: SchedulerOutput[], frame: SchedulerOutputFrame): SchedulerOutput[] {
  const byId = new Map(previous.map((item) => [item.item_id, item]));
  for (const item of frame.items) byId.set(item.item_id, item);
  return sortOutputs(Array.from(byId.values()));
}

function useSchedulerOutput(workerId: string, terminal: boolean) {
  const [items, setItems] = useState<SchedulerOutput[]>([]);
  const [connection, setConnection] = useState<ConnectionState>("connecting");
  const [error, setError] = useState("");
  const cursorRef = useRef(0);

  useEffect(() => {
    let disposed = false;
    let timer = 0;
    let controller: AbortController | null = null;
    const merge = (frame: SchedulerOutputFrame) => {
      if (disposed) return;
      cursorRef.current = Math.max(cursorRef.current, frame.cursor);
      setItems((previous) => mergeFrame(previous, frame));
    };

    const loadInitial = async () => {
      setError("");
      setConnection("connecting");
      cursorRef.current = 0;
      const loaded: SchedulerOutput[] = [];
      for (let page = 0; page < 12; page += 1) {
        const frame = await api.schedulerOutput(workerId, cursorRef.current, 500);
        loaded.push(...frame.items);
        cursorRef.current = Math.max(cursorRef.current, frame.cursor);
        if (frame.items.length < 500) break;
      }
      if (!disposed) {
        setItems(sortOutputs(loaded));
        setConnection(terminal ? "done" : "live");
      }
    };

    const poll = async (): Promise<void> => {
      if (disposed || terminal) return;
      try {
        const frame = await api.schedulerOutput(workerId, cursorRef.current, 500);
        merge(frame);
        setConnection("polling");
        setError("");
      } catch (candidate) {
        if (!disposed) setError(candidate instanceof ApiError ? candidate.message : "轮询实时输出失败");
      }
      if (!disposed && !terminal) timer = window.setTimeout(() => void poll(), 2000);
    };

    const connect = async (attempt: number): Promise<void> => {
      if (disposed || terminal) {
        if (!disposed) setConnection("done");
        return;
      }
      controller = new AbortController();
      setConnection(attempt === 0 ? "live" : "connecting");
      try {
        await consumeSchedulerOutputStream({
          workerId,
          after: cursorRef.current,
          signal: controller.signal,
          onFrame: merge,
          onDone: () => setConnection("done"),
        });
        if (!disposed && !terminal) {
          setConnection("connecting");
          timer = window.setTimeout(() => void connect(attempt + 1), 800);
        }
      } catch (candidate) {
        if (disposed || controller.signal.aborted) return;
        const status = candidate instanceof Error && "status" in candidate
          ? Number((candidate as Error & { status?: number }).status ?? 0)
          : 0;
        if (status === 401 || status === 403) {
          setConnection("error");
          setError(candidate instanceof Error ? candidate.message : "实时输出鉴权失败");
          return;
        }
        if (attempt >= 2) {
          setConnection("polling");
          setError(candidate instanceof Error ? candidate.message : "实时连接中断，改用轮询");
          void poll();
          return;
        }
        setConnection("connecting");
        timer = window.setTimeout(() => void connect(attempt + 1), Math.min(8000, 700 * 2 ** attempt));
      }
    };

    void loadInitial().then(() => void connect(0)).catch((candidate) => {
      if (disposed) return;
      setConnection("error");
      setError(candidate instanceof ApiError ? candidate.message : "读取实时输出失败");
    });
    return () => {
      disposed = true;
      controller?.abort();
      window.clearTimeout(timer);
    };
  }, [terminal, workerId]);

  return { items, connection, error };
}

function when(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "" : date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function statusLabel(status: string): string {
  return ({
    completed: "完成",
    failed: "失败",
    running: "运行中",
    streaming: "输出中",
    cancelled: "已取消",
    denied: "已拒绝",
    pending: "等待",
  } as Record<string, string>)[status] ?? status;
}

function statusTone(status: string): string {
  if (status === "failed" || status === "denied") return "bad";
  if (status === "completed") return "ok";
  if (status === "running" || status === "streaming") return "live";
  return "";
}

function MarkdownContent({ content }: { content: string }) {
  return (
    <div className="codex-markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[rehypeSanitize]}
        components={{
          a: ({ children, href }) => (
            <a href={href} target="_blank" rel="noreferrer">{children}</a>
          ),
        }}
      >
        {content}
      </ReactMarkdown>
    </div>
  );
}

function clipTerminalOutput(output: string): { text: string; hidden: number } {
  const lines = output.split("\n");
  if (lines.length <= 320) return { text: output, hidden: 0 };
  return { text: lines.slice(-320).join("\n"), hidden: lines.length - 320 };
}

function OutputEntry({ item, onOpenRaw }: { item: SchedulerOutput; onOpenRaw: (item: SchedulerOutput) => void }) {
  const metadata = asRecord(item.metadata);
  const tone = statusTone(item.status);
  const stamp = when(item.updated_at || item.created_at);

  if (item.kind === "agent_message") {
    return (
      <article className="codex-output-card agent-message-card">
        <div className="codex-output-rail"><span className="codex-avatar">C</span></div>
        <div className="codex-output-body">
          <div className="codex-output-head"><strong>Codex</strong><span className={"codex-status " + tone}>{statusLabel(item.status)}</span><span className="tiny">{stamp}</span></div>
          {item.content ? <MarkdownContent content={item.content} /> : <div className="codex-waiting">正在生成回复…</div>}
        </div>
      </article>
    );
  }

  if (item.kind === "plan") {
    const plan = outputPlan(item);
    const explanation = textValue(asRecord(item.metadata).explanation);
    return (
      <article className="codex-output-card plan-output-card">
        <div className="codex-output-head"><strong>执行计划</strong><span className={"codex-status " + tone}>{statusLabel(item.status)}</span></div>
        {plan.length > 0 && (
          <ol className="codex-plan-list">
            {plan.map((step, index) => (
              <li data-status={step.status} key={step.text + index}>
                <span>{step.status === "completed" ? "✓" : step.status === "inProgress" ? "●" : "○"}</span>{step.text}
              </li>
            ))}
          </ol>
        )}
        {plan.length > 0
          ? explanation && <MarkdownContent content={explanation} />
          : item.content && <MarkdownContent content={item.content} />}
      </article>
    );
  }

  if (item.kind === "reasoning_summary") {
    return (
      <details className="codex-output-card reasoning-output-card">
        <summary><span>推理摘要</span><span className={"codex-status " + tone}>{statusLabel(item.status)}</span></summary>
        {item.content ? <MarkdownContent content={item.content} /> : <div className="codex-waiting">正在整理推理摘要…</div>}
      </details>
    );
  }

  if (item.kind === "command") {
    const clipped = clipTerminalOutput(item.output ?? "");
    const failed = item.status === "failed";
    const running = item.status === "running" || item.status === "streaming";
    const exitCode = numberValue(metadata.exitCode);
    const duration = numberValue(metadata.durationMs);
    const cwd = textValue(metadata.cwd);
    return (
      <details className="codex-output-card command-output-card" open={failed || running}>
        <summary>
          <span className="codex-command-prompt">$</span>
          <span className="codex-command-title" title={item.title}>{item.title || "执行命令"}</span>
          <span className={"codex-status " + tone}>{statusLabel(item.status)}</span>
          {exitCode !== undefined && <span className={"codex-exit " + (exitCode === 0 ? "ok" : "bad")}>exit {exitCode}</span>}
        </summary>
        <div className="codex-command-meta">
          {cwd && <span className="mono" title={cwd}>{cwd}</span>}
          {duration !== undefined && <span>{duration < 1000 ? duration + " ms" : (duration / 1000).toFixed(1) + " s"}</span>}
          {item.truncated && <span className="codex-truncated">已保留头尾摘要</span>}
        </div>
        <pre className="codex-terminal">{clipped.text || (running ? "等待命令输出…" : "（无输出）")}</pre>
        {clipped.hidden > 0 && <div className="codex-output-note">已折叠较早的 {clipped.hidden} 行</div>}
        {item.truncated && <button className="codex-link-button" onClick={() => onOpenRaw(item)} type="button">查看完整日志</button>}
      </details>
    );
  }

  if (item.kind === "file_change") {
    const files = outputFiles(item);
    const isTurnDiff = textValue(metadata.source) === "turn_diff";
    const additions = files.reduce((sum, file) => sum + (file.additions ?? 0), 0);
    const deletions = files.reduce((sum, file) => sum + (file.deletions ?? 0), 0);
    const clipped = clipTerminalOutput(item.output ?? "");
    const heading = isTurnDiff
      ? "本轮完整变更"
      : files.length === 1
        ? "已编辑 " + files[0].path
        : files.length > 1
          ? "已编辑 " + files.length + " 个文件"
          : item.title || "文件变更";
    return (
      <details className="codex-output-card file-output-card">
        <summary>
          <span className="file-output-icon">±</span>
          <span><strong title={files.length === 1 ? files[0].path : undefined}>{heading}</strong>{files.length === 0 && item.content && <small>{item.content}</small>}</span>
          {(additions > 0 || deletions > 0) && <span className="file-total"><b>+{additions}</b><i>-{deletions}</i></span>}
          <span className={"codex-status " + tone}>{statusLabel(item.status)}</span>
        </summary>
        {files.length > 0 && (
          <div className="codex-file-list">
            {files.map((file) => (
              <details key={file.path}>
                <summary><span className="mono" title={file.path}>{file.path}</span><span className="ok">+{file.additions ?? 0}</span><span className="bad">-{file.deletions ?? 0}</span></summary>
                {file.diff && <pre className="codex-diff">{file.diff}</pre>}
              </details>
            ))}
          </div>
        )}
        {files.length === 0 && item.output && <pre className="codex-diff">{clipped.text}</pre>}
        {item.truncated && <button className="codex-link-button" onClick={() => onOpenRaw(item)} type="button">查看完整变更</button>}
      </details>
    );
  }

  if (item.kind === "system") {
    const level = textValue(metadata.level) || "info";
    return (
      <div className={"codex-system-notice " + level}>
        <span className="codex-system-icon">{level === "error" ? "!" : level === "warning" ? "△" : "i"}</span>
        <div><strong>{item.title || "引擎提示"}</strong><div>{item.content}</div></div>
      </div>
    );
  }

  if (item.kind === "token_usage") {
    return <div className="codex-token-usage"><span>Token</span>{item.content}</div>;
  }

  return (
    <details className="codex-output-card tool-output-card">
      <summary><strong>{item.title || "工具调用"}</strong><span className={"codex-status " + tone}>{statusLabel(item.status)}</span></summary>
      {item.content && <pre className="codex-terminal">{item.content}</pre>}
      {item.output && <pre className="codex-terminal">{clipTerminalOutput(item.output).text}</pre>}
    </details>
  );
}

function RawOutputModal({ workerId, item, onClose }: { workerId: string; item: SchedulerOutput; onClose: () => void }) {
  const [page, setPage] = useState<SchedulerOutputRawPage | null>(null);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async (offset: number) => {
    setBusy(true);
    setError("");
    try {
      const next = await api.schedulerOutputRaw(workerId, item.item_id, offset);
      setPage(next);
      setText((current) => offset === 0 ? next.data : current + next.data);
    } catch (candidate) {
      setError(candidate instanceof ApiError ? candidate.message : "读取完整日志失败");
    } finally {
      setBusy(false);
    }
  }, [item.item_id, workerId]);

  useEffect(() => { void load(0); }, [load]);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="codex-raw-backdrop" onMouseDown={onClose} role="presentation">
      <section className="codex-raw-modal" onMouseDown={(event) => event.stopPropagation()} role="dialog" aria-modal="true" aria-label="完整输出">
        <header>
          <div><strong>{item.title || "完整输出"}</strong><div className="tiny">完整日志 · {page?.total_bytes.toLocaleString() ?? "…"} 字节</div></div>
          <div><button className="btn small" onClick={() => void load(0)}>重新读取</button><button className="btn small" onClick={onClose}>关闭</button></div>
        </header>
        {error && <div className="codex-import-error">{error}</div>}
        {page?.missing ? <div className="empty">这条日志已归档或清理。</div> : <pre className="codex-raw-log">{text || (busy ? "正在读取…" : "（空）")}</pre>}
        <footer>
          <span className="tiny">offset {page?.offset ?? 0} · {text.length.toLocaleString()} 字符</span>
          {page?.has_more && <button className="btn small primary" disabled={busy} onClick={() => void load(page.next_offset)}>继续加载</button>}
        </footer>
      </section>
    </div>
  );
}

export function SchedulerOutputPanel({ workerId, terminal }: { workerId: string; terminal: boolean }) {
  const { items, connection, error } = useSchedulerOutput(workerId, terminal);
  const [query, setQuery] = useState("");
  const [onlyErrors, setOnlyErrors] = useState(false);
  const [rawItem, setRawItem] = useState<SchedulerOutput | null>(null);
  const [newCount, setNewCount] = useState(0);
  const scrollRef = useRef<HTMLDivElement>(null);
  const followingRef = useRef(true);

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return items.filter((item) => (!onlyErrors || itemIsError(item)) && (!needle || itemHaystack(item).includes(needle)));
  }, [items, onlyErrors, query]);

  useEffect(() => {
    const element = scrollRef.current;
    if (!element) return;
    if (followingRef.current) {
      requestAnimationFrame(() => { element.scrollTop = element.scrollHeight; });
      setNewCount(0);
    } else {
      setNewCount((count) => count + 1);
    }
  }, [items]);

  const onScroll = () => {
    const element = scrollRef.current;
    if (!element) return;
    const nearBottom = element.scrollHeight - element.scrollTop - element.clientHeight < 90;
    followingRef.current = nearBottom;
    if (nearBottom) setNewCount(0);
  };

  const jumpLatest = () => {
    const element = scrollRef.current;
    if (!element) return;
    followingRef.current = true;
    setNewCount(0);
    element.scrollTo({ top: element.scrollHeight, behavior: "smooth" });
  };

  const connectionLabel = connection === "live" ? "实时连接" : connection === "polling" ? "轮询恢复" : connection === "done" ? "已结束" : connection === "error" ? "连接失败" : "连接中";
  return (
    <div className="scheduler-output-shell">
      <div className="scheduler-output-toolbar">
        <span className={"scheduler-live " + connection}><span className="dot" />{connectionLabel}</span>
        <input className="scheduler-output-search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索输出、命令或文件…" />
        <button className={"btn small" + (onlyErrors ? " primary" : "")} onClick={() => setOnlyErrors((value) => !value)}>只看异常</button>
        <span className="tiny">{filtered.length}/{items.length} 条</span>
      </div>
      {error && <div className="scheduler-stream-error">{error}</div>}
      <div className="scheduler-output-scroll" onScroll={onScroll} ref={scrollRef}>
        {filtered.length === 0 ? (
          <div className="scheduler-output-empty">
            <div className="scheduler-output-empty-mark">›_ </div>
            <strong>{items.length === 0 ? (terminal ? "这次执行没有留下输出" : "等待 Codex 输出…") : "没有匹配的输出"}</strong>
            <span>{items.length === 0 ? "消息、命令、文件变更和异常会按产生顺序出现在这里。" : "换个关键词或关闭“只看异常”。"}</span>
          </div>
        ) : filtered.map((item) => <OutputEntry item={item} key={item.item_id} onOpenRaw={setRawItem} />)}
        {!terminal && connection !== "done" && <div className="scheduler-output-cursor"><span />Codex 正在工作</div>}
      </div>
      {newCount > 0 && <button className="scheduler-jump-latest" onClick={jumpLatest} type="button">有 {newCount} 条新输出，回到最新 ↓</button>}
      {rawItem && <RawOutputModal item={rawItem} onClose={() => setRawItem(null)} workerId={workerId} />}
    </div>
  );
}
