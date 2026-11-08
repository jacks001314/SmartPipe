import { useEffect, useRef, useState } from "react";
import { api, ApiError } from "../api";
import type { ModelConfigImportPreview, ModelConfigImportResult } from "../types";
import type { Notify } from "../App";

type ImportMode = "file" | "directory" | "text";
type DirectoryCandidate = { path: string; file: File };

const MAX_CONFIG_BYTES = 1024 * 1024;

const EXAMPLE_CONFIG = `profile = "team-gateway"
model = "gpt-5.4"
model_provider = "team-gateway"
model_reasoning_effort = "high"

[profiles.team-gateway]
model = "gpt-5.4"
model_provider = "team-gateway"
model_reasoning_effort = "high"

[model_providers.team-gateway]
name = "Team Gateway"
base_url = "https://gateway.example.com/v1"
wire_api = "responses"
experimental_bearer_token = "sk-..."`;

const MODE_COPY: Record<ImportMode, string> = {
  file: "从本机选择 ~/.codex/config.toml。浏览器只读取你明确选择的文件。",
  directory: "选择 ~/.codex 或包含 config.toml 的目录，系统会列出找到的配置文件。",
  text: "直接粘贴 Codex TOML，支持 Profile、Provider 和顶层默认模型。",
};

const keyLabel = (source: string) => ({
  toml: "TOML",
  saved: "已保存",
  env: "ENV",
  missing: "未提供",
}[source] ?? source);

const keyTone = (source: string) => source === "toml" || source === "saved" ? "ok" : "warn";

export function ModelConfigImportModal({
  open,
  notify,
  onClose,
  onImported,
}: {
  open: boolean;
  notify: Notify;
  onClose: () => void;
  onImported: () => void;
}) {
  const [mode, setMode] = useState<ImportMode>("file");
  const [source, setSource] = useState("");
  const [content, setContent] = useState("");
  const [preview, setPreview] = useState<ModelConfigImportPreview | null>(null);
  const [selectedProfiles, setSelectedProfiles] = useState<Set<string>>(new Set());
  const [selectedProviders, setSelectedProviders] = useState<Set<string>>(new Set());
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
    setSelectedProfiles(new Set());
    setSelectedProviders(new Set());
    setResult(null);
    setDirectoryFiles([]);
    setBusy("");
  }, [open]);

  const close = () => {
    if (!busy) onClose();
  };

  const clearPreview = () => {
    setPreview(null);
    setSelectedProfiles(new Set());
    setSelectedProviders(new Set());
    setResult(null);
  };

  const changeMode = (next: ImportMode) => {
    setMode(next);
    clearPreview();
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
      clearPreview();
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
    clearPreview();
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
      setSelectedProfiles(new Set());
      setSelectedProviders(new Set());
      notify(`解析完成：${next.profile_count} 个 Profile，${next.provider_count} 个 Provider`);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "解析配置失败", "error");
    } finally {
      setBusy("");
    }
  };

  const toggleProfile = (key: string, checked: boolean) => {
    if (!preview) return;
    const nextProfiles = new Set(selectedProfiles);
    const nextProviders = new Set(selectedProviders);
    checked ? nextProfiles.add(key) : nextProfiles.delete(key);
    const profile = preview.profiles.find((item) => item.key === key);
    if (checked && profile && !profile.provider_missing) nextProviders.add(profile.provider_key);
    setSelectedProfiles(nextProfiles);
    setSelectedProviders(nextProviders);
  };

  const toggleProvider = (key: string, checked: boolean) => {
    if (!preview) return;
    const nextProviders = new Set(selectedProviders);
    const nextProfiles = new Set(selectedProfiles);
    checked ? nextProviders.add(key) : nextProviders.delete(key);
    if (!checked) {
      for (const profile of preview.profiles) {
        if (profile.provider_key === key) nextProfiles.delete(profile.key);
      }
    }
    setSelectedProviders(nextProviders);
    setSelectedProfiles(nextProfiles);
  };

  const toggleAllProfiles = (checked: boolean) => {
    if (!preview) return;
    if (!checked) {
      setSelectedProfiles(new Set());
      return;
    }
    const nextProfiles = new Set(selectedProfiles);
    const nextProviders = new Set(selectedProviders);
    for (const profile of preview.profiles) {
      if (profile.provider_missing) continue;
      nextProfiles.add(profile.key);
      nextProviders.add(profile.provider_key);
    }
    setSelectedProfiles(nextProfiles);
    setSelectedProviders(nextProviders);
  };

  const toggleAllProviders = (checked: boolean) => {
    if (!preview) return;
    if (!checked) {
      setSelectedProviders(new Set());
      setSelectedProfiles(new Set());
      return;
    }
    setSelectedProviders(new Set(preview.providers.map((provider) => provider.key)));
  };

  const executeImport = async () => {
    if (!preview || (selectedProfiles.size === 0 && selectedProviders.size === 0)) return;
    setBusy("import");
    try {
      const imported = await api.importModelConfig(
        source || "手动输入",
        content,
        Array.from(selectedProfiles),
        Array.from(selectedProviders),
      );
      setResult(imported);
      onImported();
      notify(
        `导入完成：${imported.imported_profiles} 个新 Profile，${imported.imported_providers} 个新 Provider` +
          (imported.warnings.length ? `，有 ${imported.warnings.length} 条提示` : ""),
      );
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "导入配置失败", "error");
    } finally {
      setBusy("");
    }
  };

  const allProfilesChecked = !!preview && preview.profiles.filter((p) => !p.provider_missing).length > 0 &&
    preview.profiles.filter((p) => !p.provider_missing).every((p) => selectedProfiles.has(p.key));
  const allProvidersChecked = !!preview && preview.providers.length > 0 &&
    preview.providers.every((p) => selectedProviders.has(p.key));

  return (
    <div className={"modal-backdrop" + (open ? " open" : "")}>
      <div className="modal" style={{ width: "min(980px, 100%)", maxHeight: "calc(100vh - 36px)" }}>
        <div className="modal-head">
          <div>
            <div className="modal-title">导入本机模型配置</div>
            <div className="modal-sub">勾选一项或多项写入 Master；Profile 会自动携带它依赖的 Provider，导入的模型同时启用。</div>
          </div>
          <button className="icon-btn" onClick={close} disabled={!!busy}>✕</button>
        </div>

        <div className="modal-body" style={{ overflow: "auto", maxHeight: "calc(100vh - 205px)" }}>
          <div style={{ marginBottom: 12 }}>
            <div className="tiny" style={{ fontWeight: 650, marginBottom: 6 }}>配置来源</div>
            <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
              {(["file", "directory", "text"] as ImportMode[]).map((item) => (
                <button key={item} className={"btn small " + (mode === item ? "primary" : "")} onClick={() => changeMode(item)} disabled={!!busy}>
                  {item === "file" ? "本机文件" : item === "directory" ? "指定目录" : "手动输入"}
                </button>
              ))}
              <span className="tiny mono" style={{ marginLeft: 4 }}>{source || "尚未选择配置"}</span>
            </div>
            <div className="tiny" style={{ marginTop: 5 }}>{MODE_COPY[mode]}</div>
            <div style={{ marginTop: 9 }}>
              <input ref={fileInput} type="file" accept=".toml,text/plain,application/toml" style={{ display: "none" }} onChange={(event) => void chooseFile(event)} />
              <input ref={directoryInput} type="file" multiple style={{ display: "none" }} onChange={(event) => void chooseDirectory(event)} />
              {mode === "file" && <button className="btn" disabled={!!busy} onClick={() => fileInput.current?.click()}>选择 config.toml</button>}
              {mode === "directory" && <button className="btn" disabled={!!busy} onClick={() => directoryInput.current?.click()}>选择目录</button>}
              {mode === "directory" && directoryFiles.length > 1 && (
                <select value={source} onChange={(event) => void selectDirectoryFile(event.target.value)} disabled={!!busy} style={{ marginLeft: 8, minWidth: 300 }}>
                  {directoryFiles.map((item) => <option key={item.path} value={item.path}>{item.path}</option>)}
                </select>
              )}
            </div>
          </div>

          {mode === "text" && (
            <textarea
              value={content}
              onChange={(event) => { setContent(event.target.value); setSource("手动输入"); clearPreview(); }}
              placeholder={EXAMPLE_CONFIG}
              spellCheck={false}
              style={{ width: "100%", minHeight: 210, padding: 12, borderRadius: 10, border: "1px solid var(--line)", fontFamily: "ui-monospace, SFMono-Regular, Consolas, monospace", fontSize: 12, lineHeight: 1.55, resize: "vertical" }}
            />
          )}

          {mode !== "text" && content && (
            <div className="tiny" style={{ marginBottom: 12, padding: "9px 11px", borderRadius: 8, background: "#edf4ff", color: "#24508f" }}>
              已读取 {source} · {content.length.toLocaleString()} 个字符 · API Key 不回显
            </div>
          )}

          {result ? (
            <>
              <div className="notice">
                <span>✓</span>
                <div>
                  <strong>已写入模型配置。</strong>
                  新增 {result.imported_profiles} 个 Profile、{result.imported_providers} 个 Provider；更新
                  {result.updated_profiles} 个 Profile、{result.updated_providers} 个 Provider。
                  {result.default.model ? ` 默认模型已切换为 ${result.default.model}。` : ""}
                </div>
              </div>
              {result.warnings.length > 0 && (
                <div style={{ border: "1px solid #f2d18a", background: "#fff9eb", borderRadius: 10, padding: 12, marginTop: 12 }}>
                  {result.warnings.map((warning, index) => (
                    <div key={index} className="tiny" style={{ color: "#8a5b00", marginTop: index ? 6 : 0 }}>• {warning}</div>
                  ))}
                </div>
              )}
            </>
          ) : preview ? (
            <div style={{ display: "grid", gap: 14 }}>
              <div style={{ border: "1px solid var(--line)", borderRadius: 10, overflow: "hidden" }}>
                <table className="table">
                  <thead>
                    <tr>
                      <th>Profile</th><th>Model</th><th>Provider</th><th>推理强度</th><th>API Key</th>
                      <th style={{ width: 60, textAlign: "center" }}><input type="checkbox" checked={allProfilesChecked} onChange={(event) => toggleAllProfiles(event.target.checked)} /> 选择</th>
                    </tr>
                  </thead>
                  <tbody>
                    {preview.profiles.length === 0 ? (
                      <tr><td colSpan={6} className="tiny" style={{ textAlign: "center", padding: 18 }}>配置中没有 Profile</td></tr>
                    ) : preview.profiles.map((profile) => (
                      <tr key={profile.key}>
                        <td><div className="strong">{profile.key}</div><div className="tiny">{profile.action === "update" ? "更新已有" : "新建"}</div></td>
                        <td className="mono tiny">{profile.model}</td>
                        <td><div>{profile.provider_name || profile.provider_key}</div>{profile.provider_missing && <div className="tiny" style={{ color: "var(--danger)" }}>Provider 缺失</div>}</td>
                        <td className="tiny">{profile.reasoning_effort || "默认"}</td>
                        <td><span className={"chip " + keyTone(profile.api_key_source)}>{keyLabel(profile.api_key_source)}</span></td>
                        <td style={{ textAlign: "center" }}><input type="checkbox" disabled={profile.provider_missing} checked={selectedProfiles.has(profile.key)} onChange={(event) => toggleProfile(profile.key, event.target.checked)} /></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              <div style={{ border: "1px solid var(--line)", borderRadius: 10, overflow: "hidden" }}>
                <table className="table">
                  <thead>
                    <tr>
                      <th>Provider</th><th>Base URL</th><th>格式</th><th>API Key</th>
                      <th style={{ width: 60, textAlign: "center" }}><input type="checkbox" checked={allProvidersChecked} onChange={(event) => toggleAllProviders(event.target.checked)} /> 选择</th>
                    </tr>
                  </thead>
                  <tbody>
                    {preview.providers.map((provider) => (
                      <tr key={provider.key}>
                        <td><div className="strong">{provider.name}</div><div className="tiny mono">{provider.key} · {provider.action === "update" ? "更新" : "新建"}</div></td>
                        <td className="mono tiny" style={{ maxWidth: 330, wordBreak: "break-all" }}>{provider.base_url}</td>
                        <td><span className="chip">{provider.wire_api}</span></td>
                        <td><span className={"chip " + keyTone(provider.api_key_source)}>{keyLabel(provider.api_key_source)}</span>{provider.env_key && <div className="tiny mono" style={{ marginTop: 3 }}>{provider.env_key}</div>}</td>
                        <td style={{ textAlign: "center" }}><input type="checkbox" checked={selectedProviders.has(provider.key)} onChange={(event) => toggleProvider(provider.key, event.target.checked)} /></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              {preview.warnings.length > 0 && (
                <div style={{ border: "1px solid #f2d18a", background: "#fff9eb", borderRadius: 10, padding: 12 }}>
                  {preview.warnings.map((warning, index) => (
                    <div key={index} className="tiny" style={{ color: "#8a5b00", marginTop: index ? 6 : 0 }}>• {warning}</div>
                  ))}
                </div>
              )}
            </div>
          ) : (
            <div className="empty" style={{ padding: "24px 16px" }}>
              <h3 style={{ fontSize: 13 }}>读取配置后先预览，再确定要导入的 Profile 和 Provider。</h3>
              <div className="tiny">选择 Profile 时会自动勾选它依赖的 Provider；导入的模型默认启用。</div>
            </div>
          )}
        </div>

        <div className="modal-foot">
          <div className="tiny">
            {result ? "可以关闭窗口，在模型配置页继续调整 Profile 或 Provider。" : "同名 Profile/Provider 会更新，已有模型目录不会删除。"}
          </div>
          <div>
            {result ? (
              <button className="btn primary" onClick={onClose}>完成</button>
            ) : (
              <>
                <button className="btn" onClick={close} disabled={!!busy}>取消</button>{" "}
                <button className="btn" disabled={!!busy || !content.trim()} onClick={() => void parsePreview()}>{busy === "preview" ? "读取中…" : "读取配置"}</button>{" "}
                <button className="btn primary" disabled={!!busy || !preview || (selectedProfiles.size === 0 && selectedProviders.size === 0)} onClick={() => void executeImport()}>
                  {busy === "import" ? "导入中…" : `导入 ${selectedProfiles.size} 个 Profile / ${selectedProviders.size} 个 Provider`}
                </button>
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
