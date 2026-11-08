import { useEffect, useRef, useState } from "react";
import { api, ApiError } from "../api";
import type { ModelConfigImportPreview, ModelConfigImportResult } from "../types";
import type { Notify } from "../App";

type ImportMode = "file" | "directory" | "text";
type DirectoryCandidate = { path: string; file: File };

const MAX_CONFIG_BYTES = 1024 * 1024;

const EXAMPLE_CONFIG = `model = "gpt-5.4"
model_provider = "team-gateway"
model_reasoning_effort = "high"

[model_providers.team-gateway]
name = "Team Gateway"
base_url = "https://gateway.example.com/v1"
wire_api = "responses"
experimental_bearer_token = "sk-..."`;

const MODE_COPY: Record<ImportMode, { title: string; hint: string }> = {
  file: {
    title: "选择 config.toml",
    hint: "从本机选择 ~/.codex/config.toml。浏览器只读取你明确选择的文件。",
  },
  directory: {
    title: "选择 Codex 目录",
    hint: "选择 ~/.codex 或任意包含 config.toml 的目录，系统会列出找到的配置文件。",
  },
  text: {
    title: "粘贴或手动输入",
    hint: "可直接粘贴 Codex TOML；也支持少量字段，例如只录入 model_providers 与默认模型。",
  },
};

function keyStatus(provider: ModelConfigImportPreview["providers"][number]) {
  if (provider.has_api_key) return { text: "将加密保存", tone: "ok" };
  if (provider.has_existing_credential) return { text: "沿用已保存 Key", tone: "ok" };
  if (provider.env_key) return { text: "需补 Key · " + provider.env_key, tone: "warn" };
  return { text: "未提供 Key", tone: "warn" };
}

export function ModelConfigImportModal({
  open,
  notify,
  onClose,
  onImported,
}: {
  open: boolean;
  notify: Notify;
  onClose: () => void;
  onImported: (providerID: string) => void;
}) {
  const [mode, setMode] = useState<ImportMode>("file");
  const [source, setSource] = useState("");
  const [content, setContent] = useState("");
  const [preview, setPreview] = useState<ModelConfigImportPreview | null>(null);
  const [result, setResult] = useState<ModelConfigImportResult | null>(null);
  const [directoryFiles, setDirectoryFiles] = useState<DirectoryCandidate[]>([]);
  const [busy, setBusy] = useState<"" | "preview" | "import">("");
  const fileInput = useRef<HTMLInputElement | null>(null);
  const directoryInput = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    directoryInput.current?.setAttribute("webkitdirectory", "");
  }, []);

  useEffect(() => {
    if (open) return;
    setMode("file");
    setSource("");
    setContent("");
    setPreview(null);
    setResult(null);
    setDirectoryFiles([]);
    setBusy("");
  }, [open]);

  const close = () => {
    if (busy) return;
    onClose();
  };

  const changeMode = (next: ImportMode) => {
    setMode(next);
    setPreview(null);
    setResult(null);
  };

  const readFile = async (file: File, nextSource: string) => {
    if (file.size > MAX_CONFIG_BYTES) {
      notify("config.toml 超过 1 MB，无法导入", "error");
      return;
    }
    try {
      const text = await file.text();
      if (text.trim() === "") {
        notify("选中的 config.toml 是空文件", "error");
        return;
      }
      setContent(text);
      setSource(nextSource);
      setPreview(null);
      setResult(null);
    } catch {
      notify("读取本机配置文件失败", "error");
    }
  };

  const chooseFile = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const input = event.currentTarget;
    const file = input.files?.[0];
    input.value = "";
    if (file) await readFile(file, file.name);
  };

  const chooseDirectory = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const input = event.currentTarget;
    const candidates = Array.from(input.files ?? [])
      .filter((file) => file.name.toLowerCase() === "config.toml")
      .map((file) => ({ path: file.webkitRelativePath || file.name, file }))
      .sort((a, b) => a.path.length - b.path.length || a.path.localeCompare(b.path));
    input.value = "";
    setDirectoryFiles(candidates);
    setPreview(null);
    setResult(null);
    if (candidates.length === 0) {
      setContent("");
      setSource("");
      notify("所选目录中没有找到 config.toml", "error");
      return;
    }
    await readFile(candidates[0].file, candidates[0].path);
  };

  const selectDirectoryFile = async (path: string) => {
    const candidate = directoryFiles.find((item) => item.path === path);
    if (candidate) await readFile(candidate.file, candidate.path);
  };

  const parsePreview = async () => {
    if (content.trim() === "") {
      notify("请先选择或输入 config.toml 内容", "error");
      return;
    }
    setBusy("preview");
    try {
      const next = await api.previewModelConfigImport(source || "手动输入", content);
      setPreview(next);
      setResult(null);
      notify("解析完成，请确认供应商与默认模型");
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "解析配置失败", "error");
    } finally {
      setBusy("");
    }
  };

  const executeImport = async () => {
    if (!preview) return;
    setBusy("import");
    try {
      const imported = await api.importModelConfig(source || "手动输入", content);
      setResult(imported);
      const firstProvider = imported.providers[0]?.id ?? "";
      if (firstProvider) onImported(firstProvider);
      notify(
        `导入完成：新增 ${imported.imported} 个，更新 ${imported.updated} 个供应商` +
          (imported.warnings.length ? `，有 ${imported.warnings.length} 条提示` : ""),
      );
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "导入配置失败", "error");
    } finally {
      setBusy("");
    }
  };

  const copy = MODE_COPY[mode];

  return (
    <div className={"modal-backdrop" + (open ? " open" : "")}>
      <div className="modal" style={{ width: "min(920px, 100%)" }}>
        <div className="modal-head">
          <div>
            <div className="modal-title">导入 Codex 模型配置</div>
            <div className="modal-sub">先解析预览，再写入供应商、模型目录与默认模型；密钥只进入加密凭据库。</div>
          </div>
          <button className="icon-btn" onClick={close} disabled={!!busy}>✕</button>
        </div>

        <div className="modal-body">
          {result ? (
            <>
              <div className="notice">
                <span>✓</span>
                <div>
                  <strong>配置已导入。</strong>
                  新增 {result.imported} 个供应商，更新 {result.updated} 个；默认模型
                  {result.default.model ? `已切换为 ${result.default.model}` : "未切换"}。
                </div>
              </div>
              {result.warnings.length > 0 && (
                <div style={{ border: "1px solid #f2d18a", background: "#fff9eb", borderRadius: 10, padding: 14, marginTop: 14 }}>
                  <div style={{ fontSize: 13, fontWeight: 650, marginBottom: 8 }}>需要处理</div>
                  {result.warnings.map((warning, index) => (
                    <div key={index} className="tiny" style={{ color: "#8a5b00", marginTop: index ? 6 : 0 }}>• {warning}</div>
                  ))}
                </div>
              )}
            </>
          ) : (
            <>
              <div style={{ display: "flex", gap: 8, marginBottom: 14 }}>
                {(["file", "directory", "text"] as ImportMode[]).map((item) => (
                  <button
                    key={item}
                    className={"btn " + (mode === item ? "primary" : "")}
                    onClick={() => changeMode(item)}
                    disabled={!!busy}
                  >
                    {item === "file" ? "本机文件" : item === "directory" ? "指定目录" : "手动输入"}
                  </button>
                ))}
              </div>

              <div style={{ border: "1px solid var(--line)", borderRadius: 12, padding: 16, background: "#fbfcfe" }}>
                <div style={{ fontSize: 13, fontWeight: 650 }}>{copy.title}</div>
                <div className="tiny" style={{ marginTop: 4, lineHeight: 1.7 }}>{copy.hint}</div>

                {mode === "file" && (
                  <div style={{ marginTop: 12 }}>
                    <input ref={fileInput} type="file" accept=".toml,text/plain,application/toml" style={{ display: "none" }} onChange={(event) => void chooseFile(event)} />
                    <button className="btn primary" disabled={!!busy} onClick={() => fileInput.current?.click()}>选择 config.toml</button>
                  </div>
                )}

                {mode === "directory" && (
                  <div style={{ marginTop: 12, display: "grid", gap: 10 }}>
                    <input ref={directoryInput} type="file" multiple style={{ display: "none" }} onChange={(event) => void chooseDirectory(event)} />
                    <div>
                      <button className="btn primary" disabled={!!busy} onClick={() => directoryInput.current?.click()}>选择目录</button>
                    </div>
                    {directoryFiles.length > 1 && (
                      <div className="field">
                        <label>找到 {directoryFiles.length} 个 config.toml</label>
                        <select value={source} onChange={(event) => void selectDirectoryFile(event.target.value)} disabled={!!busy}>
                          {directoryFiles.map((item) => <option key={item.path} value={item.path}>{item.path}</option>)}
                        </select>
                      </div>
                    )}
                  </div>
                )}

                {mode === "text" && (
                  <textarea
                    value={content}
                    onChange={(event) => { setContent(event.target.value); setPreview(null); setResult(null); setSource("手动输入"); }}
                    placeholder={EXAMPLE_CONFIG}
                    spellCheck={false}
                    style={{ width: "100%", minHeight: 220, marginTop: 12, padding: 12, borderRadius: 10, border: "1px solid var(--line)", fontFamily: "ui-monospace, SFMono-Regular, Consolas, monospace", fontSize: 12, lineHeight: 1.55, resize: "vertical" }}
                  />
                )}

                {mode !== "text" && content && (
                  <div className="tiny" style={{ marginTop: 12, padding: "9px 11px", borderRadius: 8, background: "#edf4ff", color: "#24508f" }}>
                    已读取 {source} · {content.length.toLocaleString()} 个字符 · 密钥不会回显
                  </div>
                )}
              </div>

              {preview && (
                <div style={{ marginTop: 16 }}>
                  <div style={{ display: "flex", alignItems: "center", gap: 12, marginBottom: 10 }}>
                    <div style={{ flex: 1 }}>
                      <div style={{ fontSize: 14, fontWeight: 650 }}>导入预览</div>
                      <div className="tiny">新增 {preview.create_count} 个 · 更新 {preview.update_count} 个 · 默认模型 {preview.default_model || "不切换"}{preview.reasoning_effort ? " · " + preview.reasoning_effort : ""}</div>
                    </div>
                    <span className="chip ok"><span className="dot" />解析通过</span>
                  </div>

                  <table className="table">
                    <thead><tr><th>供应商</th><th>Base URL</th><th>协议</th><th>密钥</th><th>模型</th><th>动作</th></tr></thead>
                    <tbody>
                      {preview.providers.map((provider) => {
                        const status = keyStatus(provider);
                        return (
                          <tr key={provider.key}>
                            <td><div className="strong">{provider.name}</div><div className="tiny mono">{provider.key}</div></td>
                            <td className="tiny mono" style={{ maxWidth: 250, wordBreak: "break-all" }}>{provider.base_url}</td>
                            <td><span className="chip">{provider.wire_api}</span></td>
                            <td><span className={"chip " + status.tone}>{status.text}</span></td>
                            <td className="tiny">{provider.models.length ? provider.models.join(", ") : "—"}</td>
                            <td><span className={"chip " + (provider.action === "update" ? "warn" : "ok")}>{provider.action === "update" ? "更新" : "新增"}</span></td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>

                  {preview.warnings.length > 0 && (
                    <div style={{ border: "1px solid #f2d18a", background: "#fff9eb", borderRadius: 10, padding: 12, marginTop: 12 }}>
                      {preview.warnings.map((warning, index) => (
                        <div key={index} className="tiny" style={{ color: "#8a5b00", marginTop: index ? 6 : 0 }}>• {warning}</div>
                      ))}
                    </div>
                  )}
                </div>
              )}
            </>
          )}
        </div>

        <div className="modal-foot">
          <div className="tiny">{result ? "可以关闭窗口继续编辑供应商，或补充缺少的 API Key。" : "导入会更新同名供应商；已有模型目录不会删除。"}</div>
          <div>
            {result ? (
              <button className="btn primary" onClick={onClose}>完成</button>
            ) : (
              <>
                <button className="btn" onClick={close} disabled={!!busy}>取消</button>{" "}
                <button className="btn" disabled={!!busy || !content.trim()} onClick={() => void parsePreview()}>{busy === "preview" ? "解析中…" : "解析预览"}</button>{" "}
                <button className="btn primary" disabled={!!busy || !preview} onClick={() => void executeImport()}>{busy === "import" ? "导入中…" : "确认导入"}</button>
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
