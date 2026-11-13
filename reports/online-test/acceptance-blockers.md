# Outstanding product acceptance — 2026-11-13

This replaces the obsolete download/PG17 blockers in the earlier version.

## Completed

- SmartPipe main 21d2998 contains the actual command CLI integration test.
- Actual Node CLI → Iris HTTP → isolated PostgreSQL16 → evidence download → report → machine release → release gate: verified with fixture scripts. Six packages:111 PASS,0 FAIL,0 SKIP. See command-cli-verification.md and command-cli-integration.jsonl.
- Native archives completed and verified; Windows FFI load passed. See windows-native-load.json and mac-native-compatibility.md.
- Linux 192.168.1.99: sudo -n docker ps succeeds; smartpipe-app-1 and smartpipe-worker-1 healthy, existing smartpipe-postgres uses postgres:16. Rechecked this turn. PG17 is not a requirement.
- macOS 192.168.1.8: sw_vers rechecked, 12.4 build21F79.

## Remaining external inputs / constraints

1. Product acceptance needs an identified candidate package, its matching source commit and an independently controllable instance. No candidate has been established for actual installed-product acceptance. User was asked for candidate path and source SHA this turn.
2. The running Windows Syntropy hosts this assistant. Computer policy prohibits controlling the assistant's own UI. Do not bypass this through another automation channel. User also stopped browser automation; do not resume it implicitly.
3. The pinned macOS native library requires macOS13, while current host is12.4. Existing diagnostic is a load failure, not product acceptance. No OS upgrade is authorized or performed.

After these inputs are available: deploy the identified candidate in an isolated instance, implement real independent business assertions and restart/upgrade/cleanup checks, then evaluate the gate against those results.

Do not rebuild or fix Syntropy as a prerequisite: user explicitly rejected that scope drift. No product release has been performed. Repeating protocol tests or updating documentation does not complete product acceptance.
