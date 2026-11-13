import { useEffect, useState } from "react";
import type { Notify } from "../App";
import { ReleasePage } from "./ReleasePage";
import { ReleaseV2Page } from "./ReleaseV2Page";
import { ReleaseV2SettingsPage } from "./ReleaseV2SettingsPage";

export type ReleaseTab = "runs" | "legacy" | "settings";

const TABS: { key: ReleaseTab; label: string; hint: string }[] = [
  { key: "runs", label: "发版运行", hint: "计划、构建机、制品与发布状态" },
  { key: "legacy", label: "旧版发版", hint: "标签候选、每日发版与回滚" },
  { key: "settings", label: "发版设置", hint: "升级服务、GitHub 与对象存储" },
];

// ReleaseCenterPage 把发版相关的三个页面收进一个入口：v2 运行、旧版发版、发版设置。
// 页面本身继续复用原有组件，旧路由（#release / #releaseV2 / #releaseSettings）
// 通过 initialTab 落到对应页签。
export function ReleaseCenterPage({ notify, initialTab = "runs" }: { notify: Notify; initialTab?: ReleaseTab }) {
  const [tab, setTab] = useState<ReleaseTab>(initialTab);
  useEffect(() => {
    setTab(initialTab);
  }, [initialTab]);

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
      {tab === "runs" && <ReleaseV2Page notify={notify} />}
      {tab === "legacy" && <ReleasePage notify={notify} />}
      {tab === "settings" && <ReleaseV2SettingsPage notify={notify} />}
    </>
  );
}
