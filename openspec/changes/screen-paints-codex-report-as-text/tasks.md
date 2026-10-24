## 1. Codex report on the screen

- [ ] 1.1 In `internal/run/agent.go`, make the stream recognise codex's final report: the last `agent_message` of the turn whose text ends in a JSON object is the final message (`final` for codex, or `flush` without the `s.final` suffix check when the harness keeps its final in the `-o` file), so `flush(true)` strips the object and the `Informe` row is the only place it is painted.

## 2. End-to-end test

- [ ] 2.1 Add the codex case to `TestRealScreenShowsTheReportAsRows` (`internal/run/report_rows_unix_test.go`): the fake agent (`internal/run/testdata/fakeagent`) ends its codex stream with an `agent_message` of prose plus the report; assert the ✎ line holds the prose only, one `Informe` row, and no `{`, `"test_file"` or `\"` on the screen. It fails on main and passes with the change.
- [ ] 2.2 `make check` passes: every changed line is executed by a real `atm` end-to-end test.
