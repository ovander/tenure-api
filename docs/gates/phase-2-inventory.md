# Phase 2 gate, inventory part: one real lease from entry to deposit settlement

This procedure backs RENT-INV-001 §9 slice 11: « Phase 2 gate: one real lease run from entry to
deposit settlement in the tool ». A gate is passed on the owner's real portfolio, not on test data
(RENT-SPEC roadmap). Nothing here signs itself: the record at the bottom is the owner's.

**Scope (Q-117).** The roadmap's phase 2 gate runs « from candidate to deposit settlement ». This
procedure covers the inventory and deposit part, the only part built so far. Passing it does not
pass phase 2. Candidate onboarding (RENT-SPEC-009), arrears recovery (011) and bank reconciliation
(013) each add their own steps when they are built.

**Rehearsal.** `e2e/flows/phase2.spec.ts` runs the same steps in a browser against a real tenure,
with fake parties and dates of today, on every CI run. It shows the tool can do the run. The gate
shows the run was done, on a real lease, with real people and real dates.

## Before you start

- tenure runs a release with the slice 11 rehearsal, and `tenure version` names it.
- The legal points that RENT-INV-001 « Sources » lists as not yet checked against Légifrance have
  been checked, or each one is recorded as accepted:
  - the 10-day and heating-season windows;
  - the shared commissaire-de-justice cost (loi 89-462 art. 3-2);
  - the décret 2015-981 furniture list;
  - Code civil art. 1730–1731.
- The lease chosen is real and its tenant agrees to sign the inventories in Tenure, by hand or
  electronically.

The run spans the whole lease, so each step is done when it happens in real life. Record the date
of each step and the number of each document as you go.

## Steps and what to check

| # | Step | Where | Check |
| --- | --- | --- | --- |
| 1 | The asset's layout seeded from the template, then corrected to the real rooms and elements | S15a | Each room and element is on site; installation dates are entered where known |
| 2 | The entry inventory scheduled, captured on site item by item, with photos and meter readings, the keys handed over | S15b | « Terminer la saisie » is offered only once the blocking checks clear |
| 3 | The entry signed by the owner and each co-tenant, then issued | S15c | An `EE-…` document is in the archive; each signature and reservation is printed on it |
| 4 | The lease activated | S05 | No activation blocker is left |
| 5 | A completion request, if the tenant makes one within ten days (or in the heating season), decided line by line | Completions screen | An `EC-…` document is issued; the home stops listing the request |
| 6 | The notice recorded with its end date | S05 | The calendar lists « Fin du préavis » |
| 7 | The exit inventory created from the issued entry, every item checked on site, the keys' return date and the tenant's new address | S15b | Each item starts « à vérifier »; « Terminer la saisie » waits for the keys' date and the address |
| 8 | The exit signed and issued | S15c | An `ES-…` document is in the archive |
| 9 | The comparison read with the tenant's copy at hand | S15d | Each worse or missing item matches what was seen on site |
| 10 | The wear grid annexed to the lease (the one the contract cites, or a collective agreement) | Deposit tab | The grid's source is named |
| 11 | Each deduction entered with its quote or invoice | Deposit tab | The tenant's share follows the grid and the element's age |
| 12 | The restitution statement issued before the deadline shown, then, for a collective building, the release of the holdback once the accounts are approved | Deposit tab | An `RD-…` document is issued within the deadline; the calendar stops listing it |
| 13 | The audit chain and the archive verified over the run | `tenure job audit.verify`, `tenure job archive.verify` | Both succeed, and their `job_runs` rows are kept |

The tenant receives each document as the law requires: the EE and the ES at signature, the RD
with the balance. Tenure does not post them.

## Findings

Every defect met during the run gets an issue and is fixed before sign-off. If a defect is
accepted instead, the record says why. A step done outside Tenure means the gate is not passed.

## Sign-off

| Field | Value |
| --- | --- |
| Lease (Tenure id, never the tenant's name) | |
| Release used | |
| Entry `EE-…` and date | |
| Completion `EC-…` and date, or « none requested » | |
| Exit `ES-…` and date | |
| Statement `RD-…`, date and deadline | |
| Release of the holdback `RD-…`, or « not a collective building » | |
| `audit.verify` and `archive.verify` runs | |
| Issues opened during the run | |
| Signed off by the owner, date | |
