import { useEffect, useState } from "react";
import { api, ApiError } from "../api";
import type { Notify } from "../App";
import type { Project, ReleaseV2ProjectReleaseConfig } from "../types";

type BuildRecipe = ReleaseV2ProjectReleaseConfig["builds"][string];
type ArtifactDraft = NonNullable<BuildRecipe["artifacts"]>[number];
type BuildDraft = { key: string; id: string; recipe: BuildRecipe };

let draftSeq = 0;

function draftKey(id: string) {
  draftSeq += 1;
  return id + "-" + draftSeq;
}

function cloneRecipe(recipe?: BuildRecipe): BuildRecipe {
  return {
    build: recipe?.build ?? "",
    sign: recipe?.sign ?? "",
    verify: recipe?.verify ?? "",
    timeout: recipe?.timeout ?? "60m",
    manifest_path: recipe?.manifest_path ?? "",
    host_os: recipe?.host_os ?? "",
    host_arch: recipe?.host_arch ?? "",
    artifacts: (recipe?.artifacts ?? []).map((artifact) => ({
      ...artifact,
      role: artifact.role ?? "",
      kind: artifact.kind ?? "",
      glob: artifact.glob ?? "",
      content_type: artifact.content_type ?? "",
      required: artifact.required ?? true,
    })),
  };
}

function splitList(value: string) {
  return value.split(",").map((item) => item.trim()).filter(Boolean);
}

function parseObject<T extends object>(value: string, label: string, fallback: T): T {
  const trimmed = value.trim();
  if (!trimmed) return fallback;
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch {
    throw new Error(label + " 不是合法 JSON");
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error(label + " 必须是 JSON 对象");
  }
  return parsed as T;
}

function errorMessage(error: unknown, fallback: string) {
  return error instanceof ApiError ? error.message : error instanceof Error ? error.message : fallback;
}

export function ReleaseV2ConfigModal({
  project,
  onClose,
  onSaved,
  notify,
}: {
  project: Project | null;
  onClose: () => void;
  onSaved: () => void;
  notify: Notify;
}) {
  const projectId = project?.id ?? "";
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [drafts, setDrafts] = useState<BuildDraft[]>([]);
  const [manifestPath, setManifestPath] = useState("");
  const [versionFile, setVersionFile] = useState("");
  const [versionJSONPath, setVersionJSONPath] = useState("version");
  const [buildVersionTemplate, setBuildVersionTemplate] = useState("{version}");
  const [notesPath, setNotesPath] = useState("");
  const [channelText, setChannelText] = useState("beta, stable");
  const [serverText, setServerText] = useState("");
  const [publishingText, setPublishingText] = useState("{}");
  const [policiesText, setPoliciesText] = useState("{}");

  useEffect(() => {
    if (!projectId) return;
    let cancelled = false;
    setLoading(true);
    setError("");
    api.getReleaseV2ProjectConfig(projectId)
      .then((cfg) => {
        if (cancelled) return;
        setDrafts(Object.entries(cfg.builds ?? {}).map(([id, recipe]) => ({
          key: draftKey(id),
          id,
          recipe: cloneRecipe(recipe),
        })));
        setManifestPath(cfg.manifest_path ?? "");
        setVersionFile(cfg.version_file ?? "");
        setVersionJSONPath(cfg.version_json_path || "version");
        setBuildVersionTemplate(cfg.build_version_template || "{version}");
        setNotesPath(cfg.notes_path ?? "");
        setChannelText((cfg.channel_order ?? ["beta", "stable"]).join(", "));
        setServerText((cfg.update_server_ids ?? []).join(", "));
        setPublishingText(JSON.stringify(cfg.publishing ?? {}, null, 2));
        setPoliciesText(JSON.stringify(cfg.policies ?? {}, null, 2));
      })
      .catch((err) => {
        if (!cancelled) setError(errorMessage(err, "读取发版配置失败"));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => { cancelled = true; };
  }, [projectId]);

  function updateRecipe(key: string, patch: Partial<BuildRecipe>) {
    setDrafts((prev) => prev.map((draft) => (
      draft.key === key ? { ...draft, recipe: { ...draft.recipe, ...patch } } : draft
    )));
  }

  function updateArtifact(key: string, index: number, patch: Partial<ArtifactDraft>) {
    setDrafts((prev) => prev.map((draft) => {
      if (draft.key !== key) return draft;
      const artifacts = [...(draft.recipe.artifacts ?? [])];
      artifacts[index] = { ...artifacts[index], ...patch };
      return { ...draft, recipe: { ...draft.recipe, artifacts } };
    }));
  }

  function addArtifact(key: string) {
    setDrafts((prev) => prev.map((draft) => {
      if (draft.key !== key) return draft;
      const artifacts = [...(draft.recipe.artifacts ?? []), {
        role: "installer",
        kind: "installer",
        glob: "",
        content_type: "",
        required: true,
      }];
      return { ...draft, recipe: { ...draft.recipe, artifacts } };
    }));
  }

  function removeArtifact(key: string, index: number) {
    setDrafts((prev) => prev.map((draft) => {
      if (draft.key !== key) return draft;
      return {
        ...draft,
        recipe: {
          ...draft.recipe,
          artifacts: (draft.recipe.artifacts ?? []).filter((_, i) => i !== index),
        },
      };
    }));
  }

  function addBuild() {
    const used = new Set(drafts.map((draft) => draft.id));
    let id = "linux-amd64";
    let index = 2;
    while (used.has(id)) {
      id = "build-" + index;
      index += 1;
    }
    setDrafts((prev) => [...prev, { key: draftKey(id), id, recipe: cloneRecipe() }]);
  }

  function removeBuild(key: string) {
    setDrafts((prev) => prev.filter((draft) => draft.key !== key));
  }

  async function save() {
    if (!project) return;
    setError("");
    const seen = new Set<string>();
    const builds: ReleaseV2ProjectReleaseConfig["builds"] = {};
    for (const draft of drafts) {
      const id = draft.id.trim().toLowerCase();
      if (!id) {
        setError("构建目标 ID 不能为空");
        return;
      }
      if (seen.has(id)) {
        setError("构建目标 " + id + " 重复");
        return;
      }
      seen.add(id);
      if (!draft.recipe.build.trim()) {
        setError(id + " 还没有构建命令");
        return;
      }
      if (Boolean(draft.recipe.host_os) !== Boolean(draft.recipe.host_arch)) {
        setError(id + " 的构建机 OS 和架构必须同时填写或同时留空");
        return;
      }
      const artifacts = (draft.recipe.artifacts ?? []).map((artifact, index) => {
        const role = (artifact.role ?? "").trim();
        const glob = (artifact.glob ?? "").trim();
        if (!role || (!glob && !draft.recipe.manifest_path && !manifestPath.trim())) {
          throw new Error(id + " 的第 " + (index + 1) + " 条制品规则缺少 role 或 glob");
        }
        return {
          role,
          kind: (artifact.kind ?? "").trim(),
          glob,
          content_type: (artifact.content_type ?? "").trim(),
          required: artifact.required ?? true,
          ...(artifact.signed === undefined ? {} : { signed: artifact.signed }),
        };
      });
      builds[id] = {
        build: draft.recipe.build.trim(),
        sign: (draft.recipe.sign ?? "").trim(),
        verify: (draft.recipe.verify ?? "").trim(),
        timeout: (draft.recipe.timeout ?? "").trim(),
        manifest_path: (draft.recipe.manifest_path ?? "").trim(),
        host_os: (draft.recipe.host_os ?? "").trim().toLowerCase(),
        host_arch: (draft.recipe.host_arch ?? "").trim().toLowerCase(),
        artifacts,
      };
    }
    try {
      const publishing = parseObject<Record<string, Record<string, unknown>>>(publishingText, "publishing", {});
      const policies = parseObject<Record<string, unknown>>(policiesText, "policies", {});
      setSaving(true);
      await api.saveReleaseV2ProjectConfig(project.id, {
        project_id: project.id,
        builds,
        manifest_path: manifestPath.trim(),
        version_file: versionFile.trim(),
        version_json_path: versionJSONPath.trim() || "version",
        build_version_template: buildVersionTemplate.trim() || "{version}",
        notes_path: notesPath.trim(),
        channel_order: splitList(channelText),
        update_server_ids: splitList(serverText),
        publishing,
        policies,
      });
      notify("发版配置已保存", "ok");
      onSaved();
      onClose();
    } catch (err) {
      const message = errorMessage(err, "保存发版配置失败");
      setError(message);
      notify(message, "error");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="modal-backdrop open" onMouseDown={(event) => {
      if (event.target === event.currentTarget && !saving) onClose();
    }}>
      <div className="modal release-config-modal" onMouseDown={(event) => event.stopPropagation()}>
        <div className="modal-head">
          <div>
            <div className="modal-title">项目发版配置</div>
            <div className="modal-sub">{project ? project.name + " · " + project.id : "未选择项目"}；这里是项目级配置（构建命令、制品规则、渠道顺序、默认升级服务）。升级服务的地址和令牌在「全局设置」里维护。</div>
          </div>
          <button className="icon-btn" disabled={saving} onClick={onClose} aria-label="关闭">×</button>
        </div>
        <div className="modal-body">
          {loading && <div className="notice">正在读取项目发版配置…</div>}
          {error && <div className="notice bad" style={{ marginBottom: 12 }}>{error}</div>}
          {!loading && (
            <>
              <div className="form-grid">
                <label className="field"><span>清单路径 manifest_path</span><input value={manifestPath} onChange={(event) => setManifestPath(event.target.value)} placeholder="desktop_app/dist/update-bundle/update-bundle.json" /></label>
                <label className="field"><span>版本文件</span><input value={versionFile} onChange={(event) => setVersionFile(event.target.value)} placeholder="package.json" /></label>
                <label className="field"><span>版本 JSON 路径</span><input value={versionJSONPath} onChange={(event) => setVersionJSONPath(event.target.value)} placeholder="version" /></label>
                <label className="field"><span>构建版本模板</span><input value={buildVersionTemplate} onChange={(event) => setBuildVersionTemplate(event.target.value)} placeholder="{version}.{date}" /></label>
                <label className="field"><span>发布说明路径</span><input value={notesPath} onChange={(event) => setNotesPath(event.target.value)} placeholder="docs/releases/{tag}.md" /></label>
                <label className="field"><span>渠道顺序（逗号分隔）</span><input value={channelText} onChange={(event) => setChannelText(event.target.value)} placeholder="beta, stable" /></label>
                <label className="field full"><span>默认升级服务器 ID（逗号分隔，可留空）</span><input value={serverText} onChange={(event) => setServerText(event.target.value)} placeholder="cdn, app" /></label>
              </div>

              <div className="release-config-section-head">
                <div>
                  <h4>构建目标</h4>
                  <div className="tiny">一行对应一个产物平台；构建机 OS / 架构留空表示与产物平台相同。</div>
                </div>
                <button className="btn small" onClick={addBuild}>＋ 添加构建目标</button>
              </div>
              {drafts.length === 0 && <div className="notice warn">还没有构建目标。至少添加一个才能创建发布。</div>}
              {drafts.map((draft) => (
                <div className="release-config-build" key={draft.key}>
                  <div className="release-config-build-head">
                    <label className="field"><span>构建目标 ID</span><input value={draft.id} onChange={(event) => {
                      const id = event.target.value;
                      setDrafts((prev) => prev.map((item) => item.key === draft.key ? { ...item, id } : item));
                    }} placeholder="windows-amd64" className="mono" /></label>
                    <label className="field"><span>构建机 OS</span><select value={draft.recipe.host_os ?? ""} onChange={(event) => updateRecipe(draft.key, { host_os: event.target.value })}>
                      <option value="">与产物相同</option>
                      <option value="linux">linux</option>
                      <option value="darwin">darwin</option>
                      <option value="windows">windows</option>
                    </select></label>
                    <label className="field"><span>构建机架构</span><select value={draft.recipe.host_arch ?? ""} onChange={(event) => updateRecipe(draft.key, { host_arch: event.target.value })}>
                      <option value="">与产物相同</option>
                      <option value="amd64">amd64</option>
                      <option value="arm64">arm64</option>
                    </select></label>
                    <button className="btn danger-ghost small" onClick={() => removeBuild(draft.key)}>删除目标</button>
                  </div>
                  <div className="form-grid">
                    <label className="field full"><span>构建命令</span><textarea rows={3} value={draft.recipe.build} onChange={(event) => updateRecipe(draft.key, { build: event.target.value })} placeholder="cd desktop_app && pnpm install --frozen-lockfile && ..." spellCheck={false} /></label>
                    <label className="field"><span>签名命令（可选）</span><textarea rows={2} value={draft.recipe.sign ?? ""} onChange={(event) => updateRecipe(draft.key, { sign: event.target.value })} spellCheck={false} /></label>
                    <label className="field"><span>校验命令（可选）</span><textarea rows={2} value={draft.recipe.verify ?? ""} onChange={(event) => updateRecipe(draft.key, { verify: event.target.value })} spellCheck={false} /></label>
                    <label className="field"><span>超时</span><input value={draft.recipe.timeout ?? ""} onChange={(event) => updateRecipe(draft.key, { timeout: event.target.value })} placeholder="60m" /></label>
                    <label className="field"><span>平台清单路径（可选）</span><input value={draft.recipe.manifest_path ?? ""} onChange={(event) => updateRecipe(draft.key, { manifest_path: event.target.value })} /></label>
                  </div>
                  <div className="release-config-artifacts">
                    <div className="release-config-artifacts-head">
                      <div><b>制品规则</b><div className="tiny">glob 相对源码根目录；required 未匹配会让该 target 失败。</div></div>
                      <button className="btn small" onClick={() => addArtifact(draft.key)}>＋ 添加制品</button>
                    </div>
                    {(draft.recipe.artifacts ?? []).length === 0 && <div className="tiny">还没有制品规则。</div>}
                    {(draft.recipe.artifacts ?? []).length > 0 && (
                      <table className="table">
                        <thead><tr><th>role</th><th>kind</th><th>glob</th><th>content-type</th><th>required</th><th>signed</th><th /></tr></thead>
                        <tbody>
                          {(draft.recipe.artifacts ?? []).map((artifact, index) => (
                            <tr key={index}>
                              <td><input value={artifact.role} onChange={(event) => updateArtifact(draft.key, index, { role: event.target.value })} placeholder="installer" /></td>
                              <td><input value={artifact.kind ?? ""} onChange={(event) => updateArtifact(draft.key, index, { kind: event.target.value })} placeholder="installer" /></td>
                              <td><input value={artifact.glob} onChange={(event) => updateArtifact(draft.key, index, { glob: event.target.value })} placeholder="desktop_app/dist/installers/*.exe" className="mono" /></td>
                              <td><input value={artifact.content_type ?? ""} onChange={(event) => updateArtifact(draft.key, index, { content_type: event.target.value })} placeholder="application/octet-stream" /></td>
                              <td><input type="checkbox" checked={artifact.required ?? true} onChange={(event) => updateArtifact(draft.key, index, { required: event.target.checked })} /></td>
                              <td><select value={artifact.signed === true ? "true" : artifact.signed === false ? "false" : ""} onChange={(event) => updateArtifact(draft.key, index, { signed: event.target.value === "" ? undefined : event.target.value === "true" })}>
                                <option value="">跟随</option>
                                <option value="true">是</option>
                                <option value="false">否</option>
                              </select></td>
                              <td><button className="btn danger-ghost small" onClick={() => removeArtifact(draft.key, index)}>删除</button></td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    )}
                  </div>
                </div>
              ))}

              <details className="release-config-advanced">
                <summary>高级配置（publishing / policies JSON）</summary>
                <div className="form-grid" style={{ marginTop: 12 }}>
                  <label className="field"><span>publishing</span><textarea rows={8} value={publishingText} onChange={(event) => setPublishingText(event.target.value)} spellCheck={false} /></label>
                  <label className="field"><span>policies</span><textarea rows={8} value={policiesText} onChange={(event) => setPoliciesText(event.target.value)} spellCheck={false} /></label>
                </div>
              </details>
            </>
          )}
        </div>
        <div className="modal-foot">
          <span className="tiny">保存后预览会重新读取平台、版本和构建机绑定。</span>
          <div className="btn-row">
            <button className="btn" disabled={saving} onClick={onClose}>取消</button>
            <button className="btn primary" disabled={saving || loading || !project} onClick={() => void save()}>{saving ? "保存中…" : "保存配置"}</button>
          </div>
        </div>
      </div>
    </div>
  );
}
