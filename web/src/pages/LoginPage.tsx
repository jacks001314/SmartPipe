import { useState } from "react";
import { api, ApiError } from "../api";
import type { AuthSession } from "../types";

export function LoginPage({ onSuccess, hint }: { onSuccess: (session: AuthSession) => void; hint?: string }) {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (username.trim() === "" || password === "") {
      setError("用户名和密码都不能为空");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const session = await api.login(username.trim(), password);
      onSuccess(session);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "登录失败，请重试");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-shell">
      <div className="login-card">
        <div className="login-left">
          <div className="login-brand">
            <div className="brand-mark">SP</div>
            <div>
              <div className="login-brand-title">SmartPipe</div>
              <div className="login-brand-sub">CONFIG CONSOLE</div>
            </div>
          </div>
          <div>
            <div className="login-headline">项目与凭据的统一入口</div>
            <div className="login-desc">
              管理 GitHub 仓库、蓝信群绑定和模型供应商。凭据加密存储，流水线只读取 project_id。
            </div>
            <ul className="login-points">
              <li>项目、凭据与连接测试</li>
              <li>模型供应商与蓝信群绑定</li>
              <li>补丁验证与发版流水线</li>
            </ul>
          </div>
          <div className="login-foot">本地开发环境（SIT）</div>
        </div>

        <div className="login-right">
          <div className="login-form-title">登录配置中心</div>
          <div className="login-form-sub">使用用户名和密码继续</div>
          <div className="field">
            <label>用户名</label>
            <input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" />
          </div>
          <div className="field" style={{ marginTop: 14 }}>
            <label>密码</label>
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") void submit(); }}
              autoComplete="current-password"
              placeholder="输入密码"
            />
          </div>
          <div className="login-error">{error || hint}</div>
          <button className="btn primary login-submit" disabled={busy} onClick={() => void submit()}>
            {busy ? "登录中…" : "登录"}
          </button>
          <div className="login-note">
            登录状态保存在 HttpOnly 会话 Cookie 中，关闭浏览器后按服务端有效期失效。
          </div>
        </div>
      </div>
    </div>
  );
}