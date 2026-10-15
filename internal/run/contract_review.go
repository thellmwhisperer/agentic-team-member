package run

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	dir, err := placeSkill(clone, a.Harness, "task-slicing") // before readonly's snapshot, like ponytail-review
	if err != nil {
		return err
	}
	if dir == "" { // ACP distributes skills in the prompt; ponytail-review still supplies its proof field.
		b, _ := skills.ReadFile("task-slicing/SKILL.md")
		text += "\n## SKILL `task-slicing`\n" + string(b)
	} else {
		text += "\nRead the complete `task-slicing` skill at `" + filepath.Join(dir, "SKILL.md") + "`.\n"
	}
	if a.Harness == "pi" {
		a.args = append(slices.Clone(a.args), "--skill", dir)
	}
	return a.readonly(clone, head, "contract reviewer", func() error {
		ev, err := a.run(clone, "contract-review", "ponytail-review", text, func(r map[string]any) error {
			return approved(r, base, head)
		})
		rec["report"] = ev["report"]
		left, dirty := git(clone, "status", "--porcelain")
		if dirty == nil && left != "" {
			dirty = errors.New("the contract reviewer left uncommitted changes: " + left)
		}
		return errors.Join(err, dirty)
	})
}

// readonly runs review, an agent's with the ponytail-review skill, in clone, and adds why it was not read only:
// who changed the clone, ignored files too, or moved its HEAD from head.
func (a agent) readonly(clone, head, who string, review func() error) error {
	if _, err := placeSkill(clone, a.Harness, "ponytail-review"); err != nil { // ATM's file, before the snapshot
		return err
	}
	tree, err := whole(clone)
	if err != nil {
		return err
	}
	err = review()
	now, e := whole(clone)
	if e == nil && now != tree {
		e = errors.New("the " + who + " changed the clone")
	}
	return errors.Join(err, e, moved(clone, head, "the "+who))
}

// reviewScope has the agent review, read only, the paths follow-up f's unit n would change, on commit head in
// clone, against issue i, as it was first read, and keeps the review in f.Scope: nil when it approved them, and
// else why not.
func (a agent) reviewScope(clone, head string, n int, i Issue, f *followUp) error {
	s := &scopeReview{Paths: proposed(*f)}
	err := a.scopeReview(clone, head, n, i, *f, s)
	s.Approved, f.Scope = err == nil, s
	if err != nil {
		s.Reason = err.Error()
	}
	return err
}

func (a agent) scopeReview(clone, head string, n int, i Issue, f followUp, s *scopeReview) error {
	if len(f.Paths) == 0 {
		return fmt.Errorf("follow-up %s has no proposed paths for unit %d", f.RedTest, n)
	}
	text := scopeBrief(i.Text, n, f, s.Paths)
	return a.readonly(clone, head, "scope reviewer", func() error {
		ev, err := a.run(clone, "scope-review-unit"+strconv.Itoa(n), "ponytail-review", text,
			func(r map[string]any) error { return scopeApproved(r, s.Paths, i.Text) })
		s.Report, _ = ev["report"].(map[string]any)
		return err
	})
}

// scopeApproved is nil when the scope reviewer's report r approves paths, and else why not: a block, with each
// path, the clause of issue it breaks, verbatim, and why, or a report that is not one.
func scopeApproved(r map[string]any, paths []string, issue string) error {
	b, _ := json.Marshal(r)
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var got struct {
		Disposition, Summary *string
		Paths                *[]string
		Findings             *[]struct{ Path, Clause, Reason string }
	}
	if err := dec.Decode(&got); err != nil {
		return fmt.Errorf("invalid scope review: %w", err)
	}
	if got.Disposition == nil || got.Summary == nil || got.Paths == nil || got.Findings == nil {
		return errors.New("invalid scope review: want disposition, paths, findings and summary")
	}
	if !slices.Equal(*got.Paths, paths) {
		return fmt.Errorf("scope review of paths %q, not the proposed %q", *got.Paths, paths)
	}
	why, err := scopeFindings(*got.Findings, paths, issue)
	switch {
	case err != nil:
		return err
	case *got.Disposition == "approved" && len(why) == 0:
		return nil
	case *got.Disposition == "blocked" && len(why) > 0:
		return errors.New("the scope review blocked the follow-up: " + strings.Join(why, "; "))
	}
	return fmt.Errorf("invalid scope review: disposition %q with %d findings", *got.Disposition, len(why))
}

// scopeFindings is each finding of a scope review on paths, its path, clause and reason, or why one is not a
// finding: a path not of paths, a clause not in issue verbatim but for line breaks, or no reason.
func scopeFindings(fs []struct{ Path, Clause, Reason string }, paths []string, issue string) ([]string, error) {
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	var why []string
	for _, f := range fs {
		if !slices.Contains(paths, f.Path) || f.Clause == "" || f.Reason == "" ||
			!strings.Contains(flat(issue), flat(f.Clause)) {
			return nil, errors.New("invalid scope review: a finding without a proposed path, the issue's clause " +
				"verbatim or a reason")
		}
		why = append(why, fmt.Sprintf("%s: %q: %s", f.Path, f.Clause, f.Reason))
	}
	return why, nil
}

// scopeBrief is the scope reviewer's brief: issue, verbatim, and the paths follow-up f's unit n would change.
func scopeBrief(issue string, n int, f followUp, paths []string) string {
	list, _ := json.Marshal(paths)
	return "Read `AGENTS.md` at the clone root if it exists. Where it and this brief disagree, this brief wins.\n\n" +
		"# Scope review\n\n## GOAL\nATM is about to start unit " + strconv.Itoa(n) + " of this issue on a follow-up. " +
		"Decide whether every path it would change, its red test among them, lies within the scope the issue " +
		"declares and outside what it reserves to another owner. Review only: change nothing.\n\n## ISSUE\n" + issue +
		"\n\n## PLAN\n- Follow-up: " + f.Title + "\n- Criterion: " + f.Criterion + "\n- Red test: " + f.RedTest +
		"\n- Paths: " + string(list) + "\n\n## RULES\nUse the `ponytail-review` skill for its one-line findings; " +
		"this brief wins over its scope: judge the paths against the issue, not code.\n" +
		"- Read only: edit, create, delete, stage, commit or check out nothing. ATM blocks the unit if the clone " +
		"or HEAD changes.\n" +
		"- Block a path outside the issue's declared scope or non-goals, or one it reserves to another owner. The " +
		"criterion the follow-up quotes does not widen them, however needed the path is.\n" +
		"- Block a path the issue's scope does not clearly allow: unknown scope fails closed.\n" +
		"- Each finding names one of the paths, quotes the issue's clause verbatim and says why.\n\n" +
		"## SCOPE\n- Read, search and run only inside this clone: never the repository around it, `.atm/runs/` or " +
		"another clone.\n- No network.\n\n## FORBIDDEN\n- `git commit`, `git push`, `git rebase`, any `gh` command.\n\n" +
		"## REPORT\nYour final message must be ONLY this JSON object, nothing else:\n" +
		`{"disposition": "<approved or blocked>", "paths": ` + string(list) + `, "findings": [{"path": "<one of ` +
		`the paths>", "clause": "<the issue's clause, verbatim>", "reason": "<why>"}], "summary": "<one paragraph>"}` +
		"\nUse `\"approved\"` with `\"findings\": []` only when nothing blocks; `\"blocked\"` needs at least one " +
		"finding.\n"
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
		TaskSlicing                      *taskSlicingReview `json:"task_slicing"`
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
		return slicingApproved(got.TaskSlicing)
	case *got.Disposition == "blocked" && len(why) > 0:
		return errors.Join(errors.New("the contract review blocked the delivery: "+strings.Join(why, "; ")),
			slicingApproved(got.TaskSlicing))
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
		"Also load the complete `task-slicing` skill. Report explicitly whether Non-goals, Invariants, executable " +
		"Acceptance and Split triggers are present in the literal issue, and whether the diff needs splitting. " +
		"Use the skill's task_slicing report schema below, alongside this review's existing fields.\n" +
		"- Judge meaning, including prose without Markdown headings; empty headings do not supply requirements.\n" +
		"- Unready issues and independent contracts in the diff block delivery. Never repair them in this review.\n" +
		"- Optional improvements outside the goal belong in local drafts, not product changes; their absence alone " +
		"does not block delivery. Mandatory pending requirements always block, even in drafts or existing follow-ups.\n" +
		"- Supply drafts with all ten task-slicing contract fields, cause and parent dependency. ATM saves the report " +
		"in the run directory; do not write drafts to the product clone or publish or execute children.\n" +
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
		`in the diff and what it does>"}], "summary": "<one paragraph>", "task_slicing": ` + slicingSchema + `}` +
		"\nUse `\"approved\"` with " +
		"`\"findings\": []` only when nothing blocks; `\"blocked\"` needs at least one finding.\n"
}
