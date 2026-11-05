// 用 Chrome DevTools Protocol 截图，并注入管理台令牌（localStorage）。
// 用法：node scripts/web-shot.mjs <url> <out.png> [token]
import { spawn } from "node:child_process";
import { writeFileSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const [url, out, token = ""] = process.argv.slice(2);
if (!url || !out) {
  console.error("usage: node scripts/web-shot.mjs <url> <out.png> [token]");
  process.exit(2);
}

const chrome = process.env.CHROME_PATH || "C:/Program Files/Google/Chrome/Application/chrome.exe";
const port = 9333;
const profile = mkdtempSync(join(tmpdir(), "smartpipe-shot-"));

const child = spawn(chrome, [
  "--headless=new",
  "--disable-gpu",
  "--hide-scrollbars",
  "--no-first-run",
  "--window-size=1440,1000",
  "--remote-debugging-port=" + port,
  "--user-data-dir=" + profile,
  "about:blank",
], { stdio: "ignore" });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function waitFor(fn, tries = 60) {
  for (let i = 0; i < tries; i++) {
    try {
      const v = await fn();
      if (v) return v;
    } catch {}
    await sleep(250);
  }
  throw new Error("timeout waiting for chrome");
}

const target = await waitFor(async () => {
  const res = await fetch("http://127.0.0.1:" + port + "/json/list");
  const list = await res.json();
  return list.find((t) => t.type === "page");
});

const ws = new WebSocket(target.webSocketDebuggerUrl);
let id = 0;
const pending = new Map();
ws.addEventListener("message", (ev) => {
  const msg = JSON.parse(ev.data);
  if (msg.id && pending.has(msg.id)) {
    pending.get(msg.id)(msg);
    pending.delete(msg.id);
  }
});
await new Promise((resolve, reject) => {
  ws.addEventListener("open", resolve, { once: true });
  ws.addEventListener("error", reject, { once: true });
});

function send(method, params = {}) {
  const msgId = ++id;
  ws.send(JSON.stringify({ id: msgId, method, params }));
  return new Promise((resolve) => pending.set(msgId, resolve));
}

await send("Page.enable");
await send("Runtime.enable");
await send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });

await send("Page.navigate", { url });
await sleep(1500);
if (token) {
  const res = await send("Runtime.evaluate", {
    expression: "localStorage.setItem('smartpipe.adminToken', " + JSON.stringify(token) + "); localStorage.getItem('smartpipe.adminToken') !== null",
    returnByValue: true,
  });
  console.log("token injected: " + JSON.stringify(res.result?.result?.value));
  await send("Page.reload", { ignoreCache: false });
}
await sleep(4500);

const shot = await send("Page.captureScreenshot", { format: "png", captureBeyondViewport: true });
if (!shot.result?.data) {
  console.error(JSON.stringify(shot).slice(0, 400));
  process.exit(1);
}
writeFileSync(out, Buffer.from(shot.result.data, "base64"));
console.log("saved " + out);
ws.close();
child.kill();
process.exit(0);
