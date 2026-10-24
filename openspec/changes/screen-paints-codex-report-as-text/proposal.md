## Why

The brief (`internal/run/brief.md.tmpl`, REPORT) requires the worker's final message
to be one JSON object: `test_file`, `changed_files`, `summary`, `commands_run`. Issue
#588 (closed 21-oct-2026) made the Agent box paint that object as an `Informe` row
with its summary in prose and four folded rows, never as JSON. That holds for Claude
only. The stream keeps the last message as `pending` and, at the end, strips the JSON
from it only when `s.final` ends with that text (`internal/run/agent.go`, `flush`).
`final()` fills `s.final` for claude, opencode, pi and cursor; for codex it returns
nothing, because codex's final message lives in its `-o` file. So with Codex the last
`agent_message`, which is the report, is flushed as plain prose: braces, quoted keys
and escaped quotes on the screen, and then the `Informe` row under it.

Who asked: Javi, 24-oct-2026 ~18:30 BST, decision 8162: the report is painted as text, without JSON. The screen paints what the binary gives it, as text; JSON is a
transport between the worker and ATM, never something the operator reads.

Proof: run 551-8 of la-roca, 24-oct-2026 ~18:25 BST, worker codex gpt-6.1-sol over
ACP. `worker.jsonl` ends with `item.completed agent_message` whose text is the JSON
object, then `turn.completed`; `atm attach 551-8` painted that object as one ✎ line
while the test step ran. The same report in the fake agent's claude stream
(`TestRealScreenShowsTheReportAsRows`) paints as rows. Reproduce: any `atm run` with
`agent: codex`, then `atm attach` when the agent step ends.

## What Changes

- With every harness, the agent's last message that is the report is painted once, as
  the `Informe` row, and never as a ✎ line. For codex the stream recognises the report
  in the last `agent_message` of the turn, the same object `report()` reads from the
  `-o` file.
- Prose before the JSON in that message stays a ✎ line, as with Claude today.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `agent-report-screen`: how the Agent box paints the worker's final report, for every
  harness ATM drives.

## Impact

- Code: `internal/run/agent.go` (`final` or `flush` for the codex path; `event.codex`).
- End-to-end tests to write, run by `make check`: `TestRealScreenShowsTheReportAsRows`
  gains a codex case whose fake agent ends its stream with an `agent_message` holding
  prose and the report, and asserts the ✎ line holds the prose only, the `Informe` row
  is there once, and no `{`, `"test_file"` or `\"` is on the screen.
- Not touched: the brief and the worker's JSON contract; `report.json`; `report()` and
  its checks; the Log box and the gate box; the ponytail rows and the delivery gate.
