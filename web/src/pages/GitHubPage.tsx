import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError } from "../api";
import type {
  Credential,
  GitHubAppSetup,
  GitHubAppSetupStatus,
  GitHubDeviceFlow,
  GitHubOAuthView,
  GitHubRepo,
  GitHubRepoList,
  Project,
} from "../types";
import type { Notify } from "../App";

const formatTime = (value: string) => {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString("zh-CN", { hour12: false });
};

const formatNumber = (value: number) => (value >= 1000 ? (value / 1000).toFixed(1) + "k" : String(value ?? 0));

export function GitHubPage({ notify }: { notify: Notify }) {
  const [repos, setRepos] = useState<GitHubRepoList | null>(null);
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [visibility, setVisibility] = useState<"all" | "public" | "private">("all");
  const [origin, setOrigin] = useState<"all" | "own" | "fork">("all");
  const [access, setAccess] = useState<"all" | "own" | "invited">("all");
  const [binding, setBinding] = useState<"all" | "bound" | "unbound">("all");
  const [appSetup, setAppSetup] = useState<GitHubAppSetup | null>(null);
  const [appStatus, setAppStatus] = useState<GitHubAppSetupStatus | null>(null);
  const [appBusy, setAppBusy] = useState(false);
  const [oauth, setOauth] = useState<GitHubOAuthView | null>(null);
  const [device, setDevice] = useState<GitHubDeviceFlow | null>(null);
  const [deviceBusy, setDeviceBusy] = useState(false);
  const [proxyURL, setProxyURL] = useState("");
  const [proxyBusy, setProxyBusy] = useState("");
  const [proxyNote, setProxyNote] = useState("");
  const [proxyNoteOk, setProxyNoteOk] = useState(true);
  const [patOpen, setPatOpen] = useState(false);
  const [patToken, setPatToken] = useState("");
  const [patBusy, setPatBusy] = useState(false);
  const [patNote, setPatNote] = useState("");
  const [patNoteOk, setPatNoteOk] = useState(true);
  const accountScope = repos?.oauth_scope ?? "";
  const scopeHasRepo = accountScope.split(",").map((item) => item.trim()).includes("repo");

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [repoList, creds, projs, oauthView, proxyView] = await Promise.all([
        api.listGitHubRepos(),
        api.listCredentials(),
        api.listProjects(),
        api.getGithubOAuth(),
        api.getGithubProxy(),
      ]);
      setProxyURL(proxyView.url ?? "");
      setRepos({ ...repoList, repos: repoList.repos ?? [] });
      setOauth(oauthView);
      setCredentials(creds ?? []);
      setProjects(projs ?? []);
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载 GitHub 配置失败");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!appSetup) return;
    let stopped = false;
    const tick = async () => {
      try {
        const res = await api.getGithubAppStatus(appSetup.setup_id);
        if (stopped) return;
        setAppStatus(res);
        if (res.status === "installed") {
          setAppSetup(null);
          notify("GitHub 已连接" + (res.app_slug ? "：" + res.app_slug : ""), "ok");
          await load();
        } else if (res.status === "expired" || res.status === "error") {
          setAppSetup(null);
          notify(res.message || (res.status === "expired" ? "连接已过期，请重新发起" : "GitHub 连接失败"), "error");
        }
      } catch (err) {
        if (stopped) return;
        setAppSetup(null);
        notify(err instanceof ApiError ? err.message : "查询 GitHub 连接状态失败", "error");
      }
    };
    void tick();
    const timer = window.setInterval(() => void tick(), Math.max(appSetup.poll_interval || 2, 2) * 1000);
    return () => {
      stopped = true;
      window.clearInterval(timer);
    };
  }, [appSetup, load, notify]);

  useEffect(() => {
    if (!device) return;
    let stopped = false;
    const poll = async () => {
      try {
        const res = await api.pollGithubOAuth(device.flow_id);
        if (stopped) return;
        if (res.status === "authorized") {
          setDevice(null);
          notify("GitHub 账号已授权" + (res.login ? "：" + res.login : ""), "ok");
          await load();
        } else if (res.status === "expired" || res.status === "denied" || res.status === "error") {
          setDevice(null);
          notify(res.message || "GitHub 授权未完成，请重试", "error");
        }
      } catch {
        /* 轮询失败继续等下一轮 */
      }
    };
    const timer = window.setInterval(() => void poll(), Math.max(device.interval || 5, 3) * 1000);
    return () => {
      stopped = true;
      window.clearInterval(timer);
    };
  }, [device, load, notify]);

  const saveProxy = async () => {
    setProxyBusy("save");
    setProxyNote("");
    try {
      const view = await api.setGithubProxy(proxyURL.trim());
      setProxyURL(view.url);
      setProxyNote(view.url ? "代理已保存，GitHub 登录与仓库请求会走它" : "已切换为直连");
      setProxyNoteOk(true);
      notify(view.url ? "GitHub 代理已保存" : "已清除 GitHub 代理");
      await load();
    } catch (err) {
      setProxyNote(err instanceof ApiError ? err.message : "保存代理失败");
      setProxyNoteOk(false);
    } finally {
      setProxyBusy("");
    }
  };

  const testProxy = async () => {
    setProxyBusy("test");
    setProxyNote("");
    try {
      const res = await api.testGithubProxy(proxyURL.trim());
      setProxyNote((res.ok ? "代理可用：" : "代理不可用：") + res.message + `（${res.latency_ms}ms）`);
      setProxyNoteOk(res.ok);
    } catch (err) {
      setProxyNote(err instanceof ApiError ? err.message : "测试代理失败");
      setProxyNoteOk(false);
    } finally {
      setProxyBusy("");
    }
  };

  const startAccountLogin = async () => {
    setDeviceBusy(true);
    try {
      const flow = await api.startGithubOAuth();
      setDevice(flow);
      window.open(flow.verification_uri, "_blank", "noopener,noreferrer");
      notify("请在 GitHub 页面输入代码 " + flow.user_code, "ok");
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "发起 GitHub 账号登录失败", "error");
    } finally {
      setDeviceBusy(false);
    }
  };

  const disconnectAccount = async () => {
    if (!window.confirm("解绑 GitHub 账号授权？解绑后将看不到被邀请参与的仓库。")) return;
    try {
      await api.deleteGithubOAuth();
      notify("GitHub 账号授权已解绑");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "解绑失败", "error");
    }
  };

  const savePAT = async () => {
    const token = patToken.trim();
    if (!token) {
      setPatNote("请先粘贴访问令牌");
      setPatNoteOk(false);
      return;
    }
    setPatBusy(true);
    setPatNote("");
    try {
      const res = await api.saveGithubToken(token);
      setPatToken("");
      setPatNote(res.message);
      setPatNoteOk(res.private_count > 0 || res.invited_count > 0);
      notify("GitHub 访问令牌已保存", "ok");
      await load();
    } catch (err) {
      setPatNote(err instanceof ApiError ? err.message : "保存访问令牌失败");
      setPatNoteOk(false);
    } finally {
      setPatBusy(false);
    }
  };

  const removePAT = async () => {
    if (!window.confirm("移除已保存的 GitHub 访问令牌？移除后将只能看到 App 安装范围内的仓库。")) return;
    setPatBusy(true);
    try {
      await api.deleteGithubToken();
      setPatNote("访问令牌已移除");
      setPatNoteOk(true);
      notify("GitHub 访问令牌已移除");
      await load();
    } catch (err) {
      notify(err instanceof ApiError ? err.message : "移除访问令牌失败", "error");
    } finally {
      setPatBusy(false);
    }
  };

  const connectGithub = async () => {
    const authWindow = window.open("about:blank", "_blank");
    setAppBusy(true);
    setAppStatus({ status: "pending", message: "正在创建 GitHub 连接" });
    try {
      const setup = await api.startGithubApp();
      setAppSetup(setup);
      if (authWindow) {
        authWindow.location.href = setup.launch_url;
      } else {
        notify("浏览器拦截了 GitHub 窗口，请点击页面里的连接链接继续", "error");
      }
    } catch (err) {
      authWindow?.close();
      setAppStatus(null);
      notify(err instanceof ApiError ? err.message : "发起 GitHub 连接失败", "error");
    } finally {
      setAppBusy(false);
    }
  };

  const githubAppCred = credentials.find((item) => item.kind === "github_app" && item.enabled);
  const patCred = credentials.find(
    (item) =>
      item.kind === "github_pat" &&
      item.enabled &&
      String(item.metadata?.source ?? "") !== "oauth_device",
  );
  const connected = Boolean(repos?.connected);
  const account = repos?.account || "";
  const appSlug =
    repos?.app_slug ||
    (typeof githubAppCred?.metadata.slug === "string" ? githubAppCred.metadata.slug : "");
  const reopenUrl =
    appStatus?.status === "created" && appStatus.app_slug
      ? "https://github.com/apps/" + encodeURIComponent(appStatus.app_slug) + "/installations/new"
      : appSetup?.launch_url || "";

  const projectFor = useCallback(
    (repo: GitHubRepo) =>
      projects.find((project) => {
        const url = (project.github_repo_url || "").toLowerCase();
        return url !== "" && (url.includes(repo.full_name.toLowerCase()) || url.endsWith("/" + repo.name.toLowerCase()));
      }) ?? null,
    [projects],
  );

  const list = repos?.repos ?? [];
  const me = (repos?.oauth_login || repos?.account || "").toLowerCase();
  const isInvited = useCallback(
    (repo: GitHubRepo) => me !== "" && repo.owner.toLowerCase() !== me,
    [me],
  );
  const counts = useMemo(() => {
    const privateCount = list.filter((repo) => repo.private).length;
    const forkCount = list.filter((repo) => repo.fork).length;
    const invited = list.filter(isInvited).length;
    const bound = list.filter((repo) => Boolean(projectFor(repo))).length;
    return {
      private: privateCount,
      public: list.length - privateCount,
      fork: forkCount,
      own: list.length - forkCount,
      invited,
      mine: list.length - invited,
      bound,
      unbound: list.length - bound,
    };
  }, [list, projectFor, isInvited]);

  const filtered = useMemo(() => {
    const keyword = query.trim().toLowerCase();
    const rows = list.filter((repo) => {
      if (visibility === "private" && !repo.private) return false;
      if (visibility === "public" && repo.private) return false;
      if (origin === "own" && repo.fork) return false;
      if (origin === "fork" && !repo.fork) return false;
      if (access === "own" && isInvited(repo)) return false;
      if (access === "invited" && !isInvited(repo)) return false;
      const bound = Boolean(projectFor(repo));
      if (binding === "bound" && !bound) return false;
      if (binding === "unbound" && bound) return false;
      if (!keyword) return true;
      return [repo.full_name, repo.description, repo.language, repo.default_branch]
        .join(" ")
        .toLowerCase()
        .includes(keyword);
    });
    return [...rows].sort((left, right) => {
      const lt = Date.parse(left.updated_at) || 0;
      const rt = Date.parse(right.updated_at) || 0;
      return rt - lt;
    });
  }, [list, query, visibility, origin, access, binding, projectFor, isInvited]);

  const filtersActive = visibility !== "all" || origin !== "all" || access !== "all" || binding !== "all";
  const clearFilters = () => {
    setVisibility("all");
    setOrigin("all");
    setAccess("all");
    setBinding("all");
  };

  return (
    <>
      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">GitHub 账户</div>
            <div className="card-sub">只负责授权与仓库读取；仓库绑定在项目中完成。</div>
          </div>
          <span className={"chip " + (connected ? "ok" : "warn")}>
            <span className="dot" />
            {connected ? "已连接" + (account ? " · " + account : "") : "未连接"}
          </span>
        </div>
        <div className="card-body">
          {appSetup ? (
            <>
              <div>已打开 GitHub 授权窗口，请在浏览器里完成登录、创建应用并选择仓库。</div>
              <div className="tiny" style={{ marginTop: 8 }}>{appStatus?.message || "等待 GitHub 返回…"}</div>
              <div className="row-actions" style={{ marginTop: 12 }}>
                <a className="btn" href={reopenUrl} target="_blank" rel="noreferrer">重新打开 GitHub</a>
                <button className="btn" onClick={() => { setAppSetup(null); setAppStatus(null); }}>取消</button>
              </div>
            </>
          ) : connected ? (
            <div className="github-account">
              <div className="github-account-main">
                <div className="strong">{account || appSlug || "GitHub"}</div>
                <div className="tiny">
                  {repos?.source === "app"
                    ? `GitHub App${appSlug ? " · " + appSlug : ""} 已安装，后台按需换取 installation token`
                    : "GitHub PAT 授权，可直接读取账号可见仓库"}
                  {repos?.token_source === "pat" ? " · 账号 PAT 已启用" : repos?.token_source === "oauth" ? " · 账号登录授权" : ""}
                </div>
              </div>
              <div className="row-actions">
                <button className="btn" disabled={appBusy} onClick={() => void load()}>刷新仓库</button>
                <button className="btn primary" disabled={appBusy} onClick={() => void connectGithub()}>
                  {appBusy ? "正在准备…" : "重新连接 GitHub"}
                </button>
              </div>
            </div>
          ) : (
            <>
              <div>
                点击“连接 GitHub”，浏览器会打开 GitHub，由你登录并选择要授权的仓库；完成后自动回到这里。
                应用凭据加密保存在后台，不需要手工填写 Client ID 或 PAT。
              </div>
              {repos?.error && <div className="tiny" style={{ marginTop: 8, color: "var(--warn)" }}>{repos.error}</div>}
              <div className="row-actions" style={{ marginTop: 12 }}>
                <button className="btn primary" disabled={appBusy} onClick={() => void connectGithub()}>
                  {appBusy ? "正在准备…" : "连接 GitHub"}
                </button>
              </div>
            </>
          )}
          <div className="github-proxy">
            <span className="github-proxy-label">网络代理</span>
            <input
              value={proxyURL}
              onChange={(event) => setProxyURL(event.target.value)}
              placeholder="http://172.17.0.1:7890（留空表示直连）"
              spellCheck={false}
            />
            <button className="btn small" disabled={!!proxyBusy} onClick={() => void saveProxy()}>
              {proxyBusy === "save" ? "保存中…" : "保存"}
            </button>
            <button className="btn small" disabled={!!proxyBusy} onClick={() => void testProxy()}>
              {proxyBusy === "test" ? "测试中…" : "测试"}
            </button>
            {proxyNote && <span className={"tiny " + (proxyNoteOk ? "" : "bad-note")}>{proxyNote}</span>}
            <span className="tiny github-proxy-hint">
              支持 http / https / socks5；容器部署请填宿主机代理地址，登录和仓库请求都会走这里。
            </span>
          </div>
          <div className="github-account-auth">
            <div className="tiny">
              App 安装与 GitHub 账号登录只能看到授权范围内的仓库；别人仓库里邀请你参与的项目，用底部「访问令牌（PAT）」读取才完整。
            </div>
            {device ? (
              <div className="github-device">
                <span>在 GitHub 输入代码</span>
                <code className="github-device-code">{device.user_code}</code>
                <a className="btn small" href={device.verification_uri} target="_blank" rel="noreferrer">打开 GitHub</a>
              </div>
            ) : oauth?.authorized && !scopeHasRepo ? (
              <div className="github-device github-device-warn">
                <span className="chip warn">账号已登录 · 仅公开仓库权限</span>
                <span className="tiny">GitHub App 用户授权拿不到 repo 权限，被邀请的私有仓库请用下方「访问令牌（PAT）」。</span>
                <button className="btn small" onClick={() => void disconnectAccount()}>解绑账号</button>
              </div>
            ) : oauth?.authorized ? (
              <div className="github-device">
                <span className="chip ok">账号已授权{oauth.login ? " · " + oauth.login : ""}</span>
                <span className="tiny">权限：{accountScope || "默认"}</span>
                <button className="btn small" onClick={() => void disconnectAccount()}>解绑账号</button>
              </div>
            ) : (
              <div className="github-device">
                <button className="btn small" disabled={deviceBusy} onClick={() => void startAccountLogin()}>
                  {deviceBusy ? "正在申请…" : "登录 GitHub 账户（读取公开仓库）"}
                </button>
              </div>
            )}
          </div>
          <div className="github-pat">
            <button className="github-pat-toggle" onClick={() => setPatOpen((value) => !value)}>
              {patOpen ? "▾" : "▸"} 访问令牌（PAT）
            </button>
            {patCred ? (
              <span className="chip ok">已配置{typeof patCred.metadata?.login === "string" && patCred.metadata.login ? " · " + patCred.metadata.login : ""}</span>
            ) : (
              <span className="tiny">没配置</span>
            )}
            <span className="tiny github-pat-hint">看到“邀请协作”仓库需要带 repo 权限的经典 PAT</span>
            {patOpen && (
              <div className="github-pat-body">
                <div className="tiny">
                  打开 GitHub → Settings → Developer settings → Personal access tokens (classic)，勾选 <code>repo</code>（需要组织仓库再加 <code>read:org</code>），把生成的令牌粘到这里。令牌只保存在后台加密存储，不写进配置文件。
                </div>
                <div className="github-pat-row">
                  <input
                    type="password"
                    value={patToken}
                    onChange={(event) => setPatToken(event.target.value)}
                    placeholder="ghp_… 或 github_pat_…"
                    autoComplete="off"
                    spellCheck={false}
                  />
                  <button className="btn small primary" disabled={patBusy} onClick={() => void savePAT()}>
                    {patBusy ? "校验中…" : "验证并保存"}
                  </button>
                  {patCred && (
                    <button className="btn small" disabled={patBusy} onClick={() => void removePAT()}>
                      移除
                    </button>
                  )}
                  <a
                    className="btn small"
                    href="https://github.com/settings/tokens/new?scopes=repo,read:org&description=SmartPipe"
                    target="_blank"
                    rel="noreferrer"
                  >
                    生成经典 PAT
                  </a>
                </div>
                {patNote && <div className={"tiny " + (patNoteOk ? "" : "bad-note")}>{patNote}</div>}
              </div>
            )}
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div>
            <div className="card-title">仓库列表</div>
            <div className="card-sub">
              {loading
                ? "正在读取 GitHub 仓库…"
                : `${list.length} 个仓库 · ${counts.private} 个私有 · ${counts.invited} 个邀请协作${filtersActive ? ` · 已筛选 ${filtered.length} 个` : ""}${repos?.catalog_count ? ` · 已入库 ${repos.catalog_count} 个` : ""}${repos?.fetched_at ? " · 更新于 " + formatTime(repos.fetched_at) : ""}`}
            </div>
          </div>
          <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
            <input
              className="github-repo-search"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="搜索仓库、语言或分支"
            />
            <button className="btn" onClick={() => void load()} disabled={loading}>刷新</button>
          </div>
        </div>
        {(repos?.warning || repos?.user_error) && (
          <div className="github-warn-banner">
            <span className="github-warn-text">
              {repos?.user_error ? "账号授权读取失败：" + repos.user_error : repos?.warning}
            </span>
            {repos?.warning ? (
              <button
                className="btn small primary"
                onClick={() => {
                  setPatOpen(true);
                  document.querySelector(".github-pat")?.scrollIntoView({ behavior: "smooth", block: "center" });
                }}
              >
                用 PAT 导入
              </button>
            ) : (
              <button className="btn small" onClick={() => void load()}>重新读取</button>
            )}
          </div>
        )}
        <div className="github-filters">
          <span className="github-filter-label">可见性</span>
          <button className="github-filter-chip" data-active={visibility === "all"} onClick={() => setVisibility("all")}>全部 {list.length}</button>
          <button className="github-filter-chip" data-active={visibility === "public"} onClick={() => setVisibility("public")}>公开 {counts.public}</button>
          <button className="github-filter-chip" data-active={visibility === "private"} onClick={() => setVisibility("private")}>私有 {counts.private}</button>
          <span className="github-filter-sep" />
          <span className="github-filter-label">类型</span>
          <button className="github-filter-chip" data-active={origin === "all"} onClick={() => setOrigin("all")}>全部 {list.length}</button>
          <button className="github-filter-chip" data-active={origin === "own"} onClick={() => setOrigin("own")}>自建 {counts.own}</button>
          <button className="github-filter-chip" data-active={origin === "fork"} onClick={() => setOrigin("fork")}>Fork {counts.fork}</button>
          <span className="github-filter-sep" />
          <span className="github-filter-label">归属</span>
          <button className="github-filter-chip" data-active={access === "all"} onClick={() => setAccess("all")}>全部 {list.length}</button>
          <button className="github-filter-chip" data-active={access === "own"} onClick={() => setAccess("own")}>本人 {counts.mine}</button>
          <button className="github-filter-chip" data-active={access === "invited"} onClick={() => setAccess("invited")}>邀请协作 {counts.invited}</button>
          <span className="github-filter-sep" />
          <span className="github-filter-label">绑定</span>
          <button className="github-filter-chip" data-active={binding === "all"} onClick={() => setBinding("all")}>全部 {list.length}</button>
          <button className="github-filter-chip" data-active={binding === "bound"} onClick={() => setBinding("bound")}>已绑定 {counts.bound}</button>
          <button className="github-filter-chip" data-active={binding === "unbound"} onClick={() => setBinding("unbound")}>未绑定 {counts.unbound}</button>
          {filtersActive && <button className="github-filter-clear" onClick={clearFilters}>清除筛选</button>}
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {loading ? (
            <div className="empty">加载中…</div>
          ) : error ? (
            <div className="empty">
              <h3>无法读取仓库列表</h3>
              <div className="tiny">{error}</div>
              <button className="btn" style={{ marginTop: 12 }} onClick={() => void load()}>重试</button>
            </div>
          ) : !connected ? (
            <div className="empty">
              <h3>还没有连接 GitHub</h3>
              <div className="tiny">连接 GitHub 账户后，这里会列出授权范围内的仓库。</div>
            </div>
          ) : filtered.length === 0 ? (
            <div className="empty">
              <h3>{list.length === 0 ? "授权范围内没有仓库" : "没有匹配的仓库"}</h3>
              <div className="tiny">{list.length === 0 ? "确认 GitHub App 安装时勾选了仓库，或重新连接一次。" : "换一个关键词试试，或清除筛选条件。"}</div>
              {list.length > 0 && filtersActive && (
                <button className="btn" style={{ marginTop: 12 }} onClick={clearFilters}>清除筛选</button>
              )}
            </div>
          ) : (
            <table className="table github-repo-table">
              <thead>
                <tr>
                  <th>仓库</th>
                  <th>默认分支</th>
                  <th>语言</th>
                  <th>Star</th>
                  <th>Issue</th>
                  <th>最近更新</th>
                  <th>绑定项目</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((repo) => {
                  const project = projectFor(repo);
                  return (
                    <tr key={repo.id}>
                      <td>
                        <div className="strong mono">{repo.full_name}</div>
                        <div className="tiny" style={{ display: "flex", gap: 6, flexWrap: "wrap", marginTop: 4 }}>
                          <span className={"chip " + (repo.private ? "warn" : "")}>{repo.private ? "私有" : "公开"}</span>
                          {isInvited(repo) && <span className="chip ok">邀请协作 · {repo.owner}</span>}
                          {!isInvited(repo) && repo.user_visible && <span className="chip">账号可见</span>}
                          {repo.fork && <span className="chip">Fork</span>}
                          {repo.archived && <span className="chip bad">已归档</span>}
                          {repo.description && <span>{repo.description}</span>}
                        </div>
                      </td>
                      <td className="mono">{repo.default_branch || "—"}</td>
                      <td>{repo.language || "—"}</td>
                      <td>{formatNumber(repo.stars)}</td>
                      <td>{formatNumber(repo.open_issues)}</td>
                      <td className="tiny">{formatTime(repo.updated_at)}</td>
                      <td>
                        {project ? <span className="chip ok">{project.name}</span> : <span className="tiny">未绑定</span>}
                      </td>
                      <td>
                        <div className="row-actions">
                          <a className="btn small" href={repo.html_url} target="_blank" rel="noreferrer">打开</a>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </>
  );
}
