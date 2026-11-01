# Open questions

Spec gaps and ambiguities found while implementing phase 1. Each entry gives the provisional
reading in force until the owner answers. That reading is the most conservative one: fail closed,
and never overstate a tenant's debt or an owner's deduction. When an answer arrives, update the
spec in the same PR and mark the entry **Answered**, with the date.

Status: **Open** (provisional reading applies), **Needs answer** (blocks the slice named),
**Answered**.

## Identity and security

**Q-01 Socrate dev values — Open (slice 2).** The issuer URL, API audience, role claim and step-up
value were not supplied. Provisional: `TENURE_SOCRATE_ISSUER`, `TENURE_SOCRATE_JWKS_URL`,
`TENURE_SOCRATE_AUDIENCE` and `TENURE_SOCRATE_ADMIN_ROLE` are required and have no default, so
`tenure serve` refuses to start without them. `.env.example` holds obvious placeholders. The
admin role is read from backendkit's `role` claim (`ctxutil.GetUserRole`).

**Q-02 Step-up freshness — Open (slice 2).** RENT-BE-001 §8b asks for "an `acr` fresh within 5
minutes". backendkit v1.27.0 does not expose `acr`; it exposes `auth_time` and `amr`
(`ctxutil.GetAuthTime`, `ctxutil.GetAMR`), and `acr` carries no time anyway. Provisional: a
`step_up` route requires `auth_time` to be set and at most 5 minutes old, and `amr` to contain
`mfa`. A missing `auth_time` (0) means "unknown" and is refused. The 5-minute window is a
security setting, not a legal constant: `TENURE_STEP_UP_MAX_AGE`, default 5m, never above it. Asking backendkit to expose `acr` would be a separate
backendkit PR.

**Q-03 Database role names — Open (slice 2).** RENT-BE-001 §8b names `rent_api`, `rent_migrate`
and `rent_jobs`. These names predate the rename to Tenure (BE-001 v0.3), and the slice 2 brief
uses `tenure_api`, `tenure_migrate` and `tenure_jobs`. Provisional: use `tenure_*`, and amend §8b
in the slice 2 PR.

**Q-04 Subject format — Open (slice 2).** backendkit documents Socrate's `sub` as a numeric
string, not a UUID. Provisional: `owners.socrate_sub` is `text`, unique, and is read with
`ctxutil.GetUserSub`, never `GetUserID`.

**Q-05 Grants in RLS — Open (slice 2).** RENT-BE-001 §2 says grants are "joined in the policy",
but `owner_grants` is missing from the migration 000001 table list. Provisional: create
`owner_grants` in 000001. The policy is `owner_id = app.owner_id AND tenure_scope_allowed(owner_id)`,
where the function checks either `owners.socrate_sub = app.actor_sub` or a grant row. Write
policies also require the owner role, so co-owners and accountants are read-only in the
database, not only in the API. The grant endpoints come in slice 8, and BE-001 §3 lists none.
They will be `GET/POST /grants` and `DELETE /grants/{id}`, all tagged `step_up`.

**Q-06 `PUT /me` — Open (slice 2).** RENT-FE-001 §7 persists the language with `PUT /me`, but
BE-001 §3 lists only `GET /me`. Provisional: add `PUT /me` (language only) in slice 2, and amend
§3.

## Calculation engine (slice 1)

**Q-07 2044 line numbers — Needs answer (slice 1 fixtures).** RENT-SPEC-004 maps repairs to line
221 and management fees and insurance to 222/223. On form 2044 as we read it, 221 is "frais
d'administration et de gestion", 222 is "autres frais de gestion" (a flat amount per property),
223 is insurance premiums, 224 is repairs, maintenance and improvement, and 213 is subsidies and
insurance indemnities. The map is a parameter (`form_2044_line_map`), so only fixture data
depends on the answer. Provisional: the fixture follows the spec table as written, and this
entry stays open until checked against the current year's 2044 notice.

**Q-08 Recoverable charges on line 211 — Needs answer (slice 1).** RENT-SPEC-004 puts "rents and
recoverable charges received" on line 211. Normally, recovered charges are left out of income
when the matching expenses are not deducted (line 225 handles the exception). Provisional: follow
the spec. Recoverable provisions received go to 211, and recoverable charges paid are not
deducted twice. Each line carries the ids of its sources, so the figure can be reviewed.

**Q-09 Missing legal parameters — Open (slice 1).** The tax and charge rules need constants that
RENT-BE-001 §7 does not list: the micro-foncier allowance (30 %), the 222 flat amount per
property, the cap on deficit imputation against global income and the deficit carry-forward period. Provisional: add them as `legal_params` keys with a `source_url`
(`micro_foncier_allowance_pct`, `form_2044_flat_fee_per_property_cents`,
`deficit_global_income_cap_cents`, `deficit_carry_forward_years`),
and amend §7.

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

**Q-12 IRL cap source — Open (slice 1).** RENT-SPEC-003 caps the revision by "any applicable
rent-control rule stored on the asset". RENT-BE-001 §7 uses `irl_cap.applies` and
`irl_cap.rate` from params. Provisional: follow BE-001. The cap is a maximum variation from
params, effective as of the revision date. An asset-level rent-control ceiling (encadrement des
loyers) is out of phase 1 unless confirmed.

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
