## 1. One event for the restore

- [ ] 1.1 In `internal/run/ponytail.go`, replace the per-path `put back <path>` events with one `put back N files` check event, state `passed`, carrying the paths in `files`; keep the paths in `report.json` under the ponytail node through the existing verdict path.

## 2. End-to-end test

- [ ] 2.1 Write a `TestReal*` under `internal/run` whose fake agent leaves more than one thousand files in the clone and whose ponytail cut is rejected; assert one `put back 1000 files` row under ponytail on the screen, no row naming a single path, and every path under the ponytail node of `report.json`. It fails on main and passes with the change.
- [ ] 2.2 `make check` passes: every changed line is executed by a real `atm` end-to-end test.
