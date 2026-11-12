import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError } from "../api";
import type { RuntimeResource } from "../types";
import type { Notify } from "../App";

type ResourceStatus = "all" | "online" | "offline" | "untested";

type ResourceForm = {
  name: string;
  description: string;
  host: string;
  port: string;
  username: string;
  auth_type: "password" | "private_key";
  secret: string;
  passphrase: string;
  host_key_fingerprint: string;
  tags: string;
  enabled: boolean;
};

const EMPTY_FORM: ResourceForm = {
  name: "",
  description: "",
  host: "",
  port: "22",
  username: "",
  auth_type: "password",
  secret: "",
  passphrase: "",
  host_key_fingerprint: "",
  tags: "build",
  enabled: true,
};

const formatTime = (value: string | null) => {
  if (!value) return "从未检测";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString("zh-CN", { hour12: false });
};

const formatBytes = (value?: number) => {
  if (!value || value <= 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return (unit === 0 ? String(Math.round(size)) : size.toFixed(size >= 10 ? 1 : 2)) + " " + units[unit];
};

const formatUptime = (value?: number) => {
  if (!value || value <= 0) return "—";
  const days = Math.floor(value / 86400);
  const hours = Math.floor((value % 86400) / 3600);
  if (days > 0) return `${days} 天 ${hours} 小时`;
  return `${hours} 小时 ${Math.floor((value % 3600) / 60)} 分`;
};

const statusOf = (resource: RuntimeResource): ResourceStatus => {
  if (resource.last_test_ok === true) return "online";
  if (resource.last_test_ok === false) return "offline";
  return "untested";
};

const statusMeta = (resource: RuntimeResource) => {
  const status = statusOf(resource);
  if (status === "online") return { label: "连接正常", tone: "ok" };
  if (status === "offline") return { label: "连接失败", tone: "bad" };
  return { label: "尚未检测", tone: "warn" };
};

const splitTags = (value: string) =>
  Array.from(new Set(value.split(/[,，]/).map((tag) => tag.trim()).filter(Boolean)));

const sshCommand = (resource: RuntimeResource) =>
  `ssh ${resource.username}@${resource.host} -p ${resource.port}`;

export function ResourcesPage({ notify }: { notify: Notify }) {
  const [resources, setResources] = useState<RuntimeResource[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState<ResourceStatus>("all");
  const [selectedId, setSelectedId] = useState("");
  const [busy, setBusy] = useState("");
  const [formResource, setFormResource] = useState<RuntimeResource | null | undefined>(undefined);
  const [form, setForm] = useState<ResourceForm>(EMPTY_FORM);
  const [formBusy, setFormBusy] = useState<"save" | "test" | "">("");
  const [formError, setFormError] = useState("");
  const detailRef = useRef<HTMLDivElement | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const items = await api.listResources();
      setResources(items ?? []);
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载资源列表失败");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!selectedId) return;
    const timer = window.setTimeout(() => detailRef.current?.scrollIntoView({ behavior: "smooth", block: "start" }), 60);
    return () => window.clearTimeout(timer);
  }, [selectedId]);

  const selected = resources.find((resource) => resource.id === selectedId) ?? null;

  const replaceResource = (updated: RuntimeResource) => {
    setResources((current) => current.map((resource) => (resource.id === updated.id ? updated : resource)));
  };

  const filtered = useMemo(() => {
    const keyword = query.trim().toLowerCase();
    return resources.filter((resource) => {
      if (statusFilter !== "all" && statusOf(resource) !== statusFilter) return false;
      if (!keyword) return true;
      return [resource.name, resource.host, resource.username, resource.description, resource.system_info?.os, ...(resource.tags ?? [])]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(keyword));
    });
  }, [query, resources, statusFilter]);

  const onlineCount = resources.filter((resource) => resource.last_test_ok === true).length;
  const failedCount = resources.filter((resource) => resource.last_test_ok === false).length;
  const inspectedCount = resources.filter((resource) => Boolean(resource.inspected_at)).length;
  const platformCount = new Set(resources.map((resource) => resource.system_info?.os || resource.system_info?.architecture).filter(Boolean)).size;
  const tags = Array.from(new Set(resources.flatMap((resource) => resource.tags ?? []))).sort();

  const openCreate = () => {
    setFormResource(null);
    setForm({ ...EMPTY_FORM, tags: "build" });
    setFormError("");
  };

  const openEdit = (resource: RuntimeResource) => {
    setFormResource(resource);
    setForm({
      name: resource.name,
      description: resource.description,
      host: resource.host,
      port: String(resource.port),
      username: resource.username,
      auth_type: resource.auth_type,
      secret: "",
      passphrase: "",
      host_key_fingerprint: resource.host_key_fingerprint,
      tags: (resource.tags ?? []).join(", "),
      enabled: resource.enabled,
    });
    setFormError("");
  };

  const closeForm = () => {
    if (formBusy) return;
    setFormResource(undefined);
    setFormError("");
  };

  const submit = async (testAfterSave: boolean) => {
    if (!form.name.trim() || !form.host.trim() || !form.username.trim()) {
      setFormError("资源名称、主机和用户名不能为空");
      return;
    }
    if (!formResource && !form.secret.trim()) {
      setFormError(form.auth_type === "password" ? "请填写 SSH 密码" : "请粘贴 SSH 私钥");
      return;
    }
    setFormBusy(testAfterSave ? "test" : "save");
    setFormError("");
    try {
      const payload = {
        name: form.name.trim(),
        description: form.description.trim(),
        host: form.host.trim(),
        port: Number(form.port) || 22,
        username: form.username.trim(),
        auth_type: form.auth_type,
        secret: form.secret || undefined,
        passphrase: form.passphrase || undefined,
        host_key_fingerprint: form.host_key_fingerprint.trim(),
        tags: splitTags(form.tags),
        enabled: form.enabled,
      };
      let saved = formResource
        ? await api.updateResource(formResource.id, payload)
        : await api.createResource({ ...payload, secret: form.secret });
      if (testAfterSave) {
        const result = await api.testResource(saved.id);
        saved = result.resource;
        notify(result.ok ? "资源已保存，SSH 连接正常" : result.message, result.ok ? "ok" : "error");
      } else {
        notify(formResource ? "资源信息已保存" : "资源已添加", "ok");
      }
      replaceResource(saved);
      setFormResource(undefined);
      setSelectedId(saved.id);
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : "保存资源失败");
    } finally {
      setFormBusy("");
    }
  };

  const testResource = async (resource: RuntimeResource) => {
    setBusy("test:" + resource.id);
    try {
      const result = await api.testResource(resource.id);
      replaceResource(result.resource);
      notify(result.message, result.ok ? "ok" : "error");
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "测试连接失败", "error");
    } finally {
      setBusy("");
    }
  };

  const inspectResource = async (resource: RuntimeResource) => {
    setBusy("inspect:" + resource.id);
    setSelectedId(resource.id);
    try {
      const result = await api.inspectResource(resource.id);
      replaceResource(result.resource);
      notify(result.message, result.ok ? "ok" : "error");
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "采集系统信息失败", "error");
    } finally {
      setBusy("");
    }
  };

  const toggleResource = async (resource: RuntimeResource) => {
    setBusy("enabled:" + resource.id);
    try {
      const updated = await api.setResourceEnabled(resource.id, !resource.enabled);
      replaceResource(updated);
      notify(updated.enabled ? "资源已启用，可供其他模块选择" : "资源已停用");
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "更新资源状态失败", "error");
    } finally {
      setBusy("");
    }
  };

  const deleteResource = async (resource: RuntimeResource) => {
    if (!window.confirm(`删除资源“${resource.name}”？保存的 SSH 凭据也会一并删除。`)) return;
    setBusy("delete:" + resource.id);
    try {
      await api.deleteResource(resource.id);
      setResources((current) => current.filter((item) => item.id !== resource.id));
      if (selectedId === resource.id) setSelectedId("");
      notify("资源已删除");
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "删除资源失败", "error");
    } finally {
      setBusy("");
    }
  };

  const copyText = async (value: string, label: string) => {
    try {
      await navigator.clipboard.writeText(value);
      notify(`${label}已复制`);
    } catch {
      notify("浏览器不允许写入剪贴板", "error");
    }
  };

  return (
    <>
      <div className="stat-grid resource-stats">
        <div className="stat">
          <div className="stat-label">已登记资源</div>
          <div className="stat-value">{resources.length}</div>
          <div className="stat-foot">{resources.filter((resource) => resource.enabled).length} 个已启用</div>
        </div>
        <div className="stat">
          <div className="stat-label">连接正常</div>
          <div className="stat-value resource-ok-text">{onlineCount}</div>
          <div className="stat-foot">{failedCount ? `${failedCount} 个需要处理` : "当前没有失败连接"}</div>
        </div>
        <div className="stat">
          <div className="stat-label">系统画像</div>
          <div className="stat-value">{inspectedCount}</div>
          <div className="stat-foot">{platformCount} 种系统 / 架构组合</div>
        </div>
        <div className="stat">
          <div className="stat-label">用途标签</div>
          <div className="stat-value">{tags.length}</div>
          <div className="stat-foot">发版模块可按 build 标签选择</div>
        </div>
      </div>

      <div className="resource-toolbar card">
        <div className="resource-toolbar-main">
          <input
            className="resource-search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="搜索名称、主机、系统或标签"
          />
          <div className="resource-segment" role="group" aria-label="连接状态筛选">
            {(["all", "online", "offline", "untested"] as ResourceStatus[]).map((value) => (
              <button
                key={value}
                className={statusFilter === value ? "active" : ""}
                onClick={() => setStatusFilter(value)}
              >
                {{ all: "全部", online: "正常", offline: "失败", untested: "未检测" }[value]}
              </button>
            ))}
          </div>
        </div>
        <div className="resource-toolbar-actions">
          <button className="btn" onClick={() => void load()} disabled={loading}>刷新</button>
          <button className="btn primary" onClick={openCreate}>＋ 添加资源</button>
        </div>
      </div>

      {error && <div className="notice warn">{error}</div>}

      <div className="card resource-list-card">
        <div className="card-head">
          <div>
            <div className="card-title">SSH 资源目录</div>
            <div className="card-sub">密码与私钥由服务端 AES-256-GCM 加密保存；资源 ID 可被发版、验证和调度模块复用。</div>
          </div>
          <span className="chip">{filtered.length} / {resources.length}</span>
        </div>
        {loading && resources.length === 0 ? (
          <div className="empty">正在加载资源…</div>
        ) : filtered.length === 0 ? (
          <div className="empty">
            <div className="resource-empty-icon">⌁</div>
            <h3>{resources.length === 0 ? "还没有在线资源" : "没有符合条件的资源"}</h3>
            <p>{resources.length === 0 ? "添加第一台可 SSH 登录的构建机、测试机或硬件平台。" : "调整搜索词或连接状态筛选。"}</p>
            {resources.length === 0 && <button className="btn primary" onClick={openCreate}>添加第一台资源</button>}
          </div>
        ) : (
          <div className="resource-table-wrap">
            <table className="table resource-table">
              <thead>
                <tr>
                  <th>资源</th>
                  <th>SSH 地址</th>
                  <th>系统</th>
                  <th>连接状态</th>
                  <th>最近检测</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((resource) => {
                  const status = statusMeta(resource);
                  const info = resource.system_info ?? {};
                  return (
                    <tr key={resource.id} data-disabled={!resource.enabled}>
                      <td>
                        <button className="resource-name-button" onClick={() => setSelectedId(resource.id)}>
                          <span className="resource-avatar">{resource.name.slice(0, 1).toUpperCase()}</span>
                          <span className="resource-name-copy">
                            <span className="strong">{resource.name}</span>
                            <small>{resource.description || "未填写用途说明"}</small>
                            <span className="resource-tags">
                              {(resource.tags ?? []).slice(0, 4).map((tag) => <em key={tag}>{tag}</em>)}
                              {!resource.enabled && <em className="disabled">已停用</em>}
                            </span>
                          </span>
                        </button>
                      </td>
                      <td>
                        <div className="mono resource-address">{resource.username}@{resource.host}:{resource.port}</div>
                        <div className="tiny">{resource.auth_type === "private_key" ? "私钥认证" : "密码认证"} · {resource.secret_mask || "凭据已保存"}</div>
                      </td>
                      <td>
                        {info.os || info.architecture ? (
                          <>
                            <div className="resource-system-name">{info.os || info.architecture}</div>
                            <div className="tiny">{info.architecture || "架构未知"}{info.cpu_count ? ` · ${info.cpu_count} vCPU` : ""}{info.memory_total_bytes ? ` · ${formatBytes(info.memory_total_bytes)}` : ""}</div>
                          </>
                        ) : <span className="muted">采集后显示</span>}
                      </td>
                      <td>
                        <span className={"chip " + status.tone}><span className="dot" />{status.label}</span>
                        <div className="resource-message" title={resource.last_test_message}>{resource.last_test_message || "尚未测试连接"}</div>
                      </td>
                      <td className="resource-time-cell">{resource.last_tested_at ? formatTime(resource.last_tested_at) : <span className="muted">—</span>}</td>
                      <td>
                        <div className="resource-row-actions">
                          <button className="btn small" disabled={Boolean(busy)} onClick={() => void testResource(resource)}>{busy === "test:" + resource.id ? "测试中…" : "测连"}</button>
                          <button className="btn small" disabled={Boolean(busy)} onClick={() => void inspectResource(resource)}>{busy === "inspect:" + resource.id ? "采集中…" : "系统信息"}</button>
                          <button className="btn small ghost" onClick={() => openEdit(resource)}>编辑</button>
                          <button className="btn small ghost" disabled={Boolean(busy)} onClick={() => void deleteResource(resource)}>删除</button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {selected && (
        <div className="resource-detail" ref={detailRef}>
          <div className="resource-detail-head">
            <div className="resource-detail-title-wrap">
              <span className="resource-avatar large">{selected.name.slice(0, 1).toUpperCase()}</span>
              <div>
                <div className="resource-detail-kicker">ONLINE RESOURCE · SSH</div>
                <h2>{selected.name}</h2>
                <p>{selected.description || "未填写用途说明"}</p>
              </div>
            </div>
            <div className="resource-detail-head-actions">
              <button className="btn small" disabled={Boolean(busy)} onClick={() => void testResource(selected)}>测试连接</button>
              <button className="btn small primary" disabled={Boolean(busy)} onClick={() => void inspectResource(selected)}>{busy === "inspect:" + selected.id ? "采集中…" : "刷新系统信息"}</button>
              <button className="btn small" onClick={() => openEdit(selected)}>编辑</button>
              <button className="icon-btn" onClick={() => setSelectedId("")}>✕</button>
            </div>
          </div>

          {(selected.last_test_message || !selected.enabled) && (
            <div className={"resource-detail-note " + (selected.last_test_ok === false ? "bad" : "")}>
              {!selected.enabled ? "该资源已停用，不会出现在其他模块的可用资源目录中。" : selected.last_test_message}
            </div>
          )}

          <div className="resource-detail-meta">
            <div><span>SSH 地址</span><strong className="mono">{sshCommand(selected)}</strong><button className="link-btn" onClick={() => void copyText(sshCommand(selected), "SSH 命令")}>复制</button></div>
            <div><span>资源 ID</span><strong className="mono">{selected.id}</strong><button className="link-btn" onClick={() => void copyText(selected.id, "资源 ID")}>复制</button></div>
            <div><span>鉴权</span><strong>{selected.auth_type === "private_key" ? "SSH 私钥" : "密码"}{selected.passphrase_set ? " + 私钥口令" : ""}</strong><small>{selected.secret_mask}</small></div>
            <div><span>最近检测</span><strong>{formatTime(selected.last_tested_at)}</strong><small>{selected.last_test_ok === true ? "连接正常" : selected.last_test_ok === false ? "连接失败" : "等待检测"}</small></div>
          </div>

          <div className="resource-profile-grid">
            <section className="resource-profile-card">
              <div className="resource-section-title"><span>主机与系统</span><small>{selected.inspected_at ? "采集于 " + formatTime(selected.inspected_at) : "尚未采集"}</small></div>
              <div className="resource-kv-list">
                <div><span>主机名</span><b>{selected.system_info?.hostname || "—"}</b></div>
                <div><span>操作系统</span><b>{selected.system_info?.os || "—"}</b></div>
                <div><span>内核 / 架构</span><b>{[selected.system_info?.kernel, selected.system_info?.architecture].filter(Boolean).join(" / ") || "—"}</b></div>
                <div><span>CPU</span><b>{selected.system_info?.cpu_model || "—"}</b><small>{selected.system_info?.cpu_count ? selected.system_info.cpu_count + " 逻辑核心" : ""}</small></div>
                <div><span>运行时间</span><b>{formatUptime(selected.system_info?.uptime_seconds)}</b></div>
                <div><span>登录用户 / HOME</span><b>{[selected.system_info?.login_user, selected.system_info?.home].filter(Boolean).join(" · ") || "—"}</b></div>
              </div>
            </section>

            <section className="resource-profile-card">
              <div className="resource-section-title"><span>容量与加速设备</span><small>只读采集</small></div>
              <div className="resource-capacity-row">
                <div><span>内存</span><strong>{formatBytes(selected.system_info?.memory_total_bytes)}</strong></div>
                <div><span>根分区</span><strong>{formatBytes(selected.system_info?.disk_total_bytes)}</strong><small>{selected.system_info?.disk_available_bytes ? formatBytes(selected.system_info.disk_available_bytes) + " 可用" : ""}</small></div>
              </div>
              <div className="resource-gpu-list">
                {selected.system_info?.gpus?.length ? selected.system_info.gpus.map((gpu, index) => (
                  <div key={`${gpu.name}-${index}`} className="resource-gpu"><span>GPU {index + 1}</span><b>{gpu.name}</b><small>{[gpu.memory_mb ? gpu.memory_mb + " MB" : "", gpu.driver_version ? "驱动 " + gpu.driver_version : ""].filter(Boolean).join(" · ")}</small></div>
                )) : <div className="resource-profile-empty">未发现 NVIDIA GPU，或目标机未安装 nvidia-smi。</div>}
              </div>
            </section>

            <section className="resource-profile-card">
              <div className="resource-section-title"><span>工具链</span><small>资源能力画像</small></div>
              <div className="resource-tool-list">
                {Object.entries(selected.system_info?.tools ?? {}).map(([name, version]) => (
                  <div key={name}><span>{name}</span><code>{version}</code></div>
                ))}
                {Object.keys(selected.system_info?.tools ?? {}).length === 0 && <div className="resource-profile-empty">尚未采集到 Docker、Git、Go 或 Node 信息。</div>}
              </div>
            </section>

            <section className="resource-profile-card">
              <div className="resource-section-title"><span>连接安全</span><small>主机指纹固定</small></div>
              <div className="resource-fingerprint">
                <span>SHA256 指纹</span>
                <code>{selected.host_key_fingerprint || "首次测连成功后自动固定"}</code>
                {selected.host_key_fingerprint && <button className="link-btn" onClick={() => void copyText(selected.host_key_fingerprint, "主机指纹")}>复制</button>}
              </div>
              <div className="resource-enabled-row">
                <div><b>供其他模块使用</b><small>关闭后保留配置，但不出现在可用资源目录。</small></div>
                <button className={"btn small " + (selected.enabled ? "" : "primary")} disabled={Boolean(busy)} onClick={() => void toggleResource(selected)}>{selected.enabled ? "停用资源" : "启用资源"}</button>
              </div>
              <div className="resource-consumer-hint">可用资源目录：<code>GET /admin/resources/available?tag=build</code></div>
            </section>
          </div>
        </div>
      )}

      <div className={"modal-backdrop" + (formResource !== undefined ? " open" : "")}>
        <div className="modal resource-form-modal" role="dialog" aria-modal="true">
          <div className="modal-head">
            <div>
              <div className="modal-title">{formResource ? "编辑 SSH 资源" : "添加 SSH 资源"}</div>
              <div className="modal-sub">适用于构建机、测试机、GPU 节点及其他可远程登录的硬件平台。</div>
            </div>
            <button className="icon-btn" onClick={closeForm} disabled={Boolean(formBusy)}>✕</button>
          </div>
          <div className="modal-body resource-form-body">
            {formError && <div className="resource-form-error">{formError}</div>}
            <div className="form-grid">
              <div className="field">
                <label>资源名称 *</label>
                <input value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} placeholder="例如：Linux 构建机 01" />
              </div>
              <div className="field">
                <label>用途标签</label>
                <input value={form.tags} onChange={(event) => setForm({ ...form, tags: event.target.value })} placeholder="build, gpu, release" />
              </div>
              <div className="field full">
                <label>用途说明</label>
                <input value={form.description} onChange={(event) => setForm({ ...form, description: event.target.value })} placeholder="例如：北京机房，用于 Electron Linux 构建" />
              </div>
              <div className="field resource-host-field">
                <label>主机名或 IP *</label>
                <input value={form.host} onChange={(event) => setForm({ ...form, host: event.target.value })} placeholder="10.0.1.20 或 build-01.example.com" />
              </div>
              <div className="field resource-port-field">
                <label>SSH 端口</label>
                <input inputMode="numeric" value={form.port} onChange={(event) => setForm({ ...form, port: event.target.value.replace(/[^0-9]/g, "") })} placeholder="22" />
              </div>
              <div className="field">
                <label>用户名 *</label>
                <input value={form.username} onChange={(event) => setForm({ ...form, username: event.target.value })} placeholder="root 或 builder" autoComplete="username" />
              </div>
              <div className="field">
                <label>认证方式</label>
                <select value={form.auth_type} onChange={(event) => setForm({ ...form, auth_type: event.target.value as ResourceForm["auth_type"], secret: "", passphrase: "" })}>
                  <option value="password">密码</option>
                  <option value="private_key">SSH 私钥</option>
                </select>
              </div>
              <div className="field full">
                <label>{form.auth_type === "password" ? "SSH 密码" : "SSH 私钥"}{formResource ? "（留空保持不变）" : " *"}</label>
                {form.auth_type === "password" ? (
                  <input type="password" value={form.secret} onChange={(event) => setForm({ ...form, secret: event.target.value })} placeholder={formResource ? "留空保留已保存密码" : "输入登录密码"} autoComplete="new-password" />
                ) : (
                  <textarea className="resource-key-input" value={form.secret} onChange={(event) => setForm({ ...form, secret: event.target.value })} placeholder={formResource ? "留空保留已保存私钥" : "-----BEGIN OPENSSH PRIVATE KEY-----"} spellCheck={false} />
                )}
              </div>
              {form.auth_type === "private_key" && (
                <div className="field full">
                  <label>私钥口令（可选，留空保持不变）</label>
                  <input type="password" value={form.passphrase} onChange={(event) => setForm({ ...form, passphrase: event.target.value })} placeholder="私钥未加密时留空" autoComplete="new-password" />
                </div>
              )}
              <div className="field full">
                <label>固定主机指纹（可选）</label>
                <div className="resource-inline-input">
                  <input value={form.host_key_fingerprint} onChange={(event) => setForm({ ...form, host_key_fingerprint: event.target.value })} placeholder="SHA256:...；首次测连后会由服务端自动固定" className="mono" />
                  {form.host_key_fingerprint && <button type="button" className="btn small" onClick={() => setForm({ ...form, host_key_fingerprint: "" })}>清除</button>}
                </div>
              </div>
              <label className="resource-checkbox full">
                <input type="checkbox" checked={form.enabled} onChange={(event) => setForm({ ...form, enabled: event.target.checked })} />
                启用此资源，允许发版、验证和调度模块按标签选择
              </label>
            </div>
            <div className="notice resource-form-note">
              凭据只在服务端加密保存，页面不会回显明文。首次测连采用 TOFU 记录主机指纹，后续连接会固定校验该指纹。
            </div>
          </div>
          <div className="modal-foot">
            <div className="tiny">资源 ID 在保存后生成，可供其他模块稳定引用。</div>
            <div className="resource-form-actions">
              <button className="btn" onClick={closeForm} disabled={Boolean(formBusy)}>取消</button>
              <button className="btn" onClick={() => void submit(false)} disabled={Boolean(formBusy)}>{formBusy === "save" ? "保存中…" : "保存"}</button>
              <button className="btn primary" onClick={() => void submit(true)} disabled={Boolean(formBusy)}>{formBusy === "test" ? "保存并测试中…" : "保存并测试"}</button>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}
