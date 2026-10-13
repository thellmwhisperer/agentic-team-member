package run

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

type contractCriterion struct {
	Criterion string `json:"criterion"`
	Evidence  string `json:"evidence"`
	Satisfied bool   `json:"satisfied"`
}

type contractChild struct {
	Cause      string `json:"cause"`
	Contract   string `json:"contract"`
	Dependency string `json:"dependency"`
	Evidence   string `json:"evidence"`
}

func (c contractChild) complete() bool {
	return c.Cause != "" && c.Contract != "" && c.Evidence != "" &&
		(c.Dependency == "blocks_parent" || c.Dependency == "independent")
}

type dispatchGates struct {
	NonGoals             bool `json:"non_goals"`
	Invariants           bool `json:"invariants"`
	ExecutableAcceptance bool `json:"executable_acceptance"`
	SplitTriggers        bool `json:"split_triggers"`
	SingleContract       bool `json:"single_contract"`
}

func (g dispatchGates) ready() bool {
	return g.NonGoals && g.Invariants && g.ExecutableAcceptance && g.SplitTriggers && g.SingleContract
}

type contractReview struct {
	metadata   string              // Host-owned; config may contain secrets, never serialize it.
	Dispatch   dispatchGates       `json:"dispatch"`
	Head       string              `json:"head"`
	Base       string              `json:"base"`
	Status     string              `json:"status"`
	Acceptance []contractCriterion `json:"acceptance"`
	Children   []contractChild     `json:"children"`
}

func (r contractReview) current(clone string) error {
	head, err := git(clone, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	dirty, err := git(clone, "status", "--porcelain")
	if err != nil {
		return err
	}
	if r.Status != "passed" || r.Head != head || dirty != "" || len(r.Acceptance) == 0 || !r.Dispatch.ready() {
		return errors.New("delivery blocked: missing or stale contract review for current head")
	}
	metadata, err := deliveryMetadata(clone)
	if err != nil {
		return err
	}
	if metadata != r.metadata {
		return errors.New("contract review or repair changed delivery metadata; delivery blocked")
	}
	for _, c := range r.Acceptance {
		if !c.Satisfied {
			return errors.New("delivery blocked: mandatory criterion unmet")
		}
	}
	return nil
}

func decodeContractReview(report map[string]any, body string) (contractReview, error) {
	b, _ := json.Marshal(report)
	var r contractReview
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, fmt.Errorf("invalid contract review: %w", err)
	}
	if r.Head != "" || r.Base != "" || r.Status != "passed" && r.Status != "repair" && r.Status != "split" ||
		r.Acceptance == nil || r.Children == nil {
		return r, errors.New("invalid contract review: want status, acceptance and children; ATM binds head/base")
	}
	return r, r.validate(body)
}

func (r contractReview) validate(body string) error {
	if !r.Dispatch.ready() && r.Status != "split" {
		return errors.New("task-slicing dispatch gates unmet or multiple contracts: return to planning with a split")
	}
	for _, c := range r.Acceptance {
		if !quoted(body, c.Criterion) || c.Evidence == "" {
			return errors.New("invalid contract review: criterion must quote the issue and carry evidence")
		}
		if r.Status == "passed" && !c.Satisfied {
			return errors.New("mandatory criterion unmet")
		}
	}
	if r.Status == "passed" && len(r.Acceptance) == 0 {
		return errors.New("invalid contract review: no acceptance evidence")
	}
	blocking := false
	for _, c := range r.Children {
		if !c.complete() {
			return errors.New("invalid contract review: child needs cause, contract, dependency and diff evidence")
		}
		blocking = blocking || c.Dependency == "blocks_parent"
	}
	if blocking == (r.Status == "passed") {
		return errors.New("invalid contract review: blocked work needs a concrete blocking child, passed work none")
	}
	return nil
}

// inspectContract keeps judgment in the reviewer, and freshness, correction limits and proofs in ATM.
// Two corrections total also bound any one root cause to two, without model-generated cause IDs.
func (a agent) inspectContract(root, clone, base string, i Issue, proofs []proof, pending []followUp,
	c config.Config) (contractReview, error) {
	var latest contractReview
	metadata, err := deliveryMetadata(clone)
	if err != nil {
		return latest, err
	}
	var rounds []contractReview
	for round := 0; ; round++ {
		var err error
		latest, err = a.contractReviewRound(clone, base, i, metadata, round)
		latest.metadata = metadata
		if err != nil {
			return latest, err
		}
		if latest.Status == "repair" && round == 2 {
			latest.Status = "split"
		}
		rounds = append(rounds, latest)
		b, _ := json.MarshalIndent(rounds, "", "  ")
		if err := os.WriteFile(filepath.Join(a.dir, "contract-review.json"), append(b, '\n'), 0600); err != nil {
			return latest, err
		}
		for _, f := range pending {
			if quoted(i.Body, f.Criterion) {
				return latest, fmt.Errorf("mandatory criterion unmet: %s; see follow-ups.json", f.Criterion)
			}
		}
		switch latest.Status {
		case "passed":
			return latest, latest.current(clone)
		case "split":
			return latest, errors.New("contract review requires split: child contracts in contract-review.json for CoS")
		}
		if err := a.repairContract(root, clone, i, proofs, c, latest, round); err != nil {
			return latest, err
		}
	}
}

func (a agent) repairContract(root, clone string, i Issue, history []proof, c config.Config,
	r contractReview, round int) error {
	head := r.Head
	var blockers []contractChild
	for _, child := range r.Children {
		if child.Dependency == "blocks_parent" {
			blockers = append(blockers, child)
		}
	}
	b, _ := json.Marshal(blockers)
	prompt := "# Contract repair\nUse the `ponytail` skill. You are the sole writer. " +
		"Repair only original issue violations. Do not commit, edit tests, weaken requirements, add adjacent features " +
		"or create issues. Return {\"summary\":\"what changed\"}.\n\n" + i.Text + "\n\nViolations:\n" + string(b)
	name := fmt.Sprintf("contract-repair-%d", round)
	_, err := a.run(clone, name, "ponytail", prompt, func(report map[string]any) error {
		s, _ := report["summary"].(string)
		if s == "" {
			return errors.New("repair needs summary")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := checkRepairState(clone, r.metadata, head); err != nil {
		return err
	}
	tree, err := snapshot(clone)
	if err != nil {
		return err
	}
	names, err := git(clone, "diff", "--name-only", "--no-renames", "-z", head, tree)
	if err != nil {
		return err
	}
	for _, name := range strings.Split(names, "\x00") {
		if first([]string{name}, c.TestPatterns, true) != "" {
			return fmt.Errorf("contract repair changed protected test %s", name)
		}
	}
	if err := contractRechecks(a.dir, clone, head, history, c, round); err != nil {
		return err
	}
	checked, err := snapshot(clone)
	if err != nil {
		return err
	}
	if checked != tree {
		return errors.New("contract repair checks changed the work; fresh checks required")
	}
	id, err := identity(root)
	if err != nil {
		return err
	}
	next, err := git(clone, append(id, "commit-tree", tree, "-p", head, "-m", "fix: satisfy delivery contract")...)
	if err != nil {
		return err
	}
	_, err = git(clone, "reset", "-q", next)
	return err
}

func checkRepairState(clone, before, head string) error {
	metadata, err := deliveryMetadata(clone)
	if err != nil {
		return err
	}
	if metadata != before {
		return errors.New("contract repair changed delivery metadata; delivery blocked")
	}
	return moved(clone, head, "contract repair worker")
}

func (a agent) contractReviewRound(clone, base string, i Issue, metadata string,
	round int) (r contractReview, err error) {
	head, err := git(clone, "rev-parse", "HEAD")
	if err != nil {
		return r, err
	}
	diff, err := git(clone, "diff", base, head)
	if err != nil {
		return r, err
	}
	prompt := fmt.Sprintf(contractReviewPrompt, i.Text, base, head, diff)
	name := fmt.Sprintf("contract-review-%d", round)
	if err := os.WriteFile(filepath.Join(a.dir, "brief-"+name+".md"), []byte(prompt), 0600); err != nil {
		return r, err
	}
	if _, err := placeSkill(clone, a.Harness, "task-slicing"); err != nil {
		return r, err
	}
	tree, err := whole(clone)
	if err != nil {
		return r, err
	}
	_, err = a.run(clone, name, "task-slicing", prompt, func(report map[string]any) error {
		var e error
		r, e = decodeContractReview(report, i.Body)
		return e
	})
	if err != nil {
		return r, err
	}
	nowMetadata, err := deliveryMetadata(clone)
	if err != nil {
		return r, err
	}
	if nowMetadata != metadata {
		return r, errors.New("contract reviewer changed delivery metadata; delivery blocked")
	}
	now, err := whole(clone)
	if err != nil {
		return r, err
	}
	if err := moved(clone, head, "contract reviewer"); err != nil {
		return r, err
	}
	if now != tree {
		return r, errors.New("contract reviewer changed the work; delivery blocked")
	}
	r.Head, r.Base = head, base
	return r, nil
}

func deliveryMetadata(clone string) (string, error) {
	branch, err := git(clone, "symbolic-ref", "HEAD")
	if err != nil {
		return "", err
	}
	settings, err := git(clone, "config", "--null", "--list", "--show-origin")
	if err != nil {
		return "", err
	}
	hooks := filepath.Join(clone, ".git", "hooks")
	h := sha256.New()
	err = filepath.WalkDir(hooks, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(hooks, path)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(h, rel+"\x00"+info.Mode().String()+"\x00")
		if entry.IsDir() {
			return nil
		}
		var content []byte
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			content = []byte(link)
		} else {
			content, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		_, err = h.Write(append(content, 0))
		return err
	})
	return branch + "\x00" + settings + "\x00" + fmt.Sprintf("%x", h.Sum(nil)), err
}

func contractRechecks(dir, clone, head string, history []proof, c config.Config, round int) error {
	if len(history) == 0 {
		return errors.New("contract repair has no proofs to rerun")
	}
	proofBase := history[len(history)-1].base
	if _, err := git(clone, "reset", "-q", proofBase); err != nil {
		return err
	}
	why, ran, commands, checkErr := reprove(dir, clone, proofBase, history, c)
	_, resetErr := git(clone, "reset", "-q", head)
	evidence, _ := json.MarshalIndent(map[string]any{
		"head": head, "reproofs": ran, "commands": commands, "reason": why}, "", "  ")
	path := filepath.Join(dir, fmt.Sprintf("contract-rechecks-%d.json", round))
	writeErr := os.WriteFile(path, append(evidence, '\n'), 0600)
	if err := errors.Join(checkErr, resetErr, writeErr); err != nil {
		return err
	}
	if why != "" {
		return fmt.Errorf("contract repair checks: %s", why)
	}
	return nil
}

const contractReviewPrompt = `# Pre-delivery contract review
Load and apply the ` + "`task-slicing`" + ` skill. READ ONLY.
Inspect the actual generated diff against the entire original issue,
all acceptance and invariants. Do not edit or commit. Do not substitute LOC or heuristics for acceptance,
and do not drop requirements or tests. Inspect source and test evidence as needed.
Reject dispatch-unready issues: explicit Non-goals, Invariants, executable Acceptance and Split triggers are mandatory.
Compare actual diff against the single correctable contract. Multiple independent contracts require split/replanning.
Missing gates require split/replanning, never an invented requirement or an automatic repair.
Preserve every requirement.
Return JSON: {"status":"passed|repair|split",
"dispatch":{"non_goals":true,"invariants":true,"executable_acceptance":true,
"split_triggers":true,"single_contract":true},
"acceptance":[{"criterion":"literal issue sentence","evidence":"concrete source/test evidence","satisfied":true}],
"children":[{"cause":"root cause","contract":"concrete corrective or separate follow-up contract",
"dependency":"blocks_parent|independent","evidence":"file/hunk and violated issue criterion"}]}.
Enumerate EVERY mandatory criterion/invariant. Passed means all are satisfied and tests/requirements preserved.
Repair and split need at least one blocks_parent child naming what blocks delivery; passed has none.
Repair is only for original-contract violations. Scope drift or an undecidable contract requires split.
Out-of-goal improvements always become independent children, never repairs. No GitHub issues: CoS creates and
 dispatches children. Retain cause/contract/dependency on every unresolved finding so it is actionable if the
 two-correction limit is reached.

Original issue:
%s
Base: %s
Head: %s
Actual diff:
%s
`
