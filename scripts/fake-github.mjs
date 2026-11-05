// 本地假 GitHub：实现桥接与发版用到的接口，便于无网络/无 token 联调。
// 用法：node scripts/fake-github.mjs [port]
// 测试辅助：POST /__issues 播种 Issue；POST /__branch {sha} 设置分支 HEAD；
//           GET /__state 读取 issues/comments/labels/releases/tags。
import { createServer } from "node:http";

const port = Number(process.argv[2] || 9099);
let seq = 100;
let commentSeq = 0;
let releaseSeq = 5000;
let branchSHA = "0000000000000000000000000000000000000000";
const issues = new Map(); // number -> {number,title,body,state,html_url,author,labels:Set}
const comments = [];
const labels = []; // 审计：{issue,label}
const releases = [];
const tags = new Map(); // tag -> sha

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
    const body = Buffer.concat(chunks).toString("utf8");
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
    if (req.method === "POST" && path === "/__branch") {
      const payload = body ? JSON.parse(body) : {};
      branchSHA = payload.sha ?? branchSHA;
      json(200, { sha: branchSHA });
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
        branch_sha: branchSHA,
      });
      return;
    }
    json(404, { message: "not found", path: req.url });
  });
});

server.listen(port, "127.0.0.1", () => {
  console.log("fake github listening on " + port);
});