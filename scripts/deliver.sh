#!/bin/sh
# ATM's delivery: every run works in a fresh clone, so no-mistakes is initialised there first.
no-mistakes init || exit 1
exec no-mistakes axi run --intent "$ATM_INTENT"
