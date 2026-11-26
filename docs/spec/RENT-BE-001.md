# Tenure — Backend specification (RENT-BE-001)

Nov 1, 2026 · @Olivier

## 1. Scope and module layout

One Go binary, `tenure`, serves the API and runs the scheduler; it is cut into nine modules that depend downward only, so billing never imports tax and nothing imports the HTTP layer.

| Module | Package | Owns | Depends on |
| --- | --- | --- | --- |
| portfolio | `internal/portfolio` | Owner, Asset, Tenant, ownership shares | — |
| lease | `internal/lease` | Lease, Guarantee, Inventory, lifecycle state machine, contract generation (RENT-SPEC-008) | portfolio, document, params |
| billing | `internal/billing` | Monthly fee, rent call, receipt, revision, arrears (RENT-SPEC-003) | lease, payment, document, params |
| charge | `internal/charge` | Charges ledger, syndic import, reconciliation (RENT-SPEC-007) | portfolio, lease, billing, document |
| payment | `internal/payment` | Payments, matching to billing documents, bank import | lease, document, params |
| tax | `internal/tax` | Annual summary, regime routing, 2044 lines (RENT-SPEC-004) | payment, charge, document, params; reads the loans' instalments (Q-126) |
| document | `internal/document` | Numbering, PDF rendering, archive metadata, object store client (RENT-SPEC-005) | params |
| inventory | `internal/inventory` | Asset layouts, captured inventories, photos, deposit engine (RENT-INV-001) | portfolio, lease, document, params |
| calendar | `internal/calendar` | The obligations calendar's computed base, read only (RENT-SPEC-012, Q-116) | portfolio, lease, inventory, maintenance, params |
| loan | `internal/loan` | Loans and their amortisation table, computed or typed in (RENT-SPEC-014, Q-126) | portfolio, document |
| ledger | `internal/ledger` | The owner's double-entry journal, computed, and the period lock's problem (RENT-SPEC-014, Q-127); reads the capitalised works paid (Q-130) | payment, tax, document, params |
| dashboard | `internal/dashboard` | The dashboard, computed from the journal, the vacancies and the arrears, read only (RENT-SPEC-016, Q-129); per person, the SCI shares, the loans for parts and the BIC result beside (Q-133) | ledger, lease, billing, portfolio, tax, bic, params |
| bic | `internal/bic` | Furnished letting under the réel (LMNP): the component grid of an asset, versioned, its furniture and works, and their depreciation; `bic.Depreciation` and `bic.Capitalise` are pure (RENT-SPEC-019, Q-132) | portfolio, document, params, ledger, tax |
| sci | `internal/sci` | The register of an SCI at the income tax: its associates, their dated holdings of parts and the movements that open and close them; the 2072, the year's result split by the parts held on 31 December; `sci.PartsOn`, `sci.MaxHeldFrom` and `sci.Allocate` are pure (RENT-SPEC-019, Q-133). The SCI itself is an owner scope (`portfolio.CreateEntity`) | portfolio, tax |
| maintenance | `internal/maintenance` | Contractors and incidents, from report to invoice; the invoice of a repair or of a dwelling's improvement becomes a repairs charge, construction and other improvements are capitalised (RENT-SPEC-015, Q-130); recurring maintenance contracts and their visits (Q-131) | charge, portfolio, document, params |
| params | `internal/params` | Legal parameters with effective dates, IRL index cache | — |

Cross-cutting packages: `internal/auth` (backendkit, roles, RLS context), internal/audit (audit events, hash chain, heads), `internal/jobs` (scheduler, job log), `internal/notify` (e-mail), `internal/httpapi` (Chi routers, handlers, swag annotations). Phase 2 to 4 modules (onboarding, calendar, maintenance, portal, capture) are added as new packages under the same rule.

&#91;embedded content: module map · 8 modules, 3 entry packages\]

Arrows read « imports »; billing is the busiest module and the one the golden tests target first.

## 2. Data model and row-level security

Every business table carries `owner_id`, and PostgreSQL row-level security filters on it from a session variable set by the auth middleware; gorm models never add an owner filter themselves.

| Table | Key columns | Notes |
| --- | --- | --- |
| owners | id, socrate\_sub, kind (person / sci), legal\_name, address, tax\_id, language (fr default, en), siren, tax\_regime (ir), regime\_from, capital\_parts, created\_at | One row per Socrate identity acting as owner, or per SCI at the income tax, which has no Socrate subject (Q-133) |
| assets | id, owner\_id, kind (house/apartment/box), address, lot, surface\_m2, furnished, dpe\_class, controlled\_zone, tense\_zone, acquired\_on, acquisition\_cents, parent\_asset\_id | parent\_asset\_id links a box to a dwelling; controlled\_zone (boolean, default false) with rent\_control\_zone and rent\_control\_category (the zone and category the ceiling table is keyed by) makes rent\_control.zone\_ceiling apply; tense\_zone (zone tendue, boolean, default false, implied by controlled\_zone and returned as true with it) makes rent\_control.relocation\_rule apply and the contract state the previous rent (Q-102) |
| asset\_shares | asset\_id, owner\_id, share\_pct | Co-ownership; shares sum to 100 |
| tenants | id, owner\_id, names\[\], email, phone, language (fr default, en), occupation (student, apprentice, other) | Personal data minimised; occupation only for the surety-with-insurance exception (Q-72) |
| leases | id, owner\_id, kind, state, start\_on, end\_on, term\_months, rent\_cents, provision\_cents, deposit\_cents, due\_day, irl\_ref\_quarter, irl\_ref\_value, revision\_month, rent\_exemption, rent\_breakdown jsonb, clauses jsonb, version | state: draft, active, notice, ended, closed; version bumps on each change; rent\_exemption (works, under\_valued) lifts the relocation rule; rent\_breakdown (per-asset rent) is optional and for reporting from phase 3, ignored by tax; all assets of a lease must have identical ownership shares (`lease/consistent-ownership-shares`, 422) |
| lease\_assets | lease\_id, asset\_id | Many-to-many |
| lease\_tenants | lease\_id, tenant\_id, solidary | — |
| guarantees | id, lease\_id, kind, guarantor jsonb, amount\_cents, valid\_from, valid\_to, status, document\_id | kind: deposit, surety, visale, insurance; combination rules on every write (Q-72); document\_id is the generated DG or CA |
| inventories | id, lease\_id, kind (entry/exit), mode, state, done\_on, legacy\_items jsonb, document\_id, and the capture columns of RENT-INV-001 §3 | One per kind and lease (Q-70). An uploaded inventory is recorded issued, `mode upload`; phase 1 rows were migrated so, their free JSON kept as `legacy_items` and `items` refused since (migration 000011). Immutable once issued, with its items, meters, keys, photos and signatures (trigger). The other inventory tables are in RENT-INV-001 §3 (Q-108) |
| asset\_diagnostics | id, owner\_id, asset\_id, kind (dpe, erp, electricity, gas, lead, asbestos), done\_on, valid\_until, result, not\_required\_reason, document\_id | One per asset and kind (Q-23, Q-73); a kind not needed is declared with a reason |
| lease\_documents | id, owner\_id, lease\_id, kind (contract, amendment, deposit\_receipt, surety\_act, owner\_notice, renewal\_proposal), number, lease\_version, guarantee\_id, document\_id, input\_snapshot, issued\_at | Created issued and never changed (trigger); a contract is current while the lease version and its guarantees match (Q-69) |
| lease\_renewals | id, owner\_id, lease\_id, renewal\_on, status (prepared, received, accepted, refused), proposal jsonb, terms jsonb, document\_id, received\_on, delivery, tracking\_ref, answered\_on, steps\_applied, version | One per lease and renewal day; replaced while prepared; the accepted steps due are added to the rent (Q-120) |
| recovery\_steps | id, owner\_id, lease\_id, kind (formal\_notice, surety\_notice, insurance\_claim, commandement, surety\_commandement, court), done\_on, outstanding\_cents, guarantee\_id, document\_id, delivery, tracking\_ref, deadline\_on, note | Immutable (trigger); the formal notice and the surety's information name their letter (Q-121) |
| payment\_plans | id, owner\_id, lease\_id, agreed\_on, debt\_cents, calls jsonb, instalments jsonb, document\_id, cancelled\_on, version | The calls overdue on the day agreed; status (respected, broken, completed, cancelled) computed on read; a respected plan suspends the recovery (Q-122) |
| bank\_accounts | id, owner\_id, label, iban\_last4, version | The full IBAN is never stored (Q-123) |
| bank\_lines | id, owner\_id, account\_id, booked\_on, amount\_cents (credit positive), label, reference, line\_key, import\_document\_id, status (unmatched, matched, ignored), note, version | Unique per account and key: a statement imported twice adds nothing (Q-123) |
| charges | id, owner\_id, asset\_id, charged\_on, paid\_on, category, label, amount\_cents, recoverable\_cents, syndic\_recoverable\_cents, exercise\_year, document\_id, source (manual/syndic/invoice), status (draft/confirmed), reconciliation\_id | category enum drives 2044 line; the recoverable part is stored in cents beside the syndic's own split; frozen once an issued reconciliation uses it (Q-77) |
| billing\_documents | id, owner\_id, lease\_id, kind, period, generation, number, lines jsonb, total\_cents, issued\_at, cancels\_id, document\_id, input\_snapshot jsonb | Created issued and never changed (trigger); a correction is a cancellation row (cancels\_id) and the next generation of the same lease, kind and period |
| document\_counters | owner\_id, class, asset\_code, year, last\_seq | Number sequence locked in the issuing transaction (Q-48) |
| document\_deliveries | id, owner\_id, document\_id, recipient, channel, status (pending, sent, failed, skipped), attempts, last\_error | Delivery status per document and recipient (Q-24, Q-58) |
| payments | id, owner\_id, lease\_id, paid\_on, amount\_cents, method, reference, bank\_line\_id | — |
| payment\_matches | payment\_id, billing\_document\_id, amount\_cents | Partial matching; at most the payment and what remains due on a live rent call of the same lease (Q-60); moved to the reissue on cancel (Q-63) |
| billing\_reminders | billing\_document\_id (unique), sent\_on, outstanding\_cents, recipients, failures | One reminder per rent call (Q-62) |
| reconciliations | id, owner\_id, asset\_id, exercise\_year, exercise\_start, exercise\_end, state (draft, allocated, issued, settled, cancelled), totals jsonb, charge\_ids, issued\_at, corrects\_id, cancellation\_document\_id | One per asset and exercise while not cancelled; a correction cancels it and a new one records corrects\_id (Q-82) |
| reconciliation\_lines | reconciliation\_id, lease\_id, days, allocated\_cents, provisions\_cents, balance\_cents, spread, instalments, proposed/new\_provision\_cents, apply\_provision, settlement (fee, deposit, direct, none), document\_id, number | Per lease allocation and its statement RG-… (Q-80, Q-84) |
| regularisation\_postings | lease\_id, reconciliation\_id, line\_id, parent\_id, reference, instalment, of, amount\_cents, due\_from, billing\_document\_id, voided\_at | What the next fees carry: a balance, an instalment, a carry-over; billing reads them, charge writes them (Q-79) |
| tax\_summaries | id, owner\_id, tax\_year, state (draft, issued), regime, summary jsonb (both regimes, every share-holder, flags, source ids), inputs, deficits\_after, issued\_at, document\_id (RF PDF), csv\_document\_id | One per owner and year; a draft until issued, then immutable (trigger, Q-88 to Q-90) |
| documents | id, owner\_id, class, asset\_id, lease\_id, period, object\_key, object\_version, sha256, size\_bytes, template\_version, issued\_at, retain\_until, source (generated, upload), content\_type, original\_name, signs\_document\_id | Metadata of every archived file; an upload has no number and keeps its sniffed type; a signed copy points at the generated document it signs; retain\_until stays unset until the retention clock starts (Q-67, Q-68) |
| legal\_params | key, value jsonb, effective\_from, source\_url | See section 7 |
| irl\_index | quarter, value, published\_on | INSEE cache |
| job\_runs | id, job, period, started\_at, finished\_at, status, summary jsonb | Idempotence key = (job, period) |
| audit\_events | id (bigserial), owner\_id (NULL: platform chain of admin actions, Q-36), actor\_sub, scope, action, entity\_type, entity\_id, before jsonb, after jsonb, request\_id, occurred\_at, prev\_hash, hash | INSERT only; trigger refuses UPDATE/DELETE; hash = sha256(prev\_hash ‖ canonical JSON of the row); one chain per owner |
| audit\_chain\_heads | owner\_id, as\_of, last\_event\_id, head\_hash, object\_key | Daily head written to the locked bucket; verified weekly against the chain |
| owner\_grants | owner\_id, grantee\_sub, role (co-owner / accountant / manager), granted\_at, revoked\_at | Joined in the RLS policy; part of migration 000001; every change audited |
| export\_jobs | id, owner\_id, requested\_by, role, year or asset\_id, state (pending, ready, failed), object\_key, object\_version, sha256, size\_bytes, documents, error, created\_at, finished\_at | State of an async ZIP export (migration 000008); read by its requester and the scope's owner (Q-91) |

**Conventions**: every mutable business table carries a `version` used as the ETag (Q-40); money as integer cents (`*_cents`), dates as `date`, timestamps `timestamptz` in UTC, periods as `char(7)` YYYY-MM, enums as PostgreSQL enums, soft delete nowhere (documents are never deleted, business rows are closed by state); every mutable business table carries created\_by, created\_at, updated\_by, updated\_at, filled by the repository layer from the request context.

**RLS**: `ALTER TABLE … ENABLE ROW LEVEL SECURITY` on every table with `owner_id`; policy `USING (owner_id = current_setting('app.owner_id')::uuid)`; the API role has no BYPASSRLS; migrations and jobs run as a separate role that sets `app.owner_id` per owner iteration. Co-owner and accountant access is granted through an `owner_grants (owner_id, grantee_sub, role)` table joined in the policy.

**Migrations**: six-digit zero-padded SQL files under `migrations/`, applied by `tenure migrate` at deploy; every migration reversible or explicitly marked not.

## 3. API surface

REST under `/api/v1`, JSON, documented by swag annotations on each handler and served at `/api/v1/openapi.json`; every list endpoint pages with `?page=&size=` and filters by query parameters, every write returns the updated resource.

| Module | Endpoints (phase 1) | Notes |
| --- | --- | --- |
| portfolio | `GET/POST /assets`, `GET/PUT /assets/{id}`, `GET/POST /assets/{id}/diagnostics`, `PUT /diagnostics/{id}`, `GET/POST /tenants`, `GET/PUT /tenants/{id}`, `GET/PUT /me`, `GET /me/export` (step\_up), `GET/POST /grants`, `DELETE /grants/{id}` (POST and DELETE step\_up) | `/me` returns the caller, roles and grants; `PUT /me` creates the caller's owner profile (no `If-Match`) or updates it, language included (Q-06, Q-35); a grant names the grantee's Socrate subject and is revoked, never deleted (Q-92); `/me/export` is the caller's RGPD export (Q-93) |
| lease | `GET/POST /leases`, `GET/PUT /leases/{id}`, `POST /leases/{id}/transitions` (activate, notice, end, close), `POST /leases/{id}/contract` (generate), `GET /leases/{id}/blockers`, `GET /leases/{id}/documents`, `POST /leases/{id}/amendments`, `GET/POST /leases/{id}/guarantees`, `PUT /guarantees/{id}`, `POST /guarantees/{id}/document` (DG or CA), `GET/POST /leases/{id}/inventories` (`mode` upload or amicable, Q-111), `GET/POST /leases/{id}/notice`, `POST /leases/{id}/notice/letter` (RENT-SPEC-010, Q-118), `GET /leases/{id}/terms`, `GET /assets/{id}/vacancies` (Q-119), `GET /leases/{id}/renewals`, `POST /leases/{id}/renewal/letter`, `POST /leases/{id}/renewals/{rid}/reception`, `/answer`, `/steps` (Q-120) | Transitions validate the state machine and required documents; blockers lists what blocks generation and activation, and the warnings (Q-75); a blocker about an asset names it in `asset_id`, with the diagnostic kind in `diagnostic` and an expired one's `valid_until` (Q-105); a notice is recorded once, received on a date, and tenure computes the end: the tenant's period by lease kind, ground and zone tendue, the owner's for the end of the term on the CG letter generated for the lease (dry\_run on both) |
| billing | `GET /billing/documents`, `GET /billing/documents/{id}`, `POST /billing/runs` (period, dry\_run), `GET /billing/runs/{id}`, `POST /billing/documents/{id}/cancel`, `GET /leases/{id}/arrears`, `POST /leases/{id}/revision`, `GET /leases/{id}/recovery`, `POST /leases/{id}/recovery/letter`, `POST /leases/{id}/recovery/steps` (RENT-SPEC-011, Q-121), `POST /leases/{id}/recovery/plans`, `POST /leases/{id}/recovery/plans/{pid}/cancel` (Q-122) | A run with dry\_run returns the computed fees without issuing; cancel issues the cancelling document and reissues the rent call (Q-56) |
| charge | `GET/POST /charges`, `GET/PUT /charges/{id}`, `POST /charges/import/syndic` (an uploaded CSV + mapping, Q-77), `GET/POST /reconciliations`, `GET /reconciliations/{id}`, `POST /reconciliations/{id}/transitions` (allocate, issue, settle), `PUT /reconciliations/{id}/lines/{lineId}`, `GET /bank-lines/{id}/charge-suggestions`, `POST /bank-lines/{id}/charges` (Q-125) | Import creates charges in draft for review |
| payment | `GET/POST /payments`, `GET/PUT /payments/{id}`, `POST /payments/{id}/matches`, `POST /payments/import` (CSV/OFX, Q-123), `GET/POST /bank-accounts`, `GET /bank-lines`, `POST /bank-lines/{id}/status`, `GET /bank-lines/{id}/suggestions`, `POST /bank-lines/{id}/match`, `POST /bank-lines/auto` (Q-124) | Matching rules in service layer (Q-60); amount and date frozen once receipted (Q-64) |
| tax | `GET /tax/summaries`, `POST /tax/summaries` (year, regime), `GET /tax/summaries/{id}`, `GET /tax/summaries/{id}/lines.csv` | Regime optional: both computed (draft), one issued; POST and lines.csv are step\_up (Q-90) |
| inventory | `GET/PUT /assets/{id}/layout` (If-Match on the layout version), `POST /assets/{id}/layout/seed`, `GET /inventory-templates` | The asset layout of RENT-INV-001 §4: seeded once from a template, then edited as a whole with its ids kept; not for an accountant (Q-109). `POST /inventories/{id}/photos` (quarantine, ClamAV, metadata stripped, archived) and `GET /inventories/{id}/photos/{photoId}/content` (signed link, audited), not for an accountant (Q-110). `GET/PUT /inventories/{id}`, `POST /inventories/{id}/items`, `PUT /inventories/{id}/items/{itemId}`, `PUT /inventories/{id}/meters`, `PUT /inventories/{id}/keys` and `POST /inventories/{id}/transitions` (schedule, start, complete, reopen, issue into `EE`; `dry_run`) capture an entry (Q-111). `GET /inventories/{id}/preview` (the PDF to sign), `GET/POST /inventories/{id}/signatures`, `POST …/signatures/refresh` and `…/cancel` sign it, by hand or through Yousign, and issue `EE` at the last signature (Q-112). An exit is created from the issued entry and `GET /inventories/{id}/comparison` compares them; the exit issues `ES` (Q-113). `GET/POST /inventories/{id}/completions` and `PUT /completions/{id}` record a tenant's completion request on an issued entry and the owner's decision, issuing `EC` (Q-114). `GET/POST /vetuste-grids`, `PUT /vetuste-grids/{id}`, `PUT /leases/{id}/vetuste-grid`, `GET /leases/{id}/deposit`, `GET/POST /leases/{id}/deposit-deductions`, `DELETE /deposit-deductions/{id}` and `POST /leases/{id}/deposit-settlement` settle the deposit into `RD` statements (class `restitution`, Q-115) |
| calendar | `GET /calendar` (`from`, `to`, `asset_id`) | The obligations calendar's computed base: diagnostics' validity, IRL revisions, ends of notice and term, the yearly insurance certificate, completion windows, deposit deadline, recurring maintenance visits (Q-131); overdue items always listed; not for an accountant (Q-116) |
| loan | `GET/POST /loans` (`asset_id`), `GET/PUT /loans/{id}` (If-Match), `GET /loans/schedule` (a preview) | A computed table (fixed rate, constant monthly instalments) or the bank's typed in; the instalments due in an income year give line 250 (Q-126); a loan of kind `parts` finances an associate's SCI parts, no asset, its interest with their shares (Q-133) |
| ledger | `GET /ledger` (`year`), `GET /ledger/journal.csv`, `GET /ledger/fec.txt` (closed year only), `GET /ledger/pack.zip` (all three step-up and audited, Q-128) | The year's journal on a cash basis, posted to the chart in `legal_params`, with each account's movements and whether the year is closed; a write into a closed year is a 409 `ledger/period-locked` (Q-127) |
| dashboard | `GET /dashboard` (`year`), `GET /dashboard/assets.csv` (step-up, audited) | Per asset and for the portfolio, from the owner's journal: rent, income, expenses, net, yields, vacant days, provisions against recoverable charges, aged arrears, months, year to date against the previous year, tax estimate when the year has its 2044 map (Q-129), with the SCI shares, the loans for parts and the BIC result beside; none in an SCI's scope (Q-133) |
| maintenance | `GET/POST /contractors`, `PUT /contractors/{id}` (If-Match), `GET/POST /incidents` (`asset_id`, `status`, `open`), `GET/PUT /incidents/{id}` (If-Match, while not done), `POST /incidents/{id}/transitions` (If-Match: quote, schedule, done, invoice, cancel) | An incident's cycle new → quoted → scheduled → done → invoiced, or cancelled; the invoice of a repair, or of an improvement of a dwelling (`tax.improvement_deductible`) or of an asset let in the same lease as one, creates a confirmed repairs charge (line 224); construction and other improvements are posted to the fixed-asset accounts `ledger.capitalised_construction` and `ledger.capitalised` and deducted nowhere (Q-130) |
| bic | `GET/PUT /assets/{id}/bic-grid` (If-Match, 0 for the first version), `GET/POST /assets/{id}/fixed-assets`, `PUT /fixed-assets/{id}` (If-Match), `GET /assets/{id}/depreciation` (`year`), `GET /bic/result` (`year`), `GET/POST /bic/declarations` (POST step_up) | The grid, versioned and never changed, its default proposed from `bic.components`; furniture and works expensed or capitalised by nature then `bic.small_equipment`, a forced treatment with its reason; the year's straight-line depreciation by days; the owner's BIC result, depreciation capped by art. 39 C with the excess carried, and the micro-BIC beside; the declaration of a year under the regime chosen, stored as issued, its grids frozen, deficits carried (Q-132) |
| portfolio, sci | `POST /entities` (step_up), `GET/PUT /entity` (If-Match), `GET /associates` (`on`), `POST /associates`, `PUT /associates/{id}` (If-Match), `GET/POST /sci/movements`, `GET /sci/result` (`year`), `GET/POST /sci/declarations` (POST step_up), `GET /sci/shares` (`year`) | An SCI at the income tax created as an owner scope, its creator its first manager; its register of associates (a name, or a Tenure person linked as self or by a live grant), their holdings of parts opened and closed by subscriptions, transfers, gifts and successions, within the capital; its 2072, the year's 2044 réel figures split by the parts held on 31 December by largest remainder, issued once and stored, after which no movement is dated in that year; the 2044 and the BIC declaration are refused in its scope; a linked associate reads their shares in their own scope, and their tax summary adds them (`tax.WithShares`) (Q-133) |
| maintenance | `GET/POST /maintenance-contracts` (`asset_id`), `GET/PUT /maintenance-contracts/{id}` (If-Match), `POST /maintenance-contracts/{id}/visits` (If-Match) | Recurring maintenance (boiler, chimney with its fuel, other); the legal rhythm and who must have it done come from `maintenance.recurring` and, for a chimney, `maintenance.chimney` by fuel; a longer interval is refused; each visit with its certificate sets the next one, which the calendar shows as `maintenance_due` (Q-131) |
| document | `GET /documents`, `GET /documents/{id}`, `GET /documents/{id}/content` (signed URL), `POST /documents` (multipart upload with class and links, file last; scanned, sniffed, 25 MB, Q-67), `POST /documents/export` (year or asset ZIP, async, step\_up), `GET /documents/exports/{id}` (state, then a signed link), `GET /templates` (read only) | Content never streamed through the API for large files: short-lived signed URL; `GET /documents` filters by asset, year, class and number, and each item carries the `number` of a document issued from a template (none for an upload); an accountant's export holds financial classes only (Q-91); `/templates` lists the embedded templates by version with their SHA-256 and how many of the scope's documents each rendered (Q-106) |
| params | `GET /params`, `GET /params/irl`, `PUT /params/{key}` (admin), `PUT /params/irl/{quarter}` (admin, manual override of a missing quarter, Q-53) | Effective-dated values |
| jobs | `GET /jobs/runs`, `POST /jobs/{name}/trigger` (admin or owner for own scope) | — |
| audit | GET /audit/events (entity, from, to), GET /audit/events.csv, GET /audit/heads | Read only; owner and accountant; includes prev\_hash and hash per event |

**Conventions**: ids are UUIDv7; errors follow RFC 9457 problem details with a stable `type` per business rule (`lease/one-active-per-asset`, `billing/period-already-issued`); ETag on resources with `If-Match` required on PUT; all amounts in cents in JSON; webhooks none in phase 1.

**Versioning**: `/api/v1` frozen at phase 1 gate; additive changes only; `/api/v2` at the phase 3 major.

## 4. Identity and roles

Socrate issues the OIDC token, backendkit validates it and populates the request context with the subject and roles; the owner scope is resolved from `owners.socrate_sub` or an `owner_grants` row, and set as `app.owner_id` for RLS before any repository call.

| Role | Granted through | Can |
| --- | --- | --- |
| owner | owners row | Everything in their scope |
| manager | owner\_grants role manager, in an SCI's scope only | The owner's rights in the SCI (writes, step-up actions, grants); `tenure_scope_role` answers owner; an SCI keeps at least one, and nobody revokes their own grant (Q-133) |
| co-owner | owner\_grants role co-owner | Read everything, download documents, no writes |
| accountant | owner\_grants role accountant | Read charges, payments, tax summaries, ledger exports; no tenant personal data beyond names |
| tenant (phase 4) | lease\_tenants + Socrate sub | Own lease documents and requests only, through the portal scope |
| admin | Socrate realm role | Params, job triggers, grants; no business data outside own ownership |

**Browsers through a Backend-for-Frontend.** A browser never holds an OAuth token (backendkit `docs/CLIENT-INTEGRATION.md` §7.1). tenure is Socrate's confidential client (`SOCRATE_CLIENT_ID`, `SOCRATE_CLIENT_SECRET`) and serves, outside `/api/v1` and without a token:

- `GET /bff/login?return_to=&step_up=1` runs authorization code + PKCE with a state and a browser binding; with step-up it asks for a fresh authentication (Q-85);
- `GET /bff/callback` exchanges the code server-side;
- `GET /bff/session` returns who is signed in and the CSRF token, never a token;
- `POST /bff/logout` needs the CSRF token, revokes the refresh token and deletes the session.

Sessions and pending logins live in PostgreSQL (`bff_store_sessions`, `bff_store_pending_logins`), encrypted under `TENURE_SESSION_KEY`; a session ends after 30 minutes idle and 8 hours at most. The browser holds only the `__Host-tenure_session` cookie (HttpOnly, SameSite=Strict, Secure). On `/api/v1`, a request carrying that cookie and no `Authorization` header is turned into a bearer in process, in front of `internal/auth`: an unsafe method needs `X-CSRF-Token` (403 `bff/csrf-failed`), the access token is refreshed when about to expire, and a refresh Socrate rejects ends the session (401 `auth/session-expired`). `internal/auth` then validates the token as for any bearer, so RLS, roles, step-up and audit are unchanged; machine clients keep sending bearers. Token calls to Socrate carry the resolved client address (client attribution).

A caller with several scopes (own portfolio plus a grant) selects the active scope with the `X-Owner-Scope` header; the default is their own. Every request is logged with subject, scope, route and outcome.

**Imposed, not chosen**: Socrate and backendkit are a platform constraint (RENT-SPEC-006). In code this means: `internal/auth` wraps backendkit and is the only package that reads a token; no handler or service imports a JWT library; there is no `users` table, no password column, no API-key table and no local login route; `owners.socrate_sub` is the only link between a person and their data; machine access (remote job triggers, phase 4 integrations) uses Socrate client-credential clients mapped to the admin or a dedicated integration role; MFA, step-up (`acr`) and session policies are configured in Socrate and consumed by backendkit; backendkit is pinned in `go.mod` and upgraded through the normal PR flow; a CI test fails the build if any route is reachable without a valid Socrate token.

## 5. Scheduler and jobs

Jobs run in-process on a cron schedule, take a lock in `job_runs` keyed on (job, period), iterate owners one by one under their RLS scope, and record a summary; a second start for the same key is a no-op that returns the existing run.

| Job | Schedule (Europe/Paris) | Period key | Does |
| --- | --- | --- | --- |
| billing.monthly | 1st, 06:00 | YYYY-MM | Issues rent calls for active and notice leases (RENT-SPEC-003) |
| billing.reminders | daily, 08:00 | YYYY-MM-DD | Reminder e-mails at due day + 5; arrears flags |
| billing.receipts | hourly | YYYY-MM-DD-HH | Issues receipts for months fully matched since last run, and partial receipts for new partial matches (Q-61) |
| irl.fetch | 15th of Jan/Apr/Jul/Oct, 07:00 | YYYY-Qn | Fetches INSEE IRL; applies due revisions and issues notices |
| charges.reconcile | on demand | `<asset id>/<year>` | Allocates the asset's reconciliation of that exercise year (draft or allocated); issuing stays an owner action (Q-83) |
| tax.annual | 10 Jan, 06:00 | YYYY | Pre-check: fails if form\_2044\_line\_map has no map for the income year; then drafts every owner’s summary for the previous year; owner issues it |
| archive.retention | 1st, 03:00 | YYYY-MM | Starts the retention clock of lease documents of ended leases and of charge supporting documents (retain\_until, object lock extended, never shortened); lists documents past retain\_until for manual review; deletes nothing (Q-95) |
| archive.verify | weekly, Sun 04:00 | YYYY-Www | Re-hashes a random sample (50) of objects against `documents.sha256`; any mismatch fails the run (Q-94) |
| audit.head | daily, 02:00 | YYYY-MM-DD | Writes each owner’s chain head to the locked bucket |
| audit.verify | weekly, Sun 05:00 | YYYY-Www | Recomputes the hash chain per owner and compares with stored heads; mismatch alerts |

All jobs are also triggerable per owner from the API with `dry_run`, which computes and returns without writing. Failures are per owner: one owner’s error is logged and the run continues; the run ends `partial` and the admin is e-mailed.

## 6. Document pipeline

A document is produced in one transaction: number reserved, input snapshot stored, HTML rendered from the versioned template, PDF generated, object written with its hash, metadata row committed; any failure rolls the number back.

- Numbering: per owner and class, `<prefix>-<asset>-<YYYY>-<seq>` with `seq` from a per-(owner, class, series, asset, year) counter table locked in the transaction; prefixes AL (rent call), QL (receipt), CG (owner's notice, RENT-SPEC-010), PR (proposal of a new rent at renewal, Q-120), and class `recovery` in series MD (mise en demeure) and IC (information of the surety) (Q-121), RG (regularisation), RV (revision notice), CT (contract), AV (amendment), DG (deposit), CA (acte de cautionnement, Q-75), RF (tax summary). Class `inventory` numbers in series of their own: EE (entry), ES (exit), EC (entry completion) (RENT-INV-001 §7, Q-111).
- Templates: Go `html/template` files under `templates/<class>/<version>/`, embedded in the release; the latest version of each is the current one; `template_version` saved on the document; a template change never re-renders issued documents. A per-owner override set is deferred: templates are read only at run time (Q-106).
- Rendering: HTML → PDF/A-2b through the engine chosen in OD-03; fonts embedded; A4; a visible document number and hash footer. The engine runs with Chromium and LibreOffice started at boot and its own request limit under the client’s 60 s; a 503 is retried once after a second, and an engine that is down, overloaded or too slow answers `document/pdf-unavailable` (503), never a 500 (Q-101).
- Archive: object key `<owner_id>/<class>/<YYYY>/<number>.pdf`; bucket versioning and object lock (compliance, 10 years); `documents.object_key`, `sha256`, `size_bytes`, `retain_until` written after a successful PUT with checksum verification.
- Immutability: no UPDATE on `billing_documents` or `documents` after `issued_at` (enforced by a trigger); corrections create a new document with `cancels_id`.
- Delivery: e-mail in the recipient’s language (French default, English if set on the owner or tenant) with the PDF attached, sent after the commit; the signed download link for tenants comes with the portal (Q-45); the PDF itself is always French; delivery status stored per document.

## 7. Legal parameters and calculation engine

Every legal constant lives in `legal_params` with an effective date, and every calculation takes the parameter set as of the period it computes, so a 2025 fee recomputed in 2027 gives the 2025 result.

| Key | Example value | Used by |
| --- | --- | --- |
| micro\_foncier\_threshold\_cents | 1500000 | tax |
| deposit\_max\_months.unfurnished / .furnished / .mobility | 1 / 2 / 0 | lease (save, deposit guarantees) |
| deposit\_warning\_months.box | 2 (product rule, Q-26) | lease (warning only) |
| mobility\_term\_months | {min 1, max 10} | lease (save) |
| lease\_term\_months | {unfurnished 36, unfurnished\_legal\_owner 72, furnished 12, furnished\_student 9} (loi 89-462 art. 10, 25-7) | web form (proposed term, Q-100) |
| diagnostics | required kinds by family, validity months by kind (0: none), dpe\_min\_done\_on | lease (generation and activation blockers, Q-73) |
| rental\_min\_dpe\_class | F from 2025-01-01 | lease (a worse class blocks, Q-73) |
| notice\_months.tenant | {unfurnished 3, reduced 1, furnished 1, mobility 1} (loi 89-462 art. 15 I, 25-8, 25-12) | lease (tenant's notice, Q-118) |
| notice\_months.owner | {unfurnished 6, furnished 3} (art. 15 I, 25-8) | lease (owner's notice: lead time before the term) |
| notice.reasons | tenant\_reduced grounds, owner grounds, repossession beneficiaries (art. 15 I) | lease (notice grounds) |
| renewal.reevaluation | {lead\_months 6, min\_references 3, min\_references\_large\_agglomeration 6, steps\_short 3, steps\_long 6, long\_term\_months 72, increase\_threshold\_bp 1000, tense\_zone\_cap\_bp 5000} (art. 17-2, zone tendue decree) | lease (rent re-evaluation at renewal, Q-120) |
| recovery.escalation | {formal\_notice\_after\_days 15, commandement\_after\_days 15, insurance\_claim\_after\_days 30, surety\_notice\_after\_months 1, surety\_commandement\_days 15, resolution\_weeks 6, former\_resolution\_months 2, resolution\_weeks\_from 2023-07-29} (loi 89-462 art. 24, loi 2023-668, code civil art. 2303; the first three are defaults) | billing (recovery of unpaid rent, Q-121) |
| recovery.plan | {grace\_days 5, max\_instalments 36} (defaults; the judge may grant up to three years, loi 89-462 art. 24 V) | billing (payment plans, Q-122) |
| bank.reconciliation | {unmatched\_alert\_days 10} (RENT-SPEC-013, a rule of the product) | payment (bank lines flagged stale, Q-123) |
| bank.matching | {window\_days 10, allowance\_keywords [CAF, APL, ALS, ALF, MSA], min\_name\_length 3} (rules of the product) | payment (credits matched to rent, Q-124) |
| bank.charges | {window\_days 45} (a rule of the product) | charge (debits matched to charges, Q-125) |
| regularisation\_spread\_after\_months | 12 | charge |
| regularisation\_supporting\_docs\_months | 6 (loi 89-462 art. 23) | charge (statement) |
| reel\_option\_lock\_years | 3 (CGI art. 32-4) | tax (summary) |
| irl\_cap.applies / irl\_cap.rate | per decree, dated | billing |
| receipt\_due\_days\_before\_reminder | 5 | billing |
| archive\_retention\_years | 10 | document (retention and object lock, Q-54) |
| form\_2044\_line\_map | by income year (tax\_year), millésime as label: category → line | tax; tax.Lines and tax.annual fail when the income year has no map |
| micro\_foncier\_allowance\_pct | 30 | tax |
| forfait\_222\_cents | 2000 (per local: per lease of unfurnished assets that produced rent) | tax |
| deficit\_global\_cap\_cents | 1070000 | tax |
| deficit\_carryforward\_years | 10 | tax |
| rent\_control.zone\_ceiling | loyer de référence majoré per m² by zone and category, dated; only for assets with controlled\_zone | billing (revision cap), lease (creation, renewal) |
| rent\_control.relocation\_rule | dated; whether a new lease on a controlled-zone asset is capped at the previous rent revised by IRL | lease (creation) |

Calculations are pure functions in each module (`billing.ComputeFee(lease, period, params)`, `charge.Allocate(...)`, `tax.Lines(...)`) with no I/O, which is what the golden-file tests in section 10 exercise. Rounding: fees to the cent, half-up; provisions proposed to the euro, up; IRL revision to the cent.

## 8. Errors, validation and observability

Validation happens once, in the service layer, and every refusal names a business rule the front-end can show verbatim.

- Handlers decode and type-check only; services validate invariants and return typed errors (`ErrInvariant{Type, Detail}`) mapped to 409/422 problem details; repositories return storage errors only.
- Structured logs (slog, JSON) with request id, subject, scope, route, duration; job logs with run id and owner; no personal data in logs beyond ids.
- Metrics (Prometheus endpoint, internal network only): request latency per route, job duration and status, documents generated, PDF render time, e-mail delivery outcomes, unmatched payments count.
- Alerts: job `failed` or `partial`, archive.verify mismatch, PDF engine down, disk or bucket quota at 80 %.
- Health: `/healthz` (process, with the version only), `/readyz` (database, bucket, Socrate JWKS reachable).
- Build: `GET /api/v1/version`, for any signed-in user, gives the API's version, commit, build time, whether it is a release build (stamped by `scripts/build.sh`), the Go toolchain, the platform, backendkit, the last embedded migration and the tenure-web release pinned in `web/VERSION`. `tenure version` prints the same facts, its first line `<version> <commit> <build time>` as the release workflow reads it (#118). An unstamped build falls back to Go's VCS stamp (#113).

## 8b. Audit trail and security controls (RENT-SPEC-022)

Every service write emits one audit event in the same transaction through `audit.Record(ctx, action, entity, before, after)`, and the HTTP and job layers enforce the ASVS L2 controls below from the first release.

- Audit emission: the service layer calls `audit.Record` after the write and before commit; the repository forbids direct writes from handlers; a missing audit call fails the integration test suite (every mutating endpoint is listed in `testdata/audited_routes.txt`).
- Chain: `hash = sha256(prev_hash ‖ canonical JSON)` computed in Go, not in SQL, with an advisory lock per owner to serialise inserts; `audit.verify` job weekly, `audit.head` job daily writes `audit_chain_heads` to `<owner_id>/audit/<date>.json` under object lock.
- Read access to audit events: owner and accountant through `GET /audit/events?entity=&from=&to=`; export as CSV with the chain hashes.
- Browser sessions: the BFF of §4; no token in the browser, `__Host-` HttpOnly SameSite=Strict cookie, CSRF token on unsafe methods, encrypted server-side sessions (Q-86).
- Authentication: backendkit validates tokens and exposes `auth_time` and `amr`; routes tagged `step_up` (tax issue, grants, params, exports) require `auth_time` at most 5 minutes old and `mfa` in `amr`, otherwise 401 with `type: auth/step-up-required`; a token without `auth_time` is refused on those routes; MFA enforced by Socrate policy for owner and admin realms.
- Headers: HSTS (backendkit's 2 years, Caddy adds 1 year only where missing); CSP set by tenure per surface: `default-src 'none'` on the API, `default-src 'self'` without inline script on the embedded web app, its `index.html` allowing the styles that carry a per-response nonce (Q-96), `X-Content-Type-Options`, `Referrer-Policy`, frame denial; API refuses non-JSON content types.
- Rate limiting: Chi middleware, 60 write requests/min per subject, 600 reads/min, 10 failed auth/15 min per IP before lockout; limits logged and alerted.
- Uploads: `POST /documents` streams to a quarantine prefix, ClamAV scan (sidecar) then move to the archive prefix; MIME sniffed server-side; max 25 MB; infected files rejected and logged. The quarantine prefix `quarantine/<owner_id>/` holds objects without retention (the bucket has no default retention rule) and every quarantined object is deleted after the scan; the scan runs before the type check so that every infected file is logged (warning log and `document.reject` audit event); no scanner answer refuses the upload with 503 (fail closed); accepted types are PDF, JPEG, PNG, and CSV or OFX by extension (Q-67).
- CSV exports (journal, balances, tax summary, audit events, dashboard, archive index): a text cell starting with `=`, `+`, `-`, `@`, a tab or a carriage return gets a leading apostrophe, so a spreadsheet never runs it as a formula; integers are left as they are; the FEC keeps its fixed format (`internal/csvx`).
- Signed URLs: 10 minutes, bound to the document id and caller subject in the signature (issued only to a caller who may read the document, Q-50); every issue recorded as an audit event `document.download`.
- Encryption: PostgreSQL on an encrypted volume, bucket SSE, backups encrypted on the host with age to a public key whose identity is kept offline, in a bucket with SSE (Q-97); TLS 1.2+ only; database connections over TLS.
- Roles: `tenure_api` (no BYPASSRLS, no DDL), `tenure_migrate` (DDL, used only by `tenure migrate`), `tenure_jobs` (sets `app.owner_id` per owner, no DDL), and `tenure_rls` (NOLOGIN, BYPASSRLS, owns only the SECURITY DEFINER functions the policies call, Q-41); credentials separate; break-glass superuser access only through the provider console, logged and alerted.
- Supply chain: `go.sum` and `package-lock.json` pinned, `govulncheck` and `npm audit` in CI, gitleaks on every PR in both repositories (secret scanning and push protection on private personal repositories need GitHub Advanced Security, so gitleaks is the day-1 control), Dependabot alerts and security updates enabled, SBOM (CycloneDX) attached to each release, container image scanned.
- RGPD: `GET /me/export` (owner) and tenant data export on request; erasure job after retention; processing register kept in `docs/rgpd/`; breach runbook in `deploy/runbooks/`.
- Pen test: external test scheduled before the phase 4 portal; phase 1 to 3 covered by the AI security audit per PR and an annual internal review.

## 9. Repository, configuration, CI and release

Two repositories: github.com/ovander/tenure-api (Go module github.com/ovander/tenure-api: service, scheduler, templates, migrations, deploy) and github.com/ovander/tenure-web (the Vue app). tenure-web publishes its built bundle as a release artefact; tenure-api pins that release and the SHA-256 of its `tenure-web-<tag>.tar.gz` asset in web/VERSION, downloads and verifies it at release build (`scripts/fetch-web.sh`) and embeds it, serving it outside `/api` and `/bff` with an SPA fallback (Q-96), so one tenure-api release still ships both and is stamped with `-ldflags` at build.

| Path | Holds |
| --- | --- |
| `cmd/tenure` | main: serve, migrate, job subcommands |
| `internal/<module>` | handler, service, repository per module |
| `internal/httpapi` | Chi routers, middleware, swag annotations |
| `migrations` | `000001_init.sql` … |
| `templates` | Document HTML templates by class and version |
| `web` | Built bundle of tenure-web at the pinned release (RENT-FE-001), embedded into `tenure` as embedded static files |
| `testdata/golden` | Golden files per calculation and tax year |
| `deploy` | Caddyfile, systemd units, sidecars compose file, deploy script, backup and restore scripts, env templates (Q-98) |
| docs/rgpd, deploy/runbooks | Processing register and privacy notice; breach, restore and break-glass runbooks |

Configuration by environment variables only (`TENURE_DB_URL`, `TENURE_S3_*`, `TENURE_SMTP_*`, `TENURE_PDF_ENGINE_URL`, and the Socrate client under Socrate's own names `SOCRATE_ISSUER`, `SOCRATE_JWKS_URL`, `SOCRATE_CLIENT_ID`, `SOCRATE_CLIENT_SECRET`, Q-99), validated at start; secrets never in the repo. Workflow, identical in both repositories: GitHub issue → branch → PR with a CI status check named `ci` (tenure-api: go vet, staticcheck, golangci-lint, govulncheck, gitleaks, unit and golden tests, migration up/down; tenure-web: vue-tsc, eslint, npm audit, gitleaks, Vitest, Playwright smoke) → AI security audit on the PR → tag `vX.Y.Z` → release (tenure-web: built bundle as release asset; tenure-api: binary embedding the pinned bundle, plus deploy script); database migrated before the binary swaps; rollback = previous binary plus the migration’s down file. Branch `main` protected in both: PR required, `ci` check required and up to date, admins included, no force push or deletion.

## 10. Testing strategy

Legal calculations are tested against golden files per tax year, so a parameter change for 2027 can never alter a 2025 result unnoticed.

- Unit: pure calculation functions with table tests; state machines (lease, reconciliation) exhaustively.
- Golden: `testdata/golden/<year>/<case>.json` holding lease, charges, payments, params and the expected fee lines, reconciliation lines and 2044 lines; one case per real asset shape (house, apartment + box, furnished studio) and per edge (prorated first month, mid-year revision, late regularisation, co-ownership split).
- Integration: PostgreSQL in CI (testcontainers), RLS verified by a cross-owner test that must read zero rows; document pipeline end to end against MinIO; job idempotence (run twice, one document).
- End to end: the phase 1 flows of RENT-FE-001 §4 in Playwright, against tenure built with the pinned web bundle and the services of docker-compose.dev.yml. Socrate, SMTP and the INSEE series are test fakes built with the `e2e` tag (`e2e/fakes`), never part of the binary and on the loopback only. `e2e/run.sh` runs them in the `ci` job (Q-104).
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
| 2026-11-01 | 1.1 | Implementation feedback: tenure\_\* DB roles, step-up on auth\_time + amr, owner\_grants in 000001, four tax parameters and rent-control zone rules in legal\_params; assets.controlled\_zone, 2044 map by millésime and tax.annual pre-check (Q-07, Q-12) |
| 2026-11-01 | 1.2 | Implementation feedback (Q-27 to Q-33): rent\_control.zone\_ceiling and relocation\_rule replace zone\_rules; assets.rent\_control\_zone/category; leases.rent\_exemption and rent\_breakdown; 2044 map and tax parameters by income year; 222 per local |
| 2026-11-01 | 1.3 | Slice 2 (Q-35 to Q-44): `PUT /me`, platform audit chain, `tenure_rls` role, `version` on every mutable table |
| 2026-11-01 | 1.4 | Slice 3 (Q-45 to Q-59): billing\_documents generations, document\_counters, document\_deliveries, object\_version, archive\_retention\_years, cancel and reissue, IRL manual override, e-mail without link |
| 2026-11-01 | 1.5 | Slice 4 (Q-60 to Q-66): matching rules, partial receipts, billing\_reminders, cancel after payment, payment edits |
| 2026-11-13 | 2.34 | The dashboard (RENT-SPEC-016 part 16a, #161, Q-129): module `dashboard`, the pure `dashboard.Build`, `GET /dashboard`, `GET /dashboard/assets.csv`; migration 000026 (parameter `dashboard.aging`); journal entries carry their asset; `lease.VacanciesInTx`, `billing.ArrearsInTx`, `portfolio.AllAssetsInTx`, `tax.EstimateInTx` |
| 2026-11-13 | 2.35 | Maintenance and incidents (RENT-SPEC-015 part 17a, #164, Q-130): module `maintenance`, contractors and incidents with their state machine, routes `/contractors` and `/incidents`; migration 000027 (tables `contractors`, `incidents`, parameter `ledger.capitalised`); `charge.CreateInTx`; journal entries of kind `improvement`, `ledger.Chart` |
| 2026-11-14 | 2.36 | Maintenance contracts (RENT-SPEC-015 part 17b, #166, Q-131): routes `/maintenance-contracts`, `maintenance.NextDue`; migration 000028 (tables `maintenance_contracts`, `maintenance_visits`, parameter `maintenance.recurring`); calendar items `maintenance_due` with `contract_id` and `responsible` |
| 2026-11-14 | 2.37 | Improvement works of a dwelling are deductible (RENT-SPEC-015, #170, Q-130 resolved): treatment `construction`, `maintenance.Deductible`; migration 000029 (parameters `tax.improvement_deductible`, `ledger.capitalised_construction`); journal role `capitalised_construction` |
| 2026-11-14 | 2.38 | Chimney sweeping by fuel (RENT-SPEC-015, #172, Q-131 resolved): `fuel` on a maintenance contract, required for a chimney; migration 000030 (parameter `maintenance.chimney`) |
| 2026-11-14 | 2.39 | A box let in the same lease as a dwelling follows its regime (RENT-SPEC-015, #178, Q-130): `maintenance.Deductible` reads the lease's other assets; Q-132 plans part 18 (LMNP réel) with its default hypotheses |
| 2026-11-14 | 2.40 | Depreciation for furnished letting (RENT-SPEC-019 part 18b, #180, Q-132): module `bic`, `bic.Depreciation`, `bic.Capitalise`; migration 000031 (tables `bic_grids`, `bic_fixed_assets`, parameters `bic.components`, `bic.furniture`, `bic.small_equipment`, `bic.acquisition_costs`); `httpx.IfMatchOrZero` |
| 2026-11-15 | 2.41 | The BIC result (RENT-SPEC-019 part 18c, #182, Q-132): `bic.Results`, `GET /bic/result`; migration 000032 (parameter `bic.micro`); `ledger.Service.InputsInTx` |
| 2026-11-15 | 2.42 | The BIC declaration (RENT-SPEC-019 part 18d, #184, Q-132): `POST /bic/declarations` (step_up), `GET /bic/declarations`; `bic.Results` takes declared years, the regime and deficits; migration 000033 (table `bic_declarations`, `bic_grids.frozen_at` updatable once, parameter `bic.deficit_years`); problem types `bic/grid-frozen`, `bic/declared`, `bic/declaration-order`, `bic/micro-closed` |
| 2026-11-15 | 2.43 | Q-133 plans part 19 (RENT-SPEC-019, #188): the SCI at the income tax as an owner scope, the `manager` role, associates and dated holdings, the 2072 split on 31 December, the associate's 2044; no code |
| 2026-11-15 | 2.44 | The SCI as an owner scope (RENT-SPEC-019 part 19b, #190, Q-133): `owners.kind`, `POST /entities`, `GET/PUT /entity`, grant role `manager`; module `sci`, its register `/associates` and `/sci/movements`; migration 000034 (tables `sci_associates`, `sci_movements`, `sci_holdings`; `tenure_scope_role` answers owner for a manager); problem types `entity/is-unsupported`, `entity/not-sci`, `grant/manager-sci-only`, `grant/last-manager`, `grant/self-revoke`, `tax/person-only`, `sci/*` |
| 2026-11-15 | 2.45 | The 2072 (RENT-SPEC-019 part 19c, #192, Q-133): `sci.Allocate`, `GET /sci/result`, `GET/POST /sci/declarations` (step_up); migration 000035 (tables `sci_declarations`, `sci_allocations`); problem types `sci/year-open`, `sci/declared`, `sci/year-declared`, `sci/furnished-receipts`, `sci/register-incomplete` |
| 2026-11-15 | 2.46 | An associate's SCI shares (RENT-SPEC-019 part 19d, #194, Q-133): `tax.WithShares`, `OwnerSummary.sci_shares`, flag `sci_pending`, `GET /sci/shares`; migration 000036 (`sci_allocations.sci_name`, `tenure_scope_person`, the associate's read policies) |
| 2026-11-15 | 2.47 | Loans for SCI parts and the consolidation per person (RENT-SPEC-019 part 19e, #196, Q-133): loan `kind` (asset, parts) and `sci_name`, `tax.PartsLoan`, `OwnerSummary.parts_loans`; the dashboard's estimate lists the SCI shares, the loans for parts, `sci_pending` and the BIC result (`bic.ResultInTx`), none in an SCI's scope (`entity/sci`); migration 000037 (problem type `loan/parts-person-only`) |
| 2026-11-17 | 2.48 | Legal parameters corrected by the tax review (#203, Q-134): `bic.micro` from 2026 (receipts threshold 83 600 €), `ledger.chart` from 2025 (penalties on 658200, the PCG 2025 reform); migration 000038 |
| 2026-11-17 | 2.49 | The 2044 lines of an associate's SCI shares (#204, Q-135): parameter `form_2044_sci_line_map`, `sci_lines` in the tax summary (section 110, lines 111 to 114), CSV rows `sci`; migration 000039 |
| 2026-11-20 | 2.50 | CSV exports neutralise spreadsheet formulas (#211, threat model A-2): `internal/csvx` |
| 2026-11-20 | 2.51 | gotenberg's Chromium reaches no URL but the conversion's files and runs no script (#212, threat model A-3): `--chromium-deny-list`, `--chromium-disable-javascript` |
| 2026-11-26 | 2.55 | `audit.verify` reads every version of every head object back from the bucket and checks it against the chain (#224): a chain rewritten consistently in the database no longer passes; `document.Store.Versions` |
| 2026-11-12 | 2.33 | Year-end exports (RENT-SPEC-014 part 15c, #158, Q-128): the pure `ledger.CSV`, `ledger.BalancesCSV`, `ledger.FEC` (LPF art. A47 A-1, closed years only, problem `ledger/year-open`), `ledger.Pieces` and `ledger.Pack`; `GET /ledger/journal.csv`, `/ledger/fec.txt`, `/ledger/pack.zip`, step-up, audited `ledger.export` |
| 2026-11-12 | 2.32 | The owner's journal and the period lock (RENT-SPEC-014 part 15b, #156, Q-127): module `ledger`, the pure `ledger.Journal`, `GET /ledger`, parameter `ledger.chart`; migration 000025 adds the triggers that refuse a write into a year whose tax summary is issued (SQLSTATE TL001, problem `ledger/period-locked`, mapped by `internal/db`) |
| 2026-11-11 | 2.31 | Loans (RENT-SPEC-014 part 15a, #154, Q-126): module `loan`, migration 000024 (`loans`, `loan_instalments`), the pure `loan.Schedule` and `loan.Check`, `GET/POST /loans`, `GET/PUT /loans/{id}`, `GET /loans/schedule`; the pure `tax.WithLoans` puts the instalments due in the year on line 250 and sets aside the loan charges typed by hand (flag `superseded_by_loan`) |
| 2026-11-10 | 2.30 | Bank debits matched to charges (RENT-SPEC-013 part 14c, #149, Q-125): migration 000023 (`charges.bank_line_id`, parameter `bank.charges`), the pure `charge.SuggestCharges`, `GET /bank-lines/{id}/charge-suggestions`, `POST /bank-lines/{id}/charges` |
| 2026-11-10 | 2.29 | Bank credits matched to rent (RENT-SPEC-013 part 14b, #147, Q-124): migration 000022 (parameters `bank.matching`), the pure `payment.Suggest` and `payment.Sure`, suggestions, a line split across leases as payments, the sure lines matched automatically |
| 2026-11-10 | 2.28 | Bank import (RENT-SPEC-013 part 14a, #145, Q-123): migration 000021 (`bank_accounts`, `bank_lines`, parameter `bank.reconciliation`), the pure `payment.ParseCSV` and `payment.ParseOFX`, `POST /payments/import`, `/bank-accounts`, `/bank-lines`; payment depends on document and params; `money.Parse` shared with the syndic import |
| 2026-11-09 | 2.27 | Payment plans (RENT-SPEC-011 part 13b, #142, Q-122): migration 000020 (`payment_plans`, parameters `recovery.plan`), the pure `billing.NewPlan` and `billing.PlanStatus`, a respected plan suspends `billing.Recovery`, `POST /leases/{id}/recovery/plans` and `/cancel` |
| 2026-11-09 | 2.26 | Recovery of unpaid rent (RENT-SPEC-011 part 13a, #140, Q-121): migration 000019 (`recovery_steps`, class `recovery` in series MD and IC, parameters `recovery.escalation`), the pure `billing.Recovery`, `GET /leases/{id}/recovery`, the MD and IC letters, the steps recorded |
| 2026-11-08 | 2.25 | Rent re-evaluation at renewal (RENT-SPEC-010 part 12d, #133, Q-120): migration 000018 (`lease_renewals`, class `renewal` numbered PR, lease document kind `renewal_proposal`, parameters `renewal.reevaluation`), the pure `lease.Reevaluate`, the PR letter, its reception, the tenant's answer and the due steps added to the rent |
| 2026-11-08 | 2.24 | Lease terms and vacancy, computed (RENT-SPEC-010 part 12c, #130, Q-119): `GET /leases/{id}/terms` (the first term, the tacit renewals, the term an end cuts short, the next renewal) and `GET /assets/{id}/vacancies` (periods without a lease, vacant days by year); no migration |
| 2026-11-07 | 2.23 | Lease notices (RENT-SPEC-010 part 12a, #127, Q-118): migration 000017 (`lease_notices`, class `notice`, lease document kind `owner_notice`, notice parameters), `GET/POST /leases/{id}/notice`, `POST /leases/{id}/notice/letter`, the CG letter; the notice transition with a typed end date stays for a box lease |
| 2026-11-07 | 2.22 | `document.View` carries `number`, read from the archive key of a document issued from a template, so the archive browser shows it (#122) |
| 2026-11-07 | 2.21 | `tenure version` starts again with `<version> <commit> <build time>`, which the release workflow checks; release v0.1.21 failed on it (#118) |
| 2026-11-07 | 2.20 | Module `calendar` and `GET /calendar`: the obligations calendar computed from diagnostics, leases, inventories and deposits (RENT-SPEC-012 base, RENT-INV-001 slice 10, Q-116) |
| 2026-11-07 | 2.19 | `GET /api/v1/version` and a fuller `tenure version`: build, toolchain, schema and pinned web bundle (#113) |
| 2026-11-06 | 2.18 | Deposit settlement: wear grids, deductions, `RD` statements with the holdback release, migration 000016 (class `restitution`, `deposit_settlements`), `assets.collective_building` in the asset API, billing's arrears handed to the inventory module (RENT-INV-001 slice 9, Q-115) |
| 2026-11-06 | 2.17 | Entry completions: windows, request and decision routes, `EC` series with template `inventory_completion/v1`, migration 000015, lease warning `inventory/completion-pending` (RENT-INV-001 slice 7, Q-114) |
| 2026-11-06 | 2.16 | Exit inventory pre-filled from the entry, comparison route, `ES` series, template `inventory/v3` (RENT-INV-001 slice 8, Q-113) |
| 2026-11-05 | 2.15 | Inventory signatures: wet-signed scan, Yousign behind `TENURE_YOUSIGN_API_URL`/`_KEY` (pulled, no webhook), migration 000014, `internal/esign` (RENT-INV-001 slice 6, Q-112) |
| 2026-11-05 | 2.14 | Entry inventory capture routes and transitions, `EE` document series, module `inventory` in §1, migration 000013 (RENT-INV-001 slice 5, Q-111) |
| 2026-11-05 | 2.13 | Inventory photo routes and pipeline; web CSP `img-src` adds `blob:` and the archive origin (RENT-INV-001 slice 4, INV-OD-12, Q-110) |
| 2026-11-05 | 2.12 | Asset layout routes and `asset_layouts` (migration 000012), RENT-INV-001 slice 3 (Q-109) |
| 2026-11-05 | 2.11 | Migration 000011: the inventory tables of RENT-INV-001 §3, RLS, issued inventories immutable, `legal_params` restitution and window keys; `items` retired (Q-108) |
| 2026-11-05 | 2.10 | `GET /templates`: the embedded templates and their versions, read only; per-owner override deferred (Q-106) |
| 2026-11-04 | 2.9 | Lease blockers carry `asset_id`, `diagnostic` and `valid_until` (Q-105) |
| 2026-11-04 | 2.8 | §10 end to end: the five phase 1 flows in a browser against a real tenure in CI, with test fakes for Socrate, SMTP and INSEE (Q-104) |
| 2026-11-03 | 2.6 | assets.tense\_zone (migration 000010): zone tendue separated from rent-ceiling zones; relocation rule and previous rent on tense assets, contract template v2 (Q-102) |
| 2026-11-03 | 2.5 | §6 rendering: gotenberg flags, one retry on 503, engine down or slow → `document/pdf-unavailable` 503 (Q-101) |
| 2026-11-03 | 2.7 | §6 rendering: gotenberg start timeouts raised so a slow boot does not exit; rendering routes document their 503 (Q-103) |
| 2026-11-03 | 2.4 | lease\_term\_months legal parameter for the term the web form proposes (migration 000009, Q-100) |
| 2026-11-03 | 2.3 | Socrate client settings read under Socrate's SOCRATE_* names, audience defaulting to the client id; former TENURE_SOCRATE_* names refused (Q-99) |
| 2026-11-01 | 2.2 | Per-response style nonce in the web app's index.html and CSP (Q-96, tenure-web Q-W1) |
| 2026-11-01 | 2.1 | Slice 8b (Q-96 to Q-98): web/VERSION with checksum and embedded bundle, CSP per surface, deploy/ (Caddyfile, systemd, sidecars, deploy.sh), age-encrypted backups and tenure\_backup role, runbooks, docs/rgpd, release workflow with CycloneDX SBOM, ASVS L2 checklist |
| 2026-11-01 | 2.0 | Slice 8a (Q-91 to Q-95): export\_jobs (migration 000008) and `GET /documents/exports/{id}`, grants routes, `GET /me/export`, number filter on `GET /documents`, archive.retention starts the retention clock, archive.verify sample |
| 2026-11-01 | 1.9 | Slice 7 (Q-87 to Q-90): tax\_summaries draft/issued with deficits\_after (migration 000007), step\_up on POST and lines.csv, CSV archived beside the RF PDF, reel\_option\_lock\_years, tax.annual pre-check |
| 2026-11-01 | 1.8 | BFF (Q-85, Q-86): browser login through backendkit bff in tenure, /bff routes, session tables (migration 000006), TENURE\_SOCRATE\_CLIENT\_ID/SECRET, TENURE\_PUBLIC\_URL, TENURE\_SESSION\_KEY |
| 2026-11-01 | 1.7 | Slice 6 (Q-77 to Q-84): charges in cents with the syndic split and draft status, reconciliation lines per lease, regularisation\_postings, charges.reconcile period key, supporting-documents parameter |
| 2026-11-01 | 1.6 | Slice 5 (Q-67 to Q-76): upload pipeline, asset\_diagnostics, lease\_documents, inventories, guarantee rules, contract blockers and routes, CA prefix, diagnostics and DPE class parameters, retention clock of lease documents |
