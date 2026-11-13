# Linux harness verification

The isolated PostgreSQL 18.3 harness now exits nonzero for failed commands or skipped tests, includes migrations 48–52 in its query, and preserves the cluster directory if shutdown fails.

- With SMARTPIPE_WORKER_JS set: process exit 0, 117 PASS / 0 FAIL / 0 SKIP across desktop-prefixed and full-package runs (counts include repeated tests).
- With SMARTPIPE_WORKER_JS omitted deliberately: process exit 1, 113 PASS / 0 FAIL / 4 SKIP; both TS worker tests are reported missing in both phases.
- Source: /tmp/smartpipe-verify-9442848/source/backend with previously transferred testlab patches; this is not a full validation of the latest merged main.
- This verifies harness failure detection, not desktop product acceptance or PostgreSQL 17 compatibility.
