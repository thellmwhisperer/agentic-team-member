## ADDED Requirements

### Requirement: The worker's report is painted as text with every harness

The Agent box SHALL paint the worker's final report as the `Informe` row, its summary
in prose and its files, commands, test and findings as folded rows, whatever harness
produced it, and SHALL NOT paint the report's JSON object as a ✎ line.

#### Scenario: A codex worker ends with the report

- **WHEN** the worker is codex and the last `agent_message` of its turn is the report
  JSON object, and the operator opens `atm attach`
- **THEN** the Agent box ends with one `Informe` row with the summary in prose and the
  folded rows, and no line on the screen holds `{`, `"test_file"` or `\"`

#### Scenario: Prose before the report stays prose

- **WHEN** the worker's last message is prose followed by the report JSON object
- **THEN** the prose is painted as a ✎ line and the report as the `Informe` row, once

#### Scenario: A claude worker keeps painting as today

- **WHEN** the worker is claude and its `result` event carries the report
- **THEN** the screen is the one `TestRealScreenShowsTheReportAsRows` asserts today

#### Scenario: A final message with no report stays a message

- **WHEN** the worker's last message holds no JSON object
- **THEN** it is painted as a ✎ line unchanged and the run records that the agent
  left no report, as `report()` does today
