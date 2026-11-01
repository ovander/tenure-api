# Phase 1 implementation plan

A working document. It turns the phase 1 slice order (RENT-SPEC, Roadmap, decided 2026-11-01)
into eight shippable slices: the calculation engine first, the screens last. Each slice is one
or more issues and PRs on `main`, and each ends with its acceptance line green in `ci`. Spec
gaps are tracked in [OPEN-QUESTIONS.md](OPEN-QUESTIONS.md) as `Q-nn`; a slice never waits on an
**Open** question (the provisional reading applies), only on a **Needs answer** one.

Legend: **Spec** = sections implemented · **Pkg** = packages created or extended ·
**Tables** = migration · **API** = routes under `/api/v1` · **Jobs** · **Tests** · **Accept**.

Cross-cutting from slice 2 onwards, never repeated below: every write goes through a service that
calls `audit.Record` in the same transaction; every owner table has RLS and joins the cross-owner
test; every mutating route is listed in `testdata/audited_routes.txt`; every route carries swag
annotations and the OpenAPI diff is checked; user-facing strings are French with an English
variant; documents are French only.

---

## Slice 1 — Calculation engine (no I/O)

- **Spec**: RENT-SPEC-003 (monthly fee, proration, IRL revision), RENT-SPEC-007 steps 3, 5, 7, 8
  and rules (allocation, balance, 12-month spread, new provision), RENT-SPEC-004 (cash basis,
  regime routing, 2044 lines, co-ownership, deficit), RENT-SPEC-021 (golden tests),
  RENT-BE-001 §7, §10 (unit, golden).
- **Pkg**:
  - `internal/money`: `Cents`, `MulDivHalfUp`, `CeilToEuro`, largest-remainder split (Q-18, Q-19).
  - `internal/period`: `Period` (`YYYY-MM`), days in month, overlap in days (Q-11).
  - `internal/params`: the `Set` type, `Lookup(key, asOf)` over effective-dated values, typed
    accessors, fixtures for 2025 and 2026 (`micro_foncier_threshold_cents` 1500000,
    `deposit_max_months.*`, `regularisation_spread_after_months` 12, `irl_cap.*`,
    `form_2044_line_map`, plus the Q-09 keys). It has no database code yet.
  - `internal/lease`: the pure domain type `Lease` (terms, assets, dates, IRL reference). It has
    no repository yet.
  - `internal/billing`: `ComputeFee(lease, period, params, opts)` returns fee lines (rent,
    provision, proration, regularisation line or instalment, carry-over) (Q-11, Q-16, Q-17);
    `Revise(lease, index, asOf, params)` returns the new rent, the capped flag, or forfeited
    (Q-12, Q-13).
  - `internal/charge`: `Allocate(charges, leases, exercise, provisionsCollected, issuedOn,
    params)` returns lines per lease: allocated, provisions, balance, spread flag, new provision
    rounded up to the euro (Q-15, Q-16).
  - `internal/payment`: the pure type `Payment` only.
  - `internal/tax`: `Lines(payments, charges, shares, year, params, priorDeficits)` returns per
    asset and per owner share: income, expense lines by 2044 line, net, the micro-foncier
    eligibility and allowance, deficit split, missing-document flags, source ids (Q-07…Q-10,
    Q-14). Furnished assets give gross receipts only (OD-01).
- **Tables / API / Jobs**: none.
- **Tests**: table tests per function (boundaries: the 1st and last day, 28/29/30/31-day months,
  zero-day overlap, a cap hit, a forfeited revision, a credit above the fee, rounding at .5).
  Golden runner `testdata/golden/<year>/<case>.json`, with `go test ./internal/... -update` to
  regenerate. Cases:
  1. `apartment-box-unfurnished-3y`: apartment plus attached box, one lease, starting
     mid-month; the first month is prorated, then a full month.
  2. `furnished-studio`: full year of fees; tax gives gross receipts only.
  3. `irl-mid-year-revision`: revision month in July, capped and uncapped variants.
  4. `late-regularisation`: statement issued more than 12 months after the exercise; 12
     instalments.
  5. `co-owned-50-50`: one asset, two owners; tax lines split, parts add up exactly.
  A test asserts that each year's golden file only reads that year's parameters.
- **Accept**: `go test ./...` green; the five golden cases are reviewed by the owner (figures in
  the PR); no legal constant in Go (a grep test checks the known values); Q-07 and Q-08
  answered, or kept open in writing.

## Slice 2 — Portfolio and lease without contract

- **Spec**: RENT-SPEC-002, RENT-SPEC-006 (identity constraint), RENT-SPEC-022 (audit trail,
  authN/Z, roles), RENT-BE-001 §1, §2, §3 (portfolio, lease without contract, params read, audit
  read, `/me`), §4, §5 (scheduler core), §8, §8b (audit, step-up, rate limit, headers, DB roles),
  §9 (config, cmd), §10 (integration, RLS, security).
- **Pkg**:
  - `cmd/tenure` with `serve`, `migrate up|down|status`, `job <name> [--owner] [--dry-run]`;
    ldflags stamping.
  - `internal/config`: `TENURE_*` variables parsed and validated (Q-01).
  - `internal/db`: pgx pool, gorm, `WithOwnerTx` (`set_config('app.owner_id', …, true)` and
    `app.actor_sub`).
  - `internal/auth`: backendkit `jwtauth` with audience and issuer; owner, co-owner or
    accountant resolution and `X-Owner-Scope`; admin from the role claim; `step_up` route tag
    (Q-02, Q-04, Q-05).
  - `internal/audit`: `Record(ctx, action, entity, before, after)`, canonical JSON, sha256
    chain, `pg_advisory_xact_lock(owner)`, `Verify(owner)`.
  - `internal/httpapi`: Chi, request id, slog, recover, security headers, JSON-only, rate limits
    (60 writes/min and 600 reads/min per subject), problem details, ETag and `If-Match`, paging,
    `/healthz`, `/readyz`, `/api/v1/openapi.json`.
  - `internal/portfolio` and `internal/lease`: repository, service and handler for each; lease
    state machine `draft → active → notice → ended → closed` with blocker hooks (Q-22); the
    one-active-lease-per-asset rule.
  - `internal/params`: repository for `legal_params` and `irl_index`.
  - `internal/jobs`: cron, `job_runs` lock on (job, period), per-owner iteration as
    `tenure_jobs`, `partial` status.
- **Tables** `000001_init.sql`: enums; `owners`, `owner_grants` (Q-05), `assets`,
  `asset_shares`, `tenants`, `leases`, `lease_assets`, `lease_tenants`, `guarantees`,
  `audit_events` (with a trigger refusing UPDATE and DELETE), `audit_chain_heads`,
  `legal_params` (seeded from the slice 1 fixtures), `irl_index`, `job_runs`, `documents`
  (metadata only, with the immutability trigger). RLS and policies on every owner table. Roles
  `tenure_api`, `tenure_migrate` and `tenure_jobs` with their grants (Q-03). Down section
  included. Migration tool per Q-21.
- **API**: `GET/PUT /me` (Q-06); `GET/POST /assets`, `GET/PUT /assets/{id}`;
  `GET/POST /tenants`, `GET/PUT /tenants/{id}`; `GET/POST /leases`, `GET/PUT /leases/{id}`,
  `POST /leases/{id}/transitions`; `GET/POST /leases/{id}/guarantees`, `PUT /guarantees/{id}`
  (data only); `GET /params`, `GET /params/irl`, `PUT /params/{key}` (admin, `step_up`);
  `GET /audit/events`, `GET /audit/events.csv`; `GET /jobs/runs`.
- **Jobs**: `audit.verify` (weekly).
- **Tests**: testcontainers PostgreSQL 17. The cross-owner test reads zero rows on every owner
  table, both as `tenure_api` and with `app.owner_id` unset. A co-owner or accountant write is
  refused by RLS. Every route in `audited_routes.txt` emits exactly one event, and a write that
  fails emits none. A tampered chain fails `audit.verify`. The router walk shows no route
  without a token except health. Step-up: fresh token accepted, stale refused, `auth_time`
  missing refused. Wrong audience and wrong issuer give 401. An unknown `X-Owner-Scope` gives
  403. Lease state machine: exhaustive. Migration up, down, up. `ETag` mismatch gives 412.
- **CI**: add the testcontainers job steps, migration up/down and the OpenAPI diff to `ci`.
- **Accept**: with docker-compose, an owner signed in through dev Socrate creates an apartment,
  a box, a tenant and a draft lease, then activates it; `/audit/events` shows the chained
  events; the second owner sees nothing.

## Slice 3 — Monthly fee: dry run, then issue

- **Spec**: RENT-SPEC-003 (monthly run, revision application), RENT-SPEC-005 (archive),
  RENT-BE-001 §3 billing and document, §5 `billing.monthly`, `irl.fetch`, `audit.head`, §6, §8b
  (signed URLs, `document.download`).
- **Pkg**: `internal/document` (numbering counters, `html/template` loader, gotenberg client,
  minio-go store with checksum check and object lock, signed URLs bound to document and
  subject); `internal/notify` (SMTP, FR/EN e-mail templates, PDF attached); billing
  repository, service and handler on top of `ComputeFee` and `Revise`; `templates/rent_call/v1`
  and `templates/revision_notice/v1`.
- **Tables** `000002_billing.sql`: `billing_documents` (immutable after `issued_at`, with
  `input_snapshot` and `cancels_id`), `document_counters`, `document_deliveries` (Q-24).
- **API**: `POST /billing/runs` (period, `dry_run`), `GET /billing/runs/{id}`,
  `GET /billing/documents`, `GET /billing/documents/{id}`,
  `POST /billing/documents/{id}/cancel`, `POST /leases/{id}/revision`; `GET /documents/{id}`,
  `GET /documents/{id}/content`; `POST /jobs/{name}/trigger`; `GET /audit/heads`.
- **Jobs**: `billing.monthly`, `irl.fetch` (INSEE, with manual override), `audit.head`.
- **Tests**: a dry run equals the golden fees. Running a period twice gives one document. A
  failure after the number is reserved rolls the number back. A trigger refuses UPDATE of an
  issued document. Cancel and reissue links `cancels_id`. End to end against MinIO and
  gotenberg: PDF hash = `documents.sha256`, object key format, retention date. A signed URL
  expires after 10 minutes and is refused for another subject. A head is written under the lock.
- **Accept**: one dry run, then issue, for a seeded month gives AL-… PDFs in MinIO with hash
  footers and e-mails in Mailpit; a re-run creates nothing.

## Slice 4 — Payments and receipts

- **Spec**: RENT-SPEC-002 (Payment), RENT-SPEC-003 (receipt, partial receipt, late rent),
  OD-02, RENT-BE-001 §3 payment, `GET /leases/{id}/arrears`, §5 `billing.receipts`,
  `billing.reminders`.
- **Pkg**: `internal/payment` (repository, service and handler; matching rules in the service);
  billing receipts and arrears; `templates/receipt/v1` (rent and charges stated separately) and
  the partial-receipt variant.
- **Tables** `000003_payments.sql`: `payments`, `payment_matches`.
- **API**: `GET/POST /payments`, `PUT /payments/{id}`, `POST /payments/{id}/matches`,
  `GET /leases/{id}/arrears`.
- **Jobs**: `billing.receipts` (hourly), `billing.reminders` (daily, due day +
  `receipt_due_days_before_reminder`).
- **Tests**: a full match gives a QL receipt; a partial one gives a partial receipt, never a
  quittance; a split match across two calls; over-matching is refused; receipts are idempotent
  per run key; reminders follow the parameter; arrears equal calls − matches.
- **Accept**: record the seeded month's payments: receipts issue on the next hourly run, and
  the arrears list is empty except for the unpaid lease.

## Slice 5 — Contract generation and guarantees

- **Spec**: RENT-SPEC-008 (contract, annexes, amendments, guarantee rules), RENT-SPEC-022
  (uploads), RENT-BE-001 §3 lease contract, amendments, inventories, `POST /documents`, §8b
  uploads.
- **Pkg**: lease contract service (variants: unfurnished, furnished, mobility, box alone);
  guarantee combination rules; deposit cap from params (Q-26); activation blockers (signed copy,
  entry inventory, DPE and diagnostics, Q-22, Q-23); upload pipeline (quarantine prefix, ClamAV
  sidecar, MIME sniffing, 25 MB); templates `contract/v1`, `amendment/v1`, `deposit_receipt/v1`,
  `surety/v1`.
- **Tables** `000004_contracts.sql`: `inventories`, `asset_diagnostics` (Q-23).
- **API**: `POST /leases/{id}/contract`, `POST /leases/{id}/amendments`,
  `POST /leases/{id}/inventories`, `POST /documents`.
- **Jobs**: none.
- **Tests**: each blocker blocks generation or activation with its problem `type`; a surety
  with insurance is refused unless the tenant is a student or apprentice; a deposit above the
  cap is refused, and a rent change does not alter a deposit already held; an infected upload
  is rejected and logged; the CT number format.
- **Accept**: a draft lease with an expired diagnostic shows its blocker; once it is fixed, CT-…
  is generated, the signed copy and entry inventory are uploaded, and the lease activates.

## Slice 6 — Charges ledger and reconciliation

- **Spec**: RENT-SPEC-007 (whole process, states, rules), RENT-BE-001 §3 charge, §5
  `charges.reconcile`.
- **Pkg**: `internal/charge` (repository, service and handler; syndic import with column mapping
  into draft charges; reconciliation state machine `draft → allocated → issued → settled`);
  billing applies the regularisation line, instalments and carry-over; provision update;
  `templates/regularisation/v1`.
- **Tables** `000005_charges.sql`: `charges`, `reconciliations`, `reconciliation_lines`.
- **API**: `GET/POST /charges`, `GET/PUT /charges/{id}`, `POST /charges/import/syndic`,
  `GET/POST /reconciliations`, `GET /reconciliations/{id}`,
  `POST /reconciliations/{id}/transitions`, `PUT /reconciliations/{id}/lines/{lineId}`.
- **Jobs**: `charges.reconcile` (on demand, per asset and year).
- **Tests**: an import maps lines and keeps the syndic's recoverable split, with overrides
  audited; allocation equals the slice 1 golden; provisions are taken from matched payments,
  not calls; a late statement spreads over 12 fees; a tenant who left settles against the
  deposit; an issued statement is immutable and a correction cancels it; state machine
  exhaustive.
- **Accept**: a seeded syndic statement goes through the wizard API end to end; RG-… issued;
  the next fee carries the balance line; the new provision applies from the next month.

## Slice 7 — Tax year

- **Spec**: RENT-SPEC-004 (both regimes, outputs, rules), RENT-SPEC-005 (tax classes),
  RENT-BE-001 §3 tax, §5 `tax.annual`, §8b step-up on tax issue.
- **Pkg**: `internal/tax` (repository, service and handler around `Lines`; prior deficits read
  from earlier summaries; CSV writer); `templates/tax_summary/v1`.
- **Tables** `000006_tax.sql`: `tax_summaries` (with the carried deficits in `lines`).
- **API**: `GET /tax/summaries`, `POST /tax/summaries` (year, regime; `step_up`),
  `GET /tax/summaries/{id}`, `GET /tax/summaries/{id}/lines.csv`.
- **Jobs**: `tax.annual` (10 January, drafts only).
- **Tests**: the summary equals the slice 1 golden for the year; a charge paid in N+1 counts in
  N+1; a charge with no document is flagged, not excluded; every figure traces to source ids;
  issuing without step-up gives 401; the RF summary and CSV are archived.
- **Accept**: the seeded owner's previous year shows micro and réel side by side; issuing gives
  the RF-… PDF and CSV in the archive.

## Slice 8 — Archive, exports, settings, grants; phase 1 gate

- **Spec**: RENT-SPEC-005 (browse, export, end of retention), RENT-SPEC-022 (RGPD export,
  grants in audit), RENT-BE-001 §3 document and params, §4 roles, §5 `archive.retention`,
  `archive.verify`, §8b; Roadmap phase 1 gate.
- **Pkg**: document browse and filters; async ZIP export with an index CSV; grants service
  (Q-05); RGPD `GET /me/export`; `docs/rgpd/` register; `deploy/` (Caddyfile, systemd unit,
  backups, runbooks); release workflow (SBOM, bundle pinned in `web/VERSION`, embedded).
- **Tables** `000007_exports.sql`: `export_jobs` if the async state needs a table (otherwise
  `job_runs`).
- **API**: `GET /documents` (asset, year, class, number), `POST /documents/export`
  (`step_up`), `GET/POST /grants`, `DELETE /grants/{id}` (`step_up`, Q-05), `GET /me/export`.
- **Jobs**: `archive.retention` (lists, deletes nothing), `archive.verify` (re-hashes a sample).
- **Tests**: an export holds only the caller's scope (cross-owner); the accountant sees financial
  classes only and no guarantor data; a revoked grant gives 403 on the next request; a
  corrupted object is caught by `archive.verify`; retention deletes nothing.
- **Accept (phase 1 gate)**: on the owner's real portfolio: one real monthly run for every
  asset, one real tax summary accepted, hash chain verified over one real month, ASVS L2
  checklist signed off.

---

## Order of work and dependencies

Slices run in order. Slice 1 only needs Go. Slice 2 brings Docker, Socrate dev values (Q-01) and
the migration tool (Q-21). Slices 3 to 8 each add one migration and depend on the previous one.
Questions that block a slice: Q-07 and Q-08 for slice 1 fixtures (provisional reading allowed);
none for the others.
