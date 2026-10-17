#!/bin/sh
# Fail when a changed Go line inside a coverage block is not executed by any end-to-end run of
# the real atm binary: name the lines. Delete them, or write the end-to-end test that reaches
# them from a real atm command. Blocks and diff only, never the content of a line (decision 7664).
set -eu

if [ -z "${GOCOVERDIR:-}" ]; then
	echo "cover-diff: GOCOVERDIR is not set"
	exit 1
fi
base=${ATM_COVER_BASE:-$(git merge-base HEAD origin/main 2>/dev/null || true)}
if [ -z "$base" ]; then
	echo "cover-diff: cannot find the base commit"
	git branch -a
	exit 1
fi
mod=$(go list -m) || { echo "cover-diff: cannot read the module path"; exit 1; }

# The profile lands inside the coverage directory the caller owns, beside the counters it is
# derived from; go tool covdata reads covmeta/covcounters only, so it does not mind the stray file.
profile=$GOCOVERDIR/cover-diff.profile
go tool covdata textfmt -i="$GOCOVERDIR" -o "$profile"
if [ ! -s "$profile" ]; then
	echo "cover-diff: no coverage data in GOCOVERDIR; no end-to-end run of the real atm binary happened"
	exit 1
fi

# ponytail: $out word-splits on spaces, so a path with a space would be reported wrong; this
# repository has none.
top=$(git rev-parse --show-toplevel)
phys=$(cd "$top" && pwd -P)
logical=$phys
if [ "$(pwd -P)" = "$phys" ]; then
	logical=$(pwd -L)
fi
out=$(git diff -U0 "$base" -- '*.go' ':(exclude)*_test.go' ':(exclude)**/testdata/**' |
	awk -v mod="$mod/" -v top="$top/" -v phys="$phys/" -v logical="$logical/" '
		# Profile blocks: name.go:startLine.startCol,endLine.endCol numStmt count, count > 0 meaning
		# an end-to-end run executed the block.
		FNR == NR {
			if (split($0, f, " ") == 3 && f[3] ~ /^[0-9]+$/ && match(f[1], /:[0-9]+\.[0-9]+,/)) {
				file = substr(f[1], 1, RSTART - 1)
				if (index(file, mod) == 1)
					file = substr(file, length(mod) + 1)
				if (index(file, top) == 1)
					file = substr(file, length(top) + 1)
				if (index(file, phys) == 1)
					file = substr(file, length(phys) + 1)
				if (index(file, logical) == 1)
					file = substr(file, length(logical) + 1)
				split(substr(f[1], RSTART + 1), be, ",")
				split(be[1], s, ".")
				split(be[2], e, ".")
				i = ++nblk[file]
				bfrom[file, i] = s[1] + 0
				bto[file, i] = e[1] + 0
				bcnt[file, i] = f[3] + 0
			}
			next
		}
		# Diff: every + line of the new side, files as git names them, line numbers from the hunk.
		/^diff --git / { file = "" }
		/^\+\+\+ / { file = substr($0, 7); next }
		/^@@ / {
			new = ""
			for (i = 1; i <= NF; i++)
				if ($i ~ /^\+[0-9][0-9]*(,.*)?$/) {
					new = substr($i, 2)
					sub(/,.*/, "", new)
					new = new + 0
					break
				}
			next
		}
		/^\+/ && file != "" {
			if (new != "") {
				any = 1
				spanned = hit = 0
				for (i = 1; i <= nblk[file]; i++)
					if (bfrom[file, i] <= new && new <= bto[file, i]) {
						spanned = 1
						if (bcnt[file, i] > 0)
							hit = 1
					}
				if (spanned) {
					counted++
					if (!hit)
						miss[++nmiss] = file ":" new
				}
			}
			new = new + 1
			next
		}
		END {
			# Insertion sort: the report is a handful of lines and the output must not depend on
			# the order the diff and the profile happened to meet.
			for (i = 2; i <= nmiss; i++) {
				v = miss[i]
				for (j = i - 1; j >= 1 && miss[j] > v; j--)
					miss[j + 1] = miss[j]
				miss[j + 1] = v
			}
			print counted + 0, any + 0
			for (i = 1; i <= nmiss; i++)
				print miss[i]
		}
	' "$profile" -
)
set -- $out
counted=$1
any=$2
shift 2
if [ "$#" -gt 0 ]; then
	echo "cover-diff: these changed lines are not executed by any end-to-end run of the real atm binary. Delete them, or write the end-to-end test that reaches them from a real atm command."
	for line; do
		echo "$line"
	done
	exit 1
fi
if [ "$any" -eq 0 ]; then
	echo "cover-diff: no Go changes"
else
	echo "cover-diff: $counted changed lines inside covered blocks, all executed by an end-to-end run"
fi
