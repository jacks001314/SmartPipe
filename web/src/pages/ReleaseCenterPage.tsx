import { useEffect, useState } from "react";
import type { Notify } from "../App";
import { ReleaseV2Page } from "./ReleaseV2Page";
import { ReleaseV2SettingsPage } from "./ReleaseV2SettingsPage";

export type ReleaseTab = "create" | "runs" | "executors" | "settings";

const TABS: { key: ReleaseTab; label: string; hint: string }[] = [
  { key: "create", label: "新建发布", hint: "选项目、升级服务、构建机，交给 Agent" },
  { key: "runs", label: "发版运行", hint: "历次运行的状态、事件与发布结果" },
  { key: "executors", label: "构建执行器", hint: "注册、心跳与 SSH 部署构建机" },
  { key: "settings", label: "全局设置", hint: "升级服务地址/令牌、GitHub 与对象存储" },
];

// ReleaseCenterPage 把发版相关的三个页面收进一个入口：v2 运行、旧版发版、发版设置。
// 页面本身继续复用原有组件，旧路由（#release / #releaseV2 / #releaseSettings）
// 通过 initialTab 落到对应页签。
export function ReleaseCenterPage({ notify, initialTab = "create", runId, onNavigate, onOpenRun }: {
  notify: Notify;
  initialTab?: ReleaseTab;
  runId?: string;
  onNavigate?: (page: "releaseCenter") => void;
  onOpenRun?: (id: string) => void;
}) {
  const [tab, setTab] = useState<ReleaseTab>(initialTab);
  useEffect(() => {
    setTab(initialTab);
  }, [initialTab]);

  // 运行详情是独立页面（#releaseRun/<id>），不在这里叠卡片。
  if (runId) {
    return <ReleaseV2Page notify={notify} view="run" runId={runId} onBack={() => onNavigate?.("releaseCenter")} />;
  }

  return (
    <>
      <div className="release-tabs" role="tablist" aria-label="发版">
        {TABS.map((item) => (
          <button
            key={item.key}
            role="tab"
            aria-selected={tab === item.key}
            className={"release-tab" + (tab === item.key ? " active" : "")}
            onClick={() => setTab(item.key)}
          >
            <span>{item.label}</span>
            <small>{item.hint}</small>
          </button>
        ))}
      </div>
      {tab !== "settings" && <ReleaseV2Page notify={notify} view={tab} onOpenRun={onOpenRun} />}
      {tab === "settings" && <ReleaseV2SettingsPage notify={notify} />}
    </>
  );
}
