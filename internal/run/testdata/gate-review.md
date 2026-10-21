`gate-review.toon` uses the public schema observed on 21 October 2026 from
no-mistakes v1.99.0 (12ad62f):

```
no-mistakes axi logs --run 01M5E06NTDTW76BKYYXQ2ND5YX --step review --full
```

The finding ID, file and description and the log contents are replaced with
the issue 509 acceptance example. The public table is
`findings[N]{id,severity,file,action,description}`. It has no source line
column, even when the underlying agent log contains JSON findings with
`line` values. The screen must not infer positions from agent prose or add
the former fixture's invented `:42`. The risk shown is the public severity,
`error`, rather than an invented `risk` field.

Logs are `log[N]{line}` rows. Without `--full`, the same command reported
`lines: 40 of 75 total (tail)` and `log[40]{line}`. The long-log acceptance
extends this three-line fixture beyond that tail and checks the original
first line and every subsequent line through the real attach screen.
