import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { Credential, ReleaseV2Settings, ReleaseV2UpdateService } from "../types";
import type { Notify } from "../App";

const EMPTY: ReleaseV2Settings = {
  update_services: [],
  update_url: "",
  update_credential: "",
  github_api_base: "https://api.github.com",
  github_credential: "",
  s3_endpoint: "",
  s3_bucket: "",
  s3_region: "",
  s3_prefix: "",
  s3_credential: "",
};

const NEW_SERVICE: ReleaseV2UpdateService = { name: "", url: "", credential: "", enabled: true };

// 凭据保存：同名同类型已存在就轮换，否则新建。
async function saveCredential(
  existing: Credential[],
  kind: string,
  name: string,
  value: string,
  metadata: Record<string, unknown> = {},
) {
  const current = existing.find((c) => c.kind === kind && c.name === name && !c.project_id);
  if (current) {
    await api.rotateCredential(current.id, value);
    return;
  }
  await api.createCredential({ kind, name, value, metadata });
}

export function ReleaseV2SettingsPage({ notify }: { notify: Notify }) {
  const [form, setForm] = useState<ReleaseV2Settings>(EMPTY);
  const [creds, setCreds] = useState<Credential[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [probing, setProbing] = useState("");
  // 每个升级服务待保存的令牌（按列表下标），保存后清空。
  const [serviceSecrets, setServiceSecrets] = useState<Record<number, string>>({});
  const [secrets, setSecrets] = useState({ s3Secret: "", s3Access: "" });

  const load = useCallback(async () => {
    try {
      const [settings, list] = await Promise.all([api.getReleaseV2Settings(), api.listCredentials()]);
      setForm({ ...EMPTY, ...settings, update_services: settings.update_services ?? [] });
      setCreds(list);
      setError("");
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const patch = (key: keyof ReleaseV2Settings, value: string) => setForm((f) => ({ ...f, [key]: value }));

  const patchService = (index: number, patchValue: Partial<ReleaseV2UpdateService>) =>
    setForm((f) => ({
      ...f,
      update_services: f.update_services.map((item, i) => (i === index ? { ...item, ...patchValue } : item)),
    }));

  const addService = () => setForm((f) => ({ ...f, update_services: [...f.update_services, { ...NEW_SERVICE }] }));
  const removeService = (index: number) => {
    setForm((f) => ({ ...f, update_services: f.update_services.filter((_, i) => i !== index) }));
    setServiceSecrets({});
  };

  const persist = async (label: string, next: ReleaseV2Settings, cred?: { kind: string; name: string; value: string; metadata?: Record<string, unknown> }): Promise<boolean> => {
    setBusy(true);
    setError("");
    try {
      if (cred && cred.name.trim() !== "" && cred.value.trim() !== "") {
        await saveCredential(creds, cred.kind, cred.name.trim(), cred.value.trim(), cred.metadata);
      }
      await api.saveReleaseV2Settings(next);
      notify(label + " 已保存", "ok");
      setSecrets({ s3Secret: "", s3Access: "" });
      setServiceSecrets({});
      await load();
      return true;
    } catch (e) {
      const message = e instanceof ApiError ? e.message : String(e);
      setError(message);
      notify(label + " 保存失败：" + message, "error");
      return false;
    } finally {
      setBusy(false);
    }
  };

  const hasCredential = (kind: string, name: string) =>
    creds.some((c) => c.kind === kind && c.name === name.trim() && !c.project_id);

  const saveUpdate = async (): Promise<boolean> => {
    const services = form.update_services.filter((s) => s.url.trim() !== "");
    for (const [index, service] of form.update_services.entries()) {
      if (service.url.trim() === "") continue;
      const name = service.credential.trim();
      const secret = serviceSecrets[index]?.trim() ?? "";
      if (!name) {
        notify(`升级服务 ${service.name || index + 1} 需要填写凭据名`, "error");
        return false;
      }
      if (!secret && !hasCredential("release_update", name)) {
        notify(`凭据 ${name} 还没有保存过管理令牌，请在“管理令牌”里填写后再保存`, "error");
        return false;
      }
      if (secret) {
        await saveCredential(creds, "release_update", name, secret);
      }
    }
    return persist("升级服务", { ...form, update_services: services });
  };

  // 测试使用服务端已保存的配置，所以先保存当前表单再探测。
  const testUpdate = async (name = "") => {
    if (!(await saveUpdate())) return;
    await probe("update", name);
  };

  const saveS3 = () =>
    persist("对象存储", form, {
      kind: "release_s3",
      name: form.s3_credential,
      value: secrets.s3Secret,
      metadata: { access_key: secrets.s3Access, endpoint: form.s3_endpoint, bucket: form.s3_bucket },
    });

  const probe = async (service: "update" | "github", target = "") => {
    const key = service + ":" + target;
    setProbing(key);
    setError("");
    try {
      const result = await api.probeReleaseV2Settings(service, target);
      const detail = result.targets?.length
        ? result.targets.map((t) => `${t.name}：${t.detail}`).join("；")
        : result.detail;
      notify(detail, result.ok ? "ok" : "error");
    } catch (e) {
      const message = e instanceof ApiError ? e.message : String(e);
      setError(message);
      notify("测试连接失败：" + message, "error");
    } finally {
      setProbing("");
    }
  };

  const credLine = (kind: string) => {
    const items = creds.filter((c) => c.kind === kind && !c.project_id);
    if (items.length === 0) return "还没有保存过凭据";
    return "已保存：" + items.map((c) => `${c.name}（${c.mask}）`).join("、");
  };

  return (
    <div className="page-body settings-page">
      {error && <div className="banner error">{error}</div>}

      <section className="card">
        <header className="card-head">
          <div>
            <h3>升级服务（syntropy_update）</h3>
            <p className="muted">可以配置多台：stage 会把同一份制品灌到每一台，promote 会逐台推进渠道并回读校验。管理令牌只在首次配置或轮换时填写；测试会先保存当前表单，再用令牌校验。</p>
          </div>
          <button className="btn" onClick={addService} disabled={busy}>＋ 添加升级服务</button>
        </header>

        {form.update_services.length === 0 && (
          <div className="empty">还没有配置升级服务。点右上角“添加升级服务”，填地址与管理令牌。</div>
        )}

        <div className="svc-list">
          {form.update_services.map((service, index) => (
            <div className="svc-row" key={index}>
              <label>
                名称
                <input placeholder="cdn1" value={service.name} onChange={(e) => patchService(index, { name: e.target.value })} />
              </label>
              <label className="svc-url">
                服务地址
                <input placeholder="https://update.example.com:8443" value={service.url} onChange={(e) => patchService(index, { url: e.target.value })} />
              </label>
              <label>
                凭据名
                <input placeholder="update-cdn1" value={service.credential} onChange={(e) => patchService(index, { credential: e.target.value })} />
              </label>
              <label>
                管理令牌
                <input
                  type="password"
                  placeholder={hasCredential("release_update", service.credential) ? "留空表示不修改" : "首次配置必须填写"}
                  value={serviceSecrets[index] ?? ""}
                  onChange={(e) => setServiceSecrets((s) => ({ ...s, [index]: e.target.value }))}
                />
              </label>
              <div className="svc-actions">
                <label className="svc-enabled">
                  <input type="checkbox" checked={service.enabled} onChange={(e) => patchService(index, { enabled: e.target.checked })} />
                  启用
                </label>
                <button className="btn" disabled={busy || probing !== ""} onClick={() => void testUpdate(service.name)}>
                  {probing === "update:" + service.name ? "测试中…" : "测试"}
                </button>
                <button className="btn danger-ghost" disabled={busy} onClick={() => removeService(index)}>移除</button>
              </div>
            </div>
          ))}
        </div>

        <p className="muted small">{credLine("release_update")}</p>
        <div className="actions">
          <button className="btn primary" disabled={busy} onClick={() => void saveUpdate()}>保存升级服务</button>
          <button className="btn" disabled={busy || probing !== "" || form.update_services.length === 0} onClick={() => void testUpdate("")}>
            {probing === "update:" ? "测试中…" : "全部测试"}
          </button>
        </div>
      </section>

      <section className="card">
        <header className="card-head">
          <div>
            <h3>GitHub 发布</h3>
            <p className="muted">令牌直接复用「GitHub 配置」页保存的账号 PAT，仓库 owner/repo 取自项目绑定的 GitHub 地址；只有自建 GitHub Enterprise 时才需要改这里的 API 地址。</p>
          </div>
        </header>
        <div className="grid two">
          <label className="span-2">API 地址<input placeholder="https://api.github.com" value={form.github_api_base} onChange={(e) => patch("github_api_base", e.target.value)} /></label>
        </div>
        <p className="muted small">{credLine("github_pat")}</p>
        <div className="actions">
          <button className="btn primary" disabled={busy} onClick={() => void persist("GitHub", form)}>保存 GitHub 设置</button>
          <button className="btn" disabled={busy || probing !== ""} onClick={() => void probe("github")}>{probing === "github:" ? "测试中…" : "测试连接"}</button>
        </div>
      </section>

      <section className="card">
        <header className="card-head">
          <div>
            <h3>制品对象存储（S3 / MinIO）</h3>
            <p className="muted">留空则用服务端默认存储；构建机上传与所有升级服务的制品都从这里读取。</p>
          </div>
        </header>
        <div className="grid two">
          <label>Endpoint<input placeholder="http://127.0.0.1:9000" value={form.s3_endpoint} onChange={(e) => patch("s3_endpoint", e.target.value)} /></label>
          <label>Bucket<input placeholder="smartpipe-releases" value={form.s3_bucket} onChange={(e) => patch("s3_bucket", e.target.value)} /></label>
          <label>Region<input placeholder="us-east-1" value={form.s3_region} onChange={(e) => patch("s3_region", e.target.value)} /></label>
          <label>前缀<input placeholder="releases" value={form.s3_prefix} onChange={(e) => patch("s3_prefix", e.target.value)} /></label>
          <label>凭据名<input placeholder="s3-prod" value={form.s3_credential} onChange={(e) => patch("s3_credential", e.target.value)} /></label>
          <label>Access Key<input value={secrets.s3Access} onChange={(e) => setSecrets((s) => ({ ...s, s3Access: e.target.value }))} /></label>
          <label>Secret Key<input type="password" placeholder="留空表示不修改" value={secrets.s3Secret} onChange={(e) => setSecrets((s) => ({ ...s, s3Secret: e.target.value }))} /></label>
        </div>
        <p className="muted small">{credLine("release_s3")}</p>
        <div className="actions">
          <button className="btn primary" disabled={busy} onClick={() => void saveS3()}>保存对象存储</button>
        </div>
      </section>

      <p className="muted small">保存后 MCP 与 release-service 会在下一次请求读取新配置，不需要重启。</p>
    </div>
  );
}