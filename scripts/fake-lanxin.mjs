// 本地假蓝信开放平台：appToken、媒体下载、发消息、WS 端点。
// 用法：node scripts/fake-lanxin.mjs [port]（默认 9092，媒体文件取自 .dev/lanxin-media/<id>）
import { createServer } from "node:http";
import { readFileSync, existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const port = Number(process.argv[2] || 9092);
const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const mediaDir = join(root, ".dev", "lanxin-media");
const sent = [];

const server = createServer((req, res) => {
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    const body = Buffer.concat(chunks).toString("utf8");
    const json = (status, payload) => {
      res.writeHead(status, { "content-type": "application/json" });
      res.end(JSON.stringify(payload));
    };
    const url = new URL(req.url, "http://127.0.0.1");

    if (url.pathname === "/v1/apptoken/create") {
      json(200, { errCode: 0, errMsg: "success", data: { appToken: "fake-app-token", expiresIn: 7200 } });
      return;
    }
    if (url.pathname === "/v1/ws/endpoint/create") {
      // 指向一个不存在的端口：WS 循环重连但不影响 HTTP 管理接口。
      json(200, { errCode: 0, errMsg: "success", data: { wsEndpoint: "ws://127.0.0.1:9093/ws" } });
      return;
    }
    const media = url.pathname.match(/^\/v1\/medias\/([^/]+)\/fetch$/);
    if (media) {
      const file = join(mediaDir, media[1]);
      if (!existsSync(file)) {
        json(404, { errCode: 404, errMsg: "media not found" });
        return;
      }
      const buf = readFileSync(file);
      res.writeHead(200, { "content-type": "application/octet-stream", "content-length": buf.length });
      res.end(buf);
      return;
    }
    if (req.method === "POST" && (url.pathname === "/v1/bot/messages/create" || url.pathname === "/v1/messages/group/create")) {
      let payload = {};
      try { payload = body ? JSON.parse(body) : {}; } catch { payload = { raw: body }; }
      sent.push({ at: new Date().toISOString(), path: url.pathname, payload });
      json(200, { errCode: 0, errMsg: "success", data: { msgId: "fake-" + sent.length } });
      return;
    }
    if (req.method === "GET" && url.pathname === "/__messages") {
      json(200, { sent });
      return;
    }
    if (req.method === "POST" && url.pathname === "/__reset") {
      sent.length = 0;
      json(200, { ok: true });
      return;
    }
    json(404, { errCode: 404, errMsg: "not found: " + url.pathname });
  });
});

server.listen(port, "127.0.0.1", () => {
  console.log("fake lanxin listening on " + port + " media=" + mediaDir);
});
