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

## Cases (2025)

| Case | Proves | Key figures |
| --- | --- | --- |
| `apartment-box-unfurnished-3y` | Proration from the 15th; one lease over an apartment and its box; 2044 lines; prior deficit used | March 17/31: rent 466,13 + provision 49,35 = 515,48 €; 211 = 8 116,13 €; net 3 710,13 €, of which 1 000,00 € covered by the 2019 deficit, taxable 2 710,13 €; micro taxable 5 681,29 € |
| `furnished-studio` | Furnished lease: fees, and gross receipts only for tax (OD-01) | Fee 670,00 €; gross receipts 7 440,00 €; no 2044 line |
| `irl-mid-year-revision` | Revision applied from the month after the anniversary, no retroactivity; zone cap; forfeiture | 700,00 → 707,28 € (×146,68/145,17); controlled zone, synthetic 0,5 % rule: 703,50 €; more than 12 months late: forfeited |
| `late-regularisation` | Pro rata temporis by days with vacancy; 12-month spread; new provision | 1 950,00 € recoverable over 365 days: 641,09 € (120 d) and 1 143,28 € (214 d), vacancy 165,63 €; balances 161,09 € (13,42 € × 11 + 13,47 €) and 303,28 € (25,27 € × 11 + 25,31 €), each over 12 instalments; new provision 163,00 € |
| `co-owned-50-50` | Split by largest remainder; deficit against global income; expired and carried deficits | 211: 7 200,06 € each; 224: 10 000,01 / 10 000,00 €; deficits against global income 5 164,95 € and 5 164,94 €; O1's 2014 deficit expired |

The index values in `irl-mid-year-revision` are illustrative inputs, not a reference series.
The `source_url` of each parameter fixture must be checked against the official text before the
slice 2 seed is deployed.
