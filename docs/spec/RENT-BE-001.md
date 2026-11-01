# Tenure — Backend specification (RENT-BE-001)

Nov 1, 2026 · @Olivier

## 1. Scope and module layout

One Go binary, `tenure`, serves the API and runs the scheduler; it is cut into eight modules that depend downward only, so billing never imports tax and nothing imports the HTTP layer.

| Module | Package | Owns | Depends on |
| --- | --- | --- | --- |
| portfolio | `internal/portfolio` | Owner, Asset, Tenant, ownership shares | — |
| lease | `internal/lease` | Lease, Guarantee, Inventory, lifecycle state machine, contract generation (RENT-SPEC-008) | portfolio, document, params |
| billing | `internal/billing` | Monthly fee, rent call, receipt, revision, arrears (RENT-SPEC-003) | lease, payment, document, params |
| charge | `internal/charge` | Charges ledger, syndic import, reconciliation (RENT-SPEC-007) | portfolio, lease, billing, document |
| payment | `internal/payment` | Payments, matching to billing documents, bank import | lease |
| tax | `internal/tax` | Annual summary, regime routing, 2044 lines (RENT-SPEC-004) | payment, charge, document, params |
| document | `internal/document` | Numbering, PDF rendering, archive metadata, object store client (RENT-SPEC-005) | params |
| params | `internal/params` | Legal parameters with effective dates, IRL index cache | — |

Cross-cutting packages: `internal/auth` (backendkit, roles, RLS context), internal/audit (audit events, hash chain, heads), `internal/jobs` (scheduler, job log), `internal/notify` (e-mail), `internal/httpapi` (Chi routers, handlers, swag annotations). Phase 2 to 4 modules (onboarding, calendar, maintenance, portal, capture) are added as new packages under the same rule.

&#91;embedded content: module map · 8 modules, 3 entry packages\]

Arrows read « imports »; billing is the busiest module and the one the golden tests target first.

## 2. Data model and row-level security

Every business table carries `owner_id`, and PostgreSQL row-level security filters on it from a session variable set by the auth middleware; gorm models never add an owner filter themselves.

| Table | Key columns | Notes |
| --- | --- | --- |
| owners | id, socrate\_sub, legal\_name, address, tax\_id, language (fr default, en), created\_at | One row per Socrate identity acting as owner |
| assets | id, owner\_id, kind (house/apartment/box), address, lot, surface\_m2, furnished, dpe\_class, acquired\_on, acquisition\_cents, parent\_asset\_id | parent\_asset\_id links a box to a dwelling |
| asset\_shares | asset\_id, owner\_id, share\_pct | Co-ownership; shares sum to 100 |
| tenants | id, owner\_id, names\[\], email, phone, language (fr default, en) | Personal data minimised |
| leases | id, owner\_id, kind, state, start\_on, end\_on, term\_months, rent\_cents, provision\_cents, deposit\_cents, due\_day, irl\_ref\_quarter, irl\_ref\_value, revision\_month, clauses jsonb, version | state: draft, active, notice, ended, closed; version bumps on each change |
| lease\_assets | lease\_id, asset\_id | Many-to-many |
| lease\_tenants | lease\_id, tenant\_id, solidary | — |
| guarantees | id, lease\_id, kind, guarantor jsonb, amount\_cents, valid\_from, valid\_to, status, document\_id | kind: deposit, surety, visale, insurance |
| inventories | id, lease\_id, kind (entry/exit), done\_on, items jsonb, document\_id | Structured items from phase 2 |
| charges | id, owner\_id, asset\_id, charged\_on, paid\_on, category, amount\_cents, recoverable, recoverable\_pct, exercise\_year, document\_id, source (manual/syndic/invoice) | category enum drives 2044 line |
| billing\_documents | id, owner\_id, lease\_id, kind, period, number, lines jsonb, total\_cents, issued\_at, cancels\_id, document\_id, input\_snapshot jsonb | Immutable after issued\_at; cancels\_id for corrective documents |
| payments | id, owner\_id, lease\_id, paid\_on, amount\_cents, method, reference, bank\_line\_id | — |
| payment\_matches | payment\_id, billing\_document\_id, amount\_cents | Partial matching |
| reconciliations | id, owner\_id, asset\_id, exercise\_year, state, statement\_document\_id, totals jsonb | One per asset and exercise |
| reconciliation\_lines | reconciliation\_id, lease\_id, charge\_id, allocated\_cents, provisions\_cents, balance\_cents | Per lease allocation |
| tax\_summaries | id, owner\_id, tax\_year, regime, lines jsonb, issued\_at, document\_id | One per owner and year |
| documents | id, owner\_id, class, asset\_id, lease\_id, period, object\_key, sha256, size\_bytes, template\_version, issued\_at, retain\_until | Metadata of every archived file |
| legal\_params | key, value jsonb, effective\_from, source\_url | See section 7 |
| irl\_index | quarter, value, published\_on | INSEE cache |
| job\_runs | id, job, period, started\_at, finished\_at, status, summary jsonb | Idempotence key = (job, period) |
| audit\_events | id (bigserial), owner\_id, actor\_sub, scope, action, entity\_type, entity\_id, before jsonb, after jsonb, request\_id, occurred\_at, prev\_hash, hash | INSERT only; trigger refuses UPDATE/DELETE; hash = sha256(prev\_hash ‖ canonical JSON of the row); one chain per owner |
| audit\_chain\_heads | owner\_id, as\_of, last\_event\_id, head\_hash, object\_key | Daily head written to the locked bucket; verified weekly against the chain |

**Conventions**: money as integer cents (`*_cents`), dates as `date`, timestamps `timestamptz` in UTC, periods as `char(7)` YYYY-MM, enums as PostgreSQL enums, soft delete nowhere (documents are never deleted, business rows are closed by state); every mutable business table carries created\_by, created\_at, updated\_by, updated\_at, filled by the repository layer from the request context.

**RLS**: `ALTER TABLE … ENABLE ROW LEVEL SECURITY` on every table with `owner_id`; policy `USING (owner_id = current_setting('app.owner_id')::uuid)`; the API role has no BYPASSRLS; migrations and jobs run as a separate role that sets `app.owner_id` per owner iteration. Co-owner and accountant access is granted through an `owner_grants (owner_id, grantee_sub, role)` table joined in the policy.

**Migrations**: six-digit zero-padded SQL files under `migrations/`, applied by `tenure migrate` at deploy; every migration reversible or explicitly marked not.

## 3. API surface

REST under `/api/v1`, JSON, documented by swag annotations on each handler and served at `/api/v1/openapi.json`; every list endpoint pages with `?page=&size=` and filters by query parameters, every write returns the updated resource.

| Module | Endpoints (phase 1) | Notes |
| --- | --- | --- |
| portfolio | `GET/POST /assets`, `GET/PUT /assets/{id}`, `GET/POST /tenants`, `GET/PUT /tenants/{id}`, `GET /me` | `/me` returns the caller, roles and grants |
| lease | `GET/POST /leases`, `GET/PUT /leases/{id}`, `POST /leases/{id}/transitions` (activate, notice, end, close), `POST /leases/{id}/contract` (generate), `POST /leases/{id}/amendments`, `GET/POST /leases/{id}/guarantees`, `PUT /guarantees/{id}`, `POST /leases/{id}/inventories` | Transitions validate the state machine and required documents |
| billing | `GET /billing/documents`, `GET /billing/documents/{id}`, `POST /billing/runs` (period, dry\_run), `GET /billing/runs/{id}`, `POST /billing/documents/{id}/cancel`, `GET /leases/{id}/arrears`, `POST /leases/{id}/revision` | A run with dry\_run returns the computed fees without issuing |
| charge | `GET/POST /charges`, `GET/PUT /charges/{id}`, `POST /charges/import/syndic` (file + mapping), `GET/POST /reconciliations`, `GET /reconciliations/{id}`, `POST /reconciliations/{id}/transitions` (allocate, issue, settle), `PUT /reconciliations/{id}/lines/{lineId}` | Import creates charges in draft for review |
| payment | `GET/POST /payments`, `PUT /payments/{id}`, `POST /payments/{id}/matches`, `POST /payments/import` (CSV/OFX, phase 2) | Matching rules in service layer |
| tax | `GET /tax/summaries`, `POST /tax/summaries` (year, regime), `GET /tax/summaries/{id}`, `GET /tax/summaries/{id}/lines.csv` | Regime optional: both computed, one issued |
| document | `GET /documents`, `GET /documents/{id}`, `GET /documents/{id}/content` (signed URL), `POST /documents` (upload with class and links), `POST /documents/export` (year or asset ZIP, async) | Content never streamed through the API for large files: short-lived signed URL |
| params | `GET /params`, `GET /params/irl`, `PUT /params/{key}` (admin) | Effective-dated values |
| jobs | `GET /jobs/runs`, `POST /jobs/{name}/trigger` (admin or owner for own scope) | — |
| audit | GET /audit/events (entity, from, to), GET /audit/events.csv, GET /audit/heads | Read only; owner and accountant; includes prev\_hash and hash per event |

**Conventions**: ids are UUIDv7; errors follow RFC 9457 problem details with a stable `type` per business rule (`lease/one-active-per-asset`, `billing/period-already-issued`); ETag on resources with `If-Match` required on PUT; all amounts in cents in JSON; webhooks none in phase 1.

**Versioning**: `/api/v1` frozen at phase 1 gate; additive changes only; `/api/v2` at the phase 3 major.

## 4. Identity and roles

Socrate issues the OIDC token, backendkit validates it and populates the request context with the subject and roles; the owner scope is resolved from `owners.socrate_sub` or an `owner_grants` row, and set as `app.owner_id` for RLS before any repository call.

| Role | Granted through | Can |
| --- | --- | --- |
| owner | owners row | Everything in their scope |
| co-owner | owner\_grants role co-owner | Read everything, download documents, no writes |
| accountant | owner\_grants role accountant | Read charges, payments, tax summaries, ledger exports; no tenant personal data beyond names |
| tenant (phase 4) | lease\_tenants + Socrate sub | Own lease documents and requests only, through the portal scope |
| admin | Socrate realm role | Params, job triggers, grants; no business data outside own ownership |

A caller with several scopes (own portfolio plus a grant) selects the active scope with the `X-Owner-Scope` header; the default is their own. Every request is logged with subject, scope, route and outcome.

**Imposed, not chosen**: Socrate and backendkit are a platform constraint (RENT-SPEC-006). In code this means: `internal/auth` wraps backendkit and is the only package that reads a token; no handler or service imports a JWT library; there is no `users` table, no password column, no API-key table and no local login route; `owners.socrate_sub` is the only link between a person and their data; machine access (remote job triggers, phase 4 integrations) uses Socrate client-credential clients mapped to the admin or a dedicated integration role; MFA, step-up (`acr`) and session policies are configured in Socrate and consumed by backendkit; backendkit is pinned in `go.mod` and upgraded through the normal PR flow; a CI test fails the build if any route is reachable without a valid Socrate token.

## 5. Scheduler and jobs

Jobs run in-process on a cron schedule, take a lock in `job_runs` keyed on (job, period), iterate owners one by one under their RLS scope, and record a summary; a second start for the same key is a no-op that returns the existing run.

| Job | Schedule (Europe/Paris) | Period key | Does |
| --- | --- | --- | --- |
| billing.monthly | 1st, 06:00 | YYYY-MM | Issues rent calls for active and notice leases (RENT-SPEC-003) |
| billing.reminders | daily, 08:00 | YYYY-MM-DD | Reminder e-mails at due day + 5; arrears flags |
| billing.receipts | hourly | YYYY-MM-DD-HH | Issues receipts for months fully matched since last run |
| irl.fetch | 15th of Jan/Apr/Jul/Oct, 07:00 | YYYY-Qn | Fetches INSEE IRL; applies due revisions and issues notices |
| charges.reconcile | on demand | asset + year | Reconciliation steps (RENT-SPEC-007) |
| tax.annual | 10 Jan, 06:00 | YYYY | Drafts every owner’s summary for the previous year; owner issues it |
| archive.retention | 1st, 03:00 | YYYY-MM | Lists documents past retain\_until for manual review; deletes nothing |
| archive.verify | weekly, Sun 04:00 | YYYY-Www | Re-hashes a sample of objects against `documents.sha256` |
| audit.head | daily, 02:00 | YYYY-MM-DD | Writes each owner’s chain head to the locked bucket |
| audit.verify | weekly, Sun 05:00 | YYYY-Www | Recomputes the hash chain per owner and compares with stored heads; mismatch alerts |

All jobs are also triggerable per owner from the API with `dry_run`, which computes and returns without writing. Failures are per owner: one owner’s error is logged and the run continues; the run ends `partial` and the admin is e-mailed.

## 6. Document pipeline

A document is produced in one transaction: number reserved, input snapshot stored, HTML rendered from the versioned template, PDF generated, object written with its hash, metadata row committed; any failure rolls the number back.

- Numbering: per owner and class, `<prefix>-<asset>-<YYYY>-<seq>` with `seq` from a per-(owner, class, asset, year) counter table locked in the transaction; prefixes AL (rent call), QL (receipt), RG (regularisation), RV (revision notice), CT (contract), AV (amendment), DG (deposit), RF (tax summary).
- Templates: Go `html/template` files under `templates/<class>/<version>/`, one set per owner overridable; `template_version` saved on the document; a template change never re-renders issued documents.
- Rendering: HTML → PDF/A-2b through the engine chosen in OD-03; fonts embedded; A4; a visible document number and hash footer.
- Archive: object key `<owner_id>/<class>/<YYYY>/<number>.pdf`; bucket versioning and object lock (compliance, 10 years); `documents.object_key`, `sha256`, `size_bytes`, `retain_until` written after a successful PUT with checksum verification.
- Immutability: no UPDATE on `billing_documents` or `documents` after `issued_at` (enforced by a trigger); corrections create a new document with `cancels_id`.
- Delivery: e-mail in the recipient’s language (French default, English if set on the owner or tenant) with the PDF attached and a signed download link valid 30 days; the PDF itself is always French; delivery status stored per document.

## 7. Legal parameters and calculation engine

Every legal constant lives in `legal_params` with an effective date, and every calculation takes the parameter set as of the period it computes, so a 2025 fee recomputed in 2027 gives the 2025 result.

| Key | Example value | Used by |
| --- | --- | --- |
| micro\_foncier\_threshold\_cents | 1500000 | tax |
| deposit\_max\_months.unfurnished / .furnished | 1 / 2 | lease |
| notice\_months.tenant.default / .tense\_zone / .furnished | 3 / 1 / 1 | lease (phase 2) |
| regularisation\_spread\_after\_months | 12 | charge |
| irl\_cap.applies / irl\_cap.rate | per decree, dated | billing |
| receipt\_due\_days\_before\_reminder | 5 | billing |
| form\_2044\_line\_map | category → line | tax |

Calculations are pure functions in each module (`billing.ComputeFee(lease, period, params)`, `charge.Allocate(...)`, `tax.Lines(...)`) with no I/O, which is what the golden-file tests in section 10 exercise. Rounding: fees to the cent, half-up; provisions proposed to the euro, up; IRL revision to the cent.

## 8. Errors, validation and observability

Validation happens once, in the service layer, and every refusal names a business rule the front-end can show verbatim.

- Handlers decode and type-check only; services validate invariants and return typed errors (`ErrInvariant{Type, Detail}`) mapped to 409/422 problem details; repositories return storage errors only.
- Structured logs (slog, JSON) with request id, subject, scope, route, duration; job logs with run id and owner; no personal data in logs beyond ids.
- Metrics (Prometheus endpoint, internal network only): request latency per route, job duration and status, documents generated, PDF render time, e-mail delivery outcomes, unmatched payments count.
- Alerts: job `failed` or `partial`, archive.verify mismatch, PDF engine down, disk or bucket quota at 80 %.
- Health: `/healthz` (process), `/readyz` (database, bucket, Socrate JWKS reachable).

## 8b. Audit trail and security controls (RENT-SPEC-022)

Every service write emits one audit event in the same transaction through `audit.Record(ctx, action, entity, before, after)`, and the HTTP and job layers enforce the ASVS L2 controls below from the first release.

- Audit emission: the service layer calls `audit.Record` after the write and before commit; the repository forbids direct writes from handlers; a missing audit call fails the integration test suite (every mutating endpoint is listed in `testdata/audited_routes.txt`).
- Chain: `hash = sha256(prev_hash ‖ canonical JSON)` computed in Go, not in SQL, with an advisory lock per owner to serialise inserts; `audit.verify` job weekly, `audit.head` job daily writes `audit_chain_heads` to `<owner_id>/audit/<date>.json` under object lock.
- Read access to audit events: owner and accountant through `GET /audit/events?entity=&from=&to=`; export as CSV with the chain hashes.
- Authentication: backendkit validates tokens with `acr` claim; routes tagged `step_up` (tax issue, grants, params, exports) require an `acr` fresh within 5 minutes or return 401 with `type: auth/step-up-required`; MFA enforced by Socrate policy for owner and admin realms.
- Headers (Caddy): HSTS 1 year, CSP `default-src 'self'` with hashed script allowances, `X-Content-Type-Options`, `Referrer-Policy`, frame denial; API refuses non-JSON content types.
- Rate limiting: Chi middleware, 60 write requests/min per subject, 600 reads/min, 10 failed auth/15 min per IP before lockout; limits logged and alerted.
- Uploads: `POST /documents` streams to a quarantine prefix, ClamAV scan (sidecar) then move to the archive prefix; MIME sniffed server-side; max 25 MB; infected files rejected and logged.
- Signed URLs: 10 minutes, bound to the document id and caller subject in the signature; every issue recorded as an audit event `document.download`.
- Encryption: PostgreSQL on an encrypted volume, bucket SSE, backups encrypted with a provider KMS key; TLS 1.2+ only; database connections over TLS.
- Roles: `rent_api` (no BYPASSRLS, no DDL), `rent_migrate` (DDL, used only by `tenure migrate`), `rent_jobs` (sets `app.owner_id` per owner, no DDL); credentials separate; break-glass superuser access only through the provider console, logged and alerted.
- Supply chain: `go.sum` and `package-lock.json` pinned, `govulncheck` and `npm audit` in CI, gitleaks on every PR in both repositories (secret scanning and push protection on private personal repositories need GitHub Advanced Security, so gitleaks is the day-1 control), Dependabot alerts and security updates enabled, SBOM (CycloneDX) attached to each release, container image scanned.
- RGPD: `GET /me/export` (owner) and tenant data export on request; erasure job after retention; processing register kept in `docs/rgpd/`; breach runbook in `deploy/runbooks/`.
- Pen test: external test scheduled before the phase 4 portal; phase 1 to 3 covered by the AI security audit per PR and an annual internal review.

## 9. Repository, configuration, CI and release

Two repositories: github.com/ovander/tenure-api (Go module github.com/ovander/tenure-api: service, scheduler, templates, migrations, deploy) and github.com/ovander/tenure-web (the Vue app). tenure-web publishes its built bundle as a release artefact; tenure-api pins that release in web/VERSION, downloads it at build and embeds it, so one tenure-api release still ships both and is stamped with `-ldflags` at build.

| Path | Holds |
| --- | --- |
| `cmd/tenure` | main: serve, migrate, job subcommands |
| `internal/<module>` | handler, service, repository per module |
| `internal/httpapi` | Chi routers, middleware, swag annotations |
| `migrations` | `000001_init.sql` … |
| `templates` | Document HTML templates by class and version |
| `web` | Built bundle of tenure-web at the pinned release (RENT-FE-001), embedded into `tenure` as embedded static files |
| `testdata/golden` | Golden files per calculation and tax year |
| `deploy` | Caddyfile, systemd unit, backup scripts |
| docs/rgpd, deploy/runbooks | Processing register and privacy notice; breach, restore and break-glass runbooks |

Configuration by environment variables only (`TENURE_DB_URL`, `TENURE_S3_*`, `TENURE_SOCRATE_ISSUER`, `TENURE_SMTP_*`, `TENURE_PDF_ENGINE_URL`), validated at start; secrets never in the repo. Workflow, identical in both repositories: GitHub issue → branch → PR with a CI status check named `ci` (tenure-api: go vet, staticcheck, golangci-lint, govulncheck, gitleaks, unit and golden tests, migration up/down; tenure-web: vue-tsc, eslint, npm audit, gitleaks, Vitest, Playwright smoke) → AI security audit on the PR → tag `vX.Y.Z` → release (tenure-web: built bundle as release asset; tenure-api: binary embedding the pinned bundle, plus deploy script); database migrated before the binary swaps; rollback = previous binary plus the migration’s down file. Branch `main` protected in both: PR required, `ci` check required and up to date, admins included, no force push or deletion.

## 10. Testing strategy

Legal calculations are tested against golden files per tax year, so a parameter change for 2027 can never alter a 2025 result unnoticed.

- Unit: pure calculation functions with table tests; state machines (lease, reconciliation) exhaustively.
- Golden: `testdata/golden/<year>/<case>.json` holding lease, charges, payments, params and the expected fee lines, reconciliation lines and 2044 lines; one case per real asset shape (house, apartment + box, furnished studio) and per edge (prorated first month, mid-year revision, late regularisation, co-ownership split).
- Integration: PostgreSQL in CI (testcontainers), RLS verified by a cross-owner test that must read zero rows; document pipeline end to end against MinIO; job idempotence (run twice, one document).
- Contract: OpenAPI generated and diffed on each PR; breaking change fails CI unless the major is bumped.
- Security: backendkit token tests, grant scopes, signed URL expiry; dependency audit in CI.

## 11. Open decisions and changelog

| Id | Question | Decision (2026-11-01) | Status |
| --- | --- | --- | --- |
| BE-OD-01 | Scheduler library | robfig/cron in-process, `job_runs` as the lock; a queue only if phase 4 integrations need retries | Decided |
| BE-OD-02 | PDF engine | gotenberg container, per OD-03 | Decided |
| BE-OD-03 | Object store client | minio-go, against Scaleway in production and MinIO in docker-compose | Decided |
| BE-OD-04 | Bank import formats for phase 2 | CSV + OFX; CAMT.053 when open banking arrives in phase 4 | Decided |

| Date | Version | Change |
| --- | --- | --- |
| 2026-11-01 | 0.1 | Initial backend specification, phase 1 scope, derived from RENT-SPEC-001 to 008 and 021 |
| 2026-11-01 | 0.2 | Audit trail (audit\_events, chain heads, \*\_by columns, audit jobs) and ASVS L2 controls added as section 8b, per RENT-SPEC-022 |
| 2026-11-01 | 0.3 | Renamed to Tenure: binary tenure, env prefix TENURE\_ |
| 2026-11-01 | 0.4 | Socrate and backendkit imposed: single auth package, no local credentials, CI guard on unauthenticated routes |
| 2026-11-01 | 0.5 | Language column on owners and tenants; e-mails in recipient language, PDFs always French |
| 2026-11-01 | 0.6 | Two repositories created: ovander/tenure-api and ovander/tenure-web; web bundle pinned and embedded at tenure-api build; CI check named ci, gitleaks and Dependabot as day-1 controls |
| 2026-11-01 | 1.0 | BE-OD-01 to 04 decided (robfig/cron, gotenberg, minio-go, CSV + OFX); baseline for the phase 1 implementation plan |
