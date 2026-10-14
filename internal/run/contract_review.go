package run

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// reviewContract has the agent review, read only, the commit head in clone, from base commit base, against
// issue i, tombs the slop detector's tombstones, and returns why the delivery may not go on, nil when it
// approved them as they still are. The issue, base, head, report and reason go to contract-review.json in the
// run's directory.
func (a agent) reviewContract(clone, base, head, tombs string, i Issue) error {
	rec := map[string]any{"issue": i.Text, "base": base, "head": head}
	err := a.contractReview(clone, base, head, tombs, i, rec)
	rec["approved"], rec["reason"] = err == nil, ""
	if err != nil {
		rec["reason"] = err.Error()
	}
	b, _ := json.MarshalIndent(rec, "", "  ")
	return errors.Join(err, os.WriteFile(filepath.Join(a.dir, "contract-review.json"), b, 0o644))
}

func (a agent) contractReview(clone, base, head, tombs string, i Issue, rec map[string]any) error {
	diff, err := git(clone, "diff", base, head)
	if err != nil {
		return err
	}
	text := reviewBrief(intent(i.Text, tombs), base, head, diff)
	if _, err := placeSkill(clone, a.Harness, "ponytail-review"); err != nil { // ATM's file, before the snapshot
		return err
	}
	tree, err := whole(clone) // the ignored files too
	if err != nil {
		return err
	}
	ev, err := a.run(clone, "contract-review", "ponytail-review", text, func(r map[string]any) error {
		return approved(r, base, head)
	})
	rec["report"] = ev["report"]
	now, e := whole(clone)
	if e == nil && now != tree {
		e = errors.New("the contract reviewer changed the clone")
	}
	left, dirty := git(clone, "status", "--porcelain")
	if dirty == nil && left != "" {
		dirty = errors.New("the contract reviewer left uncommitted changes: " + left)
	}
	return errors.Join(err, e, dirty, moved(clone, head, "the contract reviewer"))
}

// approved is nil when the contract reviewer's report r, on base commit base and HEAD head, approves them, and
// else why not: a block, with its findings, or a report that is not one.
func approved(r map[string]any, base, head string) error {
	b, _ := json.Marshal(r)
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var got struct {
		Disposition, Base, Head, Summary *string
		Findings                         *[]struct{ Criterion, Evidence string }
	}
	if err := dec.Decode(&got); err != nil {
		return fmt.Errorf("invalid contract review: %w", err)
	}
	if got.Findings == nil || slices.Contains([]*string{got.Disposition, got.Base, got.Head, got.Summary}, nil) {
		return errors.New("invalid contract review: want disposition, base, head, findings and summary")
	}
	if *got.Base != base || *got.Head != head {
		return fmt.Errorf("contract review of base %s and HEAD %s, not base %s and HEAD %s", *got.Base, *got.Head,
			base, head)
	}
	var why []string
	for _, f := range *got.Findings {
		if f.Criterion == "" || f.Evidence == "" {
			return errors.New("invalid contract review: a finding without criterion or evidence")
		}
		why = append(why, f.Criterion+" "+f.Evidence)
	}
	switch {
	case *got.Disposition == "approved" && len(why) == 0:
		return nil
	case *got.Disposition == "blocked" && len(why) > 0:
		return errors.New("the contract review blocked the delivery: " + strings.Join(why, "; "))
	}
	return fmt.Errorf("invalid contract review: disposition %q with %d findings", *got.Disposition, len(why))
}

// reviewBrief is the contract reviewer's brief: issue, the diff from base commit base to HEAD head.
func reviewBrief(issue, base, head, diff string) string {
	return "Read `AGENTS.md` at the clone root if it exists. Where it and this brief disagree, this brief wins.\n\n" +
		"# Contract review\n\n## GOAL\nThe commit below is about to be delivered for this issue. Decide whether it " +
		"does what the issue asks, and nothing it does not. Review only: change nothing.\n\n## ISSUE\n" + issue +
		"\n\n## COMMIT\n- Base: " + base + "\n- HEAD: " + head + "\n\n## RULES\n" +
		"Use the `ponytail-review` skill for its one-line findings; this brief wins over its scope: judge the diff " +
		"against the issue, not its complexity.\n" +
		"- Read only: edit, create, delete, stage, commit or check out nothing. ATM blocks the delivery if the clone " +
		"or HEAD changes.\n" +
		"- Block when the diff breaks a criterion or invariant the issue makes mandatory, or changes what is outside " +
		"the issue's scope.\n" +
		"- Each finding quotes the issue's criterion or invariant verbatim and points at its evidence in the diff.\n\n" +
		"## SCOPE\n- Read, search and run only inside this clone: never the repository around it, `.atm/runs/` or " +
		"another clone.\n- No network.\n\n## FORBIDDEN\n- `git commit`, `git push`, `git rebase`, any `gh` command.\n\n" +
		"## DIFF\n```diff\n" + diff + "\n```\n\n## REPORT\nYour final message must be ONLY this JSON object, " +
		"nothing else:\n" + `{"disposition": "<approved or blocked>", "base": "` + base + `", "head": "` + head +
		`", "findings": [{"criterion": "<the issue's criterion or invariant, verbatim>", "evidence": "<file:line ` +
		`in the diff and what it does>"}], "summary": "<one paragraph>"}` + "\nUse `\"approved\"` with " +
		"`\"findings\": []` only when nothing blocks; `\"blocked\"` needs at least one finding.\n"
}
