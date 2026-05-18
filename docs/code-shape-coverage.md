# Code-Shape Coverage Matrix

Status: v1.0 signed.

This matrix captures the current road-to-OSS coverage of testable code shapes.
It is based on cross-domain cookbook checks across prior runs, the current
semantic layer tests, and an observed API-retry run that satisfied the
unit-level red-green without satisfying the issue obligations.

The goal is to keep one language-agnostic ontology for the runner. Language
adapters should render imports, mocks, fixtures, and runner commands, but the
runner should classify the code shape and choose a verification contract
before it asks the model to write tests or fixes.

## Status Key

- `strong`: useful end-to-end behavior exists for this shape.
- `partial`: deterministic facts exist, but the runner does not reliably turn
  them into a correct issue-level fix.
- `weak`: observed runs show wrong targeting, bad scaffolds, or missing verify
  coverage.
- `untested`: no meaningful empirical coverage yet.

## Summary

| Code shape | Weight | Current status | Main risk |
| --- | ---: | --- | --- |
| Handlers, callbacks, event listeners | 25% | strong | Renderer hygiene and type-safe scaffolds still lag the semantic facts. |
| Services and classes with dependencies | 20% | weak | Class methods, construction, dependency ownership, and fallback effects are under-modeled. |
| API and SDK integrations | 20% | weak | External callsites, provider error shapes, retry semantics, and issue coverage are not verified. |
| CLI, scripts, and pipelines | 15% | weak | The runner often ranks entrypoint glue without knowing whether it is the real business target. |
| Persistence, config, and filesystem | 12% | partial | Facts exist for env, schema, worktrees, and files, but no unified fix strategy exists. |
| Pure functions and helpers | 8% | partial | The runner finds functions, but can confuse native globals, module state, and semantic state. |

Weighted view:

- Strong: 25%
- Partial: 20%
- Weak: 55%
- Untested: 0% as a named bucket, but CLI and API coverage are close to this
  in practice.

## Detailed Matrix

| Code shape | Signals to classify | Test strategy | Verify v2 minimum gate | Current evidence | Next slice |
| --- | --- | --- | --- | --- | --- |
| Handlers, callbacks, event listeners | Event registration, callback name, trigger strings, route guards, outbound observable calls. | Import the handler through safe seams, mock event dependencies, trigger one narrow route, assert observable effect. | Red must exercise an existing handler/callback or event route. Green must touch the handler or a dependency on its execution path. | Cookbooks against two event-handler functions (incoming event → outbound client call) found event registrations, outbound client calls, seams, triggers, and nearby tests. Semantic layer has handler, event, mention, and command facts. | Clean scaffold generation: remove `as never`, TODO placeholders, and low-confidence assertions from renderer output. |
| Services and classes with dependencies | Owner class, constructor deps, factory deps, provider selection, fallback branches, methods called on collaborators. | Instantiate through existing fixtures or a generated constructor fixture, mock collaborators at dependency boundary, assert fallback/effect behavior. | Red must call the class method through a valid owner/class import. Green must touch the method, constructor/dependency wiring, or an effectful collaborator used by the method. | A cookbook targeting a multi-provider service-class method failed with an invalid `Class.method` import shape, weak class-method modeling, and an unrelated assertion target. Semantic index can detect class methods and owner class, but the runner does not use that enough. | Add class-method contract: owner import, instantiation strategy, fixture reuse, constructor/factory dependency model. |
| API and SDK integrations | External SDK imports, client factory calls, awaited SDK methods, HTTP/SDK method names, retry/fallback language in issue text. | Mock SDK method at callsite, cover transient and non-transient errors, assert retry count and final behavior without real timers or network. | Each issue obligation that says "apply to API calls" must have callgraph or diff evidence showing the named SDK callsite now flows through the retry/fallback mechanism. Utility-only red-green is partial, not verified. | An observed run created a new retry utility and its unit tests but did not modify the API-callsite files identified by discovery. Red failed on the newly-introduced module path, not on the original bug. The provider SDK's actual error shape was not covered. | Add API retry obligation gate: parse issue tasks, require touched callsites, model SDK-specific error fields, reject utility-only verified state. |
| CLI, scripts, and pipelines | `main`, `process.argv`, env reads, file inputs/outputs, script names, pipeline orchestration, subprocess calls. | Test the command boundary with argv/env fixtures and temp dirs, or test the delegated business function if CLI is glue. Avoid selecting `main` unless the issue is entrypoint behavior. | Verify must prove whether the issue is about entrypoint behavior or downstream business logic. If it selects CLI glue, diff/test evidence must include argv/env/fs behavior. | In one observed run, discovery repeatedly ranked script/main functions and pipeline helpers without deciding whether they were targets or glue. Runner bootstrap covers command detection, but not issue-level CLI behavior. | Add entrypoint classifier: `main` as glue by default, promote to target only with explicit issue evidence. |
| Persistence, config, and filesystem | Config loaders, env vars, schema files, JSON/SQLite/filesystem operations, migrations, generated workspace paths. | Use temp dirs/files, explicit env fixtures, schema fixtures, and format-preserving assertions. | Red must fail on persisted/configured behavior, not merely on a missing helper file. Green must preserve existing file/schema format and cover generated/local workspace exclusions when relevant. | Existing tests cover env/config, worktree prep, generated test-runner config, schema surface, seed inserts, and discovery excludes. Generated run-worktree directories have leaked untracked side effects and have been mis-indexed by discovery in past runs — both point to integration gaps. | Formalize fs/config obligations and add generated-workspace exclusion gates for discovery and PR staging. |
| Pure functions and helpers | Top-level function with argument/return behavior, native globals, module state, small local algorithm. | Prefer direct input/output tests. Only mock module state when ownership is proven. Do not create seams for native globals. | Red must fail due to a semantic behavior gap in the helper, not because a new utility module is absent. Green must change the helper or proven state owner. | A cookbook targeting a generator helper with per-call randomness found the function but invented seams around native globals (`Math.random`) and missed an issue requirement about per-caller history. Compiler tests cover return-value scaffolds, but semantic state ownership is weak. | Add native/global denylist plus state ownership facts: argument state, module state, persistent state, external state. |

## Verify V2 Implications

`verified=True` should mean that issue obligations are covered, not just that
a new test fails without a newly created file and passes after the file
exists.

The verifier should produce a coverage record before PR creation:

```text
issue_obligation:
  id: apply_external_api_retry
  shape: api_sdk_integration
  status: missing
  required_evidence:
    - diff touches the API callsite or a wrapper used by it
    - test exercises a transient API failure through that callsite
    - retry classifies the provider's actual error shape correctly
  observed_evidence:
    - new retry utility file added
    - new retry utility unit-test file added
```

For an `api_sdk_integration` run whose issue lists "apply retry to <named
API>" as an obligation, the correct outcome under this matrix is:

```text
verified: false
outcome: partial_foundation
covered:
  - create shared retry utility
  - configurable retry options
  - transient status taxonomy, partially
missing:
  - apply retry to the named API callsites
  - cover the SDK's actual error shape (not just generic HTTP status)
```

### Minimum Viable Gate

The final API/SDK gate should be callsite-aware, but v1.0 must be executable
with the facts the runner already has.

v1.0 gate:

- If `discovery_candidates` is present, the diff must touch at least one path
  listed in `discovery_candidates`.
- If `discovery_candidates` is empty, the diff must touch at least one
  pre-existing source path in the target repo, not only newly created helper
  or test files.

v2.1 gate:

- Diff and/or callgraph evidence must show that the named external callsite
  now flows through the retry, fallback, validation, or resilience mechanism
  that satisfies the issue obligation.

In the observed `api_sdk_integration` run, the v1.0 gate would have rejected
the result: discovery candidates pointed at several pre-existing API-callsite
files, while the diff added only a new utility file and its test file.

## PR Body Contract

The PR body must be generated from the coverage record and the diff, not from
a free-form model summary. If the body claims a system or callsite was fixed,
the coverage record must contain matching evidence.

Named failure mode:

- `body-identifies-but-fix-doesnt-address`: the PR body correctly identifies
  a broken system, target file, or callsite, but the diff does not touch
  evidence needed to address it. Observed example: a PR body that described
  missing retry logic on named API calls, while the diff only added a
  standalone retry utility and its unit tests.

Required PR body sections:

```markdown
## Issue obligations
- [x] Create shared retry utility
- [ ] Apply retry to <API A> calls
- [ ] Apply retry to <API B> calls

## Diff evidence
- <retry-util-path>: added retry helper
- <retry-util-test-path>: added utility-level tests

## Verification
- Utility tests pass
- Issue-level API callsite coverage: missing

## Status
Partial foundation, not complete fix
```

If any obligation is missing, the PR can only be opened as partial work when
that mode is explicitly enabled. It must not be titled as a complete `fix`.

## Defaults

- Partial PR mode is off by default. A PR with missing obligations should
  stop before PR creation unless partial mode is explicitly enabled.
- Timer/backoff tests with real production sleeps are a hard reject for
  `api_sdk_integration` when issue text contains retry or backoff language.
  Other code shapes should start with a soft warning until there is more
  empirical evidence.
- Callgraph evidence threshold remains open for v2.1.
- Model divergence from discovery remains open until more runs show the safe
  promotion protocol.

## Implementation Order

1. Add this matrix as the canonical code-shape ontology.
2. Add a lightweight code-shape classifier before cookbook rendering.
3. Add issue obligation extraction for checklist-style issue text.
4. Extend verify to emit coverage records.
5. Generate PR bodies from coverage records plus diff evidence.
6. Block complete-fix PRs when obligations are missing.

## Open Questions

- How much callgraph evidence is enough for API/SDK integrations in v2.1?
- How should a model promote a new target when it correctly diverges from
  discovery?
