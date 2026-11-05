# Tenure — Inventory specification (RENT-INV-001)

Nov 5, 2026 · @Olivier

## 1. Scope and corpus

Every lease in Tenure carries an entry inventory and an exit inventory (état des lieux d'entrée / de sortie) that meet décret 2016-382. Phase 1 stores the signed PDF. Phase 2 builds the inventory in Tenure: the exit is pre-filled from the entry, and the differences drive the deposit settlement.

**Covers**: house, apartment (unfurnished or furnished, with the furniture list of décret 2015-981) and box, whether leased alone or attached to a dwelling lease. It also covers meters, keys and access devices, photos per item, signatures, the tenant's completion request, the vétusté grid, and the deposit deductions computed from the exit comparison.

**Out of scope**: commercial premises; native e-signature (OD-08: external eIDAS provider, never native); the tenant filling in the inventory alone (the tenant portal, phase 4, only reads and requests); quotes and works orders (RENT-SPEC-015).

| Derived from | What this spec details |
| --- | --- |
| RENT-SPEC-002 | Supporting entity Inventory (entry/exit, attached to a lease) |
| RENT-SPEC-005 | Inventories and photos archived write-once; retention clock starts at lease end |
| RENT-SPEC-008 | Activation blocked until the entry inventory exists (`lease/entry-inventory-missing`); the deposit settlement uses the exit inventory |
| RENT-SPEC-009 | Template per asset, condition scale, photos per item, exit pre-filled, differences flagged |
| RENT-SPEC-010 / 012 | Exit scheduled from the notice; deadlines on the obligations calendar once it exists (section 9) |
| RENT-SPEC-022 | Every change audited; uploads scanned |
| RENT-BE-001 2.10 | The `inventories` table, `POST /leases/{id}/inventories` (already accepts `items` JSON), the `lease` module, document prefixes |
| RENT-FE-001 1.2 | The lease hub tab `/leases/{id}/inventories`, screen S15 « Candidate file and inventories », the CSP |

A rule stated here that contradicts a RENT-SPEC section is a defect of this spec; the high-level corpus wins until it is amended.

## 2. Legal rules mapped to system rules

French law fixes what an inventory must contain, not its layout. Tenure enforces each mandatory mention as a validation that blocks issuing.

| Source | Rule | Tenure behaviour |
| --- | --- | --- |
| Loi 89-462 art. 3-2 | Inventory drawn up jointly and free of charge at handover and return of keys, dwelling let empty or furnished as a principal residence | One entry and one exit per lease; no fee line can be billed for an amicable inventory |
| Loi 89-462 art. 3-2 | If no joint inventory can be made, a commissaire de justice draws it up, cost shared half each at a regulated rate | Mode `bailiff`: the commissaire's report is uploaded as the inventory; half the invoice can be billed to the tenant as a separate line |
| Décret 2016-382 art. 1–3 | Mandatory: type (entry/exit), date, location of the dwelling, names of the parties, landlord's domicile or registered office, detail and destination of keys and access devices, per room and part the state of floor, wall and ceiling coverings, equipment and fixtures, signatures | Blocking checks on `issue`: every room has its three coverings assessed, at least one key line, every party signed |
| Décret 2016-382 | Optional: representatives' names and addresses, individual meter readings, observations, reservations, images | Captured when present; meter readings strongly prompted (they feed RENT-SPEC-007) |
| Décret 2016-382 | Exit only: the tenant's new address or accommodation, the date of the entry inventory, the changes since entry | Exit cannot be issued without `tenant_new_address`; entry date and comparison printed automatically |
| Décret 2016-382 | Entry and exit share a similar layout so they can be compared; paper or electronic; one copy per party, by hand or electronically | One template renders both; the exit PDF prints the entry and exit states side by side; issued PDF e-mailed to each party |
| Décret 2016-382 art. 4 | Vétusté = wear from time or normal use. The parties may agree on a grid (collective-agreement grid) giving a theoretical lifetime and a yearly flat reduction per material | Optional grid attached to the lease as an annex (RENT-SPEC-008); used by the deduction calculation (section 6) |
| Loi 89-462 art. 3-2 | The tenant may ask within 10 days to complete the entry inventory; the heating items within the first month of the heating season | Entry completion request with a dated window; accepted lines become an `EC` completion document, the issued entry is never edited |
| Loi 89-462 art. 7 | The tenant is not liable for vétusté, defects, construction faults, accidents or force majeure | A worsened item is flagged, not charged; the owner qualifies it before any deduction |
| Loi 89-462 art. 22 | Deposit returned within 1 month of the return of the keys if the exit inventory conforms to the entry, 2 months otherwise | The clock starts at `keys_returned_on`, not at `done_on`. Tenure applies a stricter rule by choice: 1 month unless a deduction is retained (INV-OD-13) |
| Loi 89-462 art. 22 | Deductions justified, no flat amount; in a collective building (immeuble collectif), up to 20 % held back until the annual accounts, the balance due within the month after their approval | Each deduction needs a quote, invoice or grid line; holdback allowed when `assets.collective_building` is set; the balance deadline is dated from the approval of the accounts |
| Loi 89-462 art. 22 | 10 % of the monthly rent per started month of delay, not due if the tenant gave no new address | Penalty computed and shown from the deadline |
| Décret 2015-981 | A furnished dwelling contains at least 11 items of furniture | Furnished template seeds the list; a missing item at entry warns (lease classification risk) |
| Code civil art. 1730–1731 | Box or garage leased alone falls outside loi 89-462. With an inventory, the premises are returned as described; without one, presumed received in good repair | Not required by law, but still required by Tenure before activation (RENT-SPEC-008); same engine, box template |

Legal constants (deadlines, rates, holdback, windows) go in `legal_params` with an effective date (section 7), never in code.

## 3. Data model

The current row `inventories (id, lease_id, kind, done_on, items jsonb, document_id)` of RENT-BE-001 becomes a header. Rooms, items, meters, keys, photos and signatures move to their own tables, so each exit item can point to its entry item and each deduction to the item it charges (INV-OD-01). `items` is already writable today through `POST /leases/{id}/inventories` (`internal/lease/contract.go`), so existing rows are migrated, not assumed empty (section 9).

| Table | Key columns | Notes |
| --- | --- | --- |
| asset\_rooms | id, owner\_id, asset\_id, position, kind, label, surface\_m2 | The asset's layout, kept between leases. Kinds: entry, living, kitchen, bedroom, bathroom, wc, storage, outside, garage, box, other |
| asset\_elements | id, owner\_id, room\_id, position, category, label, installed\_on | Category: floor, walls, ceiling, joinery, electrical, plumbing, heating, appliance, furniture, other. `installed_on` feeds the vétusté age |
| inventory\_templates | id, owner\_id (null = system), asset\_kind, version, structure jsonb, source | Seeds a new asset layout (section 4); the structure carries the furniture a furnished dwelling adds, so one row serves both; system rows are seeded from `testdata/inventory/templates.json` and read-only |
| inventories | id, owner\_id, lease\_id, kind (entry/exit), mode (amicable/bailiff/upload), state, scheduled\_at, done\_on (the day of the inventory, a date, nullable until completed), keys\_returned\_on, place, entry\_inventory\_id, tenant\_new\_address, observations, legacy\_items jsonb, template\_version, document\_id, version | `entry_inventory_id` is set on an exit. `keys_returned_on` starts the deposit clock (exit). `mode upload` = the phase 1 path, no child rows. `legacy_items` = the former `items`, kept read-only |
| inventory\_items | id, owner\_id, inventory\_id, room\_id, element\_id, label, condition, works, quantity, comment, entry\_item\_id | `label` is copied from the element when the item is created, so later layout edits never change an issued inventory. `works`: ok / ko / n/a |
| inventory\_meters | id, owner\_id, inventory\_id, kind, meter\_number, reading, unit, photo\_id | Kinds: elec\_peak, elec\_offpeak, elec\_base, gas, water\_cold, water\_hot, heating\_energy |
| inventory\_keys | id, owner\_id, inventory\_id, kind, destination, quantity, comment | Kind: key, badge, remote, card, code. Destination is free text (« porte d'entrée », « boîte aux lettres », « portail box ») |
| inventory\_photos | id, owner\_id, inventory\_id, item\_id, meter\_id, object\_key, sha256, original\_sha256, taken\_at, width, height | `sha256` = the cleaned file actually archived; `original_sha256` = the upload, kept for the audit event only |
| inventory\_signatures | id, owner\_id, inventory\_id, party, signer\_name, method, signed\_at, reservations, evidence\_document\_id | Party: owner, tenant (one row per co-tenant), representative. Method: esign (OD-08 provider), wet\_scan |
| inventory\_completions | id, owner\_id, inventory\_id, reason (ten\_day/heating), requested\_on, lines jsonb, decision, decided\_on, document\_id | Completes an issued entry; the decision and its `EC` PDF are archived. Named apart from lease amendments (`AV`) |
| vetuste\_grids | id, owner\_id, name, source, lines jsonb | Line: category, lifetime\_years, franchise\_years, yearly\_rate\_bp, floor\_bp |
| deposit\_deductions | id, owner\_id, lease\_id, exit\_item\_id, description, cost\_cents, tenant\_share\_bp, amount\_cents, basis (quote/invoice), document\_id (required: a grid sets the share, never the cost), charge\_id | Feeds the `RD` restitution statement (RENT-SPEC-008) |

All rates and shares are integers in basis points (10 000 bp = 100 %), like the ownership shares; no float touches money (CLAUDE.md).

Added columns: `leases.vetuste_grid_id` (nullable), so the grid is a lease annex; `assets.collective_building` (boolean), which allows the 20 % holdback.

All tables follow the RENT-BE-001 conventions: owner\_id with the same RLS policy, `*_by` / `*_at` audit columns, UUIDv7 ids, integer cents, PostgreSQL enums. Once `inventories.state = issued`, a trigger refuses UPDATE or DELETE on that row and its children, as for `billing_documents`.

Photo objects: `<owner_id>/inventory/<lease_id>/<inventory_id>/<photo_id>.jpg`, bucket object lock, retained until lease end + 10 years (RENT-SPEC-005).

Photo pipeline (new: today's upload pipeline does not touch EXIF): quarantine → ClamAV → decode → strip all EXIF and location metadata, re-encode JPEG → hash the cleaned file → archive. The audit event `inventory.photo.add` records both hashes. Photos of a dwelling may show tenants' belongings and are personal data: the processing is added to the register in `docs/rgpd/`, and the privacy notice tells tenants about it.

## 4. Templates and condition scale

When an asset is created, Tenure seeds its layout from a system template for its kind; the owner then edits rooms and elements once. Every later inventory starts from that layout, so the entry, the exit and the next lease's entry line up item by item.

| Template | Rooms seeded | Elements per room |
| --- | --- | --- |
| Apartment, unfurnished | Entrée, séjour, cuisine, chambre × n (from the asset's room count), salle d'eau, WC, cellier or cave if lot present | Sol, murs, plafond, porte, fenêtres et volets, prises et interrupteurs, éclairage, radiateur; cuisine adds évier, robinetterie, plan de travail, hotte, plaques, four; salle d'eau adds douche ou baignoire, lavabo, WC, VMC |
| House, unfurnished | As apartment, plus garage, jardin, façades visibles, portail and clôture | Exterior: portail, clôture, terrasse, gouttières visibles, éclairage extérieur, robinet extérieur |
| Furnished (apartment or house) | As above | Plus the décret 2015-981 list as `furniture` items with quantity: literie avec couette ou couverture, occultation des fenêtres des chambres, plaques de cuisson, four ou micro-ondes, réfrigérateur avec freezer, vaisselle, ustensiles de cuisine, table et sièges, étagères de rangement, luminaires, matériel d'entretien ménager |
| Box or garage | One room `box` | Sol, murs, plafond, porte (basculante, sectionnelle or battante), serrure, télécommande, éclairage, prise, ventilation, numéro de place |

A box attached to a dwelling lease gets its own room block inside the dwelling's inventory, not a separate document: one lease, one entry, one exit.

**Condition scale** (one value per item, ordinal so exit vs entry can be compared):

| Code | Label FR (document) | Meaning |
| --- | --- | --- |
| new | Neuf | Unused or freshly done |
| good | Bon état | Clean, no visible wear |
| fair | État d'usage | Normal wear, works |
| poor | Mauvais état | Damaged, stained or partly broken |
| broken | Hors d'usage | Unusable, to repair or replace |

`works` (fonctionne: oui / non / sans objet) is recorded apart from the condition for appliances, electrical and plumbing items. A `poor` or `broken` item without a comment or a photo blocks `complete`.

## 5. Lifecycle and flow

An inventory moves `draft → scheduled → in_progress → completed → signed → issued`. Only `issued` counts for the lease, and an issued inventory is never edited.

| State | Entered by | Allowed changes | Leaves by |
| --- | --- | --- | --- |
| draft | `POST /leases/{id}/inventories` | Everything; items generated from the asset layout (entry) or the entry items (exit) | `schedule` with date and place |
| scheduled | Owner | Date, place, participants | `start` on site |
| in\_progress | Owner, on the tablet | Items, meters, keys, photos, observations; autosaved per item | `complete` when the blocking checks pass |
| completed | Owner | None to the content; `reopen` goes back to in\_progress | `sign`: signatures requested from every party |
| signed | Last signature recorded | None | `issue` (automatic once all parties have signed) |
| issued | System | None (trigger). Corrections only through a completion (EC) | — |

Mode `upload` (phase 1 path, and the commissaire-de-justice report) skips straight from draft to issued when the signed PDF is attached. Signature reservations typed by a party are printed on the document; they do not block issuing.

**End-to-end flow**

1. Asset created → layout seeded from the template (section 4), edited once by the owner.
2. Lease drafted → entry inventory created in draft; the lease hub shows the existing blocker `lease/entry-inventory-missing` (RENT-SPEC-008).
3. On the handover day the owner captures the entry with the tenant, both sign, Tenure issues `EE-…`, and the PDF is e-mailed to each party.
4. The lease can now be activated. Tenure opens the 10-day completion window, and the heating-season window if the lease starts outside it; both end dates show on the inventories tab, and on the obligations calendar once RENT-SPEC-012 exists.
5. A completion request is recorded and accepted or refused line by line; Tenure issues `EC-…` linked to the entry.
6. Notice given (RENT-SPEC-010) → exit inventory created with `entry_inventory_id`; each item is pre-filled with the entry condition, plus the accepted completion lines, and marked « to check ».
7. On key return the owner records `keys_returned_on`, each item's exit condition, meters, keys and the tenant's new address; both sign; Tenure issues `ES-…`.
8. Comparison and deposit settlement (section 6) → restitution statement `RD-…` before the legal deadline.
9. The next lease's entry is pre-filled from the last exit, conditions included, so works done in between are the only edits.

## 6. Comparison, vétusté and deposit settlement

The exit comparison is a pure function `inventory.Compare(entry, completions, exit)`. It flags every difference, and the owner decides which flags become deductions. Tenure never charges a flag on its own.

**Comparison rules** (one result per exit item, against its `entry_item_id`):

| Result | Condition | Default handling |
| --- | --- | --- |
| same | Same condition and `works` | None |
| better | Higher on the scale (works by the owner) | None; the layout keeps the new state for the next lease |
| worse | One or more steps lower, or `works` ok → ko | Flagged; the owner qualifies it as normal wear, vétusté, tenant damage or other cause (art. 7) |
| missing | Furniture quantity or key count lower than at entry | Flagged as tenant liability by default; cost from a quote or invoice |
| new | Item absent at entry | Informative, no deduction possible |

Meter readings at exit close the lease's consumption for direct contracts and feed the final regularisation (RENT-SPEC-007, « a tenant who left » rule).

**Deduction amount** for an item qualified as tenant damage, with the lease's grid line for its category (age = years from `asset_elements.installed_on`, or from the lease start if unknown, to the exit date):

```latex
\text{share}_{bp} = \max\big(\text{floor}_{bp},\; 10\,000 - \text{rate}_{bp} \times \max(0,\ \text{age} - \text{franchise})\big), \qquad \text{deduction}_{cents} = \operatorname{round}\!\left(\frac{\text{cost}_{cents} \times \text{share}_{bp}}{10\,000}\right)
```

Age and franchise are counted in full years. All terms are integers, and the division goes through the project's rounding helpers (half-up to the cent), never a float. Without a grid, the share is 10 000 bp and the owner justifies the amount item by item. The cost always comes from an archived quote or invoice, never a flat amount.

**Settlement** (extends RENT-SPEC-008):

- Balance = deposit held − deductions − unpaid arrears (RENT-SPEC-003) − pending regularisation. When `assets.collective_building` is set and the annual accounts are not yet approved, a provisional holdback of up to 20 % of the deposit replaces the regularisation (RENT-SPEC-007).
- Deadline = `keys_returned_on` + 1 month when no deduction is retained, else + 2 months. The law ties the 1-month case to an exit that conforms to the entry; Tenure deliberately uses the stricter « no deduction » test, which can only shorten the delay (INV-OD-13).
- Holdback balance: due within the month after the accounts are approved; the deadline is dated from the approval date entered on the reconciliation.
- If a deadline passes, Tenure shows the penalty owed: 10 % of the monthly rent excluding charges per started month of delay. No penalty is shown when `tenant_new_address` is empty.
- The `RD` restitution statement lists each deduction with its item, entry and exit conditions, photos, grid line and supporting document. It is immutable and archived; the holdback is released by a second `RD` once the regularisation is issued. `DG` remains the deposit receipt at entry.

## 7. Backend (tenure-api)

Phase 2 adds a package `internal/inventory`. It imports `portfolio`, `lease`, `document` and `params`, and nothing imports it except `httpapi` and the wiring in `cmd/tenure`. That keeps RENT-BE-001's downward-only rule (INV-OD-06).

- **Activation check without a cycle**: `lease` declares an interface `ActivationBlocker` (« entry inventory issued? »). `inventory` implements it and is registered in `cmd/tenure`. In phase 1, `lease` keeps its existing check: an entry row in inventories linked to an uploaded document of class `inventory`.
- **Pure functions**, no I/O, golden-tested (slice 1, #89): `inventory.Seed(template, asset)` and `inventory.Draft(id, furnished, layouts…)` for a new entry; `inventory.Prefill(id, source, completions)`, both ways (entry → exit, last exit → next entry); `inventory.Validate(inv)`, whose checks carry the stage they block (complete or issue); `inventory.Compare(entry, completions, exit)`; `inventory.Deduct(input, grid)`; `inventory.SettlementDeadline(keysReturnedOn, deductionRetained, rules)`, `inventory.LatePenalty`, `inventory.HoldbackBalanceDeadline` and `inventory.Settle`. The rules are `params.DepositReturn`, read from the keys of this section. Calendar months are reckoned with `period.Date.AddMonthsClamped`: one month from 31 January is the last day of February, never 3 March.

**API** (REST under `/api/v1`, swag-annotated, ETag + If-Match on PUT, as RENT-BE-001 section 3):

| Endpoint | Purpose |
| --- | --- |
| `GET/PUT /assets/{id}/layout` | Rooms and elements of the asset, PUT as a whole with If-Match on `asset_layouts.version`; `POST /assets/{id}/layout/seed` from a template (slice 3, #93) |
| `GET /inventory-templates` | System and owner templates |
| `GET/POST /leases/{id}/inventories` | List, or create an entry or exit (kind, mode); existing endpoint, extended. `items` stays accepted for `mode upload` until the migration of section 9, then is refused with `inventory/items-field-retired` |
| `GET/PUT /inventories/{id}` | Header: schedule, place, participants, observations, new address, `keys_returned_on` |
| `PUT /inventories/{id}/items/{itemId}`, `POST /inventories/{id}/items` | Condition, works, quantity, comment; add an item found on site |
| `PUT /inventories/{id}/meters`, `PUT /inventories/{id}/keys` | Replace the meter and key lists while not completed |
| `POST /inventories/{id}/photos` | Multipart upload → quarantine → ClamAV → EXIF strip → archive prefix (section 3); linked to an item or meter |
| `GET /inventories/{id}/photos/{photoId}/content` | Short-lived signed URL, as `GET /documents/{id}/content` |
| `POST /inventories/{id}/transitions` | schedule, start, complete, reopen, sign, issue; `dry_run` returns the blocking checks |
| `POST /inventories/{id}/signatures` | Start the e-signature request or attach a wet-signed scan |
| `GET /inventories/{id}/comparison` | Exit only: per-item results of section 6 |
| `POST /inventories/{id}/completions`, `PUT /completions/{id}` | 10-day and heating requests, decision per line; distinct from lease amendments `POST /leases/{id}/amendments` |
| `GET/POST /vetuste-grids`, `PUT /leases/{id}` with `vetuste_grid_id` | Grids and their attachment to a lease |
| `GET/POST /leases/{id}/deposit-deductions`, `POST /leases/{id}/deposit-settlement?dry_run=` | Deductions and the `RD` statement (RENT-SPEC-008) |

**Problem types**: `inventory/issued-immutable`, `inventory/exit-without-entry`, `inventory/mandatory-mention-missing` (with the missing list), `inventory/signature-missing`, `inventory/completion-window-closed`, `inventory/deduction-unjustified`, `inventory/items-field-retired`. Activation keeps the existing `lease/entry-inventory-missing`.

**Documents**: new prefixes `EE` (entry), `ES` (exit), `EC` (entry completion) and `RD` (deposit restitution statement), added to `document/types.go` beside `DG` (deposit receipt), numbered `<prefix>-<asset>-<YYYY>-<seq>` like the others. The template `templates/inventory/v1` declares its version constant in the inventory module, as the template test of [ovander/tenure-api#84](https://github.com/ovander/tenure-api/pull/84) requires. It is always in French and renders to PDF/A-2b via gotenberg. Each item shows a photo thumbnail embedded in the PDF at render time; the full-size photos stay in the archive, listed by hash in an annex. Class `inventory` keeps its RENT-SPEC-005 retention clock (lease end).

**Audit**: every write above emits `audit.Record` in the same transaction. Actions: `inventory.create`, `.item.update`, `.photo.add` (both hashes), `.transition`, `.signature.add`, `.completion.decide`, `deposit.deduction.create`, `deposit.settle`. All mutating routes are added to `testdata/audited_routes.txt`.

**Jobs**: no new cron job. The existing `billing.reminders` also sends the owner a reminder 7 days before each window end and deposit deadline, read from the inventory and settlement rows. Once RENT-SPEC-012 exists, the same dates are written as obligations-calendar items; until then they show on the inventories tab and the home to-dos.

**legal\_params keys** (dated, as RENT-BE-001 section 7): `inventory.completion_window_days` = 10; `inventory.heating_window_months` = 1; `heating_season.start_month` = 10 and `.end_month` = 5 (INV-OD-07); `deposit.return_months.conforming` = 1, `.with_deductions` = 2; `deposit.collective_holdback_bp` = 2000; `deposit.holdback_balance_months_after_approval` = 1; `deposit.late_penalty_bp_of_rent` = 1000.

## 8. Frontend (tenure-web)

Inventories live under the lease hub, in the existing tab `/leases/{id}/inventories` (S15). The on-site capture screen is the only tablet-first screen of phase 2; it uses the tablet layouts allowed by FE-OD-03.

| Route | Screen | Key elements |
| --- | --- | --- |
| `/assets/{id}/layout` | S15a Asset layout | Room list with drag to reorder; elements per room; « seed from template »; `installed_on` per element |
| `/leases/{id}/inventories` | S15 Candidate file and inventories (inventories part) | Entry and exit cards with state tag, scheduled date, DocumentCard once issued, open windows (10 days, heating) and deposit deadline with their end dates |
| `/inventories/{id}/capture` | S15b Capture (tablet) | Left: rooms with progress (n of m items assessed). Right: one card per item, with condition as a 5-button SelectButton, works toggle, comment, camera button. Meters and keys as their own « rooms » |
| `/inventories/{id}/review` | S15c Review and sign | Read-only summary, blocking checks as a Message list, reservations field per party, « request signatures » |
| `/inventories/{id}/compare` | S15d Comparison (exit) | Entry vs exit side by side, filter « changes only », a result tag per row, qualify each flag (wear, vétusté, tenant damage, other) |
| `/leases/{id}/deposit` | S07 extended: settlement wizard | Steps: deductions → justifying documents → arrears and regularisation → holdback → review → issue `RD`. Summary rail with balance and deadline countdown from `keys_returned_on` |

Capture rules:

- The camera opens through `<input type="file" accept="image/*" capture="environment">`; images are resized on the client to 2 048 px on the long side (JPEG q 0.8) before upload, then uploaded in the background with a visible queue.
- Each item is saved when it changes (PUT per item), and the progress per room comes from the server. Losing the network keeps unsent changes in memory with a visible « not saved » banner. True offline capture is INV-OD-02.
- The exit capture shows the entry condition and the entry photo thumbnail under each item, so the owner only touches what changed.
- Images and CSP: today's `img-src 'self' data:` blocks both the local previews (`blob:`) and the thumbnails loaded by signed URL from the object store. RENT-FE-001 is amended per INV-OD-12 (decided): it adds `blob:` and the bucket host to `img-src` only.
- Labels come from the own dictionary (`t('inventory.condition.fair')`), FR default and EN optional; the PDF stays French whatever the UI language.

Roles follow RENT-FE-001 section 5: the owner has full access and the co-owner reads. The accountant sees no inventories, photos or guarantee data, as today for class `deposit` (Q-91). Whether the `RD` statement becomes visible to the accountant is INV-OD-11. In phase 4 the tenant portal (S20) shows the tenant's own issued inventories and lets them file the 10-day completion request.

## 9. Phasing and slice order

Phase 1 needs nothing new beyond what RENT-SPEC-008 already requires. The structured inventory is phase 2, built engine first and screens last, as in the phase 1 roadmap.

| Phase | Delivers |
| --- | --- |
| 1 | Upload of the signed entry and exit PDFs as class `inventory` (`mode upload`); activation blocker; inventory documents in the archive |
| 2 | Everything in this spec except the tenant portal parts |
| 4 | Tenant reads inventories and files completion requests in the portal (RENT-SPEC-017) |

Phase 2 slices:

1. Pure functions `Seed`, `Prefill`, `Validate`, `Compare`, `Deduction` and `SettlementDeadline`, in integer basis points with the rounding helpers, golden-tested on the real assets.
2. Migrations: the tables of section 3, RLS, immutability trigger, `legal_params` keys. Existing `inventories` rows become `state = issued`, `mode = upload`, and their `items` JSON is kept as `legacy_items` (read-only, shown as « imported items » on the card). They are not converted into `inventory_items`, because they have no stable link to an asset layout. Then the API refuses `items` (`inventory/items-field-retired`).
3. Asset layout API and screen S15a; seeding of the real assets.
4. Photo pipeline (EXIF strip, cleaned-file hash), CSP amendment of RENT-FE-001 (INV-OD-12) and RGPD register entry.
5. Entry inventory: capture API, transitions, `EE` PDF template with its version constant (tenure-api#84 test).
6. Signatures through the OD-08 provider, plus the wet-scan fallback.
7. Completions and `EC`; window end dates on the tab and in `billing.reminders`.
8. Exit inventory with pre-fill and `keys_returned_on`, comparison API and screen S15d, `ES` PDF.
9. Vétusté grids, deductions, settlement wizard and `RD` statement.
10. Obligations calendar items, only once RENT-SPEC-012 is delivered; slices 7 and 9 do not wait for it.
11. Phase 2 gate: one real lease run from entry to deposit settlement in the tool (RENT-SPEC roadmap).

## 10. Testing

The same strategy as RENT-BE-001 section 10, with golden files under `testdata/golden/<year>/inventory/`.

- Golden cases: apartment with box (one inventory, two room blocks); furnished studio with a missing furniture item; house with a broken appliance; exit with an accepted 10-day line; deduction with and without a grid, with age below and above the franchise, and a rounding case at half a cent; deadline at 1 and 2 months from `keys_returned_on` when it differs from `done_on`; late penalty over 1 and 3 started months; 20 % holdback in a collective building and its balance one month after approval.
- Integration: cross-owner RLS on every new table and on photo signed URLs (zero rows); an issued inventory refuses UPDATE and DELETE on header and children; photo upload rejected when infected or not an image; the archived photo carries no EXIF and its `sha256` matches the stored object; activation refused without an issued entry (`lease/entry-inventory-missing`).
- Migration: a phase 1 row with `items` JSON comes out as issued, `mode upload`, `legacy_items` identical; a later POST with `items` gets `inventory/items-field-retired`.
- Contract: OpenAPI diff on each PR; new routes listed in `audited_routes.txt`; the template test of tenure-api#84 passes with the `inventory/v1` constant.
- Frontend: Vitest on the condition scale and the comparison table; Playwright smoke « entry → sign → activate → exit → compare → settle » against the seeded backend, with a CSP-enforcing header so blocked images fail the test.

## 11. Open decisions

Every item below is a proposal awaiting Olivier's decision; none is applied to the high-level corpus yet.

| Id | Question | Proposal | Status |
| --- | --- | --- | --- |
| INV-OD-01 | Keep `inventories.items jsonb` or normalise | Normalise (section 3); existing rows migrated with `legacy_items` (section 9) | Decided, 2026-11-05 |
| INV-OD-02 | Offline capture in a cellar or a box with no network | Phase 2 online only, with per-item autosave and an in-memory retry queue. An IndexedDB draft queue only if a real site fails, which would change RENT-FE-001 « no offline writes » | Proposed |
| INV-OD-03 | Signing on site | OD-08 provider flow opened on the tablet, one signer after the other; wet-signed scan as fallback. Check that the provider supports in-person signing | Proposed |
| INV-OD-04 | Condition scale | 5 levels (section 4) plus a separate `works` flag | Decided, 2026-11-05 |
| INV-OD-05 | Document prefixes | EE, ES, EC and RD added to `document/types.go`; DG stays the deposit receipt | Proposed |
| INV-OD-06 | Module placement | New `internal/inventory`; `lease` exposes an `ActivationBlocker` interface (checked against the downward-only rule) | Proposed |
| INV-OD-07 | Heating-season window | The law does not fix the dates; parameter October–May, editable per asset | Proposed |
| INV-OD-08 | Default vétusté grid | No default values shipped; the owner enters a grid taken from a collective agreement and attaches it to the lease | Proposed |
| INV-OD-09 | Commissaire-de-justice cost | Half the invoice as a separate line on the next monthly fee, with the invoice archived | Proposed |
| INV-OD-10 | Alignment with the code | Done by the 2026-11-05 code review: items already writable, amendments naming, DG prefix, accountant classes, bp, CSP, EXIF, spec versions (changelog 0.2) | Resolved |
| INV-OD-11 | Accountant and the `RD` statement | (a) `RD` is a financial class: the accountant sees amounts and deduction descriptions, no photos, guarantor or tenant address; or (b) the accountant does not see it. Proposal (a), because retained amounts are figures they reconcile; class `deposit` stays non-financial (Q-91) | Decided (a), 2026-11-05 |
| INV-OD-12 | CSP for photos | (a) `img-src 'self' data: blob: <bucket host>`, signed URLs unchanged; or (b) thumbnails served by the API, which breaks « content never streamed through the API ». Proposal (a), amending RENT-FE-001 section 7 and the Caddy CSP | Decided (a), 2026-11-05 |
| INV-OD-13 | 1-month deadline test | Stricter than the law: 1 month unless a deduction is retained, instead of « exit conforms to entry ». Safe for the tenant, simpler to compute; kept as an explicit choice | Decided, 2026-11-05 |

## 12. Changelog and sources

| Date | Version | Change |
| --- | --- | --- |
| 2026-11-05 | 0.7 | Slice 4 implemented (#97): photo limits, orientation applied, `taken_at` left empty, retention from upload until lease end is known, co-owner views and accountant refused, CSP origin taken from the store (Q-110) |
| 2026-11-05 | 0.6 | Slice 3, API (#93): layout routes, `asset_layouts` holds the layout version (migration 000012), the accountant is refused (Q-109) |
| 2026-11-05 | 0.5 | Slice 2 implemented (#91, migration 000011): INV-OD-01, 04 and 13 decided as proposed; `done_on` kept as a date; `inventory_templates` without a `furnished` column; a deduction always has its document; restitution and window keys seeded in `legal_params` (Q-108) |
| 2026-11-05 | 0.4 | Slice 1 implemented (#89): function names and signatures in §7 as built; outdoor parts exempt from the three coverings; a furnished element replaces its base equivalent; a missing grid line refuses the deduction; the holdback rounds down; INV-OD-04 and INV-OD-13 followed as proposed (Q-107) |
| 2026-11-05 | 0.3 | INV-OD-11 decided: RD is a financial class visible to the accountant (amounts and descriptions only). INV-OD-12 decided: img-src opened to blob: and the bucket host; RENT-FE-001 amended |
| 2026-11-05 | 0.2 | Code and legal review applied: existing `items` migrated to `legacy_items`; inventory amendments renamed completions; `RD` restitution statement (DG stays the receipt); rates in basis points with rounding helpers; EXIF stripped and cleaned-file hash; RGPD register; `keys_returned_on` starts the deposit clock; holdback for collective buildings with the post-approval balance; calendar dependency moved to slice 10; references updated to RENT-BE-001 2.10 and RENT-FE-001 1.1; INV-OD-10 resolved, INV-OD-11 to 13 added |
| 2026-11-05 | 0.1 | Initial inventory specification derived from RENT-SPEC-009, 008 and 005; open decisions INV-OD-01 to 10 |

Sources consulted:

- [ANIL — Modalités d'établissement de l'état des lieux et prise en compte de la vétusté](https://www.anil.org/documentation-experte/analyses-juridiques-jurisprudence/analyses-juridiques/analyses-juridiques-2016/modalite-detablissement-de-letat-des-lieux-et-prise-en-compte-de-la-vetuste) (décret 2016-382: mandatory mentions, form, vétusté grid)
- [ADIL de la Savoie — Retenues sur le dépôt de garantie](https://www.cdad-savoie.justice.fr/uploaded/docs/fiche%20pratique%20retenue%20sur%20le%20d%C3%A9p%C3%B4t%20de%20garantie.pdf) (1 and 2 month deadlines, 20 % holdback, 10 % penalty, justification of deductions)

The following points were not checked against Légifrance in this version: the 10-day and heating-season windows, the shared commissaire-de-justice cost (loi 89-462 art. 3-2), the décret 2015-981 furniture list and Code civil art. 1730–1731. Verify them before slice 6.
