# Golden files

Calculation results that must not change by accident (RENT-BE-001 §10, RENT-SPEC-021).
`internal/golden` replays every case through the pure calculation engine; `go test ./...` runs it.

## Layout

```
testdata/params/<year>.json          legal parameter fixtures (seed of legal_params)
testdata/golden/<year>/<case>.json   one case: inputs, steps, expected results
```

A case file holds:

- `description`: what the case proves, in plain words.
- `params_override` (optional): extra entries added to the fixtures, each with a `source`. A
  synthetic value says so in its `source` and is never a legal figure.
- `leases`, `charges`, `payments` and `assets` (the owner shares, used by the tax step).
- `steps`, run in order. Each step is exactly one of:
  - `fee`: `billing.ComputeFee`;
  - `revise`: `billing.Revise`; with `"apply": true`, the following steps see the revised lease;
  - `allocate`: `charge.Allocate`;
  - `validate`: `lease.Validate`, whose result is the list of broken lease rules;
  - `tax`: `tax.Lines`.

  Each step's `expected` is that function's result.

A case under `<year>/` only sees parameter entries effective on or before 31 December of that
year (`params.Set.Until`). A value added for a later year therefore cannot move an earlier
golden.

## Regenerating

```bash
go test ./internal/golden -update
git diff testdata/golden
```

`-update` recomputes every `expected` block and rewrites only the files that changed. The diff
is the review: each changed figure is a behaviour change, and has to be explained in the PR and,
if it is intended, in the spec. Never regenerate to make a failing test pass without reading
the diff.

To add a case, write its inputs and steps with `"expected": null` and run `-update`. Then check
the figures by hand and record that check in the PR.

## Inventory cases (RENT-INV-001)

`testdata/golden/<year>/inventory/*.json` replay `internal/inventory` (`TestInventoryGolden`). A
case lists `assets` and optional vétusté `grids`, then `steps`, each one of:
- `seed`, an asset id: its layout from `testdata/inventory/templates.json`. The harness gives
  rooms and elements the ids `<asset>-r<n>` and `<asset>-r<n>-e<m>`;
- `draft`: a new entry over the seeded assets;
- `capture`: what was found on site (a `default` condition for every item, then `items` by
  element id), then `Validate`;
- `prefill`: the next inventory from one, with completion lines;
- `compare`: only the items that are not `same`;
- `deduct`;
- `settlement`: deadline, late penalty, balance and holdback.

| Case (2026) | Proves |
| --- | --- |
| `apartment-with-box` | One inventory, two room blocks (71 items); entry passes; exit pre-filled and marked to check; worn wall, remote ko, a key and the remote missing: 4 flagged; an unsigned exit without the new address cannot be issued |
| `furnished-studio-missing-furniture` | Studio with no bedroom: bedding and blackout seeded in the living room; the 11 furniture items, the hob and oven replacing the unfurnished ones; missing crockery warns, does not block |
| `house-broken-appliance` | Garage and outdoor parts seeded; no coverings required outdoors; a broken oven blocks until a photo explains it |
| `exit-with-ten-day-line` | An accepted completion line, not the issued entry, is compared: the same state at exit is not worse; a refused line changes nothing |
| `deductions` | Synthetic grid: within the franchise 100 %; after it 64 % (30 720 of 48 000); at the lifetime the floor 20 %; no grid 100 %; half a cent rounds up (12 348 × 87,5 % = 10 804,5 → 10 805) |
| `settlement` | The legal fixtures (loi 89-462 art. 22): 31 January + 1 month = 28 February; with a deduction, 3 February + 2 months = 3 April; penalty 1 and 3 started months (10 % of 800 € = 80 €, 240 €), none without a new address; holdback rounded down (20 % of 900,01 € = 180,00 €) and its balance 1 month after the approval |

## Cases (2025)

| Case | Proves | Key figures |
| --- | --- | --- |
| `apartment-box-unfurnished-3y` | Proration from the 15th; one lease over an apartment and its box (one local for line 222); 2044 lines; prior deficit used | March 17/31: rent 466,13 + provision 49,35 = 515,48 €; 211 = 8 116,13 €; net 3 710,13 €, of which 1 000,00 € covered by the 2019 deficit, taxable 2 710,13 €; micro taxable 5 681,29 € |
| `furnished-studio` | Furnished lease: fees, and gross receipts only for tax (OD-01) | Fee 670,00 €; gross receipts 7 440,00 €; no 2044 line |
| `irl-mid-year-revision` | Revision from the month after the anniversary, no retroactivity; national cap on the IRL variation; zone ceiling at revision and at creation; relocation rule; forfeiture | 700,00 → 707,28 € (×146,68/145,17, under a 3,5 % cap); synthetic +4,70 % index capped at 3,5 %: 724,50 €; controlled zone, synthetic ceiling 20,00 €/m² × 35,20 m² = 704,00 € instead of 707,28 €; new lease at 705,00 € breaks the ceiling; new lease at 703,00 € breaks the relocation rule (680,00 € × 146,68/145,17 = 687,07 €); more than 12 months late: forfeited |
| `late-regularisation` | Pro rata temporis by days with vacancy; 12-month spread; new provision | 1 950,00 € recoverable over 365 days: 641,09 € (120 d) and 1 143,28 € (214 d), vacancy 165,63 €; balances 161,09 € (13,42 € × 11 + 13,47 €) and 303,28 € (25,27 € × 11 + 25,31 €), each over 12 instalments; new provision 163,00 € |
| `co-owned-50-50` | Split by largest remainder; deficit against global income; expired and carried deficits | 211: 7 200,06 € each; 224: 10 000,01 / 10 000,00 €; deficits against global income 5 164,95 € and 5 164,94 €; O1's 2014 deficit expired |

The index values in `irl-mid-year-revision` are illustrative inputs, not a reference series. Its
`irl_cap` override re-dates the 2022–2024 bouclier rate to 2025, and its ceiling table is
synthetic: both are labelled as such in `source`. Tax cases read the map and parameters of their
income year (2025), labelled millésime 2026.
The `source_url` of each parameter fixture must be checked against the official text before the
slice 2 seed is deployed.
