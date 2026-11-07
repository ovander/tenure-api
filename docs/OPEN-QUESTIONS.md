# Open questions

Spec gaps and ambiguities found while implementing phase 1. Each entry gives the provisional
reading in force until the owner answers. That reading is the most conservative one: fail closed,
and never overstate a tenant's debt or an owner's deduction. When an answer arrives, update the
spec in the same PR and mark the entry **Answered**, with the date.

Status: **Open** (provisional reading applies), **Needs answer** (blocks the slice named),
**Answered**.

## Identity and security

**Q-01 Socrate dev values — Open (slice 2), values to follow from the owner.** The issuer URL, API audience, role claim and step-up
value were not supplied. Provisional: `TENURE_SOCRATE_ISSUER`, `TENURE_SOCRATE_JWKS_URL`,
`TENURE_SOCRATE_AUDIENCE` and `TENURE_SOCRATE_ADMIN_ROLE` are required and have no default, so
`tenure serve` refuses to start without them. `.env.example` holds obvious placeholders. The
admin role is read from backendkit's `role` claim (`ctxutil.GetUserRole`).
2026-11-01: the owner confirmed the reading. The four settings stay required, with no default.
Slice 2: `tenure serve` refuses to start without them; `.env.example` holds placeholders and the integration suite signs its own tokens with a local JWKS.
2026-11-03: renamed to Socrate's own names (Q-99); the audience now defaults to the client id.

**Q-02 Step-up freshness — Answered 2026-11-01.** The owner adopted the reading. A `step_up`
route requires `auth_time` at most 5 minutes old and `mfa` in `amr`; a missing `auth_time` is
refused. backendkit v1.27.0 already exposes both claims (`ctxutil.GetAuthTime`,
`ctxutil.GetAMR`), so no backendkit issue is needed. Only `internal/auth` reads them, through an
exported accessor; nothing else parses the JWT. The window is `TENURE_STEP_UP_MAX_AGE`, at most
5m. Spec: RENT-BE-001 §4, §8b.

**Q-03 Database role names — Answered 2026-11-01.** `tenure_api`, `tenure_migrate` and
`tenure_jobs`. Spec: RENT-BE-001 §8b corrected.

**Q-04 Subject format — Open (slice 2).** backendkit documents Socrate's `sub` as a numeric
string, not a UUID. Provisional: `owners.socrate_sub` is `text`, unique, and is read with
`ctxutil.GetUserSub`, never `GetUserID`.

**Q-05 Grants in RLS — Answered 2026-11-01.** `owner_grants (id, owner_id, grantee_sub,
role, granted_at, revoked_at)` is created in migration 000001 and is part of the data model.
Only rows with `revoked_at` null grant access. Every change is audited. The policy is
`owner_id = app.owner_id AND tenure_scope_allowed(owner_id)`, where the function checks either
`owners.socrate_sub = app.actor_sub` or a live grant. Write policies require the owner, so
co-owners and accountants are read-only in the database too. The grant endpoints come in
slice 8: `GET/POST /grants` and `DELETE /grants/{id}` (which sets `revoked_at`), all `step_up`.
Spec: RENT-BE-001 §2.

**Q-06 `PUT /me` — Open (slice 2).** RENT-FE-001 §7 persists the language with `PUT /me`, but
BE-001 §3 lists only `GET /me`. Provisional: add `PUT /me` (language only) in slice 2, and amend
§3.

## Calculation engine (slice 1)

**Q-07 2044 line numbers — Answered 2026-11-01.** Use the real form 2044 map:
- income: 211 gross rents, 212 owner expenses charged to the tenant, 213 subsidies and
  insurance indemnities;
- expenses: 221 administration and management, 222 flat amount per property, 223 insurance,
  224 repairs, maintenance and improvement, 225 recoverable charges not recovered at lease
  close, 227 taxe foncière excluding TEOM, 229 copropriété provisions paid in N, 230
  regularisation of N−1 provisions, 250 loan interest and insurance.

The map is the parameter `form_2044_line_map`, keyed by millésime (the form's year, i.e. income
year + 1). `tax.Lines` returns an error when the target millésime has no map, and `tax.annual`
runs the same pre-check before drafting (slice 7). Fixtures and golden files follow the map.
Spec: RENT-SPEC-004, RENT-BE-001 §5 and §7.

**Q-08 Recoverable charges on line 211 — Answered 2026-11-01.** Excluded. Provisional
charges received are not income, and recoverable expenses are not deducted. The only exception
is line 225, at lease close. `tax.Lines` takes rent income only from payment amounts matched to
rent lines. Spec: RENT-SPEC-004.

**Q-09 Missing legal parameters — Answered 2026-11-01.** Approved, effective 2025-01-01:
`micro_foncier_allowance_pct` 30, `forfait_222_cents` 2000, `deficit_global_cap_cents` 1070000,
`deficit_carryforward_years` 10. They are in the params fixtures and will be in the 000001 seed.
Spec: RENT-BE-001 §7.

**Q-10 Deficit carry-forward — Open (slice 1).** The spec only says "deficit carry-forward".
Provisional: `tax.Lines` takes prior-year deficits as input. It applies them, oldest first,
against a positive net and drops any that have expired after `deficit_carry_forward_years`. It
reports a current-year deficit in two parts: the share that can be set against global income
(up to the cap, loan interest excluded) and the share carried forward. The output is advisory;
the owner declares.

**Q-11 Proration basis — Open (slice 1).** "Prorated by days" does not say how many days a month
has. Provisional: the actual number of days in the calendar month. Occupied days count both the
start date and the end date. Rent and provision are prorated separately, each rounded half-up to
the cent.

**Q-12 IRL cap source — Answered 2026-11-01.** Two dated parameters in `legal_params`:
`irl_cap` (national, applies to every lease) and `rent_control.zone_rules`, which applies only
when `assets.controlled_zone` is set. That boolean column is added to `assets` in migration
000001. Spec: RENT-SPEC-003, RENT-BE-001 §2 and §7. `rent_control.zone_rules` was replaced by two rules on 2026-11-01 (Q-28).

**Q-13 IRL retroactivity — Open (slice 1).** RENT-SPEC-003 says "A revision older than 12 months
is not applied retroactively." Provisional: a revision never creates arrears for past months. It
takes effect from the first period the run applies it to. If the anniversary date is more than
12 months before that period, the revision for that anniversary is forfeited. "Current IRL" means
the latest published index of the lease's reference quarter.

**Q-14 Index and share scales — Open (slice 1).** Provisional: IRL values are stored as integer
hundredths (e.g. 145,47 → 14547). Ownership shares are integer basis points in Go
(10000 = 100 %) and `numeric(5,2)` in SQL. Co-ownership splits use the largest remainder, so the
parts always add up to the total.

**Q-15 Allocation rounding and vacancy — Open (slice 1).** Provisional: an exercise is a date
range (it may differ from the calendar year). A lease's share is
`recoverable × lease days ÷ exercise days`. When the exercise has days with no lease, each share
is rounded down, and the vacancy keeps the remainder as non-recovered, so a tenant is never
charged a rounding cent. When there is no vacancy, the total is split by largest remainder, so
the shares add up exactly. A box on a dwelling lease shares that lease. The proposed provision is
the asset's recoverable total ÷ 12, rounded up to the euro, for every lease still running after
the exercise.

**Q-16 Late regularisation spread — Open (slice 1).** The law gives the 12 monthly instalments on
the tenant's request; RENT-SPEC-007 makes them automatic. Provisional: follow the spec. The flag
is set when the issue date is more than `regularisation_spread_after_months` after the exercise
end. A positive balance is split into 12 equal instalments, rounded down to the cent, with the
remainder on the last one. Credits are never spread.

**Q-17 Credit larger than a fee — Open (slice 1).** Provisional: `ComputeFee` caps a credit line
at the month's fee, so a fee never goes negative, and returns the remainder to carry to the
next period.

**Q-18 Rounding of negatives — Open (slice 1).** Provisional: half away from zero, so a credit of
x,xx5 rounds the same way, in absolute value, as a debit.

**Q-19 Shared helpers — Open (slice 1).** RENT-BE-001 §1 has no package for money and periods.
Provisional: add the leaf packages `internal/money` (cents, rounding) and `internal/period`
(`YYYY-MM`, days), with no dependencies, and amend §1.
Slice 1 also adds the leaf `internal/invariant` (`Error{Type, Detail}`, RENT-BE-001 §8's `ErrInvariant`), so that every module returns the same typed rule error.

**Q-20 "Two real assets" — Open (slice 1).** The roadmap asks for golden tests on two real
assets, but no real data is in the repository (and none may be committed). Provisional: synthetic
golden cases shaped like the real assets. Cases with real figures, anonymised, are added at the
phase 1 gate.

## Platform

**Q-21 Migration tool — Open (slice 2).** The spec fixes the file naming (`000001_init.sql`) and
reversibility, but not the tool. Provisional: pressly/goose v3 with `-- +goose Up` and
`-- +goose Down` sections in one file, embedded in the binary. A migration that cannot be
reversed says so in its Down section and fails it on purpose.
Slice 2: goose v3 `Provider` over the embedded `migrations` package; `tenure migrate up|down|status`; CI runs up, down, up.

**Q-22 Activation blockers before slice 5 — Open (slice 2).** Activation needs the signed contract
and the entry inventory (RENT-SPEC-008), which only exist from slice 5. Provisional: slice 2
builds the state machine with a blocker interface and implements the blockers that need no
documents. Slice 5 adds the document blockers. No release reaches real data before the phase 1
gate.
Slice 2: activation checks one current lease per asset (assets locked `FOR UPDATE`) and every lease rule; closing requires every deposit settled.
Slice 5: activation also requires every contract-generation blocker cleared as of the activation date, a
contract generated from the current lease version and guarantees (Q-69), its signed copy uploaded,
and the entry inventory recorded (Q-70).

**Q-23 Diagnostics — Open (slice 5).** Generation is blocked by "a diagnostic past its validity,
no DPE", but the data model only has `assets.dpe_class`. Provisional: an `asset_diagnostics`
table (kind, done_on, valid_until, document_id), with validity periods in `legal_params`.
Slice 5: implemented, with the required set and the rules of Q-73.

**Q-24 Delivery status — Open (slice 3).** "Delivery status stored per document" has no table.
Provisional: a `document_deliveries` table (document_id, recipient, channel, status, at), under
RLS.

**Q-25 Logging library — Open (slice 2).** RENT-BE-001 §8 asks for slog; backendkit takes a
`*logrus.Entry`. Provisional: slog JSON for the service; the logrus entry passed to backendkit
uses a JSON formatter with the same field names.

**Q-26 Box-only lease deposit — Open (slice 5).** A box let on its own is not governed by the loi
de 1989, so `deposit_max_months` does not apply to it. Provisional: there is no legal cap. A
warning is shown above 2 months of rent.
Slice 5: the 2 months are `deposit_warning_months.box` in `legal_params` (a product rule, marked so
in its source); `GET /leases/{id}/blockers` lists the warning, which never blocks.

## Raised by the 2026-11-01 decisions

**Q-27 What counts as a property for line 222 — Answered 2026-11-01.** One flat amount per
*local* that earned rent under its own lease: count = the number of leases of unfurnished assets
that produced rent in the income year. A box let with the apartment is one local with it; a box
on its own lease is another. Furnished assets (BIC) never count. `tax.Lines` puts each amount on
the lease's principal asset. Spec: RENT-SPEC-004.

**Q-28 Rent control — Answered 2026-11-01.** Three distinct rules, all dated in `legal_params`;
they replace `rent_control.zone_rules`:
1. `irl_cap` caps the IRL variation itself when a cap is in force (the 2022–2024 bouclier, 3.5 %),
   in the revision formula, for every asset (`billing.Revise`).
2. `rent_control.zone_ceiling`: for assets with `controlled_zone` set, the rent may not exceed
   the loyer de référence majoré × surface for the asset's zone and category. It is applied in
   `billing.Revise` as a cap, and checked by `lease.Validate` at lease creation and renewal.
3. `rent_control.relocation_rule`: for a new lease in a zone tendue (Q-102), the rent may not exceed
   the previous lease's rent revised by IRL, unless an exemption (`works`, `under_valued`) is
   recorded on the lease (`lease.Validate`).

The fixtures carry an empty ceiling table, filled from the prefectoral arrêté when an asset is in
a zone, and the relocation rule in force. The golden case uses a synthetic table: one controlled
asset where the ceiling bites, one asset outside any zone. Spec: RENT-SPEC-003, RENT-BE-001 §2
and §7. Q-33 and Q-34 follow from this.

**Q-29 Copropriété provisions on line 229 — Answered 2026-11-01.** Confirmed: line 229 takes only
the non-recoverable share of the syndic provisions paid in year N. Line 230 carries the
regularisation of year N−1's non-recoverable share, **signed** (category
`copro_regularisation_prior`): positive reduces the expenses, negative adds to them. A negative
amount on any other line is refused. Spec: RENT-SPEC-004.

**Q-30 `tax.Lines` inputs — Open (slice 1).** The signature `Lines(payments, charges, shares,
year, params)` does not carry the prior-year deficits, nor the furnished flag and the
dwelling/box kind that line 222 needs. Provisional: `shares` is a list of assets, each with its
owners' shares, `Furnished` and `Kind`, and a final argument takes the prior deficits per owner.

**Q-31 Which year a millésime names — Answered 2026-11-01.** Millésime = income year + 1. The 2044
map and every tax parameter are keyed by income year (`tax_year`, read on 31 December of that
year with `params.TaxYearEnd`); the millésime is stored as a label only. The fixture holds the
income-year 2025 map, labelled millésime 2026. Spec: RENT-SPEC-004 header ("income year 2025,
form millésime 2026"), RENT-BE-001 §5 and §7.

**Q-32 Rent of a lease over several assets — Answered 2026-11-01.** A lease whose assets have
different ownership shares is invalid: `lease.Validate` returns
`lease/consistent-ownership-shares` (422 at save, slice 2), so the case never reaches tax. Rent
goes to the lease's dwelling (`lease.PrincipalAsset`). An optional per-asset rent breakdown
(`leases.rent_breakdown` jsonb) is kept for phase 3 reporting; it must add up to the rent, and
tax ignores it. Spec: RENT-SPEC-004, RENT-BE-001 §2.

**Q-33 Zone ceiling inputs — Open (slice 1).** Q-28 keys the ceiling "by zone and category", but
the data model has no zone or category on the asset, and stores the surface as `surface_m2`.
Provisional:
- `assets.rent_control_zone` and `assets.rent_control_category` (text) key the
  `rent_control.zone_ceiling` table, which holds cents per m² per month.
- The surface is read in hundredths of a m².
- The ceiling of each controlled dwelling is rounded down to the cent. Boxes are never under the
  ceiling.
- The rent compared with the ceiling is the breakdown share of the controlled dwellings when the
  lease has a breakdown, and otherwise the whole rent (the stricter reading).
- A controlled asset with no zone, no category, no surface or no table value is refused
  (`lease/zone-ceiling-unknown`), never let through.

**Q-34 Rent already above the ceiling at revision — Open (slice 1).** Q-28 says the revised rent
may not exceed the ceiling, but not what happens when the current rent is already above it.
Provisional: the revision does not raise the rent (`above_ceiling` is set so the owner is told),
a falling index still lowers it, and the rent is never cut down to the ceiling automatically,
because that is a separate procedure.

## Raised by slice 2

**Q-35 Creating the owner profile — Open (slice 2).** Nothing in the spec says how an owners row
is created. Provisional: `PUT /me` without `If-Match` creates the caller's profile, linked to
their Socrate subject. It holds no credential and is not an account, so RENT-SPEC-006 still
holds. It is audited as `owner.create` and starts the owner's audit chain. With `If-Match`, the
same route updates the profile. A grantee who has no profile of their own gets no default scope.

**Q-36 Platform audit chain — Open (slice 2).** Admin parameter changes (`PUT /params/{key}`)
belong to no owner. Provisional: `audit_events.owner_id` is nullable, and NULL is a separate
platform chain, hashed, locked and verified like the owner chains. Events are appended with a
plain `INSERT`, because `RETURNING` would require read access to the chain.

**Q-37 Who reads the audit trail — Open (slice 2).** RENT-BE-001 §3 says the owner and the
accountant; RENT-FE-001 §5 also lets the co-owner read the audit tabs. Provisional: follow the
backend spec. A co-owner gets 403, enforced both in the API and in row-level security.

**Q-38 Object store in development and tests — Needs answer (OD-04).** OD-04 puts MinIO in
docker-compose, but no MinIO image can be pulled (`minio/minio` is not on Docker Hub, and
neither quay.io nor bitnami has one). Provisional: RustFS 1.0.3, an S3-compatible store whose
versioning, compliance object lock and presigned URLs were checked with minio-go. The code
speaks plain S3 through minio-go (BE-OD-03), so production on Scaleway is unchanged. RustFS
backs the `archive` service of docker-compose and the integration suite. OD-04 is a decided
item, so the owner should confirm this substitute or name another.

**Q-39 Parameter changes with a past date — Open (slice 2).** RENT-SPEC-021 makes a parameter
change a reviewed entry, but does not say whether it may be backdated. Provisional: `PUT
/params/{key}` appends an entry effective today or later (Europe/Paris), and only for a key that
already exists. Backdating would change past results; it is done through a reviewed migration
instead.

**Q-40 Versions on every mutable resource — Open (slice 2).** RENT-BE-001 §3 requires ETag and
`If-Match` on every PUT, but §2 gives only `leases` a `version` column. Provisional: owners,
assets, tenants and guarantees carry `version` too, and the ETag is `W/"<version>"`. A missing
`If-Match` is 428; a stale one is 412.

**Q-41 Database roles beyond the three — Open (slice 2).** The policies of `owners` and
`owner_grants` would recurse into each other. Provisional: a fourth role, `tenure_rls` (NOLOGIN,
BYPASSRLS), owns only the SECURITY DEFINER functions `tenure_scope_role()` and
`audit_last_hash()`. Only `tenure_migrate` is a member of it, so that migrations can assign
those functions to it. The three login roles are unchanged.

**Q-42 Editing a lease after draft — Open (slice 2).** Provisional: `PUT /leases/{id}` changes
only a draft (`lease/not-editable` otherwise). After activation, terms change through
transitions, revisions (slice 3) and amendments (slice 5).

**Q-43 Row-level security on link tables — Open (slice 2).** Provisional: `lease_assets`,
`lease_tenants` and `guarantees` carry `owner_id`, with composite foreign keys to their parent's
`(id, owner_id)`, so nothing can link rows of two owners. `asset_shares.owner_id` is the co-owner
holding the share (RENT-BE-001 §2), so that table's policy follows the asset instead.

**Q-44 Step-up on the audit export — Open (slice 2).** RENT-BE-001 §8b puts "exports" under
`step_up`. Provisional: `GET /audit/events.csv` is step-up, and the export itself is recorded as
an `audit.export` event. `GET /openapi.json` requires a token like every route except the health
checks.

## Raised by slice 3

**Q-45 Download link in e-mails — Open (slice 3).** RENT-BE-001 §6 sends "a signed download link
valid 30 days"; §8b limits signed URLs to 10 minutes, bound to the caller. In phase 1 a tenant
has no Socrate login, so a 30-day link would be an unauthenticated way into the archive.
Provisional: the e-mail carries the PDF as an attachment and no link. Links for tenants come
with the portal (phase 4).

**Q-46 Hash in the footer — Open (slice 3).** "A visible document number and hash footer", but a
PDF cannot contain its own hash. Provisional: the footer prints the number and the first 16
hex digits of the SHA-256 of the input snapshot. `documents.sha256` is the hash of the PDF
itself, verified after upload.

**Q-47 An object written before the commit — Open (slice 3).** The PDF is written under
compliance lock before the metadata commits. If the commit then fails, the object stays in the
bucket, locked and unreferenced, and its number is given back. Provisional: the next document
reuses the number and is written as a new version of the same key; `documents.object_version`
records which version is the document. `archive.verify` (slice 8) reports unreferenced
versions.

**Q-48 Document numbers — Open (slice 3).** `<prefix>-<asset>-<YYYY>-<seq>` does not say what
`<asset>` is. Provisional: the first eight hex digits of the lease's principal asset id, in
upper case. YYYY is the year of issue (Europe/Paris), and the sequence has four digits.

**Q-49 Numbering a cancellation — Open (slice 3).** RENT-BE-001 §6 has no prefix for a cancelling
document. Provisional: it takes the next number in the class of the document it cancels (AL),
is archived in that class, and is rendered from the `cancellation` template.

**Q-50 Binding a download link to the caller — Open (slice 3).** An S3 presigned URL cannot check
who uses it. Provisional:
- a link is issued only to a caller who may read the document;
- its signature covers `x-tenure-sub` and `x-tenure-document`, so changing either makes the
  store answer 403;
- it lasts 10 minutes and is recorded as `document.download`;
- an accountant gets links for financial classes only.

**Q-51 One bucket or one per owner — Open (slice 3).** RENT-SPEC-005 says "bucket per owner";
RENT-BE-001 §6 keys objects `<owner_id>/…` in one bucket. Provisional: follow the backend spec,
with one bucket and an owner prefix.

**Q-52 When a revision is due — Open (slice 3).** The rules, as implemented in
`billing.DueRevision`:
- **Index:** the reference quarter one year later.
- **Anniversary:** in the revision month, on the lease's start day, at least 12 months after
  the start.
- **Effective from:** the first billing period starting on or after the anniversary, the
  index's publication and today.

`billing.monthly` applies a due revision before computing the fee; `irl.fetch` applies those
that become due when INSEE publishes; `POST /leases/{id}/revision` applies one on demand. A
revision more than 12 months late is forfeited (Q-13).

**Q-53 Correcting an IRL value — Open (slice 3).** Provisional: the IRL cache is insert-only.
`irl.fetch` adds only new quarters, and `PUT /params/irl/{quarter}` (admin, step-up) records a
missing quarter by hand. A wrong recorded value is corrected by a reviewed migration.

**Q-54 Retention period — Open (slice 3).** RENT-SPEC-005's 10 years is a legal constant.
Provisional: `archive_retention_years` = 10 in `legal_params`, effective 2025-01-01. Its
`source_url` is still to be filled.

**Q-55 IRL publication date — Open (slice 3).** INSEE's SDMX series gives no publication date,
and revisions must not apply before the index was known. Provisional: a fetched value is
recorded as published on the 15th of the month after its quarter, INSEE's usual calendar, and
never after the day it was fetched.

**Q-56 Cancel and reissue — Open (slice 3).** Provisional: `POST /billing/documents/{id}/cancel`
cancels a live rent call and, when the lease is still billed for that period, reissues it as
the next generation in the same transaction (RENT-FE-001 flow 5). Revision notices are not
cancelled; a wrong revision is corrected by an amendment (slice 5).

**Q-57 Audit events of jobs and runs — Open (slice 3).** RENT-SPEC-022 audits "job runs".
Provisional: every owner unit of a non-dry run records `job.<name>` in that owner's chain, and
the platform unit in the platform chain. Routes that issue documents and e-mails emit one or
more events; they are marked `+` in `testdata/audited_routes.txt`.

**Q-58 Failed e-mails — Open (slice 3).** Provisional: a failed send is recorded on its delivery
(`failed`, with the error) and audited. It neither fails the run nor rolls back the document.
Automatic retry is not built yet.

**Q-59 Atomicity of a monthly run — Open (slice 3).** RENT-BE-001 §5 makes failures per owner.
Provisional: one owner's month is one transaction, so a lease that fails rolls back that
owner's documents of the run (their numbers are given back; written objects follow Q-47). The
run ends `partial`, and the other owners are unaffected.

## Raised by slice 4

**Q-60 Matching rules — Open (slice 4).** RENT-BE-001 §3 leaves the matching rules to the
service. Provisional:
- a payment applies only to a live rent call of its own lease;
- each match is at most what remains due on that rent call, and all matches together at most
  the payment;
- `auto` (or `auto_match` at entry) applies the unapplied part to the oldest unpaid rent calls
  first;
- an overpayment stays unapplied on the payment, as a credit, and is never matched forward on
  its own.

Matching on one lease is serialised by a transaction lock.

**Q-61 Partial receipts — Open (slice 4).** "A partial payment produces a reçu partiel": the spec
does not say when or how many. Provisional:
- `billing.receipts` issues, per rent call and per run, one partial receipt (kind
  `partial_receipt`, numbered QL) for the matches no receipt covers yet;
- the quittance (kind `receipt`) is issued once matched payments reach the rent call's total;
  it lists every payment of the period and states rent and charges separately;
- a rent call never gets a quittance before it is fully paid.

**Q-62 Reminders — Open (slice 4).** Provisional:
- one reminder per rent call (`billing_reminders`), sent by `billing.reminders` once the due
  day plus `receipt_due_days_before_reminder` has passed and something is still due;
- a day the job did not run is caught up the next day;
- the e-mail carries no attachment, and the reminder is recorded after sending, with its audit
  event;
- no penalty (RENT-SPEC-003).

**Q-63 Cancelling a paid rent call — Open (slice 4).** Provisional: a rent call that has a
quittance cannot be cancelled (`billing/receipt-issued`). Otherwise, a cancel and reissue moves
its matches to the reissued rent call, up to the new total; the remainder becomes unapplied on
the payments again.

**Q-64 Editing a payment — Open (slice 4).** Provisional:
- a payment's amount and date are frozen once a receipt covers it, while method and reference
  stay editable;
- a payment never changes lease;
- the amount cannot drop below what is already applied;
- a payment date in the future is refused.

**Q-65 Splitting a payment between rent and charges for tax — Open (slice 7).** Matches are per
rent call, but `tax.Lines` counts only the rent lines (Q-08). Provisional, to be confirmed
before slice 7: a matched amount is split between the rent call's lines in proportion to their
amounts. This neither overstates nor understates line 211.
Slice 7: implemented as `tax.SplitMatch`. The rent part is the matched amount × rent ÷ the rent call's
total, rounded half-up and never above the rent line, and the provision part likewise. The rest
goes to regularisation, so a credit line never reduces the rent received.

**Q-66 Reading payments — Open (slice 4).** Provisional: owner, co-owner and accountant read
payments, arrears and receipts (RENT-FE-001 §5, "billing month, payments: read"). Only the owner
records, edits or matches a payment.

## Raised by slice 5

**Q-67 Upload pipeline — Open (slice 5).** RENT-BE-001 §8b names a quarantine prefix, a ClamAV
sidecar, server-side MIME sniffing and 25 MB. Provisional:
- the quarantine is the prefix `quarantine/<owner_id>/` of the archive bucket. Its objects carry no
  retention, so the bucket must have no default retention rule: every archived object sets its own
  compliance lock. Each quarantined object is deleted after the scan, whatever the outcome;
- the antivirus scans before the type check, so every infected file is logged: a warning log and a
  `document.reject` audit event in its own transaction. The upload is refused with
  `document/infected`;
- no answer from the scanner refuses the upload with 503 `document/scan-unavailable` (fail
  closed). `/readyz` pings the scanner, and `TENURE_CLAMAV_ADDR` is required to serve;
- accepted types, by content: PDF, JPEG, PNG. A text file is accepted only as `.csv` or `.ofx`.
  No HEIC, WebP or office formats in phase 1. The client's declared type is never used;
- uploads share the `documents` table: `source` = upload, no number, the sniffed `content_type`,
  the cleaned `original_name` (metadata only, never a key), and `signs_document_id` for a signed
  copy. The archive key is `<owner_id>/<class>/<YYYY>/<id>.<ext>`;
- the owner uploads the classes contract, amendment, deposit, surety, inventory, supporting and
  other. The generated billing and tax classes are refused (`document/class-not-uploadable`);
- the form fields come first and the file last, so the body streams without buffering. The
  upload route alone takes 25 MB plus 64 KiB; every other route keeps 1 MiB. The server's read
  and write timeouts rise to 2 and 3 minutes for that route's sake.

**Q-68 Retention clock of lease documents and uploads — Open (slice 8).** RENT-SPEC-005 starts the
10 years of leases, amendments, inventories and deposit documents at the lease end, and those of
supporting documents at the tax year of the charge; both are unknown when the file is archived.
Provisional: the object lock is set at issue or upload plus `archive_retention_years`, a floor;
`documents.retain_until` stays unset, so the document is never eligible for deletion. When the
clock starts, `archive.retention` (slice 8) sets `retain_until` and extends the lock; the
immutability trigger will then need an exception for that one column. The dates of billing
documents are unchanged.

**Q-69 Lease documents and the current contract — Open (slice 5).** The spec stores contracts,
amendments and guarantee documents only as archived documents. Provisional: a `lease_documents`
table, immutable like `billing_documents`, records each one with its number, kind, the lease
version it was generated from, its guarantee and its input snapshot. A contract is current while
the lease version and a fingerprint of the lease's guarantees match its snapshot. Editing the
draft or a guarantee therefore needs a new contract before activation. Attaching a generated DG
or CA to a guarantee does not. The signed copy is an upload whose `signs_document_id` points at
the generated contract.

**Q-70 Inventories — Open (slice 5).** Provisional:
- one entry and one exit inventory per lease, each recorded from an uploaded document of class
  inventory of that lease, once and never changed;
- the entry inventory is recorded on a draft or active lease, the exit one on a lease under
  notice or ended;
- `items` is free JSON until the structured inventory of phase 2 (RENT-SPEC-009);
- `GET /leases/{id}/inventories` is added for the lease page.

**Q-71 Amendments — Open (slice 5).** RENT-SPEC-008 lists a new tenant, a rent change outside IRL
and an added box. Provisional:
- an amendment applies only to an active lease or one under notice, and its effective date is
  not before the lease start;
- the lease rules are checked as for a renewal on the effective date (zone ceiling, consistent
  ownership shares);
- the change applies at once, so the next rent call issued uses it;
- a rent amendment never changes the deposit or the deposit guarantee, and the cap is not
  re-checked;
- an added asset must be a box of the portfolio, under no other current lease;
- the amendment AV-… is numbered on the principal asset, and its signed copy may be uploaded but
  is not required.

**Q-72 Guarantee rules — Open (slice 5), one point Needs answer.** Provisional:
- a guarantee is in force while held or valid;
- a surety and a rent insurance in force together are refused unless every tenant of the lease
  is a student or an apprentice (`tenants.occupation`, loi 89-462 art. 22-1);
- Visale with a surety is refused (RENT-SPEC-008: Visale replaces a surety);
- the deposits in force together must not exceed `deposit_max_months` × the rent excluding
  charges. This is checked at the lease save, when a deposit guarantee is created and when it
  grows, but never after a rent change;
- a surety needs the guarantor's name and address. The guarantor object reads name, address,
  legal_person and solidary (default true) for a surety, visa_number for Visale, and insurer and
  policy for an insurance.

**Needs answer:** Visale combined with a rent insurance is allowed for now. The spec is silent, and
Action Logement's conditions may forbid it.

The 2024 fixture holds only the lease keys a lease starting in 2024 reads (the deposit caps, the
mobility term and the box warning).

**Q-73 Required diagnostics — Open (slice 5), needs legal review.** Which diagnostics a let
dwelling needs depends on its construction year, its installations' age and its zone, none of
which Tenure records. Provisional, in `legal_params` `diagnostics`:
- a dwelling needs a DPE, an état des risques, electricity, gas, lead and asbestos, unless the
  owner declares a kind not required, with a reason;
- a box needs the état des risques;
- a diagnostic is valid until the earlier of the date its report states and its legal validity
  (DPE 10 years, ERP 6 months, electricity and gas 6 years, lead 6 years, asbestos no expiry); a
  DPE made before 1 July 2021 is void;
- validity is checked on the day of generation and again on the day of activation;
- the DPE class used is the worse of `assets.dpe_class` and the DPE's result, and a class worse
  than `rental_min_dpe_class` (F from 2025, loi Climat et résilience) blocks.

The lead diagnostic's unlimited validity when no lead is found is not modelled: the owner
re-records it or declares it not required.

**Q-74 Contract content and annexes — Open (slice 5), needs legal review.**
- The contract/v1 template follows the structure of the décret 2015-587 model for unfurnished,
  furnished and mobility leases, and a code-civil lease for a box let alone.
- The statutory annexes are listed by reference: the information notice of the arrêté of
  29 May 2015, and the décrets 87-712 and 87-713. Their verbatim texts are to be added in
  contract/v2 after legal review, before the phase 1 gate.
- The diagnostics file is listed from `asset_diagnostics`. The copropriété extract is listed for
  an apartment.
- The vetted special clauses are boiler_servicing, chimney_sweeping, garden_upkeep and
  insurance_certificate (`[{"code": …}]`); any other clause blocks generation.
- The surety act reproduces art. 22-1 and leaves room for the guarantor's own handwritten mention
  (C. civ. art. 2297).
- Signature stays wet or external (OD-08).

**Q-75 Routes and numbers beyond RENT-BE-001 §3 — Open (slice 5).** Added for the owner view and
the guarantees:
- `GET /leases/{id}/blockers`, which lists the generation and activation blockers and the warnings,
  with their problem types and titles;
- `GET /leases/{id}/documents` and `GET /leases/{id}/inventories`;
- `POST /guarantees/{id}/document`, which issues a DG for a deposit held or a CA for a surety;
- `GET/POST /assets/{id}/diagnostics` and `PUT /diagnostics/{id}`.

The acte de cautionnement is numbered with a new prefix CA (class surety).

**Q-76 Mobility lease — Open (slice 5).** Provisional:
- `leases.mobility_reason` (the seven reasons of art. 25-12) is required to generate the contract;
- the term must be within `mobility_term_months` and the deposit must be zero, both checked at
  save;
- the contract states the charges as a monthly flat rate (art. 25-18), so slice 6 must keep
  mobility leases out of the regularisation.

## Raised by slice 6

**Q-77 Charges ledger and syndic import — Open (slice 6).** Provisional:
- a charge stores its recoverable part in cents, so no rounding is introduced. An imported line
  also keeps `syndic_recoverable_cents`, the syndic's own split, so the owner's override stays
  visible; the audit trail records the split before and after;
- imported lines are drafts until the owner confirms them, and a draft charge in the exercise
  blocks the allocation (`reconciliation/draft-charges`);
- the import reads a CSV uploaded first through `POST /documents`, so it is scanned and archived,
  with a JSON column mapping. The default delimiter is `;`; amounts read `1 234,56` (or
  `1,234.56` with `decimal_point`);
- every line becomes `copro_statement`. A negative line is refused (a correction is entered by
  hand), and so is a recoverable part above the amount; a statement the mapping cannot read whole
  imports nothing;
- one `charge.import` audit event lists the charges created;
- a charge used by an issued reconciliation is frozen (`charge/reconciled`), and released when
  that reconciliation is cancelled.

**Q-78 Exercise and provisions collected — Open (slice 6).** Provisional:
- an exercise runs from the 1st of a month to the last day of a month, so rent-call periods fall
  wholly inside it;
- the provisions collected are taken from the live rent calls of the exercise's periods. For each
  call, the payments matched to it are split between its lines in proportion to their amounts
  (Q-65), and the provision part is rounded up to the cent, so the tenant's balance is never
  overstated;
- an unpaid call contributes nothing;
- a mobility lease is left out (Q-76): its share of the days stays with the owner, with no
  statement and no provision.

**Q-79 Carrying the balance — Open (slice 6).** Provisional:
- `regularisation_postings` holds what the next fees carry: one posting per balance, or per
  instalment of a late statement;
- a posting is due from the month after the statement is issued;
- `billing.monthly`, and the reissue after a cancel, apply the oldest posting due, one per fee,
  and record the rent call that carried it;
- a credit larger than the fee leaves a carry-over posting for the next month (Q-17). A posting
  whose rent call is cancelled is carried again by the reissue, and the carry-over of the
  earlier carry is voided;
- a reconciliation is settled once every posting is carried by a live rent call. Its payment is
  then followed as arrears (RENT-SPEC-003).

**Q-80 Statements — Open (slice 6).** Provisional:
- one statement RG-… per lease that covered the asset, even with a zero balance, e-mailed to its
  tenants with the PDF;
- the allocation is computed again when the statement is issued, as of the issue date (payments
  received since count), and the owner's choices on the lines are kept;
- the new provision applies only when the owner validated it (`apply_provision`) and the lease is
  still billed, from the next rent call;
- the supporting documents stay available until the issue date plus
  `regularisation_supporting_docs_months` (art. 23).

**Q-81 Tenant who left — Open (slice 6).** Provisional:
- a lease no longer billed settles against its deposit while one is held, otherwise directly;
- the line records the mode, with no posting;
- the deposit settlement statement itself comes with the exit flow of phase 2 (RENT-SPEC-009).

**Q-82 Correction — Open (slice 6).** Provisional:
- cancelling an issued or settled reconciliation issues a cancelling document per statement
  (class regularisation, RG number, template cancellation);
- it voids the postings not yet carried and reverses the carried ones with a negative posting
  due next month;
- it releases the charges;
- a provision already applied stays: the owner amends it if needed;
- the next reconciliation of the same exercise records `corrects_id`.

**Q-83 charges.reconcile — Open (slice 6).** Provisional: on demand only, never scheduled; its
period key is `<asset id>/<year>`. It allocates the asset's reconciliation of that exercise year
when in draft or allocated (dry run: computes only), and issuing stays an owner action.

**Q-84 Lines per lease — Open (slice 6).** RENT-BE-001 §2 gave `reconciliation_lines` a
`charge_id`. Provisional: one line per lease, with its days, share, provisions, balance,
instalments, provision proposal and settlement, while the charges used are listed on the
reconciliation (`charge_ids`) and in each statement.

## Raised by the BFF

**Q-85 Step-up through the BFF — Open (BFF), Socrate to confirm.** The step_up routes need
`auth_time` at most 5 minutes old and `mfa` in `amr` (Q-02). With the BFF, the browser cannot ask
Socrate for that itself. Provisional:
- `/bff/login?step_up=1` adds the standard OIDC `prompt=login` and `max_age=0`;
- the new session replaces the old one;
- whether Socrate honours both parameters is to be confirmed; until then, the API still refuses a
  token that is not fresh, so the route fails closed.

**Q-86 Session storage and logins — Open (BFF).** Provisional:
- sessions and pending logins are kept by backendkit's PostgreSQL stores in two tables of
  migration 000006, encrypted with AES-256-GCM under `TENURE_SESSION_KEY`, and created by the
  migration so the stores run no DDL;
- the tables have no `owner_id` and no RLS: a session belongs to a Socrate identity before any
  scope is chosen. Only `tenure_api` holds rights on them, and `tenure_jobs` none;
- a session ends after 30 minutes idle and 8 hours at most (RENT-SPEC-022: refresh 8 h);
- logins and logouts are logged (slog, subject only), not audit events: Socrate audits sign-ins,
  and the owner audit chain starts with a scope;
- logout revokes the refresh token and ends the tenure session; it does not end the user's
  Socrate SSO session, so the next login may not ask for a password.

## Raised by slice 7

**Q-87 Co-owned assets — Open (slice 7).** The data of an asset lives in its managing owner's
scope, while its income splits between every owner by share. Provisional:
- the summary computed in a scope holds every share-holder's figures (`summary.owners`);
- the PDF and the issue are for the scope's owner, on their own share;
- a co-owner whose share sits in another owner's portfolio reads it through their grant;
- no summary aggregates several scopes in phase 1.

**Q-88 What a summary counts — Open (slice 7).** Provisional:
- payments by the date they were received;
- charges by the date they were paid; a charge with no payment date is not counted;
- a draft charge is flagged (`draft_charge`), never deducted;
- an asset counts as furnished (gross receipts only, OD-01) when it is marked furnished or a
  furnished or mobility lease covered it during the year;
- the draft is recomputed by `tax.annual` and on every `POST /tax/summaries`;
- a year is issued once and is final in phase 1: correcting a filed declaration is out of scope.

**Q-89 Deficits carried — Open (slice 7).** Provisional:
- the prior deficits are those the latest issued earlier year left;
- issuing the réel keeps its carry-forward;
- issuing micro-foncier keeps the prior deficits, which micro does not use, less those that
  expired. The result is stored with the summary (`deficits_after`).

**Q-90 Outputs and access — Open (slice 7).** Provisional:
- the PDF is numbered `RF-<owner 8 hex>-<YYYY>-<seq>` like every document, the spec's
  `RF-<owner>-<YYYY>` with the sequence added;
- the CSV of the lines (one row per owner, asset and line with its source ids, then the flags and
  the supporting documents) is archived beside it, class tax_summary, retained from the issue
  date; the supporting documents are listed in both;
- `POST /tax/summaries` is a step_up route whether it drafts or issues;
- `GET …/lines.csv` is a step_up export, recorded as `tax.export`;
- the labels of the 2044 lines are in the versioned template, as French text;
- the réel option's lock-in is `reel_option_lock_years` (3, CGI art. 32-4).

## Raised by slice 8

**Q-91 Exports — Open (slice 8).** RENT-BE-001 says "async" but not where the state lives. Provisional:
- the state is a table, `export_jobs`, not `job_runs`: an export belongs to a person, not a period;
- `POST /documents/export` takes a year or an asset (both or neither is 422), records the
  request (`document.export`), answers 202 and builds the ZIP after the commit;
- the ZIP holds `index.csv` (file, id, class, source, links, period, issued\_at, sha256, size,
  retain\_until) and every document of the scope the caller may read; each object is re-hashed
  on the way and a mismatch fails the export;
- an accountant's export holds the financial classes only (no contracts, guarantees or IDs);
- the ZIP is stored at `<owner_id>/exports/<id>.zip` without an object lock: it is a copy, not
  an archive; the outcome is audited (`document.export.ready` or `.failed`);
- `GET /documents/exports/{id}` gives the state, then a 10-minute signed link (`document.download`);
  the requester and the scope's owner may read it.

**Q-92 Grants — Open (slice 8).** Provisional:
- the owner grants by the grantee's Socrate subject, which the grantee reads on their `/me`;
  no invitation by e-mail in phase 1 (it would need a Socrate lookup);
- one live grant per grantee and owner; granting oneself is refused;
- `POST /grants` and `DELETE /grants/{id}` are step\_up; a revoke sets `revoked_at` and the
  grantee's next request is 403.

**Q-93 RGPD export — Open (slice 8).** Provisional: `GET /me/export` (step\_up) returns, as JSON,
the caller's owner profile, the grants they gave and the grants they received; it is audited as
`me.export` in their chain when they own a profile. A tenant's data is exported on request
through their owner, not by this route; the business data of a portfolio is the ZIP export.

**Q-94 archive.verify sample — Open (slice 8).** The spec says "a sample". Provisional: 50
documents chosen at random each week across owners (an operational value, not a legal
constant, so it stays in code); any mismatch fails the run, which raises the alert.

**Q-95 When retention starts — Open (slice 8).** RENT-SPEC-005 starts the 10 years of lease
documents at the end of the lease, and of supporting documents at the end of the exercise.
Provisional:
- `archive.retention` sets `retain_until` once (the only change the documents trigger allows) for
  lease-bound documents of ended or closed leases, from `end_on`, and for supporting documents of
  charges, from 31 December of the latest exercise year;
- the object lock set at issue (from the issue date) is extended to that date, never shortened:
  when the lock already runs later, `retain_until` takes the lock's date;
- documents past `retain_until` are listed for review; nothing is deleted in phase 1.

**Q-96 Web bundle — Open (slice 8).** tenure-web has published no release yet. Provisional:
- `web/VERSION` holds `<tag> <sha256>` of the asset `tenure-web-<tag>.tar.gz`, or `none`;
- the committed `web/dist` is a French placeholder page;
- a release build fetches and verifies the asset (`scripts/fetch-web.sh`, which refuses `none`,
  a bad checksum or an unsafe path), so no release ships the placeholder;
- the release workflow reads tenure-web with a `TENURE_WEB_TOKEN` secret (fine-grained,
  read-only Contents);
- tenure serves the bundle for GET and HEAD outside `/api` and `/bff`; any other path gets
  `index.html`, and a missing file with an extension is a 404 problem;
- the CSP is set by tenure per surface (`'self'`, no inline script, for the app;
  `default-src 'none'` for the API); Caddy adds headers only where missing;
- `index.html` gets a fresh nonce on every response, written over its `__CSP_NONCE__`
  placeholder and allowed in `style-src`: PrimeVue writes its theme into `<style>` elements at
  run time (tenure-web Q-W1); scripts stay `'self'` and `'unsafe-inline'` is used nowhere;
- hashed `assets/` are cached for a year and `index.html` is `no-cache`.
- the first pin is tenure-web v0.1.0 (the app shell), whose archive a rebuild from its tag
  reproduces byte for byte.

**Q-97 Backups — Open (slice 8).** RENT-BE-001 says "encrypted with a provider KMS key".
Provisional, stricter:
- the dump is encrypted on the host with age to a public key whose identity is kept offline,
  and the bucket adds SSE, so neither the host nor the provider can read a backup alone;
- nightly at 01:30 Europe/Paris, kept 35 days by the bucket's lifecycle rule;
- the dump keeps owners and grants, since the RLS helper must stay owned by `tenure_rls`;
- the dump role is `tenure_backup`: `pg_read_all_data`, `BYPASSRLS` (pg_dump refuses
  filtered tables) and read-only by default (`deploy/sql/backup-role.sql`);
- a restore goes into a fresh database only. The round trip was tested on PostgreSQL 17 with
  the RLS helper still owned by `tenure_rls`.

**Q-98 Deploy — Open (slice 8).** Provisional:
- one VPS: Caddy, `tenure` under a hardened systemd unit, PostgreSQL 17 on the host, and
  gotenberg and ClamAV as loopback containers at the integration suite's versions;
- `deploy.sh` migrates with the new binary while the old one serves, then swaps the symlink;
  this needs the migrations of a release to be additive, so a rollback is the previous binary;
- a migration that is not reversible needs a backup just before the deploy;
- tenure ships no container image, so the spec's "container image scanned" applies to the
  sidecars' upstream images and is left as a follow-up in `docs/security/asvs-l2.md`.

## Raised during setup

**Q-99 Socrate variable names — Answered 2026-11-03.** The owner asked that the block Socrate's
console gives for a registered application paste into tenure's env file unchanged. Decided:
- tenure reads `SOCRATE_ISSUER`, `SOCRATE_JWKS_URL`, `SOCRATE_CLIENT_ID` and
  `SOCRATE_CLIENT_SECRET`, the names backendkit's `docs/CLIENT-INTEGRATION.md` §3 and the console
  use; all four are required and have no default;
- the audience is `SOCRATE_CLIENT_ID`, as in backendkit's example
  (`jwtauth.WithAudience(client id)`), unless `SOCRATE_AUDIENCE` is set; the application's token
  settings must list that audience;
- the console's `SOCRATE_BASE_URL`, `SOCRATE_ADMIN_BASE_URL` and `SOCRATE_APP_ID` are accepted
  and unused: tenure calls neither the admin API nor a service-account method;
- `TENURE_SOCRATE_ADMIN_ROLE` keeps its name: it is tenure's choice of role, not a client setting;
- the former `TENURE_SOCRATE_{ISSUER,JWKS_URL,AUDIENCE,CLIENT_ID,CLIENT_SECRET}` refuse to
  start, naming the new variable, so that a stale file is never half read.

**Q-100 Standard lease terms — Open (setup).** The owner asked for default values on every
input. A lease's term is set by law, and no term may be written in code. Provisional:
- `lease_term_months` holds the standard terms of loi 89-462: unfurnished 36 months, or 72
  when the lessor is a legal person (art. 10); furnished 12, or 9 for a student (art. 25-7);
- the web form proposes the term for the lease's kind; the mobility lease keeps
  `mobility_term_months`;
- the API does not yet refuse a shorter term: the parties may agree a longer one, and a
  shorter unfurnished term (art. 11) needs a stated reason that the model does not carry yet.

**Q-101 PDF engine unavailable — Answered 2026-11-03.** CI on #44 got a 500 from a document
route: gotenberg 8.21 starts Chromium and LibreOffice on the first conversion (7 s for PDF/A),
restarts them every 10 conversions, reports `/health` up before either has started, and answers
503 after its 30 s request limit. RENT-BE-001 §6 says a failure rolls the number back, not which
status the client gets. Decided:
- gotenberg runs with `--chromium-auto-start=true --libreoffice-auto-start=true
  --api-timeout=50s` everywhere (`document.GotenbergFlags`, both compose files), so its own limit
  ends a slow conversion before tenure's 60 s client does;
- tenure retries a 503 once after a second; 502, 503, 504, no answer and a client timeout map to
  `document/pdf-unavailable`, 503, « Le moteur PDF est indisponible ; réessayez plus tard. »; the
  document number is rolled back as for any failure; a 4xx from the engine stays a 500 (a bug);
- the integration suite waits for one real PDF/A conversion before its tests start.

**Q-102 Zone tendue and rent ceiling — Answered 2026-11-03.** The owner asked about a flat in
Baillargues: it is in a zone tendue (the Montpellier area, décret 2013-392) but outside the rent
ceiling, which covers only the commune of Montpellier (loi ELAN art. 140, arrêté préfectoral).
`assets.controlled_zone` drove both regimes, so that flat could be modelled neither way: set, it
needed a ceiling zone and the lease was refused; unset, the relocation rule was never checked.
Decided:
- `assets.tense_zone` (migration 000010) is the zone tendue: `rent_control.relocation_rule`
  applies to a new lease on a tense asset let before, and the contract (template v2) states the
  previous tenant's rent;
- `controlled_zone` keeps the ceiling (`rent_control.zone_ceiling`) and implies `tense_zone`: the
  service writes and returns `tense_zone` as true with it, and reads an asset as tense when either
  column is set, so existing assets need no backfill (the migrate role sees no owner's rows);
- the owner sets the flag; the list of communes is not encoded, and the help text points to
  service-public.fr's simulator. The one-month notice of a tenant in a zone tendue is not computed
  by tenure (no notice dates are), so it is not modelled yet;
- fixing it exposed a 500 on every relocation check with a previous lease:
  `LatestIRLOfQuarter` compared a text column with an integer. The integration suite now covers
  the check on a tense asset.

**Q-103 gotenberg start timeouts — Open (#53).** With Q-101's auto-start, each engine must start
within gotenberg's `--chromium-start-timeout` and `--libreoffice-start-timeout` (20 s by default)
and the API within `--api-start-timeout` (30 s); past them gotenberg 8.21 exits at boot
(`waiting for modules readiness: … context deadline exceeded`), reproduced at `--cpus=0.15`
after 21 s and 23 s. Provisional:
- `document.GotenbergFlags` and both compose files add `--chromium-start-timeout=60s
  --libreoffice-start-timeout=60s --api-start-timeout=90s`; at `--cpus=0.15` gotenberg then
  boots in 30 to 50 s and renders on the first attempt, and an unthrottled boot is unchanged;
- a unit test keeps both compose files' `command` equal to `document.GotenbergFlags`;
- the integration suite polls `/health` once a second instead of every 100 ms: at `--cpus=0.15`
  Chromium's health check timed out on every one of 333 polls in a row while the API was up,
  and the 120 s wait expired; at one poll a second the same run passes. Production's compose
  healthcheck polls every 30 s.

**Q-104 End-to-end flows in CI — Open (tenure-web slice 7b).** RENT-FE-001 §7 asks for
Playwright on the key flows against a seeded backend in CI. tenure-api runs them, because it holds
both the backend and the pinned web bundle. Provisional:
- **Where:** `e2e/` in tenure-api. `e2e/run.sh` starts the services of docker-compose.dev.yml
  under their own compose project with fresh volumes, then the test fakes, then
  `tenure migrate up` and `tenure serve` built from the tree with `web/dist`. It then runs
  `e2e/flows`. The `ci` job runs it after the integration suite, on the bundle pinned in
  `web/VERSION` (`fetch-web.sh` with `TENURE_WEB_TOKEN`, as release.yml does). A web change is
  therefore checked end to end when its release is pinned, not on its own PR, where tenure-web's
  mocks run.
- **Fakes:** `e2e/fakes` is built only with the `e2e` tag and never ships. It listens on the
  loopback only. It provides:
  - Socrate's authorize, token, userinfo, revoke and JWKS endpoints. Authorize signs the browser
    in at once as one owner with a fresh MFA authentication, so the step-up routes pass. It checks
    the client, its secret, the redirect URI and the PKCE S256 challenge;
  - an SMTP sink (`notify/smtptest`), read through `GET /__e2e/mail`, because Mailpit's image
    cannot always be pulled;
  - the INSEE IRL series, with sample values.

  None of this is a local login: tenure still only trusts Socrate's tokens through its JWKS.
- **Seed:** none beyond the migrations and their legal parameters. The flows create everything
  through the UI, in order:
  1. the profile;
  2. flow 1 (asset with its six diagnostics, draft lease, deposit, contract, signed copy, entry
     inventory, activation);
  3. flows 2 and 5 (dry run, issue and its e-mail, cancel and reissue from the archive, payment,
     receipt);
  4. flow 3 (syndic CSV, reconciliation, RG, new provision);
  5. flow 4 (a property tax without its notice, the 2044 lines, issue with PDF and CSV).
- **Years:** the income year is the latest one with a published `form_2044_line_map`, read from
  `/params`. The lease starts on its 1st of January and the current month is billed, so the
  tests need no year written in them.
- **Findings while writing them**, recorded and left as they are:
  - the tax summary is cash basis, so imported syndic lines count only once their payment date
    is set;
  - syndic statement lines carry `copro_statement`, a category on no 2044 line;
  - a cancellation is archived in the class of what it cancels (Q-49);
  - a blocker's detail names an asset by its id.
- **Cost:** about 40 s for the flows, after the services start. ClamAV's signatures and
  gotenberg's boot dominate. gotenberg starts before ClamAV and gets one restart, because
  LibreOffice can miss its start timeout while the host loads the signatures (Q-103).
- A pull request whose workflow cannot read `TENURE_WEB_TOKEN`, such as Dependabot's, fails at
  `fetch-web.sh`. Its secret is set separately (Dependabot secrets).

**Q-105 What a lease blocker is about — Open (#71).** The end-to-end flows (Q-104) showed blockers
naming their asset only inside `detail`, as an id. The web app displayed that as it was, for
example « (asset 01a1…: dpe) ». Provisional:
- `lease.Blocker` gains three optional fields. Nothing is removed, and `type`, `title` and
  `detail` keep their values; `detail` stays the English technical text of RENT-BE-001 §8:
  - `asset_id`;
  - `diagnostic`, the kind;
  - `valid_until`, for an expired diagnostic.
- They are set on `diagnostic-missing`, `diagnostic-expired`, `dpe-class-missing`,
  `dpe-class-forbidden` and `zone-ceiling-unknown`. The other blockers concern the lease
  itself (contract, guarantees, inventory) and carry none.
- The fields travel in `invariant.Error.Subject`, which is never part of a problem body. A
  refused `POST /leases/{id}/contract` therefore answers exactly as before.
- The web app shows the asset's label and the diagnostic's French name, and links to the asset
  where the diagnostic is recorded.

**Q-106 Document templates in S14 — Open (#83).** RENT-FE-001 S14 lists « templates and versions »,
and RENT-BE-001 §6 said « one set per owner overridable ». No route served the templates, and
nothing let an owner override one. Provisional:
- `GET /templates` lists the templates embedded in the release. For each one it gives:
  - its name (the directory) and the class it renders (`deposit_receipt` renders `deposit`);
  - its versions, oldest first, each with the SHA-256 of its `document.html`;
  - which version is current: the latest, since a template change only ever adds a version.
    A unit test in `internal/app` fails if a module renders a version other than the latest
    embedded one;
  - how many of the scope's generated documents each version rendered, with the first and last
    issue times, read under RLS.
- `shared/v1` (style and footer of every document) is not listed as a template.
- An accountant lists only the templates of financial classes, and cancellation, which only
  cancels billing documents (Q-49). This matches their documents (Q-91).
- The route is read only, so it needs no audit event.
- Not built, conservatively:
  - a per-owner override set. Editing the French model contract or a receipt is a legal and
    security matter: templates are code, reviewed and released, and an uploaded template would
    run in the renderer;
  - a specimen preview, which needs sample data in production code and a render on every look.
  §6 now says the override is deferred. Both wait for an owner's request and its review.

**Q-107 Inventory engine, slice 1 — Open (#89).** RENT-INV-001 slice 1 builds the pure functions
before INV-OD-04 and INV-OD-13 are decided, and leaves a few points to the implementation.
Provisional, each the conservative reading:
- **Condition scale (INV-OD-04, decided 2026-11-05 as proposed).** The code follows the proposal: five levels, `new`,
  `good`, `fair`, `poor`, `broken`, with `works` (ok, ko, n/a) recorded apart. `works` going
  from ok to ko is « worse » even when the condition stays the same.
- **Restitution delay (INV-OD-13, decided 2026-11-05 as proposed).** The code follows the proposal: the shorter delay
  applies whenever no deduction is retained, even when the exit differs from the entry. This can
  only bring the date closer for the tenant.
- **Months.** A legal delay in months ends on the same day of the month, or on the month's last
  day when there is none (`period.Date.AddMonthsClamped`; code de procédure civile art. 641).
  `AddMonths`, which would turn 31 January + 1 month into 3 March, is not used for deadlines.
- **Started months of delay.** One day after the deadline starts the first month. The m-th month
  starts the day after the deadline + (m − 1) months.
- **Vétusté.** Age is counted in full years. At the line's lifetime, the share is the floor even
  if the yearly formula gives more. A grid with no line for the item's category refuses the
  deduction (`ErrNoGridLine`) rather than charging the whole cost. Without a grid, the whole
  cost is charged and must be justified item by item, as §6 says.
- **Holdback.** The share of the deposit held back in a collective building is rounded down, so
  it never exceeds the allowed share.
- **Coverings.** Outdoor parts (room kind `outside`: garden, façades, gate) are not rooms. The
  floor, walls and ceiling are required in every other room.
- **Furnished.** A furniture element of décret 2015-981 replaces its unfurnished equivalent (the
  kitchen hob, the oven) instead of being added twice. The furniture of a room kind the asset
  lacks, such as a studio's bedroom, is seeded in the living room. A furniture item with
  quantity 0 or `broken` at entry warns, and never blocks.
- **Parameters.** The restitution keys of RENT-INV-001 §7 are named in `internal/params`
  (`DepositReturn`). Slice 1 left them unseeded; slice 2 seeded them (Q-108), and the
  settlement golden case now reads the legal fixtures instead of synthetic values.
- **Templates.** The system templates are data in `testdata/inventory/templates.json`, in French,
  which slice 2 seeds as `inventory_templates` rows. The furniture list is to be checked against
  Légifrance before inventory slice 6, with the other points RENT-INV-001 §12 lists.
- **Golden cases.** They use made-up assets, never real tenant data.

**Q-108 Inventory schema, slice 2 — Open (#91).** Migration 000011 builds RENT-INV-001 §3.
INV-OD-01 (normalised items), INV-OD-04 (the scale) and INV-OD-13 (the delay) were decided on
2026-11-05 as proposed. Provisional readings:
- **Phase 1 rows.** `tenure_migrate` reads no owner's rows (FORCE ROW LEVEL SECURITY), so the
  migration cannot UPDATE them. `state` and `mode` are therefore added with the defaults
  `issued` and `upload`, which DDL writes into every existing row, and the defaults are then
  dropped so a new row must state both. `items` is renamed `legacy_items`, its content
  unchanged. The integration suite proves this on a database stood at 000010 with a phase 1
  row; the row is not turned into `inventory_items`, since it has no link to a layout.
- **Items retired.** `POST /leases/{id}/inventories` now refuses a non-empty `items` (422
  `inventory/items-field-retired`). An uploaded inventory is recorded `issued`, `mode upload`,
  and only an issued entry clears `lease/entry-inventory-missing`. The view shows `mode`,
  `state` and, when not empty, `legacy_items`; the former `items` field is no longer
  returned.
- **Immutable once issued.** A trigger refuses to UPDATE or DELETE an issued `inventories`
  row, and to INSERT, UPDATE or DELETE an item, meter, key, photo or signature of one, even as
  superuser. If the parent cannot be read, the trigger refuses (fail closed). A completion
  belongs to an issued entry by nature, so it is not under that rule: it has its own decision.
  Other checks: an issued inventory has its document; a completed or later one has its date;
  an upload goes from draft to issued only.
- **Dates.** `done_on` stays a date, nullable until the inventory is completed, rather than the
  `done_at` timestamp of §3: an inventory is dated by its day, as the décret asks.
- **Templates.** `inventory_templates` has no `furnished` column: one structure per asset kind
  carries the furniture a furnished dwelling adds (`internal/inventory.Template`). System rows
  (`owner_id` null) are seeded from `testdata/inventory/templates.json` with an id derived from
  the asset kind and the version. They are checked by `go test ./migrations`: a changed
  template is a new version, never an edited row. Every scope and job reads them; only
  `tenure_migrate` writes them (its own policy, limited to system rows).
- **Deductions.** `deposit_deductions.document_id` is required: a vétusté grid sets the
  tenant's share, never the cost, which always comes from a quote or an invoice (art. 22).
  `amount_cents` is at most `cost_cents`, and the share is 0 to 10 000 bp.
- **Meters.** A reading is a decimal string as read (`123,45`), checked by a pattern, never a
  float.
- **Parameters.** The five restitution keys (loi 89-462 art. 22) and the two completion windows
  (art. 3-2) are seeded with their legal values and sources. `heating_season.start_month` and
  `.end_month` are a product rule (INV-OD-07: the law fixes no heating season) and say so in
  their source. All are dated 2024-01-01, like the other deposit fixtures; the legal texts date
  from loi 2014-366.
- **Policies.** Rows that may be removed before issue (layout edits, replaced meter and key
  lists, a photo, a deduction) have a delete policy for the owner. The signatures, the
  completions and the grids have none.
- **Reversibility.** Down restores the phase 1 schema while no phase 2 row exists. It fails,
  deliberately, on a draft without a date or a document.

**Q-109 Asset layout API, slice 3 — Open (#93).** RENT-INV-001 §7 names the routes, not how a
layout is versioned or edited. Provisional:
- **Version.** An asset's layout has its own version in `asset_layouts` (migration 000012),
  apart from `assets.version`, so an asset edit and a layout edit never overwrite each other.
  Version 0 means nothing has been seeded or saved yet: `GET` answers W/"0", and the first
  `PUT` sends `If-Match: W/"0"`.
- **Edit.** `PUT` replaces the whole layout in its new order, the positions following the array.
  A room or element with an `id` keeps it, so the items of past inventories stay linked
  (`element_id`); an id that is not of this asset is a field error. Rooms and elements left out
  are removed: a removed room takes its remaining elements, while an element moved to another
  room is moved first. An inventory keeps its copied label whatever happens to the layout. A
  room or element changes version only when it actually changes.
- **Seed.** `POST …/seed` uses the latest template of the asset's kind (the owner's own first,
  else the system's) with the bedroom count, the features (cellar, garage, garden) and the
  furnished flag (the asset's own unless the body says otherwise). It is refused with
  `inventory/layout-exists` once the asset has rooms: a seeded layout is edited, never seeded
  again. No migration seeds the owners' real assets: each owner seeds from screen S15a.
- **Limits.** At most 60 rooms and 80 elements per room: product limits, not legal values.
  `installed_on` cannot be in the future (Europe/Paris).
- **Roles.** The owner writes and the co-owner reads. The accountant is refused (403), as for
  inventories and photos (RENT-INV-001 §8).
- **Audit.** `layout.seed` and `layout.update`, on the asset, with the layouts before and after.

**Q-110 Inventory photo pipeline, slice 4 — Open (#97).** RENT-INV-001 §3 and §7 name the
pipeline and the routes, not the limits, the orientation or the retention before lease end.
Provisional:
- **Upload.** `POST /inventories/{id}/photos`, multipart, `item_id` or `meter_id` (exactly one)
  before the file. The upload is capped like a document (25 MB) and waits in quarantine while
  ClamAV scans it; an infected file is refused with `document/infected` and leaves an
  `inventory.photo.reject` event, a scanner failure is `document/scan-unavailable` (fail closed).
- **Cleaning.** Only JPEG and PNG are accepted, at most 8 192 px a side and 40 megapixels,
  checked from the header before decoding (product limits, not legal values); anything else is
  `inventory/photo-not-image`. The EXIF orientation of a JPEG is applied to the pixels, then
  the image is re-encoded to JPEG (quality 85) from its pixels alone, so no EXIF, location,
  maker note, XMP, ICC profile or comment survives. `sha256` is the cleaned file's,
  `original_sha256` the upload's; the audit event `inventory.photo.add` holds both. `taken_at`
  stays empty: it would come from the metadata that is stripped.
- **Retention.** The photo is locked for `archive_retention_years` from its upload: the clock
  RENT-SPEC-005 starts at lease end is not known yet. `archive.retention` extends only
  `documents` today; extending it to `inventory_photos` at lease end (never shortening a lock)
  is deferred to slice 7, which issues the exit; until then a photo is locked from its upload.
- **Issued inventory.** A photo is refused with `inventory/issued-immutable` (409) once the
  inventory is issued; the database trigger refuses it too.
- **Viewing.** `GET …/photos/{photoId}/content` gives a 10-minute signed link, shown inline,
  audited as `inventory.photo.view`. The owner and the co-owner may view; the accountant is
  refused (403), as for the layout (Q-109).
- **CSP.** The web app's `img-src` is `'self' data: blob:` plus the archive origin, taken at
  start from a link the store signs (INV-OD-12); no other directive changes. Caddy keeps its
  fallback policy for the API only. tenure-web mirrors the policy in its dev server with the
  capture screen (slice 5).

**Q-111 Entry inventory captured in Tenure, slice 5 — Open (#99).** RENT-INV-001 §5 and §7
give the lifecycle and the routes, not the creation body, what each state lets change, or how
an inventory is issued before the signatures exist. Provisional:
- **Creation.** `POST /leases/{id}/inventories` takes `mode: amicable` (the default stays
  `upload`) with no date and no document: the entry is created as a draft, one item per element
  of every room of each asset's layout, in the lease's asset order. Each item copies its room's
  label and kind (`inventory_items.room_kind`, migration 000013), so a later layout edit never
  changes the inventory or its checks. An asset without a layout is refused with
  `inventory/layout-missing`. The exit capture waits for slice 8, and `bailiff` keeps the upload
  path. One entry per lease remains (Q-70): a draft entry stands in for the upload.
- **Edits per state.** The header (date, appointment, place, observations), the meters and the
  keys change in draft, scheduled and in progress; the items and the photos change in draft and
  in progress only. Anything else is `inventory/not-editable` (409), and an issued inventory
  answers `inventory/issued-immutable`. The header, meters and keys use If-Match on the
  inventory's version; an item uses its own version. A meter with photos cannot be removed
  (`inventory/meter-has-photos`). Product limits: 5 000 items, 30 meters, 50 key lines,
  1 500-character comments.
- **Transitions.**
  - `schedule` needs `scheduled_at` and `place` (in the body or the header).
  - `start` sets the date to today (Europe/Paris) unless one is set.
  - `complete` runs `Validate` and refuses with `inventory/mandatory-mention-missing` while a
    complete-stage check blocks.
  - `reopen` takes a completed inventory back on site.
  - `issue` takes a signed inventory: it refuses with `inventory/signature-missing` while a
    party has not signed, then renders the EE document. A wrong starting state is
    `inventory/transition-invalid`. `dry_run` answers `{allowed, checks}` and writes nothing.
  - `sign` and the signatures come with slice 6. Until then no inventory reaches `signed`
    through the API.
- **Parties.** The owner (legal name) and each tenant of the lease (their names joined) sign. A
  party has signed when a signature of that party carries that name. Representatives and the
  e-mailing of the EE to each party come with slice 6.
- **EE document.** `templates/inventory/v1`, French only, with:
  - the mentions of décret 2016-382;
  - a 320 px thumbnail of each photo, re-encoded from the archived file;
  - an annex listing the photos by SHA-256.

  The tenant's completion windows are printed from `legal_params` as of the inventory's date.
  The number is `EE-<asset>-<YYYY>-<seq>` on a sequence of its own (`document_counters.series`,
  migration 000013). The document is of class `inventory`, so its retention clock starts at
  lease end.

**Q-112 Signatures of an inventory, slice 6 — Open (#102).** RENT-INV-001 §5 and §7 name the
`sign` step and `POST /inventories/{id}/signatures`. OD-08 (Yousign, external eIDAS provider)
and INV-OD-03 (signing on site, decided 2026-11-05) say who provides the signature. None says
what is signed, how the provider's state reaches Tenure, or what each party signs.
Provisional:
- **What is signed.** `GET /inventories/{id}/preview` renders the completed inventory as a PDF:
  unnumbered and not archived, under a « projet soumis à la signature » banner, with one
  signature block per party still to sign. It is printed to be signed by hand, or sent to the
  provider with an invisible anchor per signer (`{{s1|signature|180|60}}`) placing each
  signature field. The EE document stays the one Tenure issues once every party has signed. It
  lists each signature with its method, date, reservations and the SHA-256 of its evidence
  (template `inventory/v2`).
- **Parties.** The owner (legal name) and each co-tenant, one person at a time (RENT-INV-001
  §3), from the names of the lease's tenant records. A party has signed when a signature of that
  party carries that name. A representative signing for someone, and the e-mailing of the EE
  to each party, are left for later.
- **By hand (`wet_scan`).** The signed copy is uploaded through `POST /documents` (class
  inventory, this lease: scanned, archived) and named with the parties who signed it and their
  reservations. Parties may sign on separate copies, one request each.
- **Electronic (`esign`, when configured).**
  - One request covers every party still to sign, each with an e-mail, which Yousign requires.
    The owner has no e-mail in Tenure and types theirs; a tenant's comes from their record.
  - The request uses delivery mode none, simple electronic signature, no OTP. Each signer's
    link is shown to the owner only, while the request is pending, and opened on the tablet for
    each party in turn (INV-OD-03). The links are never logged and never in an audit event.
  - The state is pulled: `POST …/signatures/refresh`, called after each party signs. There is
    no public webhook, so deny-by-default (RENT-BE-001 §4) holds.
  - A party's signature is recorded once the whole request is done, with the provider's signed
    file archived as evidence (class inventory, on the lease, clock at lease end). Until then
    the request shows who has signed.
  - A request refused at the provider (declined, expired, cancelled) ends as `refused`; one
    cancelled in Tenure (`POST …/signatures/cancel`) as `cancelled`. Only one request is pending
    at a time (`inventory/signature-pending`). A provider error is
    `inventory/esign-failed` (503).
  - Without `TENURE_YOUSIGN_API_URL` and `TENURE_YOUSIGN_API_KEY` (both or neither, https),
    `esign` is refused with `inventory/esign-unavailable`.
- **Issue.** The last signature moves the inventory to `signed`, then issues `EE` in the same
  transaction (two `inventory.transition` events). If the PDF engine fails, nothing is
  recorded and the signature or refresh is retried.
- **Reopen.** Refused once a party has signed (`inventory/signature-started`) or while a request
  is pending: the parties signed that content.
- **Not verified yet.** The Yousign client follows the v3 API as documented and is tested
  against a fake of the routes it calls. Before production, it must be checked against the
  sandbox:
  - anchors parsed from a Chromium PDF;
  - in-person signing with delivery mode none;
  - the signer and request statuses;
  - the download of a single signed file.

**Q-113 Exit inventory captured in Tenure, slice 8 — Open (#105).** RENT-INV-001 §5 and §6 say
the exit is pre-filled from the entry and compared with it. They do not say what an exit needs,
what « to check » demands, or what the comparison returns before the deposit settlement.
Provisional:
- **Creation.** `POST /leases/{id}/inventories` with `kind: exit, mode: amicable`, on a lease
  under notice or ended (Q-70). It needs the lease's issued entry; otherwise it is refused with
  `inventory/exit-without-entry` (409). `Prefill` gives the items:
  - each entry item, its condition, works, quantity and comment corrected by the decided
    completion lines, linked by `entry_item_id`, marked to check;
  - the keys as handed over;
  - the meters with their kind, number and unit, without a reading.

  An entry recorded from its PDF has no items: the exit is then drafted from the layouts, as an
  entry is, with nothing linked, and every item compares as new. An uploaded exit still needs no
  entry (phase 1 path).
- **Header.** An exit also records `keys_returned_on`, which cannot be in the future, and the
  tenant's new address. An entry refuses both. The deposit's clock reads
  `keys_returned_on` (slice 9).
- **Complete.** An exit completes only once every pre-filled item has been looked at
  (`exit-item-to-check`: saving an item clears its mark), so an entry condition is never carried
  over unseen. The new address, the return of the keys and the entry's date also block
  completing: they are header mentions, and the header no longer changes once completed.
- **Comparison.** `GET /inventories/{id}/comparison`, for an exit with its entry, is `Compare`:
  - per exit item: same, better, worse, missing or new, with the entry and exit states and the
    steps down the scale;
  - per key line: entry and exit counts and a result;
  - `flagged`: the worse and missing results, for the owner to qualify before the settlement
    (slice 9).

  It reads at any state, issued included. Any other inventory answers
  `inventory/comparison-unavailable`. The capture view gives each exit item its entry state too,
  so the exit screen shows it under the item.
- **Document.** Template `inventory/v3` renders the entry and the exit. The exit document, in
  series `ES`, adds:
  - the tenant's new domicile;
  - the entry's date and the day the keys were returned;
  - an « à l'entrée » column per item;
  - « clés restituées ».

  It omits the completion window. Signing is as for the entry (Q-112).
- **Completions.** The exit reads the decided completion lines of `inventory_completions`
  (slice 7, Q-114).

**Q-114 Entry completions and the EC document, slice 7 — Open (#107).** RENT-INV-001 §5 step 5
and loi 89-462 art. 3-2 let the tenant ask, within ten days, to complete the entry, and, for the
heating, within the first month of the heating season. They do not say how Tenure records the
request, how the windows are counted, nor how the owner learns of a request. Provisional:
- **Windows.** `inventory.CompletionWindows`, a pure function golden-tested on the legal
  fixtures, counts both windows from the entry's `done_on`, both ends included:
  - the ten-day window ends `done_on + inventory.completion_window_days`; a day before the
    entry is outside it;
  - the heating window runs from the first day of the next `heating_season.start_month` for
    `inventory.heating_window_months`. It exists only when the entry was done outside the
    season (RENT-INV-001 §5 step 4). An entry done in season has the ten-day window only.

  The season is one parameter for every asset. Making it editable per asset (INV-OD-07) is left
  for later: the parameter is the default.
- **Request.** `POST /inventories/{id}/completions` records the request as the owner received
  it, on an issued entry captured in Tenure. An upload has no items to complete; the request is
  refused with `inventory/completion-unavailable`. The request holds:
  - the reason (`ten_day`, `heating`);
  - the day it was made, not in the future and within its window, otherwise
    `inventory/completion-window-closed`;
  - optionally the tenant's letter, uploaded through POST /documents (class inventory, this
    lease);
  - one line per entry item, once each, with the condition, works or a comment asked for. A
    heating request covers heating items only.

  The tenant files it themselves only with the portal (phase 4).
- **Decision.** `PUT /completions/{id}` (If-Match) accepts or refuses every line. The request
  becomes `accepted`, `refused` or `partial`, on `decided_on` (today when omitted, not before
  the request). The `EC` document (template `inventory_completion/v1`, French) is issued in
  the same transaction. It cites the EE number and the window, gives each line's entry state,
  the state asked for and the decision, and says the issued entry is not modified. It is not
  signed: it records the owner's decision.
- **Immutability.** Once decided, a request no longer changes, and none is ever deleted. A
  trigger enforces this (migration 000015). The accepted lines are the entry's state wherever
  the entry is read against an exit: the exit pre-fill (when the exit is created) and the
  comparison (always).
- **Owner reminder.** RENT-INV-001 §7 has `billing.reminders` e-mail the owner 7 days before
  each window ends. Owners have no e-mail address in Tenure (only tenants do), so this reminder
  is not sent. Instead:
  - a pending request is a lease warning, `inventory/completion-pending`, which the home lists
    as a to-do;
  - `GET /inventories/{id}/completions` gives the window end dates and which windows are open
    today, for the inventories tab.

  The e-mail waits for an owner contact address.
- **Roles.** The co-owner reads; the accountant gets 403, as for every inventory route.

**Q-115 Deposit deductions and the RD statement, slice 9 — Open (#110).** RENT-INV-001 §6 gives
the arithmetic (built and golden-tested in slice 1). It does not say where the arrears and the
regularisation come from, how a holdback is released, or how a deduction is tied to the exit.
Provisional:
- **Wear grids.**
  - `GET/POST /vetuste-grids` and `PUT /vetuste-grids/{id}` (If-Match). Each line gives a
    category, once, with a lifetime, a franchise, a yearly rate and a floor of at most
    10 000 bp. Name and source are required; none is shipped (INV-OD-08).
  - A grid is annexed with `PUT /leases/{id}/vetuste-grid` rather than `PUT /leases/{id}`:
    lease edits are draft only, and the grid may be agreed later. It can be annexed until the
    deposit is settled.
  - An RD statement keeps the grid as it was used (a snapshot).
- **Collective building.** `assets.collective_building` is now in the asset API (create and
  update).
- **Deductions.** `GET/POST /leases/{id}/deposit-deductions` and `DELETE
  /deposit-deductions/{id}`, once the exit is issued and compared with its entry. A deduction is
  either:
  - on an exit item whose result is `worse` or `missing`, once per item;
  - or on a key line `missing` at exit.

  Anything else is `inventory/deduction-unjustified`: Tenure never charges an unflagged item.
  The cost comes from a quote or invoice uploaded as class `supporting` on this lease. The
  tenant's share is `Deduct`, with the lease's grid line for the item's category. The age runs
  from the layout element's `installed_on`, or the lease start, to the exit's date. A missing
  grid line refuses the deduction (`inventory/grid-line-missing`). A key, or a lease without a
  grid, takes the whole cost.
- **First statement.** `POST /leases/{id}/deposit-settlement` (`dry_run` in the body, as the
  billing runs do), once the exit is issued with `keys_returned_on`:
  - the deposit is the lease's `deposit_cents`;
  - the arrears are what the lease's live rent calls still owe (billing, handed to the
    inventory module by `internal/app`; issued RG documents are in them);
  - in a collective building without `accounts_approved_on`, the holdback of
    `deposit.collective_holdback_bp`, rounded down;
  - otherwise, an optional regularisation not yet billed, backed by a `supporting` document of
    this lease.

  The deadline is the keys' day plus `with_deductions` months when a deduction is retained, or
  `conforming` months otherwise (INV-OD-13). The late penalty owed on the day of issue is added
  to the total: what is paid back, or, if negative, what the tenant still owes. It is issued as
  `RD` (class `restitution`, financial and lease-bound; INV-OD-11 makes it visible to the
  accountant).
- **Release.** `release: true` needs `accounts_approved_on`. It pays back the holdback, less
  the regularisation backed by its RG or `supporting` document. It is due
  `holdback_balance_months` after the approval, and it issues a second `RD` that cites the
  first.
- **Freeze.** Once the first statement exists, the deductions and the annexed grid no longer
  change. Triggers in migration 000016 also refuse any change to a statement row. One statement
  of each kind per lease.
- **Not built yet.** An uploaded exit (phase 1 path) has no `keys_returned_on`, so it cannot be
  settled in Tenure. The commissaire-de-justice half-cost (INV-OD-09) and posting a deduction as
  a charge (`charge_id`) are left for later.
- **Roles.** `GET /leases/{id}/deposit` is read by the co-owner; the accountant gets 403 and
  reads the RD documents.

**Q-116 Obligations calendar, computed base — Open (#115).** RENT-SPEC-012 lists the sources,
items, reminders and digest of the calendar. RENT-INV-001 slice 10 puts the completion windows
and deposit deadlines in it. Provisional, for a first version that only reads what Tenure
already holds:
- **Computed, not stored.** `calendar.Build(facts, from, to, today, params)` is a pure function
  with table tests and golden files. It derives each item when it is read. There is no table,
  no item entered by hand and no item marked done: an item goes away when its source changes
  (a new diagnostic, an issued RD, a revised lease).
- **Items.**
  - `diagnostic_expiry`: the latest diagnostic of each kind per asset. Its end is
    `lease.Expiry`, the same rule as the contract blockers: the earlier of the date the
    report states and the legal validity in `legal_params`.
  - `irl_revision`: the next anniversary as billing reckons it (`billing.DueRevision`), for
    an active lease.
  - `notice_end`: the end of a notice.
  - `term_end`: the end of the current term, renewed tacitly, except a mobility lease.
  - `insurance_certificate`: the lease's anniversary, a yearly reminder to ask the tenant for
    the home insurance certificate (loi 89-462 art. 7 g).
  - `completion_window`: the ten-day and heating windows of an issued entry captured in
    Tenure, while the lease runs.
  - `deposit_deadline`: the restitution deadline from the issued exit's keys date, until the
    first RD is issued.
- **Range and status.** From `from` to `to`, which default to today and today + 90 days, with
  three years at most. Past items that still call for action are always listed as `overdue`:
  an expired diagnostic, a passed deposit deadline. The other past dates are dropped.
- **Who reads.** The owner and the co-owner. The accountant gets 403, since the calendar
  carries inventory and deposit dates.
- **Not built yet** (RENT-SPEC-012, later):
  - items entered by hand: boiler, copropriété AG, taxe foncière, loans;
  - a reminder rule per item;
  - the weekly e-mail digest, which needs an owner e-mail address in Tenure (Q-114);
  - the regularisation due date;
  - the per-asset season for the heating window (INV-OD-07).

