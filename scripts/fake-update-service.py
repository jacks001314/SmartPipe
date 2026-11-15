#!/usr/bin/env python3
"""Minimal in-memory update service used to verify release_import/promote.

It implements only the endpoints the SmartPipe release adapters call:

  POST /v1/admin/components
  GET  /v1/admin/releases?component=...
  POST /v1/admin/releases
  GET  /v1/admin/releases/{id}
  PUT  /v1/admin/releases/{id}/artifacts/{slot}/{os}/{arch}
  POST /v1/admin/releases/{id}/publish
  GET  /v1/admin/distributions?channel=...
  POST /v1/admin/distributions
  GET  /v1/distributions/latest?channel=...

Test helpers:

  GET  /__state            current components/releases/distributions
  POST /__fail_uploads     {"on": true|false, "count": N} make artifact uploads
                           return 503; with count>0 only the next N uploads fail
                           and the switch clears itself afterwards

Usage:
  python3 fake-update-service.py --port 18100
"""

import argparse
import hashlib
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, unquote, urlparse

STATE = {
    "components": {},
    "releases": {},
    "distributions": [],
    "fail_uploads": False,
    "fail_remaining": 0,
    "next_release": 1,
    "next_artifact": 1,
}

LOCK = threading.Lock()


def now_ms():
    return int(time.time() * 1000)


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        print("%s - %s" % (self.address_string(), fmt % args), flush=True)

    def _json(self, status, payload):
        raw = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def _body(self):
        length = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(length) if length else b""

    def _release(self, release_id):
        return STATE["releases"].get(release_id)

    def do_GET(self):
        parsed = urlparse(self.path)
        path = parsed.path
        query = parse_qs(parsed.query)
        with LOCK:
            if path == "/healthz":
                return self._json(200, {"status": "ok", "service": "fake-update"})
            if path == "/__state":
                return self._json(200, STATE)
            if path == "/v1/admin/releases":
                component = (query.get("component") or [""])[0]
                items = [r for r in STATE["releases"].values() if not component or r["component"] == component]
                return self._json(200, {"releases": items})
            if path == "/v1/admin/distributions":
                channel = (query.get("channel") or [""])[0]
                items = [d for d in STATE["distributions"] if not channel or d["channel"] == channel]
                return self._json(200, {"distributions": items})
            if path == "/v1/distributions/latest":
                channel = (query.get("channel") or ["stable"])[0]
                published = [d for d in STATE["distributions"] if d["channel"] == channel]
                if not published:
                    return self._json(404, {"error": "no distribution for " + channel})
                latest = published[-1]
                payload = {
                    "channel": latest["channel"],
                    "productVersion": latest["productVersion"],
                    "publishedAt": latest.get("publishedAt", ""),
                    "bomSha256": latest["bomSha256"],
                    "components": latest["components"],
                }
                return self._json(200, {"payload": payload, "signature": "fake-signature"})
            if path.startswith("/v1/admin/releases/"):
                release_id = unquote(path.split("/v1/admin/releases/", 1)[1])
                release = self._release(release_id)
                if not release:
                    return self._json(404, {"error": "release not found"})
                return self._json(200, {"release": release, "artifacts": release["artifacts"]})
        return self._json(404, {"error": "not found"})

    def do_POST(self):
        parsed = urlparse(self.path)
        path = parsed.path
        body = self._body()
        payload = {}
        if body:
            try:
                payload = json.loads(body)
            except json.JSONDecodeError:
                return self._json(400, {"error": "invalid json"})
        with LOCK:
            if path == "/__fail_uploads":
                STATE["fail_uploads"] = bool(payload.get("on"))
                try:
                    STATE["fail_remaining"] = max(0, int(payload.get("count") or 0))
                except (TypeError, ValueError):
                    STATE["fail_remaining"] = 0
                return self._json(200, {"fail_uploads": STATE["fail_uploads"],
                                        "fail_remaining": STATE["fail_remaining"]})
            if path == "/v1/admin/components":
                name = (payload.get("name") or "").strip()
                if not name:
                    return self._json(400, {"error": "name required"})
                STATE["components"].setdefault(name, {"name": name})
                return self._json(200, STATE["components"][name])
            if path == "/v1/admin/releases":
                component = (payload.get("component") or "").strip()
                version = (payload.get("version") or "").strip()
                channel = (payload.get("channel") or "").strip()
                if not component or not version or not channel:
                    return self._json(400, {"error": "component, version and channel required"})
                for release in STATE["releases"].values():
                    if release["component"] == component and release["version"] == version and release["channel"] == channel:
                        return self._json(200, release)
                release_id = "rel_%d" % STATE["next_release"]
                STATE["next_release"] += 1
                release = {
                    "id": release_id,
                    "component": component,
                    "version": version,
                    "channel": channel,
                    "publishedAtMs": 0,
                    "createdAtMs": now_ms(),
                    "yanked": False,
                    "artifacts": [],
                }
                STATE["releases"][release_id] = release
                return self._json(200, release)
            if path.startswith("/v1/admin/releases/") and path.endswith("/publish"):
                release_id = unquote(path[len("/v1/admin/releases/"):-len("/publish")])
                release = self._release(release_id)
                if not release:
                    return self._json(404, {"error": "release not found"})
                release["publishedAtMs"] = now_ms()
                return self._json(200, release)
            if path == "/v1/admin/distributions":
                channel = (payload.get("channel") or "").strip()
                product_version = (payload.get("productVersion") or "").strip()
                components = payload.get("components") or []
                if not channel or not product_version or not components:
                    return self._json(400, {"error": "channel, productVersion and components required"})
                resolved = []
                for item in components:
                    name = (item.get("name") or "").strip()
                    version = (item.get("version") or "").strip()
                    match = next((r for r in STATE["releases"].values()
                                  if r["component"] == name and r["version"] == version and r["channel"] == channel), None)
                    if not match:
                        return self._json(404, {"error": "release not found for %s@%s" % (name, version)})
                    resolved.append({"name": name, "version": version, "releaseId": match["id"]})
                digest = hashlib.sha256(json.dumps([channel, product_version, resolved], sort_keys=True).encode()).hexdigest()
                distribution = {
                    "channel": channel,
                    "productVersion": product_version,
                    "components": resolved,
                    "bomSha256": digest,
                    "publishedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                }
                STATE["distributions"].append(distribution)
                return self._json(200, distribution)
        return self._json(404, {"error": "not found"})

    def do_PUT(self):
        parsed = urlparse(self.path)
        path = parsed.path
        marker = "/artifacts/"
        if not path.startswith("/v1/admin/releases/") or marker not in path:
            return self._json(404, {"error": "not found"})
        release_id, rest = path[len("/v1/admin/releases/"):].split(marker, 1)
        release_id = unquote(release_id)
        slot, platform_os, arch = (unquote(part) for part in rest.split("/"))
        body = self._body()
        with LOCK:
            if STATE["fail_uploads"]:
                if STATE["fail_remaining"] > 0:
                    STATE["fail_remaining"] -= 1
                    if STATE["fail_remaining"] == 0:
                        STATE["fail_uploads"] = False
                return self._json(503, {"error": "upload disabled for testing"})
            release = self._release(release_id)
            if not release:
                return self._json(404, {"error": "release not found"})
            digest = hashlib.sha256(body).hexdigest()
            expected = (self.headers.get("X-Sha256") or "").strip().lower()
            if expected and expected != digest:
                return self._json(409, {"error": "sha256 mismatch"})
            artifact_id = "art_%d" % STATE["next_artifact"]
            STATE["next_artifact"] += 1
            artifact = {
                "id": artifact_id,
                "releaseId": release_id,
                "slot": slot,
                "os": "" if platform_os == "any" else platform_os,
                "arch": "" if arch == "any" else arch,
                "filename": self.headers.get("X-Filename") or slot,
                "size": len(body),
                "sha256": digest,
            }
            release["artifacts"] = [a for a in release["artifacts"]
                                    if not (a["slot"] == artifact["slot"] and a["os"] == artifact["os"] and a["arch"] == artifact["arch"])]
            release["artifacts"].append(artifact)
            return self._json(200, artifact)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=18100)
    args = parser.parse_args()
    server = ThreadingHTTPServer((args.host, args.port), Handler)
    print("fake update service on http://%s:%d" % (args.host, args.port), flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
