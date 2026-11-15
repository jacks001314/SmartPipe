// 本地假 GitHub：实现桥接与发版用到的接口，便于无网络/无 token 联调。
// 用法：node scripts/fake-github.mjs [port]
// 测试辅助：POST /__issues 播种 Issue；POST /__branch {sha} 设置分支 HEAD；
//           GET /__state 读取 issues/comments/labels/releases/tags。
import { createServer } from "node:http";
import { createHash } from "node:crypto";

const port = Number(process.argv[2] || 9099);
const host = process.env.FAKE_GITHUB_HOST || "127.0.0.1";
let seq = 100;
let commentSeq = 0;
let releaseSeq = 5000;
let holdPublish = false; // 模拟 GitHub 短暂不可用，用来演练 partially_published 与续跑
// holdAfter > 0 时按次数模拟故障：前 holdAfter 次「公开草稿」PATCH 放行，
// 之后一律 503。用它可以把「github_release_sync 成功、release_publish 收口失败」
// 这个 order 确定性地做出来，不依赖在两次调用之间抢时间窗口。
let holdAfter = 0;
let publishAttempts = 0;
let branchSHA = "0000000000000000000000000000000000000000";
const issues = new Map(); // number -> {number,title,body,state,html_url,author,labels:Set}
const comments = [];
const labels = []; // 审计：{issue,label}
const releases = [];
const tags = new Map(); // tag -> sha
const userAssets = []; // 聊天图片上传：{id,name,content_type,size,repository_id}

function issueJSON(issue) {
  return {
    number: issue.number,
    title: issue.title,
    body: issue.body ?? "",
    state: issue.state ?? "open",
    html_url: issue.html_url,
    user: { login: issue.author ?? "reporter" },
    labels: [...issue.labels].map((name) => ({ name })),
    created_at: issue.created_at,
  };
}

const server = createServer((req, res) => {
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    // 发行资产可能上 GB；把它们整体拼成一个 Buffer 再转字符串会撞上 V8 的
    // 字符串长度上限。资产分支只按块流式计算大小与摘要，其余 JSON 请求才拼包。
    const assetUploadPath = /^\/repos\/[^/]+\/[^/]+\/releases\/\d+\/assets(\?|$)/.test(req.url || "");
    const rawBody = req.method === "POST" && assetUploadPath ? null : Buffer.concat(chunks);
    const body = rawBody ? rawBody.toString("utf8") : "";
    const json = (status, payload) => {
      res.writeHead(status, { "content-type": "application/json" });
      res.end(JSON.stringify(payload));
    };
    const url = new URL(req.url || "/", "http://127.0.0.1:" + port);
    const path = url.pathname;

    if (req.method === "POST" && path === "/__issues") {
      const payload = body ? JSON.parse(body) : {};
      const number = Number(payload.number || ++seq);
      seq = Math.max(seq, number);
      const issue = {
        number,
        title: payload.title ?? "",
        body: payload.body ?? "",
        state: payload.state ?? "open",
        html_url: "http://127.0.0.1:" + port + "/issues/" + number,
        author: payload.author ?? "reporter",
        labels: new Set(payload.labels ?? []),
        created_at: new Date().toISOString(),
      };
      issues.set(number, issue);
      json(201, issueJSON(issue));
      return;
    }
    if (req.method === "POST" && path === "/__hold") {
      const payload = body ? JSON.parse(body) : {};
      holdPublish = Boolean(payload.on);
      if (payload.after !== undefined) {
        holdAfter = Number(payload.after) || 0;
        publishAttempts = 0;
      }
      json(200, { hold: holdPublish, after: holdAfter, attempts: publishAttempts });
      return;
    }
    if (req.method === "POST" && path === "/__branch") {
      const payload = body ? JSON.parse(body) : {};
      branchSHA = payload.sha ?? branchSHA;
      json(200, { sha: branchSHA });
      return;
    }
    // GitHub 的 user-attachments 上传接口：Issue 正文里的图片靠它拿 URL。
    if (req.method === "POST" && path === "/user-attachments/assets") {
      const id = "asset-" + (userAssets.length + 1);
      userAssets.push({
        id,
        name: url.searchParams.get("name") || "attachment",
        content_type: url.searchParams.get("content_type") || "application/octet-stream",
        size: Buffer.concat(chunks).length,
        repository_id: url.searchParams.get("repository_id") || "",
      });
      json(201, { url: "http://127.0.0.1:" + port + "/user-attachments/assets/" + id });
      return;
    }
    if (req.method === "POST" && /^\/repos\/[^/]+\/[^/]+\/issues$/.test(path)) {
      const payload = body ? JSON.parse(body) : {};
      seq += 1;
      const issue = {
        number: seq, title: payload.title ?? "", body: payload.body ?? "", state: "open",
        html_url: "http://127.0.0.1:" + port + "/issues/" + seq,
        author: "reporter", labels: new Set(payload.labels ?? []), created_at: new Date().toISOString(),
      };
      issues.set(seq, issue);
      json(201, issueJSON(issue));
      return;
    }
    const issueMatch = /^\/repos\/[^/]+\/[^/]+\/issues\/(\d+)$/.exec(path);
    if (issueMatch && req.method === "GET") {
      const issue = issues.get(Number(issueMatch[1]));
      if (!issue) return json(404, { message: "Not Found" });
      json(200, issueJSON(issue));
      return;
    }
    if (req.method === "GET" && /^\/repos\/[^/]+\/[^/]+\/issues$/.test(path)) {
      const want = url.searchParams.get("labels");
      const state = url.searchParams.get("state") || "open";
      const list = [...issues.values()].filter((i) => {
        if (state !== "all" && i.state !== state) return false;
        if (!want) return true;
        return want.split(",").every((l) => i.labels.has(l.trim()));
      });
      json(200, list.map(issueJSON));
      return;
    }
    const commentMatch = /^\/repos\/[^/]+\/[^/]+\/issues\/(\d+)\/comments$/.exec(path);
    if (req.method === "POST" && commentMatch) {
      const payload = body ? JSON.parse(body) : {};
      commentSeq += 1;
      const comment = { id: commentSeq, issue: Number(commentMatch[1]), body: payload.body ?? "" };
      comments.push(comment);
      json(201, comment);
      return;
    }
    const labelMatch = /^\/repos\/[^/]+\/[^/]+\/issues\/(\d+)\/labels$/.exec(path);
    if (req.method === "POST" && labelMatch) {
      const payload = body ? JSON.parse(body) : {};
      const added = payload.labels ?? [];
      const issue = issues.get(Number(labelMatch[1]));
      for (const label of added) {
        labels.push({ issue: Number(labelMatch[1]), label });
        if (issue) issue.labels.add(label);
      }
      json(200, added);
      return;
    }
    const labelDeleteMatch = /^\/repos\/[^/]+\/[^/]+\/issues\/(\d+)\/labels\/(.+)$/.exec(path);
    if (req.method === "DELETE" && labelDeleteMatch) {
      const name = decodeURIComponent(labelDeleteMatch[2]);
      const issue = issues.get(Number(labelDeleteMatch[1]));
      if (!issue || !issue.labels.has(name)) return json(404, { message: "Label does not exist" });
      issue.labels.delete(name);
      labels.push({ issue: Number(labelDeleteMatch[1]), label: "-" + name });
      res.writeHead(204);
      res.end();
      return;
    }
    const commitMatch = /^\/repos\/[^/]+\/[^/]+\/commits\/(.+)$/.exec(path);
    if (req.method === "GET" && commitMatch) {
      json(200, { sha: branchSHA });
      return;
    }
    const tagRefMatch = /^\/repos\/[^/]+\/[^/]+\/git\/ref\/tags\/(.+)$/.exec(path);
    if (req.method === "GET" && tagRefMatch) {
      const tag = decodeURIComponent(tagRefMatch[1]);
      if (!tags.has(tag)) return json(404, { message: "Not Found" });
      json(200, { ref: "refs/tags/" + tag, object: { sha: tags.get(tag), type: "commit" } });
      return;
    }
    if (req.method === "POST" && /^\/repos\/[^/]+\/[^/]+\/git\/refs$/.test(path)) {
      const payload = body ? JSON.parse(body) : {};
      const ref = String(payload.ref || "");
      if (!ref.startsWith("refs/tags/")) return json(422, { message: "only tag refs supported" });
      const tag = ref.slice("refs/tags/".length);
      if (tags.has(tag)) return json(422, { message: "Reference already exists" });
      tags.set(tag, payload.sha);
      json(201, { ref, object: { sha: payload.sha, type: "commit" } });
      return;
    }
    const tagRefDelete = /^\/repos\/[^/]+\/[^/]+\/git\/refs\/tags\/(.+)$/.exec(path);
    if (req.method === "DELETE" && tagRefDelete) {
      const tag = decodeURIComponent(tagRefDelete[1]);
      if (!tags.delete(tag)) return json(404, { message: "Not Found" });
      res.writeHead(204);
      res.end();
      return;
    }
    if (req.method === "POST" && /^\/repos\/[^/]+\/[^/]+\/releases$/.test(path)) {
      const payload = body ? JSON.parse(body) : {};
      releaseSeq += 1;
      const release = {
        id: releaseSeq,
        tag_name: payload.tag_name ?? "",
        name: payload.name ?? "",
        body: payload.body ?? "",
        target_commitish: payload.target_commitish ?? "",
        html_url: "http://127.0.0.1:" + port + "/releases/" + releaseSeq,
        created_at: new Date().toISOString(),
      };
      releases.push(release);
      json(201, release);
      return;
    }
    // 仓库元信息：发版前用它确认凭据对目标仓库有权限。
    const repoMeta = /^\/repos\/([^/]+)\/([^/]+)$/.exec(path);
    if (req.method === "GET" && repoMeta) {
      json(200, { id: 1, full_name: decodeURIComponent(repoMeta[1]) + "/" + decodeURIComponent(repoMeta[2]), private: true });
      return;
    }
    // 按 tag 查 Release：同版本重发时要复用已有 Release，而不是建撞名草稿。
    const releaseByTag = /^\/repos\/[^/]+\/[^/]+\/releases\/tags\/(.+)$/.exec(path);
    if (req.method === "GET" && releaseByTag) {
      const tag = decodeURIComponent(releaseByTag[1]);
      const rel = releases.find((r) => r.tag_name === tag);
      if (!rel) return json(404, { message: "Not Found" });
      json(200, rel);
      return;
    }
    // 删除附件：已发布 Release 不能覆盖同名附件，重发前先删旧件。
    const assetDelete = /^\/repos\/[^/]+\/[^/]+\/releases\/assets\/(\d+)$/.exec(path);
    if (req.method === "DELETE" && assetDelete) {
      const assetID = Number(assetDelete[1]);
      for (const rel of releases) {
        const list = rel.assets || [];
        const idx = list.findIndex((a) => a.id === assetID);
        if (idx >= 0) {
          list.splice(idx, 1);
          res.writeHead(204);
          res.end();
          return;
        }
      }
      return json(404, { message: "Not Found" });
    }
    // PATCH 单条 release：公开草稿就是 PATCH {draft:false}。
    const releasePatch = /^\/repos\/[^/]+\/[^/]+\/releases\/(\d+)$/.exec(path);
    if (req.method === "PATCH" && releasePatch) {
      const id = Number(releasePatch[1]);
      const rel = releases.find((r) => r.id === id);
      if (!rel) return json(404, { message: "Not Found" });
      const payload = body ? JSON.parse(body) : {};
      // 只有「公开草稿」这一步算发布动作；改名/改说明不消耗 holdAfter 配额。
      if (payload.draft === false) {
        console.log(`[hold] publish patch release=${id} attempts=${publishAttempts} holdAfter=${holdAfter} hold=${holdPublish}`);
        if (holdAfter > 0) {
          if (publishAttempts >= holdAfter) return json(503, { message: "service unavailable" });
          publishAttempts += 1;
        } else if (holdPublish) {
          return json(503, { message: "service unavailable" });
        }
      }
      if (payload.draft === false && !rel.published) {
        rel.published = true;
        rel.published_at = new Date().toISOString();
      }
      if (payload.draft === true) rel.published = false;
      for (const key of ["name", "body", "tag_name", "target_commitish", "prerelease"]) {
        if (payload[key] !== undefined) rel[key] = payload[key];
      }
      json(200, rel);
      return;
    }
    // GET 单条 release：发版收口时会回读草稿，核对附件是否齐全。
    const releaseGet = /^\/repos\/[^/]+\/[^/]+\/releases\/(\d+)$/.exec(path);
    if (req.method === "GET" && releaseGet) {
      const id = Number(releaseGet[1]);
      const rel = releases.find((r) => r.id === id);
      if (!rel) return json(404, { message: "Not Found" });
      json(200, rel);
      return;
    }
    const assetUpload = /^\/repos\/[^/]+\/[^/]+\/releases\/(\d+)\/assets$/.exec(path);
    if (req.method === "POST" && assetUpload) {
      const id = Number(assetUpload[1]);
      const rel = releases.find((r) => r.id === id);
      if (!rel) return json(404, { message: "Not Found" });
      const name = url.searchParams.get("name") || "asset";
      rel.assets = rel.assets || [];
      const hasher = createHash("sha256");
      let size = 0;
      for (const chunk of chunks) {
        size += chunk.length;
        hasher.update(chunk);
      }
      const asset = {
        id: rel.assets.length + 1,
        name,
        // 附件是二进制，必须按原始字节算长度与摘要；按 UTF-8 解码会算错大小。
        size,
        digest: "sha256:" + hasher.digest("hex"),
        browser_download_url: "http://127.0.0.1:" + port + "/releases/" + id + "/assets/" + encodeURIComponent(name),
      };
      rel.assets.push(asset);
      json(201, asset);
      return;
    }
    const releaseDelete = /^\/repos\/[^/]+\/[^/]+\/releases\/(\d+)$/.exec(path);
    if (req.method === "DELETE" && releaseDelete) {
      const id = Number(releaseDelete[1]);
      const idx = releases.findIndex((r) => r.id === id);
      if (idx < 0) return json(404, { message: "Not Found" });
      releases.splice(idx, 1);
      res.writeHead(204);
      res.end();
      return;
    }
    if (req.method === "POST" && path === "/__reset") {
      issues.clear();
      comments.length = 0;
      labels.length = 0;
      releases.length = 0;
      tags.clear();
      userAssets.length = 0;
      seq = 100;
      commentSeq = 0;
      branchSHA = "0000000000000000000000000000000000000000";
      json(200, { ok: true });
      return;
    }
    if (req.method === "GET" && path === "/__state") {
      json(200, {
        issues: [...issues.values()].map(issueJSON),
        comments,
        labels,
        releases,
        tags: [...tags.entries()].map(([tag, sha]) => ({ tag, sha })),
        attachments: userAssets,
        branch_sha: branchSHA,
        hold: holdPublish,
        hold_after: holdAfter,
        publish_attempts: publishAttempts,
      });
      return;
    }
    json(404, { message: "not found", path: req.url });
  });
});

server.listen(port, host, () => {
  console.log("fake github listening on " + host + ":" + port);
});
