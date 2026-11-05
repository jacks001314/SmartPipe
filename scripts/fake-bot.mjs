// 本地假蓝信 bot：只实现 /internal/reply，用于验证桥接回执路径。
// 用法：node scripts/fake-bot.mjs [port]
import { createServer } from "node:http";

const port = Number(process.argv[2] || 9091);
const replies = [];

const server = createServer((req, res) => {
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    const body = Buffer.concat(chunks).toString("utf8");
    const json = (status, payload) => {
      res.writeHead(status, { "content-type": "application/json" });
      res.end(JSON.stringify(payload));
    };
    if (req.method === "POST" && req.url === "/internal/reply") {
      let payload = {};
      try { payload = body ? JSON.parse(body) : {}; } catch { payload = { raw: body }; }
      replies.push({ at: new Date().toISOString(), token: req.headers["x-internal-token"] ?? "", payload });
      json(200, { ok: true });
      return;
    }
    if (req.method === "GET" && req.url === "/__replies") {
      json(200, { replies });
      return;
    }
    json(404, { message: "not found", path: req.url });
  });
});

server.listen(port, "127.0.0.1", () => {
  console.log("fake bot listening on " + port);
});
