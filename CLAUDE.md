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
| Go | 1.27.3 (`go` line in `go.mod`; CI fails if it runs another version) |
| HTTP | Chi; problem details RFC 9457; JSON only |
| Database | PostgreSQL 17, gorm on the pgx driver, row-level security |
| Migrations | `migrations/NNNNNN_<name>.sql`, six-digit zero-padded, reversible or marked not; applied by `tenure migrate` |
| OpenAPI | swag v1.16.4 annotations on handlers, served at `/api/v1/openapi.json` |
| Scheduler | robfig/cron in process, `job_runs` as the lock (BE-OD-01) |
| Object store | minio-go: MinIO in docker-compose, Scaleway fr-par in production (OD-04, BE-OD-03) |
| PDF | gotenberg over HTTP, HTML → PDF/A-2b (OD-03, BE-OD-02) |
| Identity | Socrate through `github.com/ovander/backendkit` (pinned in `go.mod`) |
| Logs | `log/slog` JSON; backendkit's logrus entry is configured to JSON as well |
| Lint | golangci-lint v2.14.0 built with Go 1.27.3 (includes staticcheck), govulncheck, gitleaks |
| Build | `-ldflags "-X …version=… -X …commit=… -X …date=…"` version stamping |

Configuration comes only from `TENURE_*` environment variables, validated at start; the process
refuses to start on a missing or malformed value. Never add a default for a secret or for the
Socrate issuer, audience or JWKS URL.

## Layout and layering

```
cmd/tenure            serve | migrate | job <name> subcommands
internal/<module>     portfolio lease billing charge payment tax document params
internal/auth         the only package that reads a token (wraps backendkit)
internal/audit        audit.Record, hash chain, chain heads
internal/jobs         scheduler, job_runs lock, per-owner iteration
internal/httpapi      Chi routers, middleware, problem details, swag
internal/notify       e-mail
migrations/           SQL migrations
templates/<class>/<version>/   document templates (French)
testdata/golden/<year>/        golden files per calculation and tax year
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
  login route.** `owners.socrate_sub` is the only link between a person and their data. Machines
  use Socrate client-credential tokens. A feature that cannot be built on Socrate is an open
  question, never a local workaround.
- Owner, co-owner and accountant come from `owners` / `owner_grants`, never from a token claim;
  `admin` comes from the Socrate role claim. `X-Owner-Scope` selects among the caller's scopes
  (default: their own); an unknown or ungranted scope is 403.
- Deny by default: every route requires a valid token except `/healthz` and `/readyz`; a CI test
  walks the router and fails if any other route answers without one.
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
- Integration tests run PostgreSQL 17, MinIO and gotenberg through testcontainers.
- Never weaken a gate: no skipped or deleted test, no `t.Skip` or `//nolint` without a one-line
  reason, no required check removed.

## Local gate (what `ci` runs)

```bash
go mod tidy && git diff --exit-code go.mod go.sum
go build ./...
go vet ./...
golangci-lint run ./...          # v2.14.0 built with Go 1.27.3; staticcheck runs inside it
govulncheck ./...
go test -race -count=1 ./...
```

Integration tests and the migration up/down check need Docker. gitleaks runs in CI.

## Git workflow

- One GitHub issue per change, one branch and one PR per issue, linked; the issue names the spec
  section it implements. `main` is protected: PR required, check `ci` required. The owner merges.
- Branches `feat/<issue>-<slug>`, `fix/<issue>-<slug>`, `docs/…`, `chore/…`, `ci/…`.
- Conventional Commits (`feat:`, `fix:`, `spec:`, `docs:`, `chore:`, `ci:`, `test:`), one logical
  change per commit. Fill in the PR template (spec section, checklist).
- Never commit a secret: no keys, tokens, real client secrets or real tenant data, in code,
  tests, fixtures or examples. Use obviously fake values.
- Keep the CI job name `ci`. Do not tag releases unless asked.
