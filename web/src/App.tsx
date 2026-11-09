import { useCallback, useEffect, useState } from "react";
import { api, ApiError, getToken, setToken, setUnauthorizedHandler } from "./api";
import type { AuthSession, Health } from "./types";
import { LoginPage } from "./pages/LoginPage";
import { ProjectsPage } from "./pages/ProjectsPage";
import { GitHubPage } from "./pages/GitHubPage";
import { ModelsPage } from "./pages/ModelsPage";
import { LanxinPage } from "./pages/LanxinPage";
import { AuditPage } from "./pages/AuditPage";
import { VerificationPage } from "./pages/VerificationPage";
import { ReleasePage } from "./pages/ReleasePage";
import { CrashPage } from "./pages/CrashPage";
import { SchedulerPage } from "./pages/SchedulerPage";

type PageKey = "projects" | "credentials" | "models" | "lanxin" | "scheduler" | "verify" | "release" | "crash" | "audit";

type NavItem = { key: PageKey; label: string; icon: string };

const CONFIG_NAV: NavItem[] = [
  { key: "projects", label: "项目", icon: "▤" },
  { key: "credentials", label: "GitHub 配置", icon: "⌥" },
  { key: "models", label: "模型配置", icon: "◎" },
  { key: "lanxin", label: "蓝信机器人", icon: "✆" },
];

const OPS_NAV: NavItem[] = [
  { key: "scheduler", label: "调度器", icon: "⇄" },
  { key: "verify", label: "补丁验证", icon: "✓" },
  { key: "release", label: "发版", icon: "⬆" },
  { key: "crash", label: "崩溃上报", icon: "⚠" },
  { key: "audit", label: "审计日志", icon: "☰" },
];

const NAV = [...CONFIG_NAV, ...OPS_NAV];

const META: Record<PageKey, { crumb: string; title: string; sub: string }> = {
  projects: { crumb: "配置中心 / 项目", title: "项目", sub: "管理项目、GitHub 仓库、蓝信群和 Codex 模型绑定。" },
  credentials: { crumb: "配置中心 / GitHub 配置", title: "GitHub 配置", sub: "GitHub 账户授权与仓库列表；仓库绑定在项目中完成。" },
  models: { crumb: "配置中心 / 模型配置", title: "模型配置", sub: "管理模型供应商、模型目录与推理强度，支持从本机 Codex 配置导入。" },
  lanxin: { crumb: "配置中心 / 蓝信机器人", title: "蓝信机器人", sub: "管理多个机器人、自动同步所在群，并查看每个机器人的聊天消息与连接状态。" },
  scheduler: { crumb: "运行中心 / 调度器", title: "调度器", sub: "查看 worker 当前阶段、心跳和事件时间线，并暂停、恢复、重试或停止单个任务。" },
  verify: { crumb: "配置中心 / 补丁验证", title: "补丁验证", sub: "无 Actions 的合并门禁：干净副本应用补丁并执行 smartpipe.yaml。" },
  release: { crumb: "配置中心 / 发版", title: "发版", sub: "从干净提交构建制品，生成带校验和与签名的 manifest。" },
  crash: { crumb: "配置中心 / 崩溃上报", title: "崩溃上报", sub: "客户端 crash_bundle 脱敏、指纹去重、限流与自动建单。" },
  audit: { crumb: "配置中心 / 审计日志", title: "审计日志", sub: "记录项目、凭据与流水线的管理操作，不保存明文秘密。" },
};

export type Notify = (message: string, kind?: "ok" | "error") => void;

export function App() {
  const [page, setPage] = useState<PageKey>(() => {
    const hash = window.location.hash.replace("#", "") as PageKey;
    return NAV.some((n) => n.key === hash) ? hash : "projects";
  });
  const [health, setHealth] = useState<Health | null>(null);
  const [healthError, setHealthError] = useState("");
  const [toast, setToast] = useState<{ message: string; kind: "ok" | "error" } | null>(null);
  const [tokenOpen, setTokenOpen] = useState(false);
  const [tokenDraft, setTokenDraft] = useState(getToken());
  const [auth, setAuth] = useState<"checking" | "login" | "ready" | "token">("checking");
  const [user, setUser] = useState<AuthSession | null>(null);

  const notify = useCallback<Notify>((message, kind = "ok") => {
    setToast({ message, kind });
    window.setTimeout(() => setToast(null), 3200);
  }, []);

  useEffect(() => {
    setUnauthorizedHandler(() => {
      setUser(null);
      setAuth((prev) => (prev === "ready" ? "login" : prev));
    });
    void (async () => {
      try {
        const cfg = await api.authConfig();
        if (cfg.dev_open) {
          setAuth("ready");
          return;
        }
        if (!cfg.login_enabled) {
          setAuth("token");
          setTokenOpen(true);
          return;
        }
        try {
          const session = await api.session();
          setUser(session);
          setAuth("ready");
        } catch {
          setAuth("login");
        }
      } catch {
        setAuth("login");
      }
    })();
    return () => setUnauthorizedHandler(null);
  }, []);

  const logout = useCallback(async () => {
    try {
      await api.logout();
    } catch {
      // 会话已失效时直接回到登录页。
    }
    setUser(null);
    setAuth("login");
  }, []);

  const refreshHealth = useCallback(async () => {
    try {
      const h = await api.health();
      setHealth(h);
      setHealthError("");
    } catch (err) {
      setHealth(null);
      setHealthError(err instanceof ApiError && err.status === 401 ? "需要管理员令牌" : "后端未连接");
    }
  }, []);

  useEffect(() => {
    void refreshHealth();
    const timer = window.setInterval(() => void refreshHealth(), 15000);
    return () => window.clearInterval(timer);
  }, [refreshHealth]);

  useEffect(() => {
    window.location.hash = page;
  }, [page]);

  const meta = META[page];

  if (auth === "checking") {
    return <div className="login-shell"><div className="login-checking">正在检查登录状态…</div></div>;
  }
  if (auth === "login") {
    return (
      <LoginPage
        onSuccess={(session) => {
          setToken("");
          setUser(session);
          setAuth("ready");
          void refreshHealth();
        }}
      />
    );
  }

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-mark">SP</div>
          <div>
            <div className="brand-title">SmartPipe</div>
            <div className="brand-sub">CONFIG CONSOLE</div>
          </div>
        </div>

        <div>
          <div className="nav-label">配置中心</div>
          <nav className="nav">
            {CONFIG_NAV.map((item) => (
              <button
                key={item.key}
                className={"nav-item" + (page === item.key ? " active" : "")}
                onClick={() => setPage(item.key)}
              >
                <span className="nav-icon">{item.icon}</span>
                {item.label}
              </button>
            ))}
          </nav>
          <div className="nav-label" style={{ marginTop: 18 }}>运行中心</div>
          <nav className="nav">
            {OPS_NAV.map((item) => (
              <button
                key={item.key}
                className={"nav-item" + (page === item.key ? " active" : "")}
                onClick={() => setPage(item.key)}
              >
                <span className="nav-icon">{item.icon}</span>
                {item.label}
              </button>
            ))}
          </nav>
        </div>

        <div className="sidebar-foot">
          <div className="env-card">
            <div><b>环境</b> 本地开发（SIT）</div>
            <div><b>数据库</b> {health?.database ? "已连接" : "未连接"}</div>
            <div><b>鉴权</b> {health ? (health.auth === "dev-open" ? "dev-open" : "令牌") : "-"}</div>
          </div>
          {user ? (
            <>
              <div className="tiny" style={{ padding: "0 8px 4px" }}>{user.display_name || user.username}</div>
              <button className="nav-item" onClick={() => void logout()}>
                <span className="nav-icon">⏻</span>退出登录
              </button>
            </>
          ) : (
            <button className="nav-item" onClick={() => { setTokenDraft(getToken()); setTokenOpen(true); }}>
              <span className="nav-icon">⚙</span>管理员令牌
            </button>
          )}
        </div>
      </aside>

      <main className="main">
        <header className="topbar">
          <div>
            <div className="crumb">{meta.crumb}</div>
            <div className="page-title">{meta.title}</div>
            <div className="page-sub">{meta.sub}</div>
          </div>
          <div className="top-actions">
            <span className={"chip " + (health ? (health.database ? "ok" : "bad") : "warn")}>
              <span className="dot" />
              {health ? (health.database ? "后端已连接 · " + health.version : "数据库不可用") : healthError || "连接中"}
            </span>
            <button className="btn" onClick={() => void refreshHealth()}>刷新连接</button>
          </div>
        </header>

        <div className="content">
          {page === "projects" && <ProjectsPage notify={notify} />}
          {page === "credentials" && <GitHubPage notify={notify} />}
          {page === "models" && <ModelsPage notify={notify} />}
          {page === "lanxin" && <LanxinPage notify={notify} />}
          {page === "scheduler" && <SchedulerPage notify={notify} />}
          {page === "verify" && <VerificationPage notify={notify} />}
          {page === "release" && <ReleasePage notify={notify} />}
          {page === "crash" && <CrashPage notify={notify} />}
          {page === "audit" && <AuditPage notify={notify} />}
        </div>
      </main>

      <div className={"modal-backdrop" + (tokenOpen ? " open" : "")}>
        <div className="modal" style={{ width: "min(520px, 100%)" }}>
          <div className="modal-head">
            <div>
              <div className="modal-title">管理员令牌</div>
              <div className="modal-sub">对应服务端 SMARTPIPE_ADMIN_TOKEN；仅保存在本机浏览器。</div>
            </div>
            <button className="icon-btn" onClick={() => setTokenOpen(false)}>✕</button>
          </div>
          <div className="modal-body">
            <div className="field full">
              <label>Bearer Token</label>
              <input
                type="password"
                value={tokenDraft}
                placeholder="留空表示使用 dev-open 模式"
                onChange={(e) => setTokenDraft(e.target.value)}
              />
            </div>
          </div>
          <div className="modal-foot">
            <div className="tiny">令牌只保存在本机浏览器，不会写入日志。</div>
            <div>
              <button className="btn" onClick={() => setTokenOpen(false)}>取消</button>{" "}
              <button
                className="btn primary"
                onClick={() => {
                  setToken(tokenDraft);
                  setTokenOpen(false);
                  notify("令牌已保存（仅本机）");
                  void refreshHealth();
                }}
              >
                保存
              </button>
            </div>
          </div>
        </div>
      </div>

      <div className={"toast" + (toast ? " show" : "") + (toast?.kind === "error" ? " error" : "")}>
        {toast?.message}
      </div>
    </div>
  );
}
