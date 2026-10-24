## Why

When ponytail puts the clone back to the tree before a rejected cut,
`internal/run/ponytail.go` (the loop after `restore(clone, tree)`) emits one `check`
event per restored path, `put back <path>`, state `passed`. The screen paints one row
per check event in the left pipeline box. A worker that builds inside the clone leaves
thousands of files there, so the left box fills with thousands of `put back …` rows
and the pipeline's steps disappear below them. The operator cannot see which step the
run is in.

Who asked: Javi, 24-oct-2026 ~17:24 BST, decision 8158, on the screenshot of run 550:
the left box is the pipeline. One row per step and per check, never one row per file.

Proof: run 550-5 of la-roca, 24-oct-2026 ~17:24 BST, `atm attach 550-5`: the left box
showed hundreds of `put back .tmp/go-build/…  passed` rows and no pipeline step; the
worker had built Go with its cache under the clone. Reproduce: a run whose worker
builds with `GOCACHE` under the clone, then a ponytail cut that is rejected.

## What Changes

- Ponytail emits one check event for the restore, `put back N files`, with the paths
  in a `files` field of the event, instead of one event per path.
- The pipeline box shows that one row under the ponytail step. `report.json` keeps the
  N paths under the ponytail node, as today.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `pipeline-box`: what the left box paints for ponytail's restore.

## Impact

- Code: `internal/run/ponytail.go` (the restore loop); `internal/tui` reads the row from
  the event's `check` and `state` as for any check and ignores `files`.
- End-to-end tests to write, run by `make check`: a `TestReal*` under `internal/run`
  whose fake agent leaves more than one thousand files in the clone and whose ponytail
  cut is rejected; it asserts one `put back 1000 files` row under ponytail on the
  screen, no row naming a single path, and every path under the ponytail node of
  `report.json`.
- Not touched: what ponytail cuts, keeps or restores; the slop and lint checks; the
  report JSON on the screen and the delivery gate, which have their own changes.
