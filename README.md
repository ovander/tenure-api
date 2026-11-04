# tenure-api

Tenure — rental management backend: Go/Chi, PostgreSQL RLS, hash-chained audit trail, Socrate/backendkit identity

Part of **Tenure**, a rental-management platform for houses, apartments and parking boxes under French law:
monthly fee (rent + provisional charges), receipts, syndic charge reconciliation, annual owner tax summary (2044),
contracts and guarantees, 10-year immutable archive.

- Specifications: `docs/spec/` (RENT-SPEC corpus, RENT-BE-001, RENT-FE-001) — the source of truth; code follows the spec.
- Identity: Socrate (OIDC) through `backendkit`; no local accounts.
- Roadmap: phase 1 Foundation → 2 Operations → 3 Finance → 4 Best in class.

## Status
Phase 1 — Foundation. Not yet usable.

## Development
See [CONTRIBUTING.md](CONTRIBUTING.md) and [CLAUDE.md](CLAUDE.md). Security reports: [SECURITY.md](SECURITY.md).

```bash
docker compose -f docker-compose.dev.yml up -d      # PostgreSQL 17, archive, gotenberg, ClamAV (amd64, via Rosetta on Apple Silicon), Mailpit
docker compose -f docker-compose.dev.yml run --rm archive-init   # once: bucket with object lock, no default retention
cp .env.example .env                                # then set the Socrate values
set -a; . ./.env; set +a
go run ./cmd/tenure migrate up
go run ./cmd/tenure serve                           # API on 127.0.0.1:8080, scheduler in process
go test ./... && go test -tags integration ./internal/itest
```

The phase 1 flows run in a browser against a real tenure with `./e2e/run.sh`. The script needs
Docker, Go and Node, and a built web bundle: either `./scripts/fetch-web.sh` or
`E2E_WEB_DIST=<path to a tenure-web dist>`. It starts its own services and test fakes, so stop a
running dev stack first.

## Licence
Proprietary — see [LICENSE](LICENSE).
