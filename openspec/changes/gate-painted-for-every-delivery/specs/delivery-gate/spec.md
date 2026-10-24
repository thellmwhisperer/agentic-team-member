## ADDED Requirements

### Requirement: The gate is painted for every project's delivery

`atm attach` SHALL find the no-mistakes run its delivery launched, whatever the
project's delivery command, and SHALL paint the run's steps, log and findings in its
right column until the run ends. `atm run` SHALL refuse a delivery command that runs
`no-mistakes attach`, naming the line and the replacement, before the clone.

#### Scenario: A delivery that only launches the run

- **WHEN** a project's delivery command launches a no-mistakes run and prints nothing
  else, and the operator opens `atm attach` after the delivery step starts
- **THEN** the right column paints the gate's steps and lines from the no-mistakes
  binary until the run ends, and the delivery row shows the run's result

#### Scenario: A delivery that opens the no-mistakes screen

- **WHEN** a project's `.atm.yaml` delivery ends with `no-mistakes attach` and the
  operator runs `atm run`
- **THEN** the command exits with a code other than 0 before cloning, its message names
  the `.atm.yaml` line and the replacement, and no run directory is created

#### Scenario: The gate asks a question

- **WHEN** the no-mistakes run of a project-style delivery asks a question
- **THEN** the question is painted in ATM's screen and answered from there, as #509 and
  #482 do for ATM's own delivery
