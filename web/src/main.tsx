import { Component, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./styles.css";

type BoundaryState = { error: Error | null };

class ErrorBoundary extends Component<{ children: ReactNode }, BoundaryState> {
  state: BoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): BoundaryState {
    return { error };
  }

  componentDidCatch(error: Error) {
    console.error("页面渲染失败", error);
  }

  render() {
    if (this.state.error) {
      return (
        <div style={{ padding: 28, fontFamily: '"Segoe UI", "PingFang SC", sans-serif' }}>
          <h2 style={{ margin: "0 0 8px", fontSize: 18 }}>页面出错了</h2>
          <p style={{ color: "#64748b", fontSize: 13, margin: "0 0 12px" }}>
            把下面这段错误发给管理员即可定位；刷新通常会恢复。
          </p>
          <pre style={{ whiteSpace: "pre-wrap", color: "#b91c1c", fontSize: 12, lineHeight: 1.6, background: "#fef2f2", padding: 12, borderRadius: 8 }}>
            {String(this.state.error.message || this.state.error)}
          </pre>
          <button
            onClick={() => window.location.reload()}
            style={{ marginTop: 12, padding: "8px 14px", borderRadius: 8, border: "1px solid #2563eb", background: "#2563eb", color: "#fff", cursor: "pointer" }}
          >
            重新加载
          </button>
        </div>
      );
    }
    return this.props.children;
  }
}

createRoot(document.getElementById("root")!).render(
  <ErrorBoundary>
    <App />
  </ErrorBoundary>,
);
