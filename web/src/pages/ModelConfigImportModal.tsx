import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError } from "../api";
import type { ModelConfigImportPreview, ModelConfigImportResult, ModelLocalConfigResult } from "../types";
import type { Notify } from "../App";

export type PickedConfig = { path: string; file: File };

type Props = {
  open: boolean;
  initialFile: PickedConfig | null;
  directoryFiles: PickedConfig[];
  local: ModelLocalConfigResult | null;
  localLoading: boolean;
  notify: Notify;
  onClose: () => void;
  onImported: () => void;
  onPickFile: () => void;
  onPickDirectory: () => void;
  onSelectDirectoryFile: (path: string) => void;
  onReloadLocal: () => void;
};

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

const keyLabel = (source: string) =>
  ({
    toml: "TOML",
    saved: "已保存",
    env: "ENV",
    missing: "未提供",
  })[source] ?? source;

export function ModelConfigImportModal({
  open,
  initialFile,
  directoryFiles,
  local,
  localLoading,
  notify,
  onClose,
  onImported,
  onPickFile,
  onPickDirectory,
  onSelectDirectoryFile,
  onReloadLocal,
}: Props) {
  const [content, setContent] = useState("");
  const [source, setSource] = useState("");
  const [preview, setPreview] = useState<ModelConfigImportPreview | null>(null);
  const [selectedProfiles, setSelectedProfiles] = useState<Set<string>>(new Set());
  const [selectedProviders, setSelectedProviders] = useState<Set<string>>(new Set());
  const [result, setResult] = useState<ModelConfigImportResult | null>(null);
  const [busy, setBusy] = useState<"" | "preview" | "import">("");
  const [pasteOpen, setPasteOpen] = useState(false);
  const [parseError, setParseError] = useState("");
  const [origin, setOrigin] = useState<"local" | "content">("local");
  const lastRead = useRef("");

  const reset = useCallback(() => {
    setContent("");
    setSource("");
    setPreview(null);
    setSelectedProfiles(new Set());
    setSelectedProviders(new Set());
    setResult(null);
    setBusy("");
    setPasteOpen(false);
    setParseError("");
    setOrigin("local");
    lastRead.current = "";
  }, []);

  useEffect(() => {
    if (!open) reset();
  }, [open, reset]);

  const runPreview = useCallback(
    async (nextSource: string, nextContent: string) => {
      setOrigin("content");
      setBusy("preview");
      setParseError("");
      try {
        const next = await api.previewModelConfigImport(nextSource, nextContent);
        setPreview(next);
        setSelectedProfiles(new Set());
        setSelectedProviders(new Set());
        setResult(null);
      } catch (err) {
        setPreview(null);
        setParseError(err instanceof ApiError ? err.message : "解析配置失败");
      } finally {
        setBusy("");
      }
    },
    [],
  );

  useEffect(() => {
    if (!open || !initialFile) return;
    const marker = initialFile.path + ":" + initialFile.file.size + ":" + initialFile.file.lastModified;
    if (marker === lastRead.current) return;
    lastRead.current = marker;
    if (initialFile.file.size > MAX_CONFIG_BYTES) {
      setParseError("config.toml 超过 1 MB，无法导入");
      return;
    }
    void (async () => {
      try {
        const text = await initialFile.file.text();
        setSource(initialFile.path);
        setContent(text);
        setPasteOpen(false);
        if (text.trim() === "") {
          setParseError("选中的 config.toml 是空文件");
          return;
        }
        await runPreview(initialFile.path, text);
      } catch {
        setParseError("读取本机配置文件失败");
      }
    })();
  }, [open, initialFile, runPreview]);

  useEffect(() => {
    if (!open || !local || initialFile || busy || result) return;
    if (!local.found || !local.preview) {
      if (!content) {
        setPreview(null);
        setSource(local.source ?? "");
        setParseError(local.error ?? "服务端没有找到 config.toml");
      }
      return;
    }
    setContent("");
    setSource(local.source);
    setPreview(local.preview);
    setSelectedProfiles(new Set());
    setSelectedProviders(new Set());
    setResult(null);
    setParseError("");
    setPasteOpen(false);
    setOrigin("local");
  }, [open, local, initialFile, busy, result, content]);

  const close = () => {
    if (!busy) onClose();
  };

  const requiredProviders = new Set<string>();
  for (const profile of preview?.profiles ?? []) {
    if (selectedProfiles.has(profile.key) && !profile.provider_missing) {
      requiredProviders.add(profile.provider_key);
    }
  }

  const selectedProfileCount = (preview?.profiles ?? []).filter((p) => selectedProfiles.has(p.key)).length;
  const selectedProviderCount = (preview?.providers ?? []).filter((p) => selectedProviders.has(p.key)).length;

  const toggleProfile = (key: string, checked: boolean) => {
    if (!preview) return;
    const nextProfiles = new Set(selectedProfiles);
    const nextProviders = new Set(selectedProviders);
    if (checked) nextProfiles.add(key);
    else nextProfiles.delete(key);
    const profile = preview.profiles.find((item) => item.key === key);
    if (checked && profile && !profile.provider_missing) nextProviders.add(profile.provider_key);
    setSelectedProfiles(nextProfiles);
    setSelectedProviders(nextProviders);
  };

  const toggleProvider = (key: string, checked: boolean) => {
    if (!preview) return;
    const nextProviders = new Set(selectedProviders);
    const nextProfiles = new Set(selectedProfiles);
    if (checked) nextProviders.add(key);
    else {
      nextProviders.delete(key);
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
    if (!preview || (selectedProfileCount === 0 && selectedProviderCount === 0)) return;
    setBusy("import");
    try {
      const profileKeys = Array.from(selectedProfiles);
      const providerKeys = Array.from(selectedProviders);
      const imported =
        origin === "local" && local?.found
          ? await api.importLocalModelConfig(profileKeys, providerKeys)
          : await api.importModelConfig(source || "手动输入", content, profileKeys, providerKeys);
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

  const allProfilesChecked =
    !!preview &&
    preview.profiles.filter((p) => !p.provider_missing).length > 0 &&
    preview.profiles.filter((p) => !p.provider_missing).every((p) => selectedProfiles.has(p.key));
  const allProvidersChecked =
    !!preview && preview.providers.length > 0 && preview.providers.every((p) => selectedProviders.has(p.key));

  return (
    <div className={"modal-backdrop" + (open ? " open" : "")}>
      <div className="modal codex-import-modal">
        <div className="modal-head">
          <div>
            <div className="modal-title">导入本机模型配置</div>
            <div className="modal-sub">
              勾选一项或多项后写入 Master；Profile 会自动携带它依赖的 Provider，导入的模型同时启用。
            </div>
          </div>
          <button className="icon-btn" onClick={close} disabled={!!busy}>✕</button>
        </div>

        <div className="modal-body codex-import-body">
          <div className="codex-import-paths">
            <span>配置来源</span>
            <code>{source || (localLoading ? "正在查找 config.toml…" : "尚未找到 config.toml")}</code>
            <div className="codex-import-source-actions">
              <button className="btn small" disabled={!!busy || localLoading} onClick={onReloadLocal}>重新读取</button>
              <button className="btn small" disabled={!!busy} onClick={onPickFile}>选择 config.toml</button>
              <button className="btn small" disabled={!!busy} onClick={onPickDirectory}>指定目录</button>
              <button className="btn small" disabled={!!busy} onClick={() => setPasteOpen((v) => !v)}>
                {pasteOpen ? "收起粘贴" : "粘贴 TOML"}
              </button>
              {directoryFiles.length > 1 && (
                <select
                  className="codex-import-dir-select"
                  disabled={!!busy}
                  onChange={(event) => onSelectDirectoryFile(event.target.value)}
                  value={directoryFiles.some((item) => item.path === source) ? source : directoryFiles[0]?.path ?? ""}
                >
                  {directoryFiles.map((item) => (
                    <option key={item.path} value={item.path}>{item.path}</option>
                  ))}
                </select>
              )}
            </div>
          </div>

          {pasteOpen && (
            <div className="codex-import-paste">
              <textarea
                value={content}
                onChange={(event) => {
                  setContent(event.target.value);
                  setSource("手动输入");
                  setPreview(null);
                  setResult(null);
                }}
                placeholder={EXAMPLE_CONFIG}
                spellCheck={false}
              />
              <div className="codex-import-paste-foot">
                <span className="tiny">支持 profile、model、model_provider、model_reasoning_effort 与 [profiles.*]、[model_providers.*]。</span>
                <button
                  className="btn small primary"
                  disabled={!!busy || content.trim() === ""}
                  onClick={() => void runPreview(source || "手动输入", content)}
                >
                  {busy === "preview" ? "读取中…" : "读取配置"}
                </button>
              </div>
            </div>
          )}

          {parseError && <div className="codex-import-error">{parseError}</div>}

          {result ? (
            <>
              <div className="notice">
                <span>✓</span>
                <div>
                  <strong>已写入模型配置。</strong>
                  新增 {result.imported_profiles} 个 Profile、{result.imported_providers} 个 Provider；更新{" "}
                  {result.updated_profiles} 个 Profile、{result.updated_providers} 个 Provider。
                  {result.default.model ? ` 默认模型已切换为 ${result.default.model}。` : ""}
                </div>
              </div>
              {result.warnings.length > 0 && (
                <div className="codex-import-warnings">
                  {result.warnings.map((warning, index) => (
                    <div key={index}>• {warning}</div>
                  ))}
                </div>
              )}
            </>
          ) : preview ? (
            <>
              <div aria-label="待导入模型 Profile" className="codex-import-list" role="table">
                <div className="header" role="row">
                  <span>Profile</span>
                  <span>Model</span>
                  <span>Provider</span>
                  <span>API Key</span>
                  <label className="codex-import-select">
                    <input
                      aria-label="选择全部 Profile"
                      checked={allProfilesChecked}
                      onChange={(event) => toggleAllProfiles(event.target.checked)}
                      type="checkbox"
                    />
                    <span>选择</span>
                  </label>
                </div>
                {preview.profiles.length === 0 ? (
                  <div className="empty-row" role="row"><span>配置中没有 Profile</span></div>
                ) : (
                  preview.profiles.map((profile) => (
                    <div key={profile.key} role="row">
                      <span className="codex-import-name">
                        <strong>{profile.key}</strong>
                        <small>{profile.action === "update" ? "更新已有" : "新建"}</small>
                      </span>
                      <code>{profile.model}</code>
                      <span>
                        {profile.provider_name || profile.provider_key}
                        {profile.provider_missing ? "（Provider 缺失）" : ""}
                      </span>
                      <span data-source={profile.api_key_source}>{keyLabel(profile.api_key_source)}</span>
                      <label className="codex-import-select">
                        <input
                          aria-label={`选择 Profile ${profile.key}`}
                          checked={selectedProfiles.has(profile.key)}
                          disabled={profile.provider_missing}
                          onChange={(event) => toggleProfile(profile.key, event.target.checked)}
                          type="checkbox"
                        />
                      </label>
                    </div>
                  ))
                )}
              </div>

              <div aria-label="待导入 Provider" className="codex-import-list" role="table">
                <div className="header" role="row">
                  <span>Provider</span>
                  <span>Base URL</span>
                  <span>格式</span>
                  <span>API Key</span>
                  <label className="codex-import-select">
                    <input
                      aria-label="选择全部 Provider"
                      checked={allProvidersChecked}
                      onChange={(event) => toggleAllProviders(event.target.checked)}
                      type="checkbox"
                    />
                    <span>选择</span>
                  </label>
                </div>
                {preview.providers.map((provider) => {
                  const required = requiredProviders.has(provider.key);
                  return (
                    <div key={provider.key} role="row">
                      <span className="codex-import-name">
                        <strong>{provider.name || provider.key}</strong>
                        <small>{provider.key}</small>
                      </span>
                      <code>{provider.base_url || "默认端点"}</code>
                      <span>{provider.wire_api}</span>
                      <span data-source={provider.api_key_source}>
                        {keyLabel(provider.api_key_source)}
                        {provider.env_key ? ` · ${provider.env_key}` : ""}
                      </span>
                      <label className="codex-import-select">
                        <input
                          aria-label={
                            required ? `${provider.key} 由所选 Profile 依赖` : `选择 Provider ${provider.key}`
                          }
                          checked={required || selectedProviders.has(provider.key)}
                          disabled={required}
                          onChange={(event) => toggleProvider(provider.key, event.target.checked)}
                          type="checkbox"
                        />
                      </label>
                    </div>
                  );
                })}
              </div>

              {preview.warnings.length > 0 && (
                <div className="codex-import-warnings">
                  {preview.warnings.map((warning, index) => (
                    <div key={index}>• {warning}</div>
                  ))}
                </div>
              )}
            </>
          ) : (
            <div className="codex-import-empty">
              {busy === "preview" || localLoading
                ? "正在读取服务端的 ~/.codex/config.toml…"
                : parseError
                  ? parseError
                  : "选择 ~/.codex/config.toml、指定目录或粘贴 TOML 后，这里会列出可导入的 Profile 与 Provider。"}
              {!localLoading && local && !local.found && local.candidates?.length > 0 && (
                <div className="tiny" style={{ marginTop: 8 }}>
                  已查找：{local.candidates.join("、")}
                </div>
              )}
            </div>
          )}
        </div>

        <div className="modal-foot">
          <div className="tiny">
            {result ? "可以关闭窗口，在模型配置页继续调整供应商与模型。" : "同名 Profile/Provider 会更新，已有模型目录不会删除。"}
          </div>
          <div>
            {result ? (
              <button className="btn primary" onClick={onClose}>完成</button>
            ) : (
              <>
                <button className="btn" onClick={close} disabled={!!busy}>取消</button>{" "}
                <button
                  className="btn primary"
                  disabled={!!busy || !preview || selectedProfileCount + selectedProviderCount === 0}
                  onClick={() => void executeImport()}
                >
                  {busy === "import"
                    ? "导入中…"
                    : `导入 ${selectedProfileCount} 个 Profile / ${selectedProviderCount} 个 Provider`}
                </button>
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
