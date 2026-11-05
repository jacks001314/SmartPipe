import { useCallback, useEffect, useState } from "react";
import { api, ApiError, getToken, setToken } from "./api";
import type { Health } from "./types";
import { ProjectsPage } from "./pages/ProjectsPage";
import { CredentialsPage } from "./pages/CredentialsPage";
import { AuditPage } from "./pages/AuditPage";
import { RuntimePage } from "./pages/RuntimePage";
import { BridgePage } from "./pages/BridgePage";
import { VerificationPage } from "./pages/VerificationPage";
import { ReleasePage } from "./pages/ReleasePage";

type PageKey = "projects" | "credentials" | "runtime" | "bridge" | "verify" | "release" | "audit";

const NAV: { key: PageKey; label: string; icon: string }[] = [
  { key: "projects", label: "项目", icon: "▤" },
  { key: "credentials", label: "凭据与连接", icon: "⚿" },
  { key: "runtime", label: "Syntropy 运行时", icon: "◫" },
  { key: "bridge", label: "桥接与派单", icon: "⇄" },
  { key: "verify", label: "补丁验证", icon: "✓" },
  { key: "release", label: "发版", icon: "⬆" },
  { key: "audit", label: "审计日志", icon: "☰" },
];

const META: Record<PageKey, { crumb: string; title: string; sub: string }> = {
  projects: { crumb: "配置中心 / 项目", title: "项目", sub: "管理项目、GitHub 仓库和蓝信群绑定。Agent 只使用 project_id，不接触凭据。" },
  credentials: { crumb: "配置中心 / 凭据与连接", title: "凭据与连接", sub: "凭据密文存储，界面只显示掩码；连接测试由服务端发起。" },
  runtime: { crumb: "配置中心 / Syntropy 运行时", title: "Syntropy 运行时", sub: "App Server 进程、Master/Worker 节点和 pipeline-lead 实例。" },
  bridge: { crumb: "配置中心 / 桥接与派单", title: "桥接与派单", sub: "蓝信消息转 GitHub Issue，并把工单投递给项目的 pipeline-lead。" },
  verify: { crumb: "配置中心 / 补丁验证", title: "补丁验证", sub: "无 Actions 的合并门禁：干净副本应用补丁并执行 smartpipe.yaml。" },
  release: { crumb: "配置中心 / 发版", title: "发版", sub: "从干净提交构建制品，生成带校验和与签名的 manifest。" },
  audit: { crumb: "配置中心 / 审计日志", title: "审计日志", sub: "记录项目、凭据和运行时的管理操作，不保存明文秘密。" },
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

  const notify = useCallback<Notify>((message, kind = "ok") => {
    setToast({ message, kind });
    window.setTimeout(() => setToast(null), 3200);
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
            {NAV.map((item) => (
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
          <button className="nav-item" onClick={() => { setTokenDraft(getToken()); setTokenOpen(true); }}>
            <span className="nav-icon">⚙</span>管理员令牌
          </button>
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
          {page === "credentials" && <CredentialsPage notify={notify} />}
          {page === "runtime" && <RuntimePage notify={notify} />}
          {page === "bridge" && <BridgePage notify={notify} />}
          {page === "verify" && <VerificationPage notify={notify} />}
          {page === "release" && <ReleasePage notify={notify} />}
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
            <div className="tiny">令牌不会写入日志，也不会随请求转发给 Agent。</div>
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
