import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError } from "../api";
import type { ModelDefault, ModelLocalConfigResult, ModelProvider } from "../types";
import type { Notify } from "../App";
import { ModelConfigImportModal, type PickedConfig } from "./ModelConfigImportModal";

const WIRE_OPTIONS = [
  { value: "responses", label: "Responses（/responses）" },
  { value: "chat", label: "Chat Completions（/chat/completions）" },
];

const EFFORT_OPTIONS = ["minimal", "low", "medium", "high", "xhigh"];

type ProviderEdit = { name: string; base_url: string; wire_api: string; api_key: string };

type ProviderTemplate = {
  id: string;
  name: string;
  note: string;
  base_url: string;
  wire_api: string;
  models: string[];
};

const TEMPLATES: ProviderTemplate[] = [
  { id: "deepseek", name: "DeepSeek", note: "官方 Chat Completions 端点", base_url: "https://api.deepseek.com/v1", wire_api: "chat", models: ["deepseek-chat", "deepseek-reasoner"] },
  { id: "zhipu", name: "智谱 GLM", note: "bigmodel 兼容端点", base_url: "https://open.bigmodel.cn/api/paas/v4", wire_api: "chat", models: ["glm-4.6", "glm-4.5"] },
  { id: "qwen", name: "通义千问", note: "DashScope 兼容模式", base_url: "https://dashscope.aliyuncs.com/compatible-mode/v1", wire_api: "chat", models: ["qwen3-max"] },
  { id: "kimi", name: "Kimi", note: "Moonshot 兼容端点", base_url: "https://api.moonshot.cn/v1", wire_api: "chat", models: ["kimi-k2"] },
  { id: "ollama", name: "Ollama 本机", note: "本机 11434 端口，无需凭据", base_url: "http://127.0.0.1:11434/v1", wire_api: "chat", models: ["qwen3:8b"] },
  { id: "custom", name: "自定义供应商", note: "手填 Base URL 与模型", base_url: "", wire_api: "responses", models: [] },
];

type Draft = {
  edits: Record<string, ProviderEdit>;
  providerEnabled: Record<string, boolean>;
  modelEnabled: Record<string, boolean>;
  addedModels: Record<string, string[]>;
  def: ModelDefault | null;
};

const EMPTY_DRAFT: Draft = { edits: {}, providerEnabled: {}, modelEnabled: {}, addedModels: {}, def: null };

const modelKey = (providerId: string, model: string) => providerId + "\u0000" + model;

const shortHost = (value: string) => {
  try {
    return new URL(value).host;
  } catch {
    return value;
  }
};

const EMPTY_PROVIDER: ProviderEdit = { name: "", base_url: "", wire_api: "responses", api_key: "" };

export function ModelsPage({ notify }: { notify: Notify }) {
  const [providers, setProviders] = useState<ModelProvider[]>([]);
  const [defaults, setDefaults] = useState<ModelDefault>({ provider_id: "", model: "", reasoning_effort: "" });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [selectedId, setSelectedId] = useState("");
  const [providerQuery, setProviderQuery] = useState("");
  const [modelQuery, setModelQuery] = useState("");
  const [newModel, setNewModel] = useState("");
  const [draft, setDraft] = useState<Draft>(EMPTY_DRAFT);
  const [saving, setSaving] = useState(false);
  const [busy, setBusy] = useState("");
  const [revealed, setRevealed] = useState<Record<string, boolean>>({});
  const [importOpen, setImportOpen] = useState(false);
  const [importFile, setImportFile] = useState<PickedConfig | null>(null);
  const [localConfig, setLocalConfig] = useState<ModelLocalConfigResult | null>(null);
  const [localLoading, setLocalLoading] = useState(false);
  const [directoryFiles, setDirectoryFiles] = useState<PickedConfig[]>([]);
  const [createOpen, setCreateOpen] = useState(false);
  const [createTemplate, setCreateTemplate] = useState<ProviderTemplate>(TEMPLATES[0]);
  const [createForm, setCreateForm] = useState<ProviderEdit>(EMPTY_PROVIDER);
  const [createModels, setCreateModels] = useState("");

  const importInput = useRef<HTMLInputElement | null>(null);
  const importDirectoryInput = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    importDirectoryInput.current?.setAttribute("webkitdirectory", "");
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [items, def] = await Promise.all([api.listModelProviders(), api.getModelDefault()]);
      setProviders(items);
      setDefaults(def);
      setDraft(EMPTY_DRAFT);
      setSelectedId((prev) => (prev && items.some((item) => item.id === prev) ? prev : items[0]?.id ?? ""));
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载模型配置失败");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const editOf = (provider: ModelProvider): ProviderEdit =>
    draft.edits[provider.id] ?? {
      name: provider.name,
      base_url: provider.base_url,
      wire_api: provider.wire_api || "responses",
      api_key: "",
    };

  const enabledOf = (provider: ModelProvider) => draft.providerEnabled[provider.id] ?? provider.enabled;

  const modelsOf = (provider: ModelProvider): { id: string; enabled: boolean; added: boolean }[] => {
    const rows = provider.models.map((model) => ({
      id: model.model_id,
      enabled: draft.modelEnabled[modelKey(provider.id, model.model_id)] ?? model.enabled,
      added: false,
    }));
    for (const id of draft.addedModels[provider.id] ?? []) {
      if (!rows.some((row) => row.id === id)) rows.push({ id, enabled: true, added: true });
    }
    return rows;
  };

  const updateEdit = (provider: ModelProvider, patch: Partial<ProviderEdit>) => {
    setDraft((current) => ({
      ...current,
      edits: { ...current.edits, [provider.id]: { ...editOf(provider), ...patch } },
    }));
  };

  const setModelEnabled = (provider: ModelProvider, model: string, enabled: boolean) => {
    setDraft((current) => {
      const effective = current.def ?? defaults;
      const next: Draft = {
        ...current,
        modelEnabled: { ...current.modelEnabled, [modelKey(provider.id, model)]: enabled },
      };
      if (!enabled && effective.provider_id === provider.id && effective.model === model) {
        const fallback = provider.models
          .filter((row) => row.model_id !== model)
          .map((row) => ({ id: row.model_id, enabled: current.modelEnabled[modelKey(provider.id, row.model_id)] ?? row.enabled }))
          .find((row) => row.enabled);
        next.def = { provider_id: provider.id, model: fallback?.id ?? "", reasoning_effort: effective.reasoning_effort };
      }
      return next;
    });
  };

  const addModel = (provider: ModelProvider, model: string) => {
    const id = model.trim();
    if (!id) return;
    if (modelsOf(provider).some((row) => row.id === id)) {
      notify("模型已在列表中");
      return;
    }
    setDraft((current) => ({
      ...current,
      addedModels: { ...current.addedModels, [provider.id]: [...(current.addedModels[provider.id] ?? []), id] },
    }));
    setNewModel("");
  };

  const dirtyCount = useMemo(() => {
    let count = 0;
    for (const provider of providers) {
      const edit = draft.edits[provider.id];
      if (edit) {
        if (edit.name !== provider.name) count += 1;
        if (edit.base_url !== provider.base_url) count += 1;
        if ((edit.wire_api || "responses") !== (provider.wire_api || "responses")) count += 1;
        if (edit.api_key.trim() !== "") count += 1;
      }
      if (draft.providerEnabled[provider.id] !== undefined && draft.providerEnabled[provider.id] !== provider.enabled) count += 1;
      for (const model of provider.models) {
        const next = draft.modelEnabled[modelKey(provider.id, model.model_id)];
        if (next !== undefined && next !== model.enabled) count += 1;
      }
      count += (draft.addedModels[provider.id] ?? []).length;
    }
    if (draft.def && (draft.def.provider_id !== defaults.provider_id || draft.def.model !== defaults.model || draft.def.reasoning_effort !== defaults.reasoning_effort)) {
      count += 1;
    }
    return count;
  }, [defaults, draft, providers]);

  const discard = () => {
    setDraft(EMPTY_DRAFT);
    notify("已放弃未保存的模型修改");
  };

  const saveAll = async () => {
    if (dirtyCount === 0) return;
    setSaving(true);
    try {
      for (const provider of providers) {
        const edit = draft.edits[provider.id];
        const changes: { name?: string; base_url?: string; wire_api?: string; api_key?: string } = {};
        if (edit) {
          if (edit.name !== provider.name) changes.name = edit.name;
          if (edit.base_url !== provider.base_url) changes.base_url = edit.base_url;
          if ((edit.wire_api || "responses") !== (provider.wire_api || "responses")) changes.wire_api = edit.wire_api;
          if (edit.api_key.trim() !== "") changes.api_key = edit.api_key.trim();
        }
        if (Object.keys(changes).length > 0) {
          await api.updateModelProvider(provider.id, changes);
        }
        const enabled = draft.providerEnabled[provider.id];
        if (enabled !== undefined && enabled !== provider.enabled) {
          await api.setModelProviderEnabled(provider.id, enabled);
        }
        for (const model of provider.models) {
          const next = draft.modelEnabled[modelKey(provider.id, model.model_id)];
          if (next !== undefined && next !== model.enabled) {
            await api.setProviderModelEnabled(provider.id, model.model_id, next);
          }
        }
        for (const id of draft.addedModels[provider.id] ?? []) {
          await api.addProviderModel(provider.id, id);
        }
      }
      if (draft.def) {
        await api.putModelDefault(draft.def);
      }
      notify("模型配置已保存");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "保存模型配置失败", "error");
    } finally {
      setSaving(false);
    }
  };

  const loadLocalConfig = async () => {
    setLocalLoading(true);
    try {
      setLocalConfig(await api.previewLocalModelConfig());
    } catch (err) {
      setLocalConfig({
        found: false,
        source: "",
        candidates: [],
        error: err instanceof ApiError ? err.message : "读取服务端 Codex 配置失败",
      });
    } finally {
      setLocalLoading(false);
    }
  };

  const openImportLocal = () => {
    setImportOpen(true);
    setImportFile(null);
    setDirectoryFiles([]);
    setLocalConfig(null);
    void loadLocalConfig();
  };

  const pickImportFile = () => {
    setImportOpen(true);
    setLocalConfig(null);
    importInput.current?.click();
  };

  const pickImportDirectory = () => {
    setImportOpen(true);
    importDirectoryInput.current?.click();
  };

  const onImportFileChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    const input = event.currentTarget;
    const file = input.files?.[0];
    input.value = "";
    if (!file) return;
    setImportOpen(true);
    setImportFile({ path: file.name, file });
  };

  const onImportDirectoryChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    const input = event.currentTarget;
    const picked = Array.from(input.files ?? [])
      .filter((file) => file.name.toLowerCase() === "config.toml")
      .map((file) => ({ path: file.webkitRelativePath || file.name, file }))
      .sort((a, b) => a.path.length - b.path.length || a.path.localeCompare(b.path));
    input.value = "";
    setImportOpen(true);
    setDirectoryFiles(picked);
    if (picked.length === 0) {
      notify("所选目录中没有找到 config.toml", "error");
      return;
    }
    setImportFile(picked[0]);
  };

  const testProvider = async (provider: ModelProvider) => {
    if (Object.keys(draft.edits[provider.id] ?? {}).length > 0) {
      notify("请先保存连接修改，再测试模型", "error");
      return;
    }
    setBusy("test:" + provider.id);
    try {
      const first = modelsOf(provider).find((model) => model.enabled)?.id ?? "";
      const res = await api.testModelProvider(provider.id, first);
      notify((res.ok ? "测试通过：" : "测试失败：") + res.message + `（${res.latency_ms}ms）`, res.ok ? "ok" : "error");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "测试失败", "error");
    } finally {
      setBusy("");
    }
  };

  const refreshCatalog = async (provider: ModelProvider) => {
    setBusy("refresh:" + provider.id);
    try {
      const res = await api.refreshProviderModels(provider.id);
      notify(`目录已刷新，发现 ${res.count} 个模型（新模型默认停用）`);
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "刷新目录失败", "error");
    } finally {
      setBusy("");
    }
  };

  const removeModel = async (provider: ModelProvider, model: string) => {
    if (!window.confirm(`从 ${provider.name} 的模型目录删除 ${model}？`)) return;
    try {
      await api.deleteProviderModel(provider.id, model);
      if (defaults.provider_id === provider.id && defaults.model === model) {
        const fallback = modelsOf(provider).filter((row) => row.enabled && row.id !== model)[0]?.id ?? "";
        await api.putModelDefault({ provider_id: fallback ? provider.id : "", model: fallback, reasoning_effort: fallback ? defaults.reasoning_effort : "" });
      }
      notify("模型已删除");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "删除失败", "error");
    }
  };

  const removeProvider = async (provider: ModelProvider) => {
    if (!window.confirm(`删除供应商 ${provider.name}？它的模型目录会一起删除，引用它的项目会回到默认模型。`)) return;
    try {
      await api.deleteModelProvider(provider.id);
      if (defaults.provider_id === provider.id) {
        await api.putModelDefault({ provider_id: "", model: "", reasoning_effort: "" });
      }
      notify("供应商已删除");
      setSelectedId("");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "删除失败", "error");
    }
  };

  const setDefaultModel = (providerId: string, model: string) => {
    const provider = providers.find((item) => item.id === providerId);
    const available = provider ? modelsOf(provider).filter((row) => row.enabled) : [];
    const nextModel = model || available[0]?.id || "";
    setDraft((current) => ({
      ...current,
      def: {
        provider_id: providerId,
        model: nextModel,
        reasoning_effort:
          current.def?.provider_id === providerId ? current.def.reasoning_effort : defaults.reasoning_effort,
      },
    }));
  };

  const effectiveDefault = draft.def ?? defaults;

  const openCreate = () => {
    setCreateTemplate(TEMPLATES[0]);
    setCreateForm({ ...EMPTY_PROVIDER, name: TEMPLATES[0].name, base_url: TEMPLATES[0].base_url, wire_api: TEMPLATES[0].wire_api });
    setCreateModels(TEMPLATES[0].models.join(", "));
    setCreateOpen(true);
  };

  const chooseTemplate = (template: ProviderTemplate) => {
    setCreateTemplate(template);
    setCreateForm({ ...EMPTY_PROVIDER, name: template.name, base_url: template.base_url, wire_api: template.wire_api });
    setCreateModels(template.models.join(", "));
  };

  const createProvider = async () => {
    if (createForm.name.trim() === "" || createForm.base_url.trim() === "") {
      notify("名称和 Base URL 都不能为空", "error");
      return;
    }
    setSaving(true);
    try {
      const models = createModels.split(",").map((item) => item.trim()).filter(Boolean);
      const created = await api.createModelProvider({ ...createForm, models });
      notify("供应商已创建");
      setCreateOpen(false);
      await load();
      setSelectedId(created.id);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "创建失败", "error");
    } finally {
      setSaving(false);
    }
  };

  const exportJSON = () => {
    const payload = {
      default: effectiveDefault,
      providers: providers.map((provider) => {
        const edit = editOf(provider);
        return {
          id: provider.id,
          name: edit.name,
          base_url: edit.base_url,
          wire_api: edit.wire_api,
          enabled: enabledOf(provider),
          models: modelsOf(provider).filter((row) => row.enabled).map((row) => row.id),
        };
      }),
    };
    const blob = new Blob([JSON.stringify(payload, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = "smartpipe-models.json";
    link.click();
    URL.revokeObjectURL(url);
  };

  const filteredProviders = providers.filter((provider) => {
    const query = providerQuery.trim().toLowerCase();
    if (!query) return true;
    const edit = editOf(provider);
    return [provider.name, provider.base_url, edit.name, edit.base_url].join(" ").toLowerCase().includes(query);
  });

  const selected = providers.find((provider) => provider.id === selectedId) ?? null;
  const selectedEdit = selected ? editOf(selected) : null;
  const selectedModels = selected ? modelsOf(selected) : [];
  const visibleModels = selectedModels.filter((row) =>
    modelQuery.trim() === "" ? true : row.id.toLowerCase().includes(modelQuery.trim().toLowerCase()),
  );
  const defaultProvider = providers.find((provider) => provider.id === effectiveDefault.provider_id) ?? null;
  const defaultProviderModels = defaultProvider ? modelsOf(defaultProvider).filter((row) => row.enabled) : [];
  const selectedDirty = selected ? Boolean(draft.edits[selected.id] && Object.keys(draft.edits[selected.id]).length > 0) : false;

  return (
    <>
      <input
        ref={importInput}
        type="file"
        accept=".toml,text/plain,application/toml"
        style={{ display: "none" }}
        onChange={onImportFileChange}
      />
      <input
        ref={importDirectoryInput}
        type="file"
        multiple
        style={{ display: "none" }}
        onChange={onImportDirectoryChange}
      />

      <div className="model-toolbar">
        <div className="model-toolbar-status">
          {loading ? "正在加载模型配置…" : error ? error : `已同步 ${providers.length} 个供应商`}
        </div>
        <div className="model-toolbar-actions">
          <button className="btn" onClick={() => void load()} disabled={saving || loading}>刷新</button>
          <button className="btn primary" onClick={openImportLocal} disabled={saving}>导入本机模型配置</button>
          <button className="btn" title="选择其他 TOML 模型配置" onClick={pickImportFile} disabled={saving}>选择 TOML</button>
          <button className="btn" onClick={exportJSON} disabled={providers.length === 0}>导出</button>
          <button className="btn" onClick={discard} disabled={saving || dirtyCount === 0}>放弃</button>
          <button className="btn primary" onClick={() => void saveAll()} disabled={saving || dirtyCount === 0}>
            {saving ? "保存中…" : dirtyCount > 0 ? `保存 ${dirtyCount} 项` : "已保存"}
          </button>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">模型供应商</div>
            <div className="card-sub">{providers.length} 个供应商 · 密钥密文保存 · 支持目录刷新与真实测试</div>
          </div>
          <button className="btn primary" onClick={openCreate}>＋ 新建供应商</button>
        </div>
        <div className="card-body">
          <div className="model-default-bar">
            <span className="model-default-title">默认模型</span>
            <label>
              <span>供应商</span>
              <select
                aria-label="默认模型供应商"
                value={effectiveDefault.provider_id}
                onChange={(event) => setDefaultModel(event.target.value, "")}
              >
                <option value="">未设置</option>
                {providers
                  .filter((provider) => modelsOf(provider).some((row) => row.enabled))
                  .map((provider) => (
                    <option key={provider.id} value={provider.id}>{editOf(provider).name || provider.name}</option>
                  ))}
              </select>
            </label>
            <label>
              <span>模型</span>
              <select
                aria-label="默认模型名称"
                disabled={!defaultProvider}
                value={effectiveDefault.model}
                onChange={(event) => setDefaultModel(effectiveDefault.provider_id, event.target.value)}
              >
                <option value="">未设置</option>
                {defaultProviderModels.map((row) => (
                  <option key={row.id} value={row.id}>{row.id}</option>
                ))}
              </select>
            </label>
            <label>
              <span>推理强度</span>
              <select
                aria-label="默认推理强度"
                value={effectiveDefault.reasoning_effort}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    def: { ...effectiveDefault, reasoning_effort: event.target.value },
                  }))
                }
              >
                <option value="">默认</option>
                {EFFORT_OPTIONS.map((effort) => (
                  <option key={effort} value={effort}>{effort}</option>
                ))}
              </select>
            </label>
            {dirtyCount > 0 && <span className="model-default-hint">有 {dirtyCount} 项未保存</span>}
          </div>

          {loading ? (
            <div className="empty">加载中…</div>
          ) : error ? (
            <div className="empty">
              <h3>无法读取模型配置</h3>
              <div className="tiny">{error}</div>
              <button className="btn" style={{ marginTop: 12 }} onClick={() => void load()}>重试</button>
            </div>
          ) : providers.length === 0 ? (
            <div className="empty">
              <h3>还没有模型供应商</h3>
              <div className="tiny">从本机 Codex 配置导入，或选择模板新建供应商。</div>
              <div style={{ display: "flex", justifyContent: "center", gap: 8, marginTop: 14 }}>
                <button className="btn primary" onClick={openImportLocal}>导入本机模型配置</button>
                <button className="btn" onClick={openCreate}>新建供应商</button>
              </div>
            </div>
          ) : (
            <div className="model-provider-panel">
              <aside className="model-provider-nav" aria-label="供应商列表">
                <div className="model-provider-search">
                  <input
                    aria-label="搜索供应商"
                    value={providerQuery}
                    onChange={(event) => setProviderQuery(event.target.value)}
                    placeholder="搜索供应商"
                  />
                </div>
                <div className="model-provider-items">
                  {filteredProviders.map((provider) => {
                    const models = modelsOf(provider);
                    const enabledCount = models.filter((row) => row.enabled).length;
                    const enabled = enabledOf(provider);
                    const tone = !enabled ? "warn" : enabledCount === 0 ? "muted" : "ok";
                    return (
                      <button
                        key={provider.id}
                        type="button"
                        className="model-provider-item"
                        data-active={provider.id === selectedId}
                        onClick={() => setSelectedId(provider.id)}
                      >
                        <span className="model-provider-badge">{(editOf(provider).name || provider.name).slice(0, 1).toUpperCase()}</span>
                        <span className="model-provider-name">
                          {editOf(provider).name || provider.name}
                          <small>{enabledCount} / {models.length} 个模型已启用</small>
                        </span>
                        <span className="model-provider-dot" data-tone={tone} />
                      </button>
                    );
                  })}
                  {filteredProviders.length === 0 && <p className="model-provider-empty">没有匹配的供应商</p>}
                </div>
              </aside>

              <div className="model-provider-detail">
                {selected && selectedEdit ? (
                  <>
                    <header className="model-provider-head">
                      <span className="model-provider-badge" data-large="true">
                        {(selectedEdit.name || selected.name).slice(0, 1).toUpperCase()}
                      </span>
                      <div className="model-provider-head-main">
                        <h3>{selectedEdit.name || selected.name}</h3>
                        <p>
                          {shortHost(selectedEdit.base_url) || "未设置 Base URL"} · {selectedEdit.wire_api} ·{" "}
                          {selectedModels.filter((row) => row.enabled).length} 个模型已启用
                        </p>
                      </div>
                      <div className="model-provider-head-actions">
                        <button className="btn small" onClick={() => void testProvider(selected)} disabled={busy === "test:" + selected.id}>
                          {busy === "test:" + selected.id ? "测试中…" : "测试模型"}
                        </button>
                        <button
                          className="btn small"
                          onClick={() =>
                            setDraft((current) => ({
                              ...current,
                              providerEnabled: { ...current.providerEnabled, [selected.id]: !enabledOf(selected) },
                            }))
                          }
                        >
                          {enabledOf(selected) ? "停用供应商" : "启用供应商"}
                        </button>
                        <button className="btn small danger" onClick={() => void removeProvider(selected)}>删除</button>
                      </div>
                    </header>

                    <section className="model-card">
                      <header className="model-card-head">
                        <div>
                          <h4>连接</h4>
                          <small>决定请求打到哪个端点、用哪把 Key</small>
                        </div>
                        <span className={"chip " + (enabledOf(selected) ? "ok" : "warn")}>
                          {enabledOf(selected) ? "启用" : "停用"}
                        </span>
                      </header>
                      <div className="model-form-grid">
                        <label>
                          <span>名称</span>
                          <input
                            aria-label="供应商名称"
                            value={selectedEdit.name}
                            onChange={(event) => updateEdit(selected, { name: event.target.value })}
                          />
                        </label>
                        <label>
                          <span>API 格式</span>
                          <select
                            aria-label="API 格式"
                            value={selectedEdit.wire_api}
                            onChange={(event) => updateEdit(selected, { wire_api: event.target.value })}
                          >
                            {WIRE_OPTIONS.map((option) => (
                              <option key={option.value} value={option.value}>{option.label}</option>
                            ))}
                          </select>
                        </label>
                        <label>
                          <span>Base URL</span>
                          <input
                            aria-label="Base URL"
                            value={selectedEdit.base_url}
                            onChange={(event) => updateEdit(selected, { base_url: event.target.value })}
                            placeholder="https://api.example.com/v1"
                          />
                        </label>
                        <label>
                          <span>API Key</span>
                          <span className="model-secret">
                            <input
                              aria-label="API Key"
                              type={revealed[selected.id] ? "text" : "password"}
                              value={selectedEdit.api_key}
                              onChange={(event) => updateEdit(selected, { api_key: event.target.value })}
                              placeholder={selected.credential_id ? "已保存，留空表示不修改" : "sk-..."}
                            />
                            <button
                              type="button"
                              className="btn small"
                              onClick={() =>
                                setRevealed((current) => ({ ...current, [selected.id]: !current[selected.id] }))
                              }
                            >
                              {revealed[selected.id] ? "隐藏" : "显示"}
                            </button>
                          </span>
                        </label>
                      </div>
                      {selected.last_tested_at && (
                        <div className="model-test-note">
                          <span className={"chip " + (selected.last_test_ok ? "ok" : "bad")}>
                            {selected.last_test_ok ? "最近测试通过" : "最近测试失败"}
                          </span>
                          <span>{selected.last_test_message}</span>
                        </div>
                      )}
                      {selectedDirty && <div className="model-dirty-note">连接有未保存的修改，保存后测试使用的是新配置。</div>}
                    </section>

                    <section className="model-card">
                      <header className="model-card-head">
                        <div>
                          <h4>模型列表</h4>
                          <small>勾选后才能被项目和会话选用</small>
                        </div>
                        <button
                          className="btn small"
                          disabled={busy === "refresh:" + selected.id}
                          onClick={() => void refreshCatalog(selected)}
                        >
                          {busy === "refresh:" + selected.id ? "刷新中…" : "刷新目录"}
                        </button>
                      </header>
                      <div className="model-tools">
                        <input
                          aria-label="添加模型"
                          value={newModel}
                          onChange={(event) => setNewModel(event.target.value)}
                          onKeyDown={(event) => {
                            if (event.key === "Enter") addModel(selected, newModel);
                          }}
                          placeholder="输入模型 ID 后添加"
                        />
                        <button className="btn small" onClick={() => addModel(selected, newModel)} disabled={!newModel.trim()}>
                          ＋ 添加模型
                        </button>
                        <input
                          aria-label="搜索模型"
                          className="model-search"
                          value={modelQuery}
                          onChange={(event) => setModelQuery(event.target.value)}
                          placeholder="搜索模型 ID"
                        />
                      </div>
                      {visibleModels.length === 0 ? (
                        <p className="model-provider-empty">当前没有模型。刷新目录或手工添加模型 ID。</p>
                      ) : (
                        <div className="model-table" role="table">
                          <div className="model-table-head" role="row">
                            <span role="columnheader">模型 ID</span>
                            <span role="columnheader">来源</span>
                            <span role="columnheader">启用</span>
                            <span role="columnheader">默认</span>
                            <span role="columnheader">操作</span>
                          </div>
                          {visibleModels.map((row) => {
                            const isDefault =
                              effectiveDefault.provider_id === selected.id && effectiveDefault.model === row.id;
                            return (
                              <div className="model-table-row" role="row" key={row.id} data-enabled={row.enabled}>
                                <span className="model-table-name" role="cell">
                                  <code>{row.id}</code>
                                  {row.added && <small>待保存</small>}
                                </span>
                                <span role="cell">
                                  <span className="chip">{row.added ? "手工" : "目录"}</span>
                                </span>
                                <span role="cell">
                                  <label className="model-switch">
                                    <input
                                      type="checkbox"
                                      checked={row.enabled}
                                      onChange={(event) => setModelEnabled(selected, row.id, event.target.checked)}
                                    />
                                    <span>{row.enabled ? "已启用" : "未启用"}</span>
                                  </label>
                                </span>
                                <span role="cell">
                                  {isDefault ? (
                                    <span className="chip ok">默认</span>
                                  ) : (
                                    <button
                                      className="btn small"
                                      disabled={!row.enabled}
                                      onClick={() => setDefaultModel(selected.id, row.id)}
                                    >
                                      设为默认
                                    </button>
                                  )}
                                </span>
                                <span role="cell">
                                  {!row.added && (
                                    <button className="btn small danger" onClick={() => void removeModel(selected, row.id)}>
                                      删除
                                    </button>
                                  )}
                                </span>
                              </div>
                            );
                          })}
                        </div>
                      )}
                    </section>
                  </>
                ) : (
                  <div className="empty">选择左侧的供应商查看连接与模型</div>
                )}
              </div>
            </div>
          )}
        </div>
      </div>

      <ModelConfigImportModal
        open={importOpen}
        initialFile={importFile}
        directoryFiles={directoryFiles}
        local={localConfig}
        localLoading={localLoading}
        notify={notify}
        onClose={() => {
          setImportOpen(false);
          setImportFile(null);
          setDirectoryFiles([]);
        }}
        onImported={() => void load()}
        onPickFile={() => importInput.current?.click()}
        onPickDirectory={() => importDirectoryInput.current?.click()}
        onReloadLocal={() => void loadLocalConfig()}
        onSelectDirectoryFile={(path) => {
          const picked = directoryFiles.find((item) => item.path === path);
          if (picked) setImportFile(picked);
        }}
      />

      <div className={"modal-backdrop" + (createOpen ? " open" : "")}>
        <div className="modal model-create-modal">
          <div className="modal-head">
            <div>
              <div className="modal-title">新建供应商</div>
              <div className="modal-sub">选择模板后连接地址和推荐模型会自动填入，也可以直接创建自定义供应商。</div>
            </div>
            <button className="icon-btn" onClick={() => setCreateOpen(false)}>✕</button>
          </div>
          <div className="modal-body">
            <div className="model-template-list">
              {TEMPLATES.map((template) => (
                <button
                  key={template.id}
                  type="button"
                  className="model-template"
                  data-active={template.id === createTemplate.id}
                  onClick={() => chooseTemplate(template)}
                >
                  <strong>{template.name}</strong>
                  <small>{template.note}</small>
                </button>
              ))}
            </div>
            <div className="model-form-grid" style={{ marginTop: 14 }}>
              <label>
                <span>名称</span>
                <input value={createForm.name} onChange={(event) => setCreateForm((prev) => ({ ...prev, name: event.target.value }))} placeholder="DeepSeek" />
              </label>
              <label>
                <span>API 格式</span>
                <select value={createForm.wire_api} onChange={(event) => setCreateForm((prev) => ({ ...prev, wire_api: event.target.value }))}>
                  {WIRE_OPTIONS.map((option) => (
                    <option key={option.value} value={option.value}>{option.label}</option>
                  ))}
                </select>
              </label>
              <label>
                <span>Base URL</span>
                <input value={createForm.base_url} onChange={(event) => setCreateForm((prev) => ({ ...prev, base_url: event.target.value }))} placeholder="https://api.example.com/v1" />
              </label>
              <label>
                <span>API Key</span>
                <input type="password" value={createForm.api_key} onChange={(event) => setCreateForm((prev) => ({ ...prev, api_key: event.target.value }))} placeholder="sk-..." />
              </label>
              <label className="full">
                <span>初始模型（逗号分隔）</span>
                <input value={createModels} onChange={(event) => setCreateModels(event.target.value)} placeholder="deepseek-chat, deepseek-reasoner" />
              </label>
            </div>
          </div>
          <div className="modal-foot">
            <div className="tiny">凭据保存后只显示掩码，测试与目录刷新都由服务端发起。</div>
            <div>
              <button className="btn" onClick={() => setCreateOpen(false)}>取消</button>{" "}
              <button className="btn primary" disabled={saving} onClick={() => void createProvider()}>
                {saving ? "创建中…" : "创建供应商"}
              </button>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}
