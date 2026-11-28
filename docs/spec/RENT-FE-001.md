# Tenure — Frontend UX specification (RENT-FE-001)

Nov 1, 2026 · @Olivier

## 1. Scope, users and principles

The phase 1 app is a desktop-first web application for one owner managing a handful of assets, used a few times a month: a monthly check of billing, occasional lease work, a yearly reconciliation and tax run; speed to the right document matters more than density of features.

**Users**: owner (primary, all actions), co-owner and accountant (read, phase 1), tenant (phase 4 portal, separate app shell).

**Principles**

- Asset-centric: every screen is reached from an asset or the portfolio; a document, lease or charge always shows its asset breadcrumb.
- Nothing silent: every generated document, job run and state change appears in an activity list on the asset and on the home screen.
- Review before issue: the app always shows the computed result (fee, allocation, tax lines) with a « dry run » before the irreversible action; issued documents are read-only everywhere.
- Blocked, not surprised: a missing diagnostic, an invalid guarantee or an unmatched payment is listed as a to-do with a link, never discovered inside a failing action.
- French is the default for every interaction; English is an optional per-user setting (profile, S14) that switches the UI and e-mails only — generated documents stay in French; euro amounts formatted « 1 234,56 € »; dates « 01/11/2026 » in both languages.

## 2. Information architecture

A left navigation of six entries, with the asset page as the hub that holds its leases, documents, charges and activity in tabs.

&#91;embedded content: navigation map · 7 entries, 2 hubs\]

Billing and Charges open portfolio-wide views; the same screens are reached filtered from an asset hub.

URL scheme: `/`, `/assets`, `/assets/{id}/(overview|leases|charges|documents|activity)`, `/leases/{id}/(overview|contract|guarantees|billing|inventories)`, `/billing/{period}`, `/charges`, `/reconciliations/{id}`, `/tax/{year}`, `/documents`, `/settings/(profile|templates|params|grants)`.

## 3. Screen inventory

Fourteen screens cover phase 1; later phases add screens under the same hubs rather than new navigation entries.

| # | Screen | Purpose | Key actions | Phase |
| --- | --- | --- | --- | --- |
| S01 | Home — month cockpit | Where the month stands, step by step (rent calls, payments, receipts, bank, recovery), then the anomalies by severity, the next dates and the last job runs (§4b) | Open the step's screen, open an anomaly, switch month | 1 (cockpit: 3) |
| S02 | Assets list | All assets with kind, tenant, rent, status | Add asset, open asset | 1 |
| S03 | Asset hub | Overview, leases, charges, documents, activity, audit tabs | New lease, add charge, upload document | 1 |
| S04 | Asset form | Create or edit an asset, attach a box to a dwelling, ownership shares | Save | 1 |
| S05 | Lease hub | Overview, contract, guarantees, billing, inventories, audit tabs; state and blockers | Transition (activate, notice, end, close) | 1 |
| S06 | Lease form | Draft a lease: assets, tenants, amounts, IRL reference, clauses | Save draft, generate contract | 1 |
| S07 | Guarantees | Deposit, surety, Visale, insurance per lease with status and documents | Add, generate document, settle deposit | 1 |
| S08 | Billing month | All fees of a period: computed vs issued, receipts, exceptions | Dry run, issue, cancel and reissue, send again | 1 |
| S09 | Payments | Record and match payments to documents; unmatched list | Add payment, match, split | 1 |
| S10 | Charges ledger | Charges by asset and exercise, category, recoverable flag, document | Add, import syndic statement, edit draft lines | 1 |
| S11 | Reconciliation wizard | Steps: import → allocate → review balances → issue → new provision | Next, back, issue statement, apply provision | 1 |
| S12 | Tax year | Both regimes side by side, 2044 lines with drill-down to charges and payments, missing documents | Issue summary, download PDF and CSV | 1 |
| S13 | Documents | Archive browser by asset, year, class; search by number | Download, export year or asset ZIP | 1 |
| S14 | Settings | Profile, templates and versions, legal parameters (read, admin edit), grants to co-owner or accountant | Save, invite grantee | 1 |
| S15 | Candidate file and inventories | Under lease hub (RENT-SPEC-009) | — | 2 |
| S16 | Obligations calendar | Portfolio timeline and 90-day list (RENT-SPEC-012) | — | 2 |
| S17 | Arrears case | Escalation steps and letters per lease (RENT-SPEC-011) | — | 2 |
| S18 | Dashboard | Yield, vacancy, aging, cash (RENT-SPEC-016) | — | 3 |
| S19 | Maintenance | Incidents and works orders (RENT-SPEC-015) | — | 3 |
| S20 | Tenant portal | Separate shell: documents, payments, requests (RENT-SPEC-017) | — | 4 |

## 4. Key flows (phase 1)

Each flow ends on a read-only document or a dated event, so the user always knows what was produced.

1. **Lease to activation**: S04 asset exists → S06 draft lease → S07 guarantees entered → S06 « generate contract » shows blockers (missing DPE, deposit above cap) or produces CT-… → upload signed copy and entry inventory on S05 → « Activate » enabled → lease active, first fee planned on S08.
2. **Monthly billing review**: S01 shows « November billing: 3 fees computed, 1 exception » → S08 lists each lease with rent, provisions, proration, balance line and the exception (lease in notice ending mid-month) → « Issue » → documents AL-… listed with sent status → payments arrive on S09 → receipts QL-… appear on S08 as months complete.
3. **Charge reconciliation**: S10 « import syndic statement » → S11 step 1 maps columns and shows recoverable split per line → step 2 allocation per lease, days and amounts visible → step 3 balances with sign and the 12-month spread flag → step 4 issue RG-… → step 5 new provision proposed per lease, apply from next month.
4. **Tax year**: S12 for year N shows micro vs réel totals, each 2044 line expandable to its charges and payments, orange rows for charges without document → « Issue » → RF-… PDF and CSV in S13.
5. **Correction**: any issued document shows « Cancel and reissue »; the app explains it creates a cancelling document and a new one, both archived.

## 4b. Month cockpit (S01)

An owner opens Tenure a few times a month and must see at once where the month stands and what is
wrong, without opening five screens. S01 becomes a cockpit: the monthly cycle as steps, then the
anomalies, then the existing next dates and job runs. It replaces the to-dos the app used to
compute in the browser from several calls: one call, `GET /overview/month` (RENT-BE-001 §3),
serves it, and the API applies the rules below.

**Period.** The current month (Europe/Paris) by default; arrows switch to the previous and next
months, kept in the URL (`/?period=2026-11`). A past month shows how it ended; a future month
shows only its rent calls to come.

**Steps.** Five cards in this order, each with a state, two or three counts and one action that
opens the screen where the step is done. States: *done* (green), *to do* (amber, the owner
acts), *waiting* (grey, a job or the tenants act), *nothing to do* (no lease concerned).

| Step | Counts | State rule | Action |
| --- | --- | --- | --- |
| 1. Rent calls | billable leases; live rent calls issued; leases without one | *done* when every billable lease (active or under notice, occupied in the period) has a live rent call; *to do* otherwise, after the 1st (the monthly run issues them on the 1st at 06:00); *waiting* before | « Ouvrir la facturation » → S08 of the period |
| 2. Payments | amount called; amount received and applied; calls paid, partly paid, unpaid; of which past their due day | *done* when every call of the period is paid; *to do* when one is past its due day and not paid; *waiting* otherwise | « Enregistrer un paiement » → S09 |
| 3. Receipts | paid calls; receipts issued; paid calls without one | *done* when every paid call has its receipt; *waiting* while the hourly job has not run since the payment; *to do* when one is still missing after `overview.receipt_delay_hours` | « Émettre les quittances » → S08 |
| 4. Bank | accounts; last statement imported (date); lines unmatched, the oldest one's age | *nothing to do* with no account; *to do* when a line is unmatched past `bank.unmatched_alert_days` or no statement covers the period's last `overview.statement_gap_days`; *done* otherwise | « Rapprocher » → Bank screen |
| 5. Recovery | leases in arrears; reminders sent in the period; cases at a formal stage | *done* when no lease is in arrears; *to do* when a lease's next recovery step is due; *waiting* when reminders are sent and not yet due again | « Voir les impayés » → the lease's recovery tab, or a list when several |

When the five steps are *done* or *nothing to do*, a line says « Novembre 2026 est à jour ».

**Anomalies.** Below the steps, one list sorted by severity, then by date; each item names its
lease or asset, says what is wrong and since when, and links to the screen that fixes it. The API
detects them; the app shows them as given.

| Kind | Severity | Rule |
| --- | --- | --- |
| `rent_call_missing` | blocking | a billable lease has no live rent call after the period's monthly run |
| `job_failed` | blocking | a job run ended failed or partial in the last `overview.job_window_days` |
| `payment_overdue` | to act | a call is past its due day by more than `overview.overdue_grace_days` and not fully paid (amount left, days late) |
| `bank_line_stale` | to act | a bank line unmatched for more than `bank.unmatched_alert_days` |
| `receipt_missing` | to act | a paid call without its receipt after `overview.receipt_delay_hours` |
| `irl_revision_due` | to act | a lease's IRL revision is due and not applied (the calendar's rule) |
| `regularisation_due` | to act | an exercise of a leased asset ended more than `overview.regularisation_after_days` ago without an issued reconciliation |
| `inventory_missing` | to act | an active lease without an issued entry inventory |
| `diagnostic_expiring` | to watch | a diagnostic of a let asset expires (its `expires_on`) within `overview.expiring_days`, or has expired |
| `deposit_deadline` | to watch | a deposit to return by its legal date, from the calendar |

Severity is the order, never a colour alone: each item also carries its words (« Bloquant »,
« À traiter », « À surveiller »). The thresholds are business rules, kept with the others in
`legal_params` (`overview.*`), never in code (Q-139).

**Roles.** The owner sees every action; a co-owner and an accountant see the same cockpit, every
step and anomaly included, read only (the actions open the screens, which are read only for
them; decided 2026-11-28, Q-139). An SCI's scope has the same
cockpit; its steps cover its own leases.

**Below the cockpit.** The next dates (90 days, S16) and the last job runs stay, as today.

**Not in this cockpit:** the onboarding checklist and the « next step » cards of the asset and
lease hubs (a later spec), and notifications by e-mail.

## 5. Roles and visibility

| Element | Owner | Co-owner | Accountant |
| --- | --- | --- | --- |
| Home to-dos and job runs | full | read | read |
| Assets, leases, guarantees | full | read | names only, no guarantor data |
| Billing month, payments | full | read | read |
| Charges, reconciliation | full | read | read, export |
| Tax year | full | read | read, export |
| Documents | full | download | download financial classes only |
| Settings, grants | full | — | — |
| Audit tabs and audit export | read, export | read | read, export |

Write controls are hidden, not disabled, for read roles; a scope switcher in the top bar appears only when the user has more than one scope.

## 6. UI conventions

PrimeVue 4.4 components with the Aura preset and Tailwind for layout; one pattern per kind of content so the fourteen screens feel like one app.

| Content | Component and rule |
| --- | --- |
| Lists (assets, documents, charges, payments) | DataTable, server-side paging and sorting, sticky header, row click opens the hub; filters in a toolbar, never inline per column |
| Forms (asset, lease, charge, payment) | Two-column on desktop, one on tablet; labels above fields; InputNumber for euro amounts in cents mapped to decimals; Calendar with French format; inline field errors from problem-detail `errors[]`, a top summary for rule errors |
| Amounts | Right-aligned, two decimals, « € » suffix, negative in red with a leading « − »; totals in bold in a footer row |
| State | Tag with one colour per state across the app: draft grey, active green, notice orange, ended and closed neutral, issued blue, cancelled strikethrough |
| Documents | A DocumentCard: number, class, period, issued date, hash tooltip, Download and Send-again; issued documents never show an edit control |
| Wizards (reconciliation, tax issue) | Stepper with a persistent summary rail on the right; Back never loses entered data; the final step shows exactly what will be written |
| Dry run vs issue | Dry-run results in a dashed-border panel titled « Simulation »; the Issue button is primary, confirmed by a Dialog that restates count and total |
| Blockers and to-dos | Message (warn) list at the top of hubs with a link per item; the blocked action stays visible but disabled with a tooltip naming the blocker |
| Empty, loading, error | Skeleton rows while loading; empty state with the one action that creates the first item; errors as a Toast for transient, inline Message for business rules |
| Activity | Timeline per asset and lease: who, what, when, link to the document |
| Navigation | Left Menu, collapsible to icons; breadcrumb asset › lease › screen; browser back always works (state in URL) |

## 7. Technical

The app is a Vue 3 + TypeScript 6 single-page PWA built with Vite 8 in its own repository github.com/ovander/tenure-web, published as a release asset `tenure-web-<tag>.tar.gz` (the built files at the archive root, `index.html` among them; hashed assets under `assets/`) and served by `tenure` as embedded static files under a CSP without inline script (Q-96), talking to the API through a generated client.

- Structure: `src/{app,features/<module>,components,stores,api,i18n}`; one feature folder per backend module with its routes, views and store.
- State: Pinia 2.3 stores per feature (`useLeaseStore`, `useBillingStore`…) holding server data by id and the active scope; no global caching beyond the current view; optimistic updates only for matching payments.
- API client: generated from `/api/v1/openapi.json` (openapi-typescript + a thin fetch wrapper) with ETag handling and problem-detail parsing; no token in the client: the browser calls same-origin `/api/v1` with the BFF session cookie and sends `X-CSRF-Token` (from `GET /bff/session`) on every unsafe request; scope header set by the store.
- Routing: vue-router with route guards per role; every list filter and wizard step is in the query string.
- PWA: vite-plugin-pwa, installable, offline shell only (no offline writes); documents open in a new tab from signed URLs.
- i18n: French default, English optional, selected in the user profile and persisted on the backend (\`PUT /me\` language); own dictionary module (`t('billing.issue')`) with FR and EN JSON files, no vue-i18n; number and date formatting through `Intl`.
- Accessibility: PrimeVue ARIA defaults kept, focus management in dialogs and wizards, keyboard-complete tables, contrast checked on the state colours; target WCAG 2.2 AA.
- Testing: Vitest unit tests on stores and formatters, component tests on the DocumentCard and wizard steps, Playwright smoke on the four key flows against a seeded backend in CI.
- Performance budget: first load under 1 MB gzipped, route-level code splitting per feature, DataTable virtual scroll above 200 rows.

**Security in the client (RENT-SPEC-022, day 1)**: no token ever reaches the browser: tenure's Backend-for-Frontend holds and refreshes them (RENT-BE-001 §4), the app sees only `GET /bff/session` (who is signed in, CSRF token); a 401 sends the user to `/bff/login?return_to=…`; step-up prompts rendered as a Dialog that navigates to `/bff/login?step_up=1&return_to=…` before tax issue, grants, parameters and exports; CSP-compatible build (no inline scripts, hashed Vite preload); every document download goes through the short-lived signed URL and is logged server-side; from phase 2 the CSP's `img-src` also allows `blob:` and the archive bucket host, so inventory photos can be previewed before upload and shown as thumbnails through their signed URLs; no other directive is widened (RENT-INV-001, INV-OD-12, applied with inventory slice 4); an « Audit » tab on asset and lease hubs lists the audit events for that entity with actor, time and before/after diff (S03 and S05).

**Identity is Socrate only (RENT-SPEC-006)**: the app has no login form, no registration and no password reset screen; « Se connecter » navigates to `/bff/login`, which redirects to Socrate and returns through the BFF callback; « Se déconnecter » posts `/bff/logout` with the CSRF token; profile, MFA enrolment and password changes link out to Socrate’s account pages; the tenant portal in phase 4 uses the same flow with its own Socrate client. The session module (`app/auth`) reads `/bff/session`, keeps the CSRF token in memory and adds it to unsafe requests; the app holds no OIDC client, client id or token.

## 8. Open decisions and changelog

| Id | Question | Decision (2026-11-01) | Status |
| --- | --- | --- | --- |
| FE-OD-01 | Asset hub tabs vs separate pages | Tabs, as specified | Decided |
| FE-OD-02 | Document preview | Inline viewer (pdf.js) with Download beside it | Decided |
| FE-OD-03 | Mobile support in phase 1 | Tablet layouts only; phone layouts arrive with the phase 4 portal | Decided |

| Date | Version | Change |
| --- | --- | --- |
| 2026-11-01 | 0.1 | Initial frontend UX specification, phase 1 screens S01–S14, later screens S15–S20 listed |
| 2026-11-01 | 0.2 | Client security rules and Audit tab on hubs, per RENT-SPEC-022 |
| 2026-11-01 | 0.3 | Product named Tenure; app title and binary name aligned |
| 2026-11-01 | 0.4 | Socrate-only identity: no local login, registration or password screens |
| 2026-11-01 | 0.5 | French default, English optional per user; language setting on profile |
| 2026-11-01 | 0.6 | Own repository ovander/tenure-web; bundle released and embedded by tenure-api |
| 2026-11-01 | 1.0 | FE-OD-01 to 03 decided (tabs, inline pdf.js viewer, tablet-only in phase 1); baseline for the phase 1 implementation plan |
| 2026-11-01 | 1.1 | Login through tenure's Backend-for-Frontend: no OIDC client or token in the app, session cookie and CSRF token |
| 2026-11-05 | 1.2 | Phase 2 CSP: `img-src` adds `blob:` and the bucket host for inventory photos (RENT-INV-001, INV-OD-12) |
| 2026-11-28 | 1.3 | S01 becomes the month cockpit (§4b): the monthly cycle as five steps and the anomalies by severity, served by `GET /overview/month` (RENT-BE-001, Q-139) |
