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

**Q-23 Diagnostics — Open (slice 5).** Generation is blocked by "a diagnostic past its validity,
no DPE", but the data model only has `assets.dpe_class`. Provisional: an `asset_diagnostics`
table (kind, done_on, valid_until, document_id), with validity periods in `legal_params`.

**Q-24 Delivery status — Open (slice 3).** "Delivery status stored per document" has no table.
Provisional: a `document_deliveries` table (document_id, recipient, channel, status, at), under
RLS.

**Q-25 Logging library — Open (slice 2).** RENT-BE-001 §8 asks for slog; backendkit takes a
`*logrus.Entry`. Provisional: slog JSON for the service; the logrus entry passed to backendkit
uses a JSON formatter with the same field names.

**Q-26 Box-only lease deposit — Open (slice 5).** A box let on its own is not governed by the loi
de 1989, so `deposit_max_months` does not apply to it. Provisional: there is no legal cap. A
warning is shown above 2 months of rent.

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
3. `rent_control.relocation_rule`: for a new lease in a controlled zone, the rent may not exceed
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

**Q-38 MinIO image — Open (slice 3).** OD-04 puts MinIO in docker-compose, but `minio/minio`
cannot be pulled from Docker Hub here, and quay.io has no image either. Provisional: the
`minio` service is behind the compose profile `archive` until slice 3 settles the image source.
PostgreSQL 17 and gotenberg 8.21 start with a plain `up`.

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
