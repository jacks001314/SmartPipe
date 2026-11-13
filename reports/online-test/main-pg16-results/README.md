# Main revision database verification

Backend and sdks source exported using git archive from main 665a20dd62c8e00fc0c03ba056b2396aa950a690, extracted into a new Linux directory /tmp/smartpipe-main-665a20d/source. No old source overlay was used.

Used the already installed postgres:16 Docker image with authorized sudo, unique temporary container, loopback-only ephemeral port, and isolated configuration paths. Actual server PostgreSQL 16.15. Six packages testdb/store/testlab/adminapi/releasemcp/patch: 108 passing test events, 0 failures, 0 skipped. Counts include subtests, not necessarily 108 top-level cases. Process exit 0. Container and its anonymous volumes removed.

Real compiled TS worker module SHA256: 23630442623fc13be7178618b159b7d499081c3f7cb512e657039bd3a76594ed. The HTTP worker tests exercise success and server cancellation against the real backend/database; their scenario remains a fixture, not Syntropy desktop acceptance.
