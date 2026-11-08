import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError } from "../api";
import type { ModelDefault, ModelProfile, ModelProvider } from "../types";
import type { Notify } from "../App";
import { ModelConfigImportModal } from "./ModelConfigImportModal";

const WIRE_OPTIONS = [
  { value: "chat", label: "Chat Completions（/chat/completions）" },
  { value: "responses", label: "Responses（/responses）" },
];

type ProviderForm = {
  name: string;
  base_url: string;
  wire_api: string;
  api_key: string;
};

const EMPTY_PROVIDER: ProviderForm = { name: "", base_url: "", wire_api: "chat", api_key: "" };

export function ModelsPage({ notify }: { notify: Notify }) {
  const [items, setItems] = useState<ModelProvider[]>([]);
  const [profiles, setProfiles] = useState<ModelProfile[]>([]);
  const [selectedId, setSelectedId] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [form, setForm] = useState<ProviderForm>(EMPTY_PROVIDER);
  const [saving, setSaving] = useState(false);
  const [busy, setBusy] = useState("");
  const [addModel, setAddModel] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [createForm, setCreateForm] = useState<ProviderForm>(EMPTY_PROVIDER);
  const [createModels, setCreateModels] = useState("");
  const [defaults, setDefaults] = useState<ModelDefault>({ provider_id: "", model: "", reasoning_effort: "" });

  const selected = useMemo(() => items.find((p) => p.id === selectedId) ?? null, [items, selectedId]);

  const load = useCallback(async (keepId?: string) => {
    setLoading(true);
    try {
      const [providers, def, profileItems] = await Promise.all([
        api.listModelProviders(), api.getModelDefault(), api.listModelProfiles(),
      ]);
      setItems(providers);
      setDefaults(def);
      setProfiles(profileItems);
      setSelectedId((prev) => {
        const want = keepId ?? prev;
        if (want && providers.some((p) => p.id === want)) return want;
        return providers[0]?.id ?? "";
      });
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载模型配置失败");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  useEffect(() => {
    if (!selected) {
      setForm(EMPTY_PROVIDER);
      return;
    }
    setForm({
      name: selected.name,
      base_url: selected.base_url,
      wire_api: selected.wire_api || "chat",
      api_key: "",
    });
    setAddModel("");
  }, [selected]);

  const filtered = items.filter((p) => {
    const q = query.trim().toLowerCase();
    if (!q) return true;
    return p.name.toLowerCase().includes(q) || p.base_url.toLowerCase().includes(q);
  });

  const enabledModels = (p: ModelProvider | null) => p?.models.filter((m) => m.enabled) ?? [];

  const createProvider = async () => {
    if (createForm.name.trim() === "" || createForm.base_url.trim() === "") {
      notify("名称和 Base URL 都不能为空", "error");
      return;
    }
    setSaving(true);
    try {
      const models = createModels.split(",").map((m) => m.trim()).filter(Boolean);
      const created = await api.createModelProvider({ ...createForm, models });
      notify("供应商已创建");
      setCreateOpen(false);
      setCreateForm(EMPTY_PROVIDER);
      setCreateModels("");
      await load(created.id);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "创建失败", "error");
    } finally {
      setSaving(false);
    }
  };

  const saveProvider = async () => {
    if (!selected) return;
    setSaving(true);
    try {
      const updated = await api.updateModelProvider(selected.id, {
        name: form.name,
        base_url: form.base_url,
        wire_api: form.wire_api,
        api_key: form.api_key || undefined,
      });
      notify("连接配置已保存");
      setForm((prev) => ({ ...prev, api_key: "" }));
      await load(updated.id);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "保存失败", "error");
    } finally {
      setSaving(false);
    }
  };

  const test = async () => {
    if (!selected) return;
    setBusy("test");
    try {
      const res = await api.testModelProvider(selected.id);
      notify((res.ok ? "测试通过：" : "测试失败：") + res.message + "（" + res.latency_ms + "ms）", res.ok ? "ok" : "error");
      await load(selected.id);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "测试失败", "error");
    } finally {
      setBusy("");
    }
  };

  const toggleProvider = async () => {
    if (!selected) return;
    try {
      await api.setModelProviderEnabled(selected.id, !selected.enabled);
      notify(selected.enabled ? "供应商已停用" : "供应商已启用");
      await load(selected.id);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "操作失败", "error");
    }
  };

  const removeProvider = async () => {
    if (!selected) return;
    if (!window.confirm("删除供应商 " + selected.name + "？它的模型目录会一起删除，绑定它的项目会回到默认模型。")) return;
    try {
      await api.deleteModelProvider(selected.id);
      notify("供应商已删除");
      setSelectedId("");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "删除失败", "error");
    }
  };

  const addOneModel = async () => {
    if (!selected || addModel.trim() === "") return;
    setBusy("add");
    try {
      await api.addProviderModel(selected.id, addModel.trim());
      notify("模型已添加");
      setAddModel("");
      await load(selected.id);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "添加失败", "error");
    } finally {
      setBusy("");
    }
  };

  const toggleModel = async (modelId: string, enabled: boolean) => {
    if (!selected) return;
    try {
      await api.setProviderModelEnabled(selected.id, modelId, enabled);
      await load(selected.id);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "操作失败", "error");
    }
  };

  const removeModel = async (modelId: string) => {
    if (!selected) return;
    if (!window.confirm("从目录删除模型 " + modelId + "？")) return;
    try {
      await api.deleteProviderModel(selected.id, modelId);
      await load(selected.id);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "删除失败", "error");
    }
  };

  const refreshCatalog = async () => {
    if (!selected) return;
    setBusy("refresh");
    try {
      const res = await api.refreshProviderModels(selected.id);
      notify("目录已刷新，发现 " + res.count + " 个模型（新模型默认停用）");
      await load(selected.id);
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "刷新目录失败", "error");
    } finally {
      setBusy("");
    }
  };

  const toggleProfile = async (profile: ModelProfile) => {
    try {
      await api.setModelProfileEnabled(profile.id, !profile.enabled);
      notify(profile.enabled ? "Profile 已停用" : "Profile 已启用");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "操作失败", "error");
    }
  };

  const makeProfileDefault = async (profile: ModelProfile) => {
    try {
      await api.setModelProfileDefault(profile.id);
      notify("默认模型已切换到 " + profile.name);
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "设置默认 Profile 失败", "error");
    }
  };

  const removeProfile = async (profile: ModelProfile) => {
    if (!window.confirm("删除 Profile " + profile.name + "？已有供应商和模型目录会保留。")) return;
    try {
      await api.deleteModelProfile(profile.id);
      notify("Profile 已删除");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "删除失败", "error");
    }
  };

  const saveDefault = async (next: ModelDefault) => {
    setDefaults(next);
    try {
      await api.putModelDefault(next);
      notify("默认模型已更新");
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "保存默认模型失败", "error");
    }
  };

  const defaultProvider = items.find((p) => p.id === defaults.provider_id) ?? null;

  return (
    <>
      <div className="notice">
        <span>i</span>
        <div>
          <strong>统一管理模型供应商、模型目录与默认推理强度。</strong>
          可以从本机 Codex 配置导入，也可以手动添加；项目绑定后自动生效，未绑定项目使用这里的默认模型。
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">模型 Profile</div>
            <div className="card-sub">{profiles.length} 个可复用配置 · 名称、模型、供应商与推理强度成组保存</div>
          </div>
          <button className="btn" onClick={() => setImportOpen(true)}>从 Codex 导入</button>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {profiles.length === 0 ? (
            <div className="empty" style={{ padding: "18px 16px" }}>
              <div className="tiny">还没有 Profile。导入本机 Codex 配置后，可在这里启停或设为默认模型。</div>
            </div>
          ) : (
            <table className="table">
              <thead><tr><th>Profile</th><th>模型</th><th>供应商</th><th>推理强度</th><th>状态</th><th></th></tr></thead>
              <tbody>
                {profiles.map((profile) => {
                  const isDefault = defaults.provider_id === profile.provider_id && defaults.model === profile.model && defaults.reasoning_effort === profile.reasoning_effort;
                  return (
                    <tr key={profile.id}>
                      <td><div className="strong">{profile.name}</div><div className="tiny">{profile.source === "codex" ? "Codex 导入" : profile.source || "手动"}</div></td>
                      <td className="mono">{profile.model}</td>
                      <td>{profile.provider_name || profile.provider_id}</td>
                      <td className="tiny">{profile.reasoning_effort || "默认"}</td>
                      <td>
                        <span className={"chip " + (profile.enabled ? "ok" : "warn")}><span className="dot" />{profile.enabled ? "启用" : "停用"}</span>
                        {isDefault && <span className="chip ok" style={{ marginLeft: 6 }}>默认</span>}
                      </td>
                      <td><div className="row-actions">
                        {!isDefault && profile.enabled && <button className="btn small" onClick={() => void makeProfileDefault(profile)}>设为默认</button>}
                        <button className="btn small" onClick={() => void toggleProfile(profile)}>{profile.enabled ? "停用" : "启用"}</button>
                        <button className="btn small danger" onClick={() => void removeProfile(profile)}>删除</button>
                      </div></td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">模型供应商</div>
            <div className="card-sub">{items.length} 个供应商 · 密钥密文保存 · 支持目录刷新与真实测试</div>
          </div>
          <div style={{ display: "flex", gap: 8 }}>
            <button className="btn" onClick={() => void load()}>刷新</button>
            <button className="btn" onClick={() => setImportOpen(true)}>导入 Codex 配置</button>
            <button className="btn primary" onClick={() => { setCreateForm(EMPTY_PROVIDER); setCreateModels(""); setCreateOpen(true); }}>＋ 手动添加</button>
          </div>
        </div>
        <div className="card-body">
          <div style={{ display: "flex", alignItems: "center", gap: 10, background: "#f8fafc", border: "1px solid var(--line)", borderRadius: 10, padding: "10px 12px" }}>
            <span style={{ fontSize: 12, fontWeight: 600 }}>默认模型</span>
            <select
              value={defaults.provider_id}
              onChange={(e) => {
                const p = items.find((x) => x.id === e.target.value);
                const model = p?.models.find((m) => m.enabled)?.model_id ?? "";
                void saveDefault({ ...defaults, provider_id: e.target.value, model });
              }}
              style={{ minWidth: 160 }}
            >
              <option value="">未设置</option>
              {items.filter((p) => enabledModels(p).length > 0).map((p) => (
                <option key={p.id} value={p.id}>{p.name}</option>
              ))}
            </select>
            <select
              value={defaults.model}
              disabled={!defaultProvider}
              onChange={(e) => void saveDefault({ ...defaults, model: e.target.value })}
              style={{ minWidth: 200 }}
            >
              <option value="">未设置</option>
              {enabledModels(defaultProvider).map((m) => (
                <option key={m.model_id} value={m.model_id}>{m.model_id}</option>
              ))}
            </select>
            <select
              value={defaults.reasoning_effort}
              onChange={(e) => void saveDefault({ ...defaults, reasoning_effort: e.target.value })}
              style={{ minWidth: 130 }}
            >
              <option value="">默认强度</option>
              <option value="minimal">minimal</option>
              <option value="low">low</option>
              <option value="medium">medium</option>
              <option value="high">high</option>
              <option value="xhigh">xhigh</option>
            </select>
          </div>

          {loading ? (
            <div className="empty">加载中…</div>
          ) : error ? (
            <div className="empty">
              <h3>无法读取模型配置</h3>
              <div className="tiny">{error}</div>
              <button className="btn" style={{ marginTop: 12 }} onClick={() => void load()}>重试</button>
            </div>
          ) : items.length === 0 ? (
            <div className="empty">
              <h3>还没有模型供应商</h3>
              <div className="tiny">可从 ~/.codex/config.toml 导入，也可以手动填写 Base URL、API Key 和模型。</div>
              <div style={{ display: "flex", justifyContent: "center", gap: 8, marginTop: 14 }}>
                <button className="btn primary" onClick={() => setImportOpen(true)}>导入 Codex 配置</button>
                <button className="btn" onClick={() => setCreateOpen(true)}>手动添加</button>
              </div>
            </div>
          ) : (
            <div style={{ display: "grid", gridTemplateColumns: "240px 1fr", gap: 16, marginTop: 14, minHeight: 420 }}>
              <aside style={{ border: "1px solid var(--line)", borderRadius: 10, padding: 10, background: "#fbfcfe" }}>
                <input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="搜索供应商"
                  style={{ width: "100%", padding: "7px 9px", borderRadius: 8, border: "1px solid var(--line)", marginBottom: 10, fontSize: 12 }}
                />
                <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
                  {filtered.map((p) => {
                    const enabled = enabledModels(p).length;
                    return (
                      <button
                        key={p.id}
                        onClick={() => setSelectedId(p.id)}
                        style={{
                          display: "flex", alignItems: "center", gap: 9, textAlign: "left",
                          border: "1px solid " + (p.id === selectedId ? "#bfd6fb" : "transparent"),
                          background: p.id === selectedId ? "#eef4ff" : "transparent",
                          borderRadius: 9, padding: "8px 9px", cursor: "pointer",
                        }}
                      >
                        <span style={{
                          width: 26, height: 26, borderRadius: 7, display: "grid", placeItems: "center",
                          background: "#e7efff", color: "#2563eb", fontWeight: 700, fontSize: 12,
                        }}>{p.name.slice(0, 1).toUpperCase()}</span>
                        <span style={{ flex: 1, minWidth: 0 }}>
                          <span style={{ display: "block", fontSize: 12.5, fontWeight: 600, color: "var(--ink)", overflow: "hidden", textOverflow: "ellipsis" }}>{p.name}</span>
                          <span style={{ display: "block", fontSize: 11, color: "var(--muted)" }}>{enabled}/{p.models.length} 个模型已启用</span>
                        </span>
                        <span style={{ width: 7, height: 7, borderRadius: 99, background: p.enabled && enabled > 0 ? "var(--ok)" : p.enabled ? "#cbd5e1" : "var(--warn)" }} />
                      </button>
                    );
                  })}
                  {filtered.length === 0 && <div className="tiny" style={{ padding: 8 }}>没有匹配的供应商</div>}
                </div>
              </aside>

              <div>
                {selected ? (
                  <>
                    <div style={{ display: "flex", alignItems: "center", gap: 10, marginBottom: 12 }}>
                      <div style={{ flex: 1 }}>
                        <div style={{ fontSize: 15, fontWeight: 650 }}>{selected.name}</div>
                        <div className="tiny">
                          {selected.base_url || "未填 Base URL"} · {selected.wire_api} · {enabledModels(selected).length} 个模型已启用
                        </div>
                      </div>
                      <button className="btn small" disabled={busy === "test"} onClick={() => void test()}>{busy === "test" ? "测试中…" : "测试模型"}</button>
                      <button className="btn small" onClick={() => void toggleProvider()}>{selected.enabled ? "停用供应商" : "启用供应商"}</button>
                      <button className="btn small danger" onClick={() => void removeProvider()}>删除</button>
                    </div>

                    {selected.last_tested_at && (
                      <div className="tiny" style={{ marginBottom: 10 }}>
                        <span className={"chip " + (selected.last_test_ok ? "ok" : "bad")} style={{ marginRight: 6 }}>
                          <span className="dot" />{selected.last_test_ok ? "最近测试通过" : "最近测试失败"}
                        </span>
                        {selected.last_test_message}
                      </div>
                    )}

                    <div style={{ border: "1px solid var(--line)", borderRadius: 10, padding: 14, marginBottom: 12 }}>
                      <div style={{ fontSize: 13, fontWeight: 600, marginBottom: 10 }}>连接</div>
                      <div className="form-grid">
                        <div className="field">
                          <label>名称</label>
                          <input value={form.name} onChange={(e) => setForm((p) => ({ ...p, name: e.target.value }))} />
                        </div>
                        <div className="field">
                          <label>API 格式</label>
                          <select value={form.wire_api} onChange={(e) => setForm((p) => ({ ...p, wire_api: e.target.value }))}>
                            {WIRE_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
                          </select>
                        </div>
                        <div className="field full">
                          <label>Base URL</label>
                          <input value={form.base_url} onChange={(e) => setForm((p) => ({ ...p, base_url: e.target.value }))} placeholder="https://gateway.example.com/v1" />
                        </div>
                        <div className="field full">
                          <label>API Key</label>
                          <input
                            type="password"
                            value={form.api_key}
                            onChange={(e) => setForm((p) => ({ ...p, api_key: e.target.value }))}
                            placeholder={selected.credential_id ? "已保存，留空表示不修改" : "粘贴 API Key"}
                          />
                        </div>
                      </div>
                      <div style={{ display: "flex", justifyContent: "flex-end", marginTop: 10 }}>
                        <button className="btn primary" disabled={saving} onClick={() => void saveProvider()}>{saving ? "保存中…" : "保存连接"}</button>
                      </div>
                    </div>

                    <div style={{ border: "1px solid var(--line)", borderRadius: 10, padding: 14 }}>
                      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 10 }}>
                        <div style={{ fontSize: 13, fontWeight: 600, flex: 1 }}>模型列表</div>
                        <button className="btn small" disabled={busy === "refresh"} onClick={() => void refreshCatalog()}>
                          {busy === "refresh" ? "刷新中…" : "刷新目录"}
                        </button>
                      </div>
                      <div style={{ display: "flex", gap: 8, marginBottom: 10 }}>
                        <input
                          value={addModel}
                          onChange={(e) => setAddModel(e.target.value)}
                          onKeyDown={(e) => { if (e.key === "Enter") void addOneModel(); }}
                          placeholder="输入模型 ID，例如 deepseek-v4-flash"
                          style={{ flex: 1, padding: "7px 9px", borderRadius: 8, border: "1px solid var(--line)", fontSize: 12 }}
                        />
                        <button className="btn small" disabled={busy === "add"} onClick={() => void addOneModel()}>＋ 添加模型</button>
                      </div>
                      {selected.models.length === 0 ? (
                        <div className="tiny" style={{ padding: "12px 4px" }}>还没有模型。点「刷新目录」从网关拉取，或手工添加模型 ID。</div>
                      ) : (
                        <table className="table">
                          <thead>
                            <tr>
                              <th>模型 ID</th>
                              <th>来源</th>
                              <th>启用</th>
                              <th></th>
                            </tr>
                          </thead>
                          <tbody>
                            {selected.models.map((m) => (
                              <tr key={m.model_id}>
                                <td className="mono">{m.model_id}</td>
                                <td><span className="chip">目录</span></td>
                                <td>
                                  <label style={{ display: "inline-flex", alignItems: "center", gap: 6, cursor: "pointer" }}>
                                    <input type="checkbox" checked={m.enabled} onChange={(e) => void toggleModel(m.model_id, e.target.checked)} />
                                    <span className="tiny">{m.enabled ? "已启用" : "未启用"}</span>
                                  </label>
                                </td>
                                <td>
                                  <div className="row-actions">
                                    <button className="btn small danger" onClick={() => void removeModel(m.model_id)}>删除</button>
                                  </div>
                                </td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      )}
                    </div>
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
        notify={notify}
        onClose={() => setImportOpen(false)}
        onImported={() => void load()}
      />

      <div className={"modal-backdrop" + (createOpen ? " open" : "")}>
        <div className="modal">
          <div className="modal-head">
            <div>
              <div className="modal-title">手动添加供应商</div>
              <div className="modal-sub">填写连接信息与 API Key；模型可以在创建后刷新目录或手工添加。</div>
            </div>
            <button className="icon-btn" onClick={() => setCreateOpen(false)}>✕</button>
          </div>
          <div className="modal-body">
            <div className="form-grid">
              <div className="field">
                <label>名称</label>
                <input value={createForm.name} onChange={(e) => setCreateForm((p) => ({ ...p, name: e.target.value }))} placeholder="litellm" />
              </div>
              <div className="field">
                <label>API 格式</label>
                <select value={createForm.wire_api} onChange={(e) => setCreateForm((p) => ({ ...p, wire_api: e.target.value }))}>
                  {WIRE_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
                </select>
              </div>
              <div className="field full">
                <label>Base URL</label>
                <input value={createForm.base_url} onChange={(e) => setCreateForm((p) => ({ ...p, base_url: e.target.value }))} placeholder="https://gateway.example.com/v1" />
              </div>
              <div className="field full">
                <label>API Key</label>
                <input type="password" value={createForm.api_key} onChange={(e) => setCreateForm((p) => ({ ...p, api_key: e.target.value }))} placeholder="sk-..." />
              </div>
              <div className="field full">
                <label>初始模型（可选，逗号分隔）</label>
                <input value={createModels} onChange={(e) => setCreateModels(e.target.value)} placeholder="deepseek-v4-flash, deepseek-v4-pro" />
              </div>
            </div>
          </div>
          <div className="modal-foot">
            <div className="tiny">凭据保存后只显示掩码，测试与刷新都在服务端发起。</div>
            <div>
              <button className="btn" onClick={() => setCreateOpen(false)}>取消</button>{" "}
              <button className="btn primary" disabled={saving} onClick={() => void createProvider()}>{saving ? "创建中…" : "创建"}</button>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}