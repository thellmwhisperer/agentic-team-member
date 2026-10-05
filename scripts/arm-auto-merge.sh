#!/bin/sh
# Arms GitHub auto-merge by rebase on PR $1: GitHub merges it once main's required checks pass.
set -eu
gh pr merge "$1" --auto --rebase --repo "$GITHUB_REPOSITORY"
