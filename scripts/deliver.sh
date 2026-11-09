#!/bin/sh
# ATM's delivery: every run works in a fresh clone, so no-mistakes is initialised there first.
# --wait 24h: axi run holds until the gate stops for an answer or ends; its default 8 minute hold would end this
# command with exit 1 while the gate goes on, and the run would take Delivery as failed.
no-mistakes init || exit 1
exec no-mistakes axi run --intent "$ATM_INTENT" --wait 24h
