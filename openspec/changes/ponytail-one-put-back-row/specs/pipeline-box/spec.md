## ADDED Requirements

### Requirement: Ponytail's restore is one row

The pipeline box SHALL paint ponytail's restore of the clone as one row, `put back N
files`, under the ponytail step, and SHALL NOT paint one row per restored file.
`report.json` SHALL keep every restored path under the ponytail node.

#### Scenario: Thousands of files put back

- **WHEN** ponytail restores a clone where the worker left more than one thousand files
  and the operator opens `atm attach`
- **THEN** the left box shows one `put back N files` row, state `passed`, under
  ponytail, no row names a single path, and the pipeline's steps stay visible without
  scrolling

#### Scenario: The paths stay in the run

- **WHEN** ponytail restores N files
- **THEN** `report.json` lists the N paths under the ponytail node

#### Scenario: Nothing to put back

- **WHEN** ponytail restores no file
- **THEN** no `put back` row appears
