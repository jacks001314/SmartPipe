# Command CLI integration

Verified 2026-11-13 using Linux 192.168.1.99 and an isolated PostgreSQL 16 container; no business database changes.

Actual compiled worker-cli.js loads CommandScenario, claims a task over HTTP from Iris, verifies artifact/script digests, executes setup/check/cleanup child processes, uploads outcomes, submits report, and releases machine. Test asserts cleanup marker, removed local lock, downloaded evidence hash/content, persisted PASS, gate rejection before evidence and on changed source, gate success after matching evidence, and idle on second CLI invocation. Child check also asserts the machine token is not inherited.

Fixture artifact/script only: this does not establish installed desktop product acceptance or macOS compatibility.

Reproduce from ~/smartpipe-test-assets/evidence-verify (source/backend and source/sdks snapshot, worker.mjs, cli/{package.json,worker-cli.js,worker.js,command-scenario.js,session.js}):

```sh
SMARTPIPE_WORKER_CLI_JS="$PWD/cli/worker-cli.js" python3 run-existing-pg16.py
```

Six Go packages: 111 PASS events, 0 fail, 0 skip. Detailed adminapi events: command-cli-integration.jsonl. Summary: command-cli-summary.json.

Windows: npm --prefix test-runner test: 21 passed, 0 failed/skipped (includes TypeScript build).
