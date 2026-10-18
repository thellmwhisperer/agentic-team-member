package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// maxRepairRounds is the corrective rounds one run may spend in this phase, and so per cause.
// ponytail: fixed cap; a later issue owns configuration and a third round.
const maxRepairRounds = 2

type repairFinding struct{ Criterion, Evidence string }

type repairAttempt struct {
	Round    int          `json:"round"`
	From     string       `json:"from"`
	Head     string       `json:"head,omitempty"`
	Criteria []string     `json:"criteria"`
	Result   string       `json:"result"`
	Reproofs []unitResult `json:"reproofs,omitempty"`
	Commands []cmdResult  `json:"commands,omitempty"`
}

type unmatchedFinding struct {
	Criterion   string `json:"criterion"`
	Evidence    string `json:"evidence"`
	Disposition string `json:"disposition"`
}

type repairDraft struct {
	Purpose   string `json:"purpose"`
	Cause     string `json:"cause"`
	Criterion string `json:"criterion"`
	Evidence  string `json:"evidence"`
	Parent    string `json:"parent"`
}

type repairEvidence struct {
	Rounds    int                `json:"rounds"`
	Heads     []string           `json:"heads"`
	Attempts  []repairAttempt    `json:"attempts,omitempty"`
	Cause     string             `json:"cause,omitempty"`
	Draft     *repairDraft       `json:"draft,omitempty"`
	Unmatched []unmatchedFinding `json:"unmatched,omitempty"`
	Optional  []any              `json:"optional,omitempty"`
}

func (a agent) repairBlocked(clone, base, tombs string, i Issue, root string, history []proof, c config.Config,
	rec map[string]any, err error) (map[string]any, repairEvidence, error) {
	ev := repairEvidence{Heads: []string{textOf(rec, "head")}}
	var last []repairFinding
	_, stray, _ := selectable(err, rec, i.Text)
	ev.keep(stray, optionalDrafts(rec))
	for round := 1; round <= maxRepairRounds; round++ {
		findings, _, ok := selectable(err, rec, i.Text)
		if !ok {
			break
		}
		last = findings
		var next string
		var attempt repairAttempt
		_, stop := row(liveIn(clone), "repair "+strconv.Itoa(round), func() (err error) {
			next, attempt, err = a.oneRepair(clone, root, textOf(rec, "head"), i, history, c, round, findings)
			return err
		})
		ev.Attempts = append(ev.Attempts, attempt)
		if stop != nil {
			err = stop
			ev.fail(i, findings, stop)
			break
		}
		ev.Heads = append(ev.Heads, next)
		_, err = row(liveIn(clone), "review "+strconv.Itoa(round+1), func() (err error) {
			rec, err = a.reviewed(clone, base, next, tombs, i)
			return err
		})
		_, stray, _ = selectable(err, rec, i.Text)
		ev.keep(stray, optionalDrafts(rec))
	}
	ev.finish(i, rec, last, err)
	if ev.Rounds > 0 || len(ev.Unmatched) > 0 {
		rec["repair"] = ev
	}
	return rec, ev, err
}

func (ev *repairEvidence) keep(stray []unmatchedFinding, optional []any) {
	ev.Unmatched = append(ev.Unmatched, stray...)
	ev.Optional = append(ev.Optional, optional...)
}

func (ev *repairEvidence) fail(i Issue, findings []repairFinding, err error) {
	ev.Cause = err.Error()
	ev.Draft = continuationDraft(i, findings, err)
}

func (ev *repairEvidence) finish(i Issue, rec map[string]any, last []repairFinding, err error) {
	ev.Rounds = len(ev.Attempts)
	if err == nil || ev.Cause != "" || ev.Rounds == 0 {
		return
	}
	findings, _, _ := selectable(err, rec, i.Text)
	if len(findings) == 0 {
		findings = last
	}
	ev.fail(i, findings, err)
}

func (a agent) oneRepair(clone, root, head string, i Issue, history []proof, c config.Config, round int,
	findings []repairFinding) (string, repairAttempt, error) {
	attempt := repairAttempt{Round: round, From: head, Criteria: criteriaOf(findings), Result: "blocked"}
	if err := a.repairWork(clone, head, c, round, findings); err != nil {
		return head, attempt, repairErr(round, err)
	}
	if len(history) == 0 {
		return head, attempt, repairErr(round, errors.New("no saved proof"))
	}
	// reprove judges the last unit from the commit it was proven on, work uncommitted, as ponytail does.
	base := history[len(history)-1].base
	if _, err := git(clone, "reset", "-q", base); err != nil {
		return head, attempt, repairErr(round, err)
	}
	why, reproofs, commands, err := reprove(a.dir, clone, base, history, c)
	attempt.Reproofs, attempt.Commands = reproofs, commands
	if err != nil {
		return head, attempt, repairErr(round, err)
	}
	if why != "" {
		return head, attempt, repairErr(round, errors.New(why))
	}
	next, err := commitRepair(root, clone, head, round, i)
	if err != nil {
		return head, attempt, repairErr(round, err)
	}
	attempt.Head, attempt.Result = next, "committed"
	return next, attempt, nil
}

func (a agent) repairWork(clone, head string, c config.Config, round int, findings []repairFinding) error {
	if _, err := a.run(clone, "repair-"+strconv.Itoa(round), "ponytail", repairBrief(findings), testFile); err != nil {
		return err
	}
	after, err := git(clone, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if after != head {
		return fmt.Errorf("the repair agent moved HEAD from %s to %s; it may only edit files", head, after)
	}
	changed, err := git(clone, "diff", "--name-status", "--no-renames", "-z", head)
	for f := strings.Split(changed, "\x00"); err == nil && len(f) > 1; f = f[2:] {
		if f[0] != "A" && first([]string{f[1]}, c.TestPatterns, true) != "" {
			return errors.New("changed protected test " + f[1])
		}
	}
	return err
}

func repairErr(round int, err error) error {
	return fmt.Errorf("contract repair round %d: %w", round, err)
}

func repairBrief(findings []repairFinding) string {
	var b strings.Builder
	b.WriteString("Read `AGENTS.md` at the clone root if it exists. ")
	b.WriteString("Where it and this brief disagree, this brief wins.\n\n")
	b.WriteString("# Contract repair\n\n")
	b.WriteString("Make the minimal change that satisfies each original criterion. ")
	b.WriteString("Do not edit, delete, or weaken original tests or existing tests. ")
	b.WriteString("Never run `git merge`, `git pull`, `git rebase`, `git cherry-pick`, `git reset`, or `git commit`; ")
	b.WriteString("you may only edit files. ")
	b.WriteString("Adding a test does not replace re-running the protected ones.\n\n")
	b.WriteString("Use the `ponytail` skill.\n\n## VIOLATIONS\n")
	for _, f := range findings {
		fmt.Fprintf(&b, "- Criterion: %s\n  Evidence: %s\n", f.Criterion, f.Evidence)
	}
	b.WriteString("\n## REPORT\nYour final message must be ONLY this JSON object, nothing else:\n")
	b.WriteString(`{"test_file":"","changed_files":[],"summary":"","commands_run":[],"follow_ups":[]}` + "\n")
	return b.String()
}

func commitRepair(root, clone, parent string, round int, i Issue) (string, error) {
	id, err := identity(root)
	if err != nil {
		return "", err
	}
	tree, err := snapshot(clone)
	if err != nil {
		return "", err
	}
	parentTree, err := git(clone, "rev-parse", parent+"^{tree}")
	if err != nil {
		return "", err
	}
	if tree == parentTree {
		_, err = git(clone, "reset", "-q", parent)
		return parent, err
	}
	msg := fmt.Sprintf("fix: repair contract round %d\n\n%s", round, i.Title)
	head, err := git(clone, append(id, "commit-tree", tree, "-p", parent, "-m", msg)...)
	if err != nil {
		return "", err
	}
	_, err = git(clone, "reset", "-q", head)
	return head, err
}

func selectable(err error, rec map[string]any, issue string) ([]repairFinding, []unmatchedFinding, bool) {
	if repairStopped(err, rec) {
		return nil, nil, false
	}
	report, _ := rec["report"].(map[string]any)
	fix, stray := splitFindings(report, issue)
	return fix, stray, len(fix) > 0
}

func repairStopped(err error, rec map[string]any) bool {
	return !errors.Is(err, errContractBlocked) || errors.Is(err, errInvalidSlicing) ||
		errors.Is(err, errReviewMutation) || slicingStops(rec)
}

func slicingStops(rec map[string]any) bool {
	report, _ := rec["report"].(map[string]any)
	s, ok := report["task_slicing"].(map[string]any)
	if !ok {
		return true
	}
	if d, _ := s["disposition"].(string); d != "ready" {
		return true
	}
	fields, _ := s["fields"].(map[string]any)
	for _, name := range []string{"non_goals", "invariants", "acceptance", "split_triggers"} {
		f, _ := fields[name].(map[string]any)
		if status, _ := f["status"].(string); status != "present" {
			return true
		}
	}
	return false
}

func splitFindings(report map[string]any, issue string) ([]repairFinding, []unmatchedFinding) {
	raw, _ := report["findings"].([]any)
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	issueFlat := flat(issue)
	var fix []repairFinding
	var stray []unmatchedFinding
	for _, item := range raw {
		m, _ := item.(map[string]any)
		c, _ := m["criterion"].(string)
		e, _ := m["evidence"].(string)
		if c != "" && strings.Contains(issueFlat, flat(c)) {
			fix = append(fix, repairFinding{c, e})
			continue
		}
		stray = append(stray, unmatchedFinding{c, e, strayDisposition(report, c, e)})
	}
	return fix, stray
}

func strayDisposition(report map[string]any, criterion, evidence string) string {
	for _, d := range optionalDrafts(map[string]any{"report": report}) {
		b, _ := json.Marshal(d)
		text := string(b)
		if criterion != "" && strings.Contains(text, criterion) || evidence != "" && strings.Contains(text, evidence) {
			return "optional"
		}
	}
	return "owner"
}

func optionalDrafts(rec map[string]any) []any {
	report, _ := rec["report"].(map[string]any)
	slicing, _ := report["task_slicing"].(map[string]any)
	drafts, _ := slicing["drafts"].([]any)
	var out []any
	for _, d := range drafts {
		m, _ := d.(map[string]any)
		if m["purpose"] == "optional" {
			out = append(out, d)
		}
	}
	return out
}

func continuationDraft(i Issue, findings []repairFinding, err error) *repairDraft {
	var criteria, evidence []string
	for _, f := range findings {
		criteria = append(criteria, f.Criterion)
		evidence = append(evidence, f.Evidence)
	}
	return &repairDraft{Purpose: "required", Cause: err.Error(), Criterion: strings.Join(criteria, "; "),
		Evidence: strings.Join(evidence, "; "), Parent: i.Title}
}

func criteriaOf(findings []repairFinding) []string {
	out := make([]string, len(findings))
	for i, f := range findings {
		out[i] = f.Criterion
	}
	return out
}

func textOf(rec map[string]any, key string) string {
	s, _ := rec[key].(string)
	return s
}
