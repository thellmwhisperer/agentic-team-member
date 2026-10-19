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

// reviewScratch empties the scratch directory of agent call name, a review or a worker, under the run's directory
// and so outside the clone, and points the agent's TMPDIR and the clone boundary check at it: a fresh one per call.
func (a *agent) reviewScratch(name string) (string, error) {
	dir := filepath.Join(a.dir, name+"-scratch")
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	a.env = append(slices.DeleteFunc(slices.Clone(a.env), func(kv string) bool {
		return strings.HasPrefix(kv, "TMPDIR=")
	}), "TMPDIR="+dir)
	a.scratch = dir
	return dir, nil
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
	if changed := errors.Join(e, moved(clone, head, "the "+who)); changed != nil {
		return errors.Join(err, errReviewMutation, changed)
	}
	return err
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
	scratch, err := a.reviewScratch("scope-review-unit" + strconv.Itoa(n))
	if err != nil {
		return err
	}
	text := scopeBrief(i.Text, n, f, s.Paths, scratch)
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
// scratch is the review's own directory outside the clone, where its checks' files go.
func scopeBrief(issue string, n int, f followUp, paths []string, scratch string) string {
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
		"- Write any file a check needs (copies, outputs, temp files) only under `" + scratch +
		"`; never inside the clone.\n" +
		"- Block a path outside the issue's declared scope or non-goals, or one it reserves to another owner. The " +
		"criterion the follow-up quotes does not widen them, however needed the path is.\n" +
		"- Block a path the issue's scope does not clearly allow: unknown scope fails closed.\n" +
		"- Each finding names one of the paths, quotes the issue's clause verbatim and says why.\n\n" +
		"## SCOPE\n- Read inside this clone; write only under `" + scratch + "`: never the repository around it, " +
		"`.atm/runs/` of other runs or another clone.\n- No network.\n\n## FORBIDDEN\n- `git commit`, " +
		"`git push`, `git rebase`, any `gh` command.\n\n" +
		"## REPORT\nYour final message must be ONLY this JSON object, nothing else:\n" +
		`{"disposition": "<approved or blocked>", "paths": ` + string(list) + `, "findings": [{"path": "<one of ` +
		`the paths>", "clause": "<the issue's clause, verbatim>", "reason": "<why>"}], "summary": "<one paragraph>"}` +
		"\nUse `\"approved\"` with `\"findings\": []` only when nothing blocks; `\"blocked\"` needs at least one " +
		"finding.\n"
}

var errReviewMutation = errors.New("review mutation or inspection failure")
