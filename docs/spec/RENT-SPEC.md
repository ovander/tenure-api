# Tenure — Rental Management Platform, High-Level Specifications (RENT-SPEC)

Nov 1, 2026 · @Olivier

## RENT-SPEC-001 — Purpose, scope and corpus

Tenure (product name decided on 2026-11-01) manages a small portfolio of rented real-estate assets (house, apartment, parking box) for one or more owners: it issues the monthly rent call and receipt per asset, produces each owner’s annual tax summary (French revenus fonciers), and keeps every generated document for 10 years.

**In scope (v1)**

- Assets of three kinds: house, apartment, box (parking/storage). Mixed leases (apartment + box) are one lease over several assets.
- Residential leases under French law (bail d’habitation, loi du 6 juillet 1989), furnished or unfurnished; commercial leases are out of scope.
- Lease contract generation from the model contract, with annexes, amendments and guarantees (deposit, surety, Visale, rent insurance).
- Monthly billing: appel de loyer, quittance de loyer, provisions sur charges, annual charge regularisation, IRL-based rent revision.
- Annual owner tax summary: income and deductible expenses per asset and per owner, in the structure of form 2044 (régime réel) and the micro-foncier threshold check.
- Document archive: generated PDFs, uploaded supporting documents (leases, invoices, inventories), immutable, retained 10 years.

**Out of scope (v1)**

- Payment collection (SEPA mandates, PSP integration); payments are recorded manually or imported from a bank CSV.
- Accounting ledger export (FEC) and SCI corporate accounts.
- Tenant self-service portal (planned v2, see Open decisions).

**Corpus map**

| Document | Covers | Phase |
| --- | --- | --- |
| RENT-SPEC-001 | Vision, scope, corpus | 1 |
| RENT-SPEC-002 | Domain model | 1 |
| RENT-SPEC-008 | Lease contract and guarantees | 1 |
| RENT-SPEC-003 | Monthly fee generation | 1 |
| RENT-SPEC-007 | Charge reconciliation (syndic annual accounts) | 1 |
| RENT-SPEC-004 | Annual tax summary | 1 |
| RENT-SPEC-005 | Document retention and archive | 1 |
| RENT-SPEC-006 | Architecture and stack | 1 |
| RENT-SPEC-009 | Tenant onboarding and inventories | 2 |
| RENT-SPEC-010 | Lease end, renewal and vacancy | 2 |
| RENT-SPEC-011 | Arrears recovery | 2 |
| RENT-SPEC-012 | Obligations calendar | 2 |
| RENT-SPEC-013 | Bank reconciliation | 2 |
| RENT-SPEC-014 | Loans and owner ledger | 3 |
| RENT-SPEC-015 | Maintenance and incidents | 3 |
| RENT-SPEC-016 | Reporting and dashboard | 3 |
| RENT-SPEC-017 | Tenant portal | 4 |
| RENT-SPEC-018 | Integrations | 4 |
| RENT-SPEC-019 | SCI, LMNP and multi-entity | 4 |
| RENT-SPEC-020 | Assisted document capture | 4 |
| RENT-SPEC-021 | Engineering governance | all |
| RENT-SPEC-022 | Security and audit trail | 1, day 1 |

Each section is the high-level spec of its document; detailed specs (API, schema, UI) are derived from them and cross-reference these ids.

Detailed specs derived from this corpus: Backend spec (RENT-BE-001) and Frontend UX spec (RENT-FE-001).

## RENT-SPEC-002 — Domain model

Seven entities carry the whole system; everything billed or declared is derived from Lease, Charge and Payment, never typed twice.

| Entity | Key attributes | Rules |
| --- | --- | --- |
| Owner | legal name, address, tax id (SPI), ownership share per asset (%) | One owner account = one Socrate identity; co-ownership splits income and expenses pro rata |
| Asset | kind (house / apartment / box), address, lot number, surface (m²), furnished (y/n), DPE class, acquisition date and price | Immutable kind; a box may be attached to a dwelling asset for a combined lease |
| Tenant | name(s), contact, guarantor | Several tenants on one lease (solidarité) |
| Lease | assets\[\], tenants\[\], kind, start date, term, base rent (€), charges provision (€), deposit (€), IRL reference quarter and index, revision month, due day, special clauses | Exactly one active lease per asset at a time; end = notice, expiry or termination event |
| Guarantee | lease, kind (deposit, surety, Visale, insurance), guarantor, amount (€), term, status, document | Combination rules per RENT-SPEC-008 |
| Charge | asset, date, category (copropriété, taxe foncière, insurance, repairs, management fees, loan interest, recoverable utilities), amount (€), recoverable to tenant (y/n), supporting document | Category drives both regularisation and the 2044 line |
| Billing document | lease, period (YYYY-MM), kind (rent call, receipt, regularisation statement, revision notice), lines\[\], total (€), PDF | Generated, numbered, immutable once issued (see RENT-SPEC-005) |
| Payment | lease, date, amount (€), method, matched billing documents | Partial and late payments allowed; drives receipt vs rent call |

Supporting entities: Inventory (état des lieux entry/exit, attached to a lease), Contract document (lease, amendments, annexes, see RENT-SPEC-008), and Document (any archived file, see RENT-SPEC-005).

**Lease lifecycle**: draft → active → notice given → ended → closed (deposit settled). Billing runs only in active and notice-given states.

&#91;embedded content: domain model · 8 entities\]

The lease issues the monthly documents; charges and payments feed both the regularisation and the owner’s tax summary.

## RENT-SPEC-008 — Lease contract and guarantees

The platform generates the signed-ready lease contract from the Lease record, with its mandatory annexes and the guarantee documents (deposit, surety, Visale or insurance), so that every clause and amount matches what the billing engine will apply.

**Contract generation**

- Template: the French model contract (décret 2015-587), one variant per lease kind — unfurnished, furnished, mobility lease, box alone; the template version is stored with the document.
- Fields come only from Lease, Asset, Owner, Tenant and Guarantee: parties, designation of the premises and annexes (box, cellar), surface, term and start date, rent, provisional charges, revision clause with IRL reference, deposit, payment terms, special clauses picked from a vetted list.
- Annexes assembled into the same PDF or as separate archived files: information notice, diagnostics file (DPE, lead, asbestos, electricity and gas, natural-risk statement), copropriété rules extract, inventory template, list of recoverable charges and repairs at tenant’s charge.
- Output: `CT-<asset>-<YYYY>-<seq>` PDF, archived as a lease document (RENT-SPEC-005); the Lease moves from draft to active only once the signed copy and the entry inventory are uploaded.
- Missing mandatory data (a diagnostic past its validity, no DPE) blocks generation and is listed on the lease page.
- Amendments (avenant) use the same engine: new tenant, rent change outside IRL, added box; each amendment is a numbered document linked to the lease.

**Guarantees** — a Guarantee entity attached to the lease, one or several, each with its own document:

| Kind | Rule | Generated document |
| --- | --- | --- |
| Deposit (dépôt de garantie) | ≤ 1 month rent unfurnished, ≤ 2 months furnished, none for a mobility lease; held, then returned within 1 month (2 if deductions) after exit inventory | Deposit receipt at entry; settlement statement at exit, deductions listed with supporting charges |
| Surety (caution simple or solidaire) | Natural or legal person; typed statutory mention, amount and term; cannot be combined with an insurance guarantee except for a student or apprentice tenant | Acte de cautionnement, one per guarantor, annexed to the contract with a copy of the lease |
| Visale | Action Logement visa number and validity; replaces a surety | Visa reference recorded, certificate archived |
| Rent insurance (GLI) | Owner’s policy number, insurer, covered amount; blocks adding a surety | Policy and tenant eligibility file archived |

**Rules**

- The deposit amount on the contract is computed from the rent on the lease; a change of rent never changes a deposit already held.
- A surety’s term follows the lease and its renewals unless a fixed term is entered; an expired or revoked surety raises a warning on the lease.
- Deposit settlement at exit uses the exit inventory, the arrears list (RENT-SPEC-003) and any pending regularisation (RENT-SPEC-007); the statement is immutable and archived.
- Signature: v1 produces a PDF for wet or external e-signature and stores the signed upload; native e-signature is an open decision.

**Owner view**: per lease — contract and amendments, guarantees with status (held, valid, expired, settled), missing items blocking activation.

## RENT-SPEC-003 — Monthly fee generation

On the 1st of each month a scheduled job issues one rent call per active lease; the monthly fee is always monthly rent + provisional charges, and a receipt is issued automatically once the month is paid in full.

**Monthly run (job `billing.monthly`, 1st at 06:00 Europe/Paris)**

1. Select leases in state active or notice-given whose period overlaps the month.
2. Compute the monthly fee = monthly rent + provisional charges, both as fixed on the lease, prorated by days for the first and last month; a regularisation balance (RENT-SPEC-007) is added as a separate line when one is due.
3. Apply any rent revision due this month (IRL, see below) before computing.
4. Number the document `AL-<asset>-<YYYY>-<seq>`, render the PDF (template per owner), archive it, notify the tenant by e-mail with the PDF attached.
5. Mark the run in a billing log; a re-run of the same period is idempotent (no duplicate documents).

**Receipt (quittance)**: issued when matched payments cover the month’s rent call in full; a partial payment produces a reçu partiel, not a quittance. Receipts are numbered `QL-<asset>-<YYYY>-<seq>` and cite the rent and charges amounts separately, as required by law.

**Rent revision (IRL)**: at the lease’s revision month, new rent = base rent × (current IRL / reference IRL), rounded to the cent, with the IRL variation capped by the national `irl_cap` when one is in force (the 2022–2024 « bouclier » style); for an asset in a controlled zone the revised rent may also not exceed the zone ceiling (loyer de référence majoré × surface, `rent_control.zone_ceiling`). At a new lease on a controlled-zone asset the rent may not exceed the previous lease's rent revised by IRL (`rent_control.relocation_rule`) unless an exemption (works, under-valued rent) is recorded on the lease; the zone ceiling is also checked at lease creation and renewal. All three rules are dated values in legal\_params. The job fetches the quarterly INSEE IRL index, issues a revision notice, and updates the lease’s reference index. A revision older than 12 months is not applied retroactively.

**Annual charge regularisation**: handled by RENT-SPEC-007; the resulting balance is billed or credited on the next monthly fee.

**Late and unpaid rent**: on due day + 5 a reminder is sent; the owner sees an arrears list per lease; no automatic penalties in v1.

**Owner view**: per asset and per month — called, received, outstanding; per year — the same, feeding RENT-SPEC-004.

## RENT-SPEC-007 — Charge reconciliation

Once a year, when the syndic’s annual accounts arrive, the platform reconciles the actual recoverable charges of each asset against the provisional charges collected from its tenants, bills or credits the difference, and resets next year’s provision.

**Inputs**: the syndic’s annual statement (décompte de charges) per lot, the taxe foncière notice (only the TEOM part is recoverable), direct contracts paid by the owner (water, boiler maintenance, common-area utilities), and the provisional charges collected per lease over the same exercise.

**Process (job `charges.reconcile`, started by the owner per asset and exercise)**

1. Import the syndic statement: one Charge per line, with the syndic’s own recoverable / non-recoverable split (décret 87-713); the owner can override the split per line and attaches the statement PDF.
2. Add the other recoverable charges of the exercise (TEOM, direct contracts) from the charges ledger.
3. Allocate recoverable charges to each lease that covered the asset during the exercise, pro rata temporis by days; a box attached to a dwelling lease shares that lease.
4. Sum provisional charges actually collected per lease over the same period (from matched payments, not from rent calls).
5. Compute the balance per lease: recoverable actual − provisions collected. Positive = tenant owes, negative = credit.
6. Issue the regularisation statement `RG-<asset>-<YYYY>-<seq>` with every line, the allocation key and the balance; archive it (RENT-SPEC-005).
7. Post the balance as a separate line on the next monthly fee; a credit larger than one month’s fee is carried over the following months.
8. Propose the new provisional charges = recoverable actual ÷ 12, rounded up to the euro; the owner validates, the lease is updated for the next month and the tenant is notified.

**States**: draft → allocated → issued → settled (balance fully paid or credited). An issued statement is never edited; a correction is a new statement that cancels the previous one.

**Rules**

- Supporting documents stay available to the tenant for 6 months after the statement is issued (loi 89-462 art. 23).
- A statement issued more than 12 months after the exercise closed must offer the tenant payment of the balance over 12 monthly instalments; the fee generator applies that spread automatically.
- Non-recoverable copropriété charges from the same statement feed the owner’s tax summary (RENT-SPEC-004, lines 229/230); the syndic’s provisional calls paid during the year are replaced by the statement’s actual figures.
- A tenant who left before the statement is issued still gets one: the balance is settled against the deposit if it is still held, otherwise billed or refunded directly.

**Owner view**: per asset and exercise — statement lines, recoverable total, provisions collected per lease, balances, new provision proposed; per lease — history of regularisations.

## RENT-SPEC-004 — Annual tax summary

In January the platform produces, per owner, a tax summary of the previous calendar year that maps directly onto the French revenus fonciers declaration (form 2044 lines for the régime réel, plus the micro-foncier check).

**Inputs**: payments received in the year (cash basis, as the tax rule requires, not rent called), deductible charges by category, owner share per asset, lease furnished flag.

**Regime routing**

- Unfurnished assets → revenus fonciers: micro-foncier if gross rents ≤ 15 000 € and no exclusion, else régime réel (2044). The summary shows both so the owner can choose, and flags the 3-year lock-in of the réel option.
- Furnished assets → BIC (LMNP): out of scope for v1 except a gross-receipts figure per asset; see Open decisions.

**Summary content (per owner, with per-asset detail)**

| Block | 2044 line (income year 2025, form millésime 2026; map kept in legal\_params by income year) | Source |
| --- | --- | --- |
| Income | 211 Loyers bruts — rent only, excluding provisional charges received | Payments matched to rent lines of rent calls |
| Income | 212 Dépenses mises par convention à la charge du locataire; 213 recettes diverses (indemnités, subventions) | Charge entries flagged as such |
| Expenses | 221 Frais d’administration et de gestion | Charges, category management |
| Expenses | 222 Autres frais de gestion — flat amount from legal\_params per local: one per lease of unfurnished assets that produced rent in the income year (a box let with the dwelling is one local with it, a box on its own lease is another); furnished assets never count | Computed |
| Expenses | 223 Primes d’assurance | Charges, category insurance |
| Expenses | 224 Réparations, entretien, amélioration | Charges, category repairs, non-recoverable |
| Expenses | 225 Charges récupérables non récupérées au départ du locataire | Reconciliation balance left unpaid at lease close |
| Expenses | 227 Taxe foncière excluding TEOM | Charge, category property tax |
| Expenses | 229 Provisions pour charges de copropriété N — non-recoverable share of the syndic provisions paid in N only; 230 régularisation of the N-1 non-recoverable share, signed | Syndic provisions paid, then prior-year statement |
| Expenses | 250 Intérêts d’emprunt and loan insurance | Charges, category loan interest |
| Result | Net per asset, net per owner share; deficit against global income capped per legal\_params, remainder carried forward per legal\_params | Computed |

**Outputs**: PDF summary `RF-<owner>-<YYYY>`, CSV of the underlying lines, and the list of supporting documents (invoices, syndic statements) archived for audit. All three go to the archive (RENT-SPEC-005).

**Rules**: a charge dated in year N but paid in N+1 is counted in N+1; a charge with no supporting document is flagged, not excluded; provisional charges received from tenants are never income and recoverable expenses are never deducted (only line 225 at lease close); the 2044 line map and every tax parameter are keyed by income year, the form millésime (income year + 1) being a label only, and the map is verified against the current year’s notice before tax.annual runs; a lease whose assets have different ownership shares is refused at save (`lease/consistent-ownership-shares`), so its rent goes to the lease's dwelling without a split; every figure is traceable to its payment or charge ids.

## RENT-SPEC-005 — Document retention and archive

Every generated or uploaded document is stored write-once, with its SHA-256 hash and metadata, and retained for 10 years from its issue date; nothing is deleted before that, and nothing issued is ever edited.

**What is archived**

| Document class | Produced by | Retention clock starts |
| --- | --- | --- |
| Rent calls, receipts, regularisation statements, revision notices | RENT-SPEC-003 | Issue date |
| Annual tax summaries, CSV lines | RENT-SPEC-004 | Issue date |
| Leases, amendments, inventories, deposit settlements | Owner upload | Lease end date |
| Charge supporting documents (invoices, syndic statements, tax notices) | Owner upload | Tax year of the charge |
| Payment imports (bank CSV) | Owner upload | Import date |
| Audit events and daily chain heads | RENT-SPEC-022 | Event date; kept as long as the documents they concern, 10 years minimum |

**Storage model**: object storage bucket per owner (S3-compatible, versioning on, object lock in compliance mode for 10 years); PostgreSQL holds the metadata row (id, class, asset, lease, period, hash, size, issued\_at, retain\_until) under row-level security by owner.

**Corrections**: a wrong document is never modified; a cancelling document (avoir / corrective statement) is issued and both are kept and linked.

**Access and export**: owner can browse by asset, year and class; one-click export of a year (ZIP with an index CSV) and of an asset’s whole history, for a tax audit or a sale. Tenants receive their own documents by e-mail; v2 portal may expose them.

**End of retention**: after retain\_until, documents become eligible for deletion; deletion is a manual, logged owner action, never automatic, since the 10 years is a floor.

**Legal basis (France)**: documents relating to a lease and rents are kept 10 years as a safe cover of the 3-year rent claim period, the 5-year civil limitation and the 6-year tax recovery window, plus the 10-year retention of accounting records for owners under BIC.

## RENT-SPEC-006 — Architecture and stack

The platform is a single Go/Chi service with a Vue 3 front-end, PostgreSQL with row-level security per owner, Socrate for identity and an S3-compatible object store for the archive; it follows the handler → service → repository layering used on the other platforms.

| Layer | Choice | Notes |
| --- | --- | --- |
| Identity | Socrate (OAuth2/OIDC) via backendkit | Roles: owner, co-owner (read), accountant (read + tax export), admin |
| Backend | Go toolchain 1.27.x, Chi, gorm on pgx, swag-generated OpenAPI | Modules: assets, leases, billing, charges, tax, archive, notifications |
| Scheduler | In-process cron (robfig/cron) with a job log table | Jobs: billing.monthly, billing.reminders, irl.fetch (quarterly), tax.annual (January), archive.retention-check |
| Database | PostgreSQL 17, RLS keyed on owner\_id, six-digit migrations | Money as integer cents, periods as YYYY-MM, all timestamps UTC |
| PDF generation | Server-side from HTML templates (chromedp or gotenberg) | One template set per owner, versioned; the template version is stored with each document |
| Archive | S3-compatible bucket per owner, versioning + object lock | Metadata in PostgreSQL (RENT-SPEC-005) |
| Front-end | Vue 3, TypeScript 6, Vite 8, PrimeVue 4.4, Pinia 2.3, Tailwind, PWA | Views: portfolio, asset, lease, billing calendar, charges ledger, tax year, archive |
| Notifications | Transactional e-mail (SMTP or provider API) with the PDF attached | Delivery status logged per document |
| Hosting | Caddy on a Linux VPS, release built with -ldflags version stamping | Two repositories, ovander/tenure-api and ovander/tenure-web; GitHub issue → PR → CI → AI security audit → versioned release in each |
| Security and audit | Hash-chained audit\_events, ClamAV sidecar for uploads, encrypted volumes and bucket, provider KMS, MFA and step-up via Socrate | RENT-SPEC-022; all day-1 |

**External data**: INSEE IRL quarterly index (fetched, cached, with a manual override); no other external dependency.

**Mandatory platform constraint — identity**: Socrate is the only identity provider of Tenure and backendkit (`github.com/ovander/backendkit`, pinned version) is the only way the backend validates tokens and resolves roles. Consequences: no local user table, password, API key or magic link in Tenure; every human (owner, co-owner, accountant, admin, tenant from phase 4) and every machine caller (jobs triggered remotely, future integrations) authenticates through Socrate (authorization code + PKCE for the SPA and portal, client credentials for machines); MFA and step-up policies live in Socrate, not in Tenure; a feature that cannot be built on Socrate is an open decision, never a local workaround. The same rule applies to every other platform of the family, so a user has one identity across them.

&#91;embedded content: architecture · 1 service, 5 dependencies\]

All business logic and the scheduler live in the one Go service; state is split between PostgreSQL (metadata, RLS) and the object store (immutable documents).

## Roadmap — from basic to best in class

Four phases, each shippable on its own and gated by a real-data check before the next starts; the governance spec (RENT-SPEC-021) runs under all of them.

&#91;embedded content: roadmap · 4 phases, 3 gates\]

A gate is passed on the owner’s real portfolio, not on test data; phase 4 has no gate after it.

| Phase | Version | Delivers | Gate to the next phase |
| --- | --- | --- | --- |
| 1 Foundation | 1.0 | RENT-SPEC-001 to 008 and 022: assets, leases and contracts, monthly fee and receipts, charge reconciliation, tax summary, 10-year archive, audit trail and security baseline | One real monthly run for every asset, one real tax summary accepted by the owner, hash chain verified, ASVS L2 checklist signed off |
| 2 Operations | 1.x | RENT-SPEC-009 to 013: tenant onboarding and inventories, lease end and vacancy, arrears recovery, obligations calendar, bank reconciliation | One lease lifecycle run end to end in the tool, from candidate to deposit settlement |
| 3 Finance and maintenance | 2.0 | RENT-SPEC-014 to 016: loans and owner ledger, maintenance and incidents, reporting | Year-end export accepted by an accountant without manual rework |
| 4 Best in class | 3.0 | RENT-SPEC-017 to 020: tenant portal, integrations, SCI and LMNP regimes, assisted document capture | Tenants use the portal for documents and requests; owner no longer types a syndic statement |

**Phase 1 slice order** (decided 2026-11-01): the calculation engine comes first, screens last.

1. Calculation engine as pure Go functions with golden tests on two real assets: monthly fee and proration, IRL revision, charge allocation, 2044 lines.
2. Portfolio and lease without contract: assets, tenants, lease draft and activation, Socrate login, RLS, audit events.
3. Monthly fee dry run, then issue: AL documents, gotenberg rendering, archive with hash, e-mail delivery.
4. Payments and receipts: manual entry, matching, QL documents, arrears list.
5. Contract generation and guarantees: CT, DG and surety documents, activation blockers.
6. Charges ledger and reconciliation wizard: syndic import, allocation, RG statement, new provision.
7. Tax year: both regimes, RF summary and CSV, missing-document flags.
8. Archive browser, exports, settings and grants; phase 1 gate run on the real portfolio.

Dev environment: Socrate and gotenberg as the only external services; PostgreSQL 17 and MinIO in docker-compose; Scaleway bucket from the first deployed release.

## RENT-SPEC-009 — Tenant onboarding and inventories (phase 2)

A candidate becomes a tenant through a structured file, and every lease carries a structured entry and exit inventory whose comparison drives the deposit settlement.

- Candidate file: identity, income proof, guarantor file, checked against the legal list of allowed documents (décret 2015-1437); scoring is the owner’s, the tool only checks completeness and rent-to-income ratio.
- Rejected candidates: file deleted after 3 months, only a dated « rejected » trace kept (RGPD).
- Inventory: template per asset (rooms, items, condition scale, meter readings, keys), photos attached per item, signed by both parties; exit inventory pre-filled from the entry one, differences flagged.
- Deposit settlement (RENT-SPEC-008) is generated from the flagged differences plus quotes or invoices, with the legal 1 or 2 month deadline tracked.

## RENT-SPEC-010 — Lease end, renewal and vacancy (phase 2)

Notice, renewal and vacancy are explicit lease events with their legal deadlines and generated letters.

- Tenant notice: receipt date, 1 or 3 month préavis computed from lease kind and zone, end date, last prorated fee (RENT-SPEC-003).
- Owner notice (congé pour vente, reprise, motif légitime): 6 month lead time (3 furnished), mandatory wording and attachments generated, registered-letter tracking.
- Tacit renewal and renewal with rent re-evaluation (under-valued rent procedure) as dated events on the lease.
- Vacancy: period between leases, with its charges flagged non-recoverable and the asset’s vacancy days feeding reporting (RENT-SPEC-016).
- Built so far (tenure-api #127, Q-118; #130, Q-119; #133, Q-120): the tenant’s notice computed from its reception, and the owner’s notice with its CG letter and lead time; the lease’s terms with tacit renewal and an asset’s vacancy, computed; the renewal of an unfurnished lease with a re-evaluated rent (art. 17-2): the PR proposal letter with its references and zone tendue cap, its reception, the tenant’s answer and the yearly steps applied to the rent. A lease under rent control and the conciliation or court procedure come later.

## RENT-SPEC-011 — Arrears recovery (phase 2)

Unpaid rent follows a configurable escalation path with generated letters and deadlines, up to the insurance claim or the bailiff.

- Steps: reminder (due day + 5), formal notice (mise en demeure, registered letter), commandement de payer via bailiff (clause résolutoire, 6 week delay), GLI claim with the insurer’s deadline, court file.
- Each step is a dated event with its letter PDF archived (RENT-SPEC-005) and a next-action date on the obligations calendar.
- Payment plans: agreed instalments recorded and matched; the plan suspends escalation while respected.
- Surety activation: the guarantor is notified from the second unpaid month, as the surety clause requires.
- Built so far (tenure-api #140, Q-121): the escalation computed from the oldest overdue due day, with the next steps and their days (formal notice, surety informed, insurer’s claim, commandement de payer and its notice to the surety, court once the clause résolutoire’s delay has run); the MD and IC letters; each step recorded as a dated event with its letter or deed; payment plans (#142, Q-122) covering the overdue calls, respected while the instalments and the current rent are paid, which suspend the escalation. The screen comes next.

## RENT-SPEC-012 — Obligations calendar (phase 2)

One calendar lists every dated obligation of owner, tenant and asset, raises reminders and shows what is overdue.

- Sources: tenant insurance certificate (yearly), diagnostics validity (DPE 10 y, electricity and gas 6 y, lead, ERP 6 months), boiler maintenance, IRL revision date, regularisation due, notice deadlines, copropriété AG, taxe foncière payment, loan instalments.
- Each item: due date, responsible party, reminder rule, link to the document that clears it.
- Rendered as a per-asset timeline and a cross-portfolio « next 90 days » list; e-mail digest weekly.
- Built so far (tenure-api #115, Q-116): the computed base, `GET /calendar`, from diagnostics, leases, inventories and deposits; items entered by hand, reminder rules and the digest come later.

## RENT-SPEC-013 — Bank reconciliation (phase 2)

Imported bank lines are matched to rent calls, regularisations and charges automatically, leaving only exceptions to the owner.

- Import: CSV or OFX per account; later open-banking (RENT-SPEC-018).
- Matching rules: amount and tenant reference, then amount and period, then manual; CAF/APL paid directly to the owner matched as part of the tenant’s fee; third-party payers allowed.
- Outgoing lines matched to charges (syndic calls, taxe foncière, insurance, contractors) so the charges ledger is bank-backed.
- Unmatched lines after 10 days appear on the dashboard; a match can be split across several documents.
- Built so far (tenure-api #145, Q-123): bank accounts (label and last four IBAN characters), the import of CSV (mapped) and OFX statements, deduplicated by the bank’s id or a hash, bank lines with their status, ignored or reopened by hand, flagged once unmatched past the alert delay; credits matched to rent (#147, Q-124): suggestions by the rent call’s number, the tenant’s name, a housing allowance naming the tenant, then the amount near the due day; the sure ones matched automatically; a line split by the owner across leases, a third party allowed; debits matched to charges (#149, Q-125), a whole syndic statement or one charge of the amount, each charge then paid on the line’s day and naming it. The screen comes next.

## RENT-SPEC-014 — Loans and owner ledger (phase 3)

Each asset can carry its loans, so interest is derived for the tax summary, and the owner gets a ledger an accountant can take over.

- Loan: principal, rate, start, amortisation table imported or computed; yearly interest and insurance go to RENT-SPEC-004 line 250 automatically.
- Ledger: every payment, charge and deposit movement as a double-entry line per owner, deposits as liabilities; period lock after the tax summary is issued.
- Exports: year-end pack (ledger CSV, 2044 figures, supporting documents index) and FEC format for SCI or LMNP accounting.
- Built so far (tenure-api #154, Q-126): loans on an asset, their table computed (fixed rate, constant monthly instalments) or typed in from the bank, and the interest and insurance of the instalments due in the income year on line 250, a loan charge typed by hand for the same asset and year set aside and flagged. The owner’s journal (#156, Q-127): every payment, charge paid, loan instalment, deposit received and deposit statement posted in double entry on a cash basis, to a chart of accounts kept in the legal parameters; a year is closed once its tax summary is issued, and nothing it was computed from changes after. The exports (#158, Q-128): the journal as CSV, the FEC of a closed year, and the year-end pack with the journal, the balances, the FEC, the issued 2044 lines and the index of the supporting documents. The screen comes next.

## RENT-SPEC-015 — Maintenance and incidents (phase 3)

Tenant requests and owner-initiated works are tracked from report to invoice, and each invoice lands in the charges ledger with the right tax treatment.

- Incident: reported by tenant (e-mail or portal), asset, urgency, photos, status (new, quoted, scheduled, done, invoiced).
- Works order: contractor, quote, scheduled date, invoice; the invoice creates the Charge with category repair (deductible) or improvement (not deductible, capitalised).
- Contractor directory per owner; recurring contracts (boiler, chimney) linked to the obligations calendar.
- Built so far (tenure-api #164, Q-130): the contractor directory; incidents reported by the tenant or started by the owner, with their channel, urgency and photos, through quote, schedule, done and invoice, or cancelled; a repair's invoice becomes a confirmed repairs charge, deductible and matched to the bank like any charge; an improvement's invoice is capitalised on the asset rather than recorded as a charge (Q-130), posted in the journal to a fixed-asset account and deducted nowhere. Recurring maintenance (tenure-api #166, Q-131): a boiler’s servicing, a chimney’s sweeping or any periodic check, with the legal rhythm and who must have it done from the legal parameters; each visit is recorded with its certificate and sets the next one, which the obligations calendar shows, overdue when missed. The screen comes next.

## RENT-SPEC-016 — Reporting and dashboard (phase 3)

The owner sees the portfolio’s performance at a glance: yield, vacancy, arrears and cash, per asset and consolidated.

- Per asset: gross and net yield, occupancy and vacancy days, arrears aging (30/60/90), charges actual vs provision, next obligations.
- Portfolio: monthly cash position, year-to-date income and expenses vs previous year, tax estimate under both regimes.
- Exports to CSV; figures are the same ones the tax summary uses, never recomputed separately.
- Built so far (tenure-api #161, Q-129): per asset and for the portfolio, from the owner’s journal: rent, income, expenses and net, gross and net yield on the acquisition price, vacant days, provisions against recoverable charges, arrears aged at 30/60/90 days, the bank by month, the year to date against the previous year, and the tax estimate once the year has its 2044 map; the CSV export. The screen comes next.

## RENT-SPEC-017 — Tenant portal (phase 4)

Tenants sign in through Socrate to read their documents, pay, and raise requests, removing e-mail as the only channel.

- Documents: contract, receipts, regularisation statements, inventories; downloads logged.
- Payments: SEPA direct debit mandate or card through a PSP; payment status feeds RENT-SPEC-003 and 013.
- Requests: incidents (RENT-SPEC-015), insurance certificate upload (clears the calendar item), notice submission (RENT-SPEC-010).
- Native e-signature of contract, amendments and inventories (resolves OD-08).

## RENT-SPEC-018 — Integrations (phase 4)

External systems replace manual entry where a stable interface exists.

- Open banking (DSP2 aggregator) for RENT-SPEC-013; e-signature provider; Visale verification; INSEE IRL already in phase 1.
- Syndic extranets: import of annual statements and appels de fonds where an export exists, else document capture (RENT-SPEC-020).
- Accounting software export (FEC, or direct API of the accountant’s tool); calendar feed (ICS) of the obligations calendar.

## RENT-SPEC-019 — SCI, LMNP and multi-entity (phase 4)

The platform handles owners that are legal entities and furnished rentals under BIC, with their own declarations.

- SCI: entity with associates and shares; income allocated per associate for 2044/2072; entity-level ledger and FEC.
- LMNP: BIC micro or réel, amortisation of the asset and furniture, 2031/2033 figures; furnished leases already supported by RENT-SPEC-008 produce these.
- One person can be a private owner, an SCI associate and an LMNP owner at once; reporting consolidates per person.

## RENT-SPEC-020 — Assisted document capture (phase 4)

Syndic statements, invoices, tax notices and bank statements are read by a model into proposed ledger entries that the owner confirms.

- Upload → extraction of lines, amounts, dates and recoverable split → proposed Charges with confidence; nothing is posted without confirmation.
- Legal watch: a yearly check of the parameters table (IRL cap, thresholds, deadlines) against official sources, proposed as a diff for the owner to apply.
- Model calls run server-side with the document only, no tenant personal data sent beyond what the document holds; provider is an open decision.

## RENT-SPEC-021 — Engineering governance (all phases)

Every phase ships with the same artefacts, so the product stays verifiable and operable by one person.

- Screen inventory and navigation map per phase; roles × permissions matrix (owner, co-owner, accountant, tenant, admin) enforced in RLS and the API.
- API surface per module in OpenAPI (swag), versioned with the release; breaking changes only at a major.
- Legal calculations (fee, revision, reconciliation, tax lines) covered by golden-file tests per tax year, re-run on every release.
- Parameters table with effective dates for every legal constant; a change is a reviewed PR, not a config edit.
- Migration of existing history: importers for prior leases, documents and spreadsheets, so the archive starts with 10 years already loaded.
- Threat model reviewed per phase (tenant data, documents, payments); AI security audit in CI; backup and restore runbook tested twice a year.
- Observability: job log, failed-run alerts, document generation metrics; status page for the tenant portal from phase 4.

## RENT-SPEC-022 — Security and audit trail (phase 1, day 1)

From the first release every business change is recorded in an append-only, hash-chained audit log, and the application meets OWASP ASVS level 2 and RGPD obligations; neither is deferred to a later phase.

**Audit trail**

- `audit_events`: append-only table (INSERT only, enforced by role grants and a trigger refusing UPDATE/DELETE) with actor, scope, action, entity, before/after JSON, request id, timestamp, and `prev_hash`/`hash` chaining each event to the previous one per owner.
- Every mutable business row carries `created_by`, `created_at`, `updated_by`, `updated_at`; every service write emits one audit event in the same transaction.
- Covered actions: lease and guarantee edits and transitions, charge creation and recoverable overrides, payment matching, document issue and cancel, grants, legal parameter changes, job runs, exports and downloads of documents, end-of-retention deletions.
- Integrity: a weekly job re-verifies the hash chain and the archive hashes (archive.verify); the chain head is written daily to the object store under object lock, so a database-level tampering is detectable.
- Retention: audit events kept 10 years with the documents; application logs 1 year; both exportable per owner for an audit.
- Reconstructability: for any issued document the input snapshot plus the audit events let the figure be recomputed and explained (piste d’audit fiable principles: chronological, immutable, traceable).

**Security baseline (OWASP ASVS L2)**

| Control | Day-1 rule |
| --- | --- |
| Authentication | Socrate OIDC with PKCE; MFA mandatory for owner and admin roles; access tokens 15 min, refresh 8 h, revocable; step-up re-authentication before issuing a tax summary, changing grants or parameters, defined as auth\_time at most 5 minutes old and mfa present in amr; a token without auth\_time is refused for these routes |
| Authorisation | RLS on every owner table, API role without BYPASSRLS, grants table reviewed in the audit log, deny-by-default routes |
| Transport and headers | TLS 1.2+ only via Caddy, HSTS, CSP without inline scripts; no token ever reaches the browser: a Backend-for-Frontend in tenure holds them server-side and the browser gets only a `__Host-`, HttpOnly, SameSite=Strict session cookie, with a CSRF token on every unsafe request |
| Data at rest | Encrypted PostgreSQL volume and bucket (SSE), encrypted backups, keys managed by the hosting provider’s KMS, rotation yearly |
| Residency | All data and backups hosted in the EU, provider DPA signed, no third-party processor for tenant data in phase 1 |
| Uploads | Allowed types PDF, images, CSV/OFX; size caps; antivirus scan before archiving; never served inline from the API |
| API hardening | Rate limiting per subject and IP, lockout on failed auth, input validation in services, problem-detail errors without stack traces |
| Secrets and roles | Secrets by environment, separate DB roles for API, migrations and jobs, admin break-glass logged and alerted |
| Supply chain | Pinned dependencies, SBOM generated at release, dependency and container scanning in CI, AI security audit per PR |
| Monitoring | Alerts on failed auth bursts, chain verification failure, unusual document download volume |
| RGPD | Records of processing, privacy notice to tenants, data-subject export and erasure procedure after retention, breach notification runbook (72 h) |
| Assurance | Threat model before the phase 1 gate; external penetration test before the phase 4 portal opens to tenants |

**Gate addition**: the phase 1 gate also requires a passing hash-chain verification over one real month and the ASVS L2 checklist signed off.

## Non-functional requirements

- **Isolation**: every query runs under RLS with the owner\_id from the Socrate token; an owner never sees another owner’s rows or objects, including through exports.
- **Idempotence**: billing and tax jobs can be re-run for a period without creating duplicates; every document has a deterministic number.
- **Auditability**: every document, charge and payment carries created\_by, created\_at and, for generated documents, the input snapshot (lease version, index values, template version).
- **Durability**: nightly PostgreSQL dump and bucket replication to a second region; restore drill twice a year; RPO 24 h, RTO 4 h.
- **Privacy (RGPD)**: tenant personal data minimised to what the lease and receipts require; tenant data of an ended lease is kept with the lease for the retention period, then eligible for deletion; data-subject export available.
- **Performance**: portfolio sizes up to 50 assets per owner, 200 owners; a monthly run finishes in under 5 minutes; a year export in under 60 seconds.
- **Localisation**: French is the default language of every user interaction (UI, e-mails, notifications, error messages); English is an optional per-user preference for the UI and e-mails, stored on the user profile; generated legal and tax documents (contracts, receipts, statements, tax summary) are always in French; code, identifiers, API and logs are in English; no vue-i18n, own lightweight dictionary; money in € with French formatting in both languages.
- **Legal currency**: rent and tax rules (IRL cap, micro-foncier threshold, 2044 lines) are configuration values with an effective date, not code constants.

## Open decisions

| Id | Question | Decision (2026-11-01) | Owner | Status |
| --- | --- | --- | --- | --- |
| OD-01 | Furnished leases (LMNP/BIC) | Gross receipts only in phase 1; full BIC in phase 4 (RENT-SPEC-019) | Olivier | Decided |
| OD-02 | Payment capture | Manual entry in phase 1; CSV and OFX import in phase 2; open banking in phase 4 | Olivier | Decided |
| OD-03 | PDF engine | gotenberg container (HTML → PDF/A over HTTP) | Olivier | Decided |
| OD-04 | Object store | Managed EU S3 (Scaleway Object Storage, fr-par) with object lock; MinIO only in docker-compose for dev and tests | Olivier | Decided |
| OD-05 | Tenant portal | Phase 4 (RENT-SPEC-017) | Olivier | Decided |
| OD-06 | Multi-owner SCI | Ownership shares in phase 1; SCI entity in phase 4 (RENT-SPEC-019) | Olivier | Decided |
| OD-07 | Product, repositories, binary | Product « Tenure », repositories ovander/tenure-api and ovander/tenure-web, binary tenure | Olivier | Decided |
| OD-08 | E-signature | External French eIDAS provider (Yousign) from phase 2; never native | Olivier | Decided |
| OD-09 | Model provider for document capture | Claude API, server-side, document content only | Olivier | Decided |

## Changelog

| Date | Version | Change |
| --- | --- | --- |
| 2026-11-01 | 0.4 | Roadmap in four phases; RENT-SPEC-009 to 021 added (operations, finance and maintenance, best in class, governance); OD-08 and OD-09 added |
| 2026-11-01 | 0.3 | Added RENT-SPEC-008 lease contract and guarantees; Guarantee entity in the domain model; scope updated |
| 2026-11-01 | 0.2 | Added RENT-SPEC-007 charge reconciliation; monthly fee defined as monthly rent + provisional charges |
| 2026-11-01 | 0.1 | Initial high-level corpus RENT-SPEC-001 to 006, open decisions OD-01 to OD-07 |
| 2026-11-01 | 0.5 | Added RENT-SPEC-022 security and audit trail as a day-1 requirement; phase 1 scope and gate updated |
| 2026-11-01 | 0.6 | Product named Tenure; OD-07 decided (repository binary tenure); spec ids RENT-SPEC/RENT-BE/RENT-FE unchanged |
| 2026-11-01 | 0.7 | Socrate and backendkit made a mandatory platform constraint (RENT-SPEC-006): no local authentication of any kind |
| 2026-11-01 | 0.8 | Language rule made explicit: French default for all user interaction, English optional per user, legal documents always French |
| 2026-11-01 | 0.9 | OD-07 finalised: two repositories ovander/tenure-api and ovander/tenure-web created on GitHub; architecture table updated |
| 2026-11-01 | 1.0 | All open decisions OD-01 to OD-09 decided; phase 1 slice order and dev environment added to the roadmap; corpus frozen as the phase 1 baseline |
| 2026-11-01 | 1.1 | Implementation feedback (OPEN-QUESTIONS Q-02, Q-07, Q-08, Q-12): 2044 line map corrected to the real form, provisional charges excluded from income, IRL cap moved to legal\_params, step-up defined by auth\_time and amr |
| 2026-11-01 | 1.2 | Implementation feedback (OPEN-QUESTIONS Q-27, Q-28, Q-29, Q-31, Q-32): 222 per local, rent control as three dated rules (irl\_cap, zone ceiling, relocation rule), 229/230 non-recoverable shares, parameters keyed by income year, consistent ownership shares per lease |
| 2026-11-01 | 1.3 | Browser login through a Backend-for-Frontend (backendkit bff): no token in the browser, session cookie and CSRF (RENT-SPEC-022) |
