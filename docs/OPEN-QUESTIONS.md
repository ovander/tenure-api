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
000001. Spec: RENT-SPEC-003, RENT-BE-001 §2 and §7. The shape of the zone rules is Q-28.

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
`recoverable × lease days ÷ exercise days`, rounded half-up. Days with no lease stay with the
owner as non-recovered. When there is no vacancy, the rounding remainder goes to the
largest-remainder lease, so the shares add up to the total. A box on a dwelling lease shares that
lease.

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

**Q-20 "Two real assets" — Open (slice 1).** The roadmap asks for golden tests on two real
assets, but no real data is in the repository (and none may be committed). Provisional: synthetic
golden cases shaped like the real assets. Cases with real figures, anonymised, are added at the
phase 1 gate.

## Platform

**Q-21 Migration tool — Open (slice 2).** The spec fixes the file naming (`000001_init.sql`) and
reversibility, but not the tool. Provisional: pressly/goose v3 with `-- +goose Up` and
`-- +goose Down` sections in one file, embedded in the binary. A migration that cannot be
reversed says so in its Down section and fails it on purpose.

**Q-22 Activation blockers before slice 5 — Open (slice 2).** Activation needs the signed contract
and the entry inventory (RENT-SPEC-008), which only exist from slice 5. Provisional: slice 2
builds the state machine with a blocker interface and implements the blockers that need no
documents. Slice 5 adds the document blockers. No release reaches real data before the phase 1
gate.

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

**Q-27 What counts as a property for line 222 — Open (slice 1).** `forfait_222_cents` is due
"per property" (par local). Provisional, so as never to overstate a deduction: one flat amount
per unfurnished asset that earned rent in the year. A box let with a dwelling counts with that
dwelling, not separately; a box let on its own counts as one property.

**Q-28 Shape of `rent_control.zone_rules` — Open (slice 1).** The decision does not say what a
zone rule contains. Provisional: a dated maximum variation of the rent at revision, in basis
points, applied after the national `irl_cap` (the stricter wins). A per-m² reference-rent
ceiling (loyer de référence majoré) would need per-asset zone data and is not modelled.

**Q-29 Copropriété provisions on line 229 — Open (slice 1).** On the form, line 229 takes the
provisions paid in N, and line 230 takes back the non-deductible part the next year. Under
Q-08, recoverable expenses are not deducted. Provisional (the conservative reading): line 229
takes only the non-recoverable share of the provisions paid (`recoverable_pct` from the syndic's
call). Line 230 takes the N−1 regularisation amount, entered as a charge of category
`copro_regularisation_prior`, and is subtracted from the expenses.

**Q-30 `tax.Lines` inputs — Open (slice 1).** The signature `Lines(payments, charges, shares,
year, params)` does not carry the prior-year deficits, nor the furnished flag and the
dwelling/box kind that line 222 needs. Provisional: `shares` is a list of assets, each with its
owners' shares, `Furnished` and `Kind`, and a final argument takes the prior deficits per owner.

**Q-31 Which year a millésime names — Open (slice 1).** The re-exported RENT-SPEC-004 table header
reads "millésime 2025". By the DGFiP convention, the form for income year N carries millésime
N+1 (the 2044 filed in 2026 for 2025 income is millésime 2026). Provisional: millésime = income
year + 1, so `tax.Lines` for income year 2025 needs the 2026 map. The fixtures hold the map
under millésime 2026 for income year 2025. If the header means "income year 2025", only its
wording changes.
