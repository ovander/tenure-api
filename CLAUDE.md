# CLAUDE.md — tenure-api

Standing instructions for Claude Code in this repository. Read this file, `CONTRIBUTING.md` and
the spec sections a change touches before writing code. When this file and a spec disagree, the
spec wins; fix this file in the same PR.

## What this is

The backend of **Tenure**, a rental-management platform under French law (loi du 6 juillet 1989):
assets (house, apartment, box), leases and guarantees, monthly rent calls and receipts, IRL
revision, syndic charge reconciliation, the owner's annual tax summary (form 2044, micro-foncier
check), and a 10-year immutable document archive with a hash-chained audit trail. One Go binary,
`tenure`, serves the API, runs the scheduler and embeds the web bundle built by
`ovander/tenure-web`.

## Sources of truth, in order

1. `docs/spec/RENT-SPEC.md` — high-level corpus RENT-SPEC-001…022, roadmap, phase 1 slice order,
   open decisions. Every decision `OD-xx` is final.
2. `docs/spec/RENT-BE-001.md` — backend spec: module layout (§1), data model and RLS (§2), API
   (§3), identity (§4), jobs (§5), document pipeline (§6), legal parameters (§7), errors (§8),
   audit and security controls (§8b), repository and CI (§9), tests (§10). `BE-OD-xx` are final.
3. `docs/spec/RENT-FE-001.md` — the web app; read it for anything the API must serve the UI.
   `docs/spec/RENT-INV-001.md` — inventories, vétusté and deposit restitution (phase 2); its
   `INV-OD-xx` marked Decided are final, the others are proposals.
4. `CONTRIBUTING.md` — rules that are never relaxed.
5. `docs/IMPLEMENTATION-PLAN.md` — phase 1 slices: what each one builds and how it is accepted.
6. `docs/OPEN-QUESTIONS.md` — spec gaps and the provisional reading chosen for each.
7. The code, and `go doc github.com/ovander/backendkit/<pkg>` for the identity library.

Ground every change in these. If your idea differs from a spec, follow the spec and say so in the
PR. If a spec is ambiguous or silent, add an entry to `docs/OPEN-QUESTIONS.md`, take the most
conservative reading (fail closed, never overstate a tenant's debt or an owner's deduction), and
keep going. If a behaviour has to change, change the spec in the same PR (CONTRIBUTING).

## Stack and versions

| Concern | Choice |
| --- | --- |
| Go | 1.27.4 (`go` line in `go.mod`; CI fails if it runs another version) |
| HTTP | Chi; problem details RFC 9457; JSON only |
| Database | PostgreSQL 17, gorm on the pgx driver, row-level security |
| Migrations | `migrations/NNNNNN_<name>.sql`, six-digit zero-padded, reversible or marked not; applied by `tenure migrate` |
| OpenAPI | swag v1.16.4 annotations on handlers, served at `/api/v1/openapi.json` |
| Scheduler | robfig/cron in process, `job_runs` as the lock (BE-OD-01) |
| Object store | minio-go: Scaleway fr-par in production (OD-04, BE-OD-03); RustFS in docker-compose and the integration suite while no MinIO image can be pulled (Q-38) |
| PDF | gotenberg over HTTP, HTML → PDF/A-2b (OD-03, BE-OD-02) |
| Antivirus | ClamAV sidecar (clamd INSTREAM over TCP, `TENURE_CLAMAV_ADDR`); every upload is scanned, fail closed (§8b, Q-67) |
| E-signature | Yousign API v3 (OD-08) through `internal/esign`, optional: `TENURE_YOUSIGN_API_URL` and `TENURE_YOUSIGN_API_KEY`, both or neither; state pulled, no webhook (Q-112) |
| Identity | Socrate through `github.com/ovander/backendkit` (pinned in `go.mod`); browsers through a BFF (`backendkit/bff`), never holding a token |
| Logs | `log/slog` JSON; backendkit's logrus entry is configured to JSON as well |
| Lint | golangci-lint v2.14.0 built with Go 1.27.4 (includes staticcheck), govulncheck, gitleaks |
| Build | `-ldflags "-X …version=… -X …commit=… -X …date=…"` version stamping |

Configuration comes only from environment variables, validated at start: tenure's own `TENURE_*`,
and the Socrate client settings under Socrate's own names (`SOCRATE_ISSUER`, `SOCRATE_JWKS_URL`,
`SOCRATE_CLIENT_ID`, `SOCRATE_CLIENT_SECRET`, optional `SOCRATE_AUDIENCE`), so the block Socrate's
console gives pastes in as it is (Q-99). The process refuses to start on a missing or malformed
value, or on a former `TENURE_SOCRATE_*` name. Never add a default for a secret or for the Socrate
issuer, JWKS URL or client; the audience is the client id unless `SOCRATE_AUDIENCE` is set.

## Layout and layering

```
cmd/tenure            serve | migrate | job <name> subcommands
internal/<module>     portfolio lease billing charge payment tax document params inventory calendar loan ledger dashboard
internal/auth         the only package that reads a token (wraps backendkit)
internal/bff          browser sessions (backendkit bff): /bff routes, session → bearer; stores tokens, never parses them
internal/audit        audit.Record, hash chain, chain heads
internal/jobs         scheduler, job_runs lock, per-owner iteration
internal/httpapi      Chi routers, middleware, problem details, swag
internal/notify       e-mail (SMTP, FR/EN), smtptest for tests
internal/esign        the e-signature provider (Yousign, OD-08), esigntest for tests
internal/document     numbering, templates, PDF engine, archive store, signed links
internal/app          wiring of the router and the jobs (cmd/tenure and the integration suite)
internal/db           scoped transactions (app.owner_id, app.actor_sub), column types
internal/migrate      goose over the embedded migrations
internal/httpx        decode, ETag/If-Match, paging, service error → problem
internal/problem      RFC 9457 bodies and the FR/EN title catalogue
internal/invariant    Error{Type, Detail}, ErrNotFound/ErrStale/ErrForbidden, Fields
internal/itest        integration suite (build tag integration, testcontainers)
e2e/                  the phase 1 flows and the inventory lifecycle in Playwright (run.sh); e2e/fakes: Socrate, SMTP, INSEE (tag e2e, tests only)
migrations/           SQL migrations (embedded; seed generated from testdata/params)
deploy/sql/roles.sql  database roles, run once per cluster by a superuser
templates/<class>/<version>/   document templates (French, embedded); a change adds a version
testdata/golden/<year>/        golden files per calculation and tax year (internal/golden runs them)
testdata/params/<year>.json    legal parameter fixtures, the seed of legal_params
testdata/inventory/            inventory templates (French), the seed of inventory_templates (RENT-INV-001)
```

- **handler → service → repository**, one direction only. Handlers decode and type-check;
  services validate invariants, return `ErrInvariant{Type, Detail}`, own the transaction and the
  audit call; repositories run SQL and return storage errors only.
- **No SQL outside repositories** — no `db.Raw`, `Exec` or gorm query builder in a handler,
  service or job.
- Modules depend downward only, as in RENT-BE-001 §1 (billing never imports tax; nothing imports
  `internal/httpapi`). Calculations (`billing.ComputeFee`, `charge.Allocate`, `tax.Lines`,
  revision) are **pure functions with no I/O**: no database, clock, network or environment. The
  caller passes the date, the period and the parameter set.

## Isolation: RLS on every owner table

- Every table with `owner_id` has `ENABLE` and `FORCE ROW LEVEL SECURITY` and a policy on
  `owner_id = current_setting('app.owner_id', true)::uuid` (unset ⇒ zero rows, never all rows).
- `app.owner_id` is set with `set_config(…, true)` inside the transaction, by the auth/db layer,
  before any repository call. gorm models never add an owner filter themselves.
- Roles: `tenure_api` (no BYPASSRLS, no DDL), `tenure_migrate` (DDL, used only by
  `tenure migrate`), `tenure_jobs` (sets `app.owner_id` per owner iteration, no DDL).
- Co-owner and accountant access comes from `owner_grants` rows with `revoked_at` null, joined
  in the policy; a grant is revoked, never deleted.
- A cross-owner integration test must read zero rows for every owner table; a new owner table
  gets added to it.

## Audit: one event per write, same transaction

- Every service write calls `audit.Record(ctx, action, entity, before, after)` after the write
  and before commit, in the same transaction. No write without an audit event; no audit event
  outside the write's transaction.
- `audit_events` is INSERT-only (grants plus a trigger refusing UPDATE/DELETE).
  `hash = sha256(prev_hash ‖ canonical JSON of the row)`, computed in Go, serialised per owner by
  a transaction-scoped advisory lock.
- Every mutating route is listed in `testdata/audited_routes.txt`; the integration suite fails if
  one of them commits without an event, or if a mutating route is missing from the list.
- Mutable business tables carry `created_by/at`, `updated_by/at`, filled by the repository from
  the request context.

## Identity: Socrate only

- `internal/auth` wraps `backendkit/jwtauth` and is the only package that reads a token. No other
  package imports a JWT library or parses `Authorization`.
- **There is no `users` table, no password column, no API-key table, no magic link and no local
  login route** (`/bff/login` only redirects to Socrate). `owners.socrate_sub` is the only link between a person and their data. Machines
  use Socrate client-credential tokens. A feature that cannot be built on Socrate is an open
  question, never a local workaround.
- Owner, co-owner and accountant come from `owners` / `owner_grants`, never from a token claim;
  `admin` comes from the Socrate role claim. `X-Owner-Scope` selects among the caller's scopes
  (default: their own); an unknown or ungranted scope is 403.
- Deny by default: every route requires a valid token except `/healthz`, `/readyz` and the BFF's
  `/bff/*` (login, callback, session, logout); a CI test walks the router and fails if any
  `/api/v1` route answers without one.
- Browsers never hold a token (RENT-BE-001 §4): tenure is Socrate's confidential client, the
  session is a `__Host-` HttpOnly SameSite=Strict cookie, unsafe requests carry `X-CSRF-Token`,
  and `internal/bff` turns the session into a bearer in front of `internal/auth`.
- Routes tagged `step_up` (tax issue, grants, params, exports) require `auth_time` at most
  5 minutes old and `mfa` in `amr` (read through `internal/auth`'s accessor over backendkit's
  `ctxutil`), otherwise 401 `type: auth/step-up-required`; a missing `auth_time` is refused.

## Money, time and legal constants

- Money is `int64` cents everywhere (`*_cents` columns and JSON fields). No `float` near money,
  shares or indices; IRL values and percentages are integers with a fixed scale.
- Rounding (RENT-BE-001 §7): fees to the cent half-up; IRL revision to the cent; provisions
  proposed to the euro, rounded up. Use the shared helpers, never ad-hoc arithmetic.
- Timestamps `timestamptz` in UTC; dates as `date`; periods `char(7)` `YYYY-MM`. Calendar rules
  (the 1st of the month, due day) are evaluated in Europe/Paris and stored in UTC.
- **Every legal constant lives in `legal_params`** with `effective_from` and `source_url`, read
  through `internal/params` as of the period being computed. No threshold, rate, month count or
  form line number in Go code — tests included, which load fixtures.

## Language

- French is the default language of every user-facing string: problem-detail `title`/`detail`,
  e-mails, notifications. English is optional per owner or tenant (`language` column) for e-mails
  and API messages only.
- Generated documents (contracts, rent calls, receipts, statements, tax summary) are **French
  only**, whatever the recipient's language.
- Code, identifiers, API field names, log messages and comments are in English.

## Tests

- Every calculation has table tests **and golden files** under `testdata/golden/<year>/<case>.json`
  (inputs, parameter set, expected lines). A parameter change for year N+1 must never move a
  year-N golden. Regenerate only with the documented `-update` flag, and review the diff.
- State machines (lease, reconciliation) are tested exhaustively: every state × every transition.
- Integration tests run PostgreSQL 17, the S3 store (RustFS, Q-38), gotenberg and ClamAV through
  testcontainers, with an in-process SMTP server (`notify/smtptest`).
- Never weaken a gate: no skipped or deleted test, no `t.Skip` or `//nolint` without a one-line
  reason, no required check removed.

## Local gate (what `ci` runs)

```bash
go mod tidy && git diff --exit-code go.mod go.sum
go build ./...
go vet -tags integration,e2e ./...
golangci-lint run --build-tags integration,e2e ./...   # v2.14.0 built with Go 1.27.4; staticcheck runs inside it
govulncheck ./...
go test -race -count=1 ./...
go test -tags integration -race -count=1 ./internal/itest   # needs Docker
./scripts/openapi.sh && git diff --exit-code internal/httpapi/openapi   # swag v1.16.4
./scripts/fetch-web.sh && ./e2e/run.sh   # the phase 1 flows and the inventory lifecycle in a browser; Docker, Node; or E2E_WEB_DIST=<dist>
```

gitleaks runs in CI. A new mutating route goes into `testdata/audited_routes.txt` and the
integration scenario; a new owner table into the cross-owner zero-rows check; a new problem type
into its module's `problem.Register` with French and English titles; a change to
`testdata/params` is followed by `go test ./migrations -update`.

## Git workflow

- One GitHub issue per change, one branch and one PR per issue, linked; the issue names the spec
  section it implements. `main` is protected: PR required, check `ci` required. The owner merges.
- Branches `feat/<issue>-<slug>`, `fix/<issue>-<slug>`, `docs/…`, `chore/…`, `ci/…`.
- Conventional Commits (`feat:`, `fix:`, `spec:`, `docs:`, `chore:`, `ci:`, `test:`), one logical
  change per commit. Fill in the PR template (spec section, checklist).
- Never commit a secret: no keys, tokens, real client secrets or real tenant data, in code,
  tests, fixtures or examples. Use obviously fake values.
- Keep the CI job name `ci`. Do not tag releases unless asked.
