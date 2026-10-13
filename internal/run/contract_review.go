package run

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
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
	Target     string `json:"target,omitempty"`
}

func (c contractChild) complete() bool {
	return c.Cause != "" && c.Contract != "" && c.Evidence != "" &&
		(c.Dependency == "blocks_parent" && c.Target != "" || c.Dependency == "independent")
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
	missing := missingIssueGates(body)
	if len(missing) != 0 && r.Status != "split" {
		return fmt.Errorf("issue missing task-slicing %s; return to planning with a split", missing[0])
	}
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
	if r.Status != "passed" && len(r.Children) == 0 {
		return errors.New("invalid contract review: blocked work needs a concrete child contract")
	}
	return r.validateChildren(contractGaps(r, missing))
}

func (r contractReview) validateChildren(gaps []string) error {
	remaining := make(map[string]int, len(gaps))
	for _, gap := range gaps {
		remaining[gap]++
	}
	for _, c := range r.Children {
		if !c.complete() {
			return errors.New("invalid contract review: child needs cause, contract, dependency, target and diff evidence")
		}
		if c.Dependency == "blocks_parent" {
			if remaining[c.Target] == 0 {
				return errors.New("invalid contract review: blocking child does not target an unmet gate or criterion")
			}
			remaining[c.Target]--
		}
	}
	for _, count := range remaining {
		if count > 0 {
			return errors.New("invalid contract review: every unmet gate or criterion needs a blocking child")
		}
	}
	if r.Status == "repair" && len(gaps) == 0 {
		return errors.New("invalid contract review: repair has no unmet criterion")
	}
	return nil
}

func contractGaps(r contractReview, missing []string) []string {
	gaps := make([]string, 0, len(missing)+len(r.Acceptance))
	for _, gate := range missing {
		gaps = append(gaps, "gate:"+gate)
	}
	for gate, ready := range map[string]bool{
		"non_goals": r.Dispatch.NonGoals, "invariants": r.Dispatch.Invariants,
		"executable_acceptance": r.Dispatch.ExecutableAcceptance, "split_triggers": r.Dispatch.SplitTriggers,
		"single_contract": r.Dispatch.SingleContract,
	} {
		if !ready && !contains(gaps, "gate:"+gate) {
			gaps = append(gaps, "gate:"+gate)
		}
	}
	for _, c := range r.Acceptance {
		if !c.Satisfied {
			gaps = append(gaps, "criterion:"+c.Criterion)
		}
	}
	return gaps
}

func missingIssueGates(body string) []string {
	source := []byte(body)
	doc := parser.New().Parse(source)
	seen := map[string]bool{}
	section := ""
	var visit func(ast.Node)
	visit = func(node ast.Node) {
		switch n := node.(type) {
		case *ast.Blockquote:
			return
		case *ast.Heading:
			line := issueText(n, source, false)
			section = issueGateName(line)
			if section == "" {
				if field, value, ok := issueField(line); ok {
					section = issueGateName(field)
					if section != "" && substantiveIssueText(value) {
						seen[section] = true
					}
				}
			}
			return
		case *ast.Paragraph:
			fieldLines, contentLines := issueLines(n, source, false), issueLines(n, source, true)
			for i, content := range contentLines {
				fieldLine := content
				if i < len(fieldLines) {
					fieldLine = fieldLines[i]
				}
				if field, value, ok := issueField(fieldLine); ok {
					section = issueGateName(field)
					if !substantiveIssueText(value) {
						if _, codeValue, codeOK := issueField(content); codeOK {
							value = codeValue
						}
					}
					if section != "" && substantiveIssueText(value) {
						seen[section] = true
					}
					continue
				}
				if section != "" && substantiveIssueText(content) {
					seen[section] = true
				}
			}
			return
		case *ast.CodeBlock:
			if section == "Acceptance criteria" && n.CodeBlockKind == ast.CodeBlockKindFenced &&
				substantiveMarkdown([]byte(n.Value.Str(source))) {
				seen[section] = true
			}
			return
		case *ast.HTMLBlock, *ast.ThematicBreak:
			return
		}
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			visit(child)
		}
	}
	for child := doc.FirstChild(); child != nil; child = child.NextSibling() {
		visit(child)
	}
	var missing []string
	for _, gate := range []struct{ name, key string }{
		{"Non-goals", "non_goals"}, {"Invariants", "invariants"},
		{"Acceptance criteria", "executable_acceptance"}, {"Split triggers", "split_triggers"},
	} {
		if !seen[gate.name] {
			missing = append(missing, gate.key)
		}
	}
	return missing
}

func issueText(node ast.Node, source []byte, code bool) string {
	return strings.Join(issueLines(node, source, code), " ")
}

func issueLines(node ast.Node, source []byte, code bool) []string {
	var line strings.Builder
	var lines []string
	flush := func() {
		lines = append(lines, line.String())
		line.Reset()
	}
	var visit func(ast.Node)
	visit = func(node ast.Node) {
		switch n := node.(type) {
		case *ast.RawHTML:
			return
		case *ast.CodeSpan:
			if !code {
				return
			}
		case *ast.Text:
			line.WriteString(n.Value.Str(source))
			if n.SoftLineBreak() || n.HardLineBreak() {
				flush()
			}
			return
		}
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			visit(child)
		}
	}
	visit(node)
	if line.Len() > 0 || len(lines) == 0 {
		flush()
	}
	return lines
}

func substantiveIssueText(line string) bool {
	for _, r := range line {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func issueField(line string) (title, value string, ok bool) {
	line = strings.TrimSpace(line)
	digits := 0
	for digits < len(line) && line[digits] >= '0' && line[digits] <= '9' {
		digits++
	}
	if digits > 0 && strings.HasPrefix(line[digits:], ". ") {
		line = strings.TrimSpace(line[digits+2:])
	}
	label, value, found := strings.Cut(line, ":")
	if !found {
		return "", "", false
	}
	label, value = strings.TrimSpace(label), strings.TrimSpace(value)
	return label, value, issueFieldName(label)
}

func issueFieldName(label string) bool {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "kind", "goal", "non-goals", "invariants", "behavioral contract / states", "scope surface",
		"acceptance", "acceptance criteria", "split triggers", "dependencies / parent", "evidence":
		return true
	default:
		return false
	}
}

func issueGateName(label string) string {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "non-goals":
		return "Non-goals"
	case "invariants":
		return "Invariants"
	case "acceptance", "acceptance criteria":
		return "Acceptance criteria"
	case "split triggers":
		return "Split triggers"
	default:
		return ""
	}
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func substantiveMarkdown(source []byte) bool {
	doc := parser.New().Parse(source)
	var substantive bool
	var visit func(ast.Node)
	visit = func(node ast.Node) {
		if substantive {
			return
		}
		switch n := node.(type) {
		case *ast.Blockquote, *ast.Heading, *ast.HTMLBlock, *ast.ThematicBreak, *ast.CodeBlock:
			return
		case *ast.Paragraph:
			substantive = substantiveIssueText(issueText(n, source, true))
			return
		}
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			visit(child)
		}
	}
	for child := doc.FirstChild(); child != nil; child = child.NextSibling() {
		visit(child)
	}
	return substantive
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
	for _, f := range pending {
		if quoted(i.Body, f.Criterion) {
			return latest, fmt.Errorf("mandatory criterion unmet: %s; see follow-ups.json", f.Criterion)
		}
	}
	var rounds []contractReview
	for round := 0; round <= 2; round++ {
		var err error
		latest, err = a.contractReviewRound(clone, base, i, round)
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
	return latest, errors.New("contract correction limit exhausted")
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
	if err := moved(clone, head, "contract repair worker"); err != nil {
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

func (a agent) contractReviewRound(clone, base string, i Issue, round int) (r contractReview, err error) {
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

// deliveryMetadata binds review/repair to the prepared branch and Git configuration,
// including origin fetch/push URLs. These live outside the working-tree snapshot.
func deliveryMetadata(clone string) (string, error) {
	branch, err := git(clone, "symbolic-ref", "HEAD")
	if err != nil {
		return "", err
	}
	settings, err := git(clone, "config", "--null", "--list", "--show-origin")
	return branch + "\x00" + settings, err
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
"dependency":"blocks_parent|independent","target":"criterion:<literal issue sentence> or gate:<dispatch field>",
"evidence":"file/hunk and violated issue criterion"}]}.
Enumerate EVERY mandatory criterion/invariant. Passed means all are satisfied and tests/requirements preserved.
Every unmet criterion and dispatch gate needs one blocking child with an exact target. Use
criterion:<exact issue sentence> or gate:<dispatch field>; do not reuse a blocking child for a different gap.
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
