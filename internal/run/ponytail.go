package run

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// families are slopslint's families for a standing tombstone, the one a ponytail finding leaves.
var families = []string{"agent_artifact_in_repo", "documented_as_convention", "environment_layout_coupling",
	"format_churn", "inline_foreign_language", "mock_heavy_test", "runtime_dependency", "self_validating_test",
	"speculative_feature", "speculative_hardening", "subprocess_foreign_interpreter", "test_weakening"}

type finding struct {
	File, Family, Finding string
	IssueSentence         *string `json:"issue_sentence"` // the sentence of the issue the cut touches, "" for none
}

// ponytail is the slop detector on the run's work in clone, from base commit sha, for issue i after its unit
// is proven: an agent that may only cut it. A cut that holds, a shorter diff that passes the unit's proof and
// every check again, leaves two commits: the unit then the cut with a tombstone per finding. A file named by a
// finding with a missing issue sentence, or one the cut deletes, adds, or touches as a test or outside the
// run's diff, is put back alone; what remains is judged,
// and hold undoes the files that fail the checks. A cut that still fails policy or the checks is undone. A
// commit by the agent or a re-check command fails the run.
func (a agent) ponytail(root, clone, sha string, proofs []proof, i Issue, c config.Config) (map[string]any,
	error) {
	base, err := git(clone, "rev-parse", "HEAD") // the last unit's
	if err != nil {
		return nil, err
	}
	before, files, text, err := review(a.dir, clone, sha, i, c)
	if err != nil {
		return nil, err
	}
	if _, err := placeSkill(clone, a.Harness, "ponytail-review"); err != nil { // ATM's file, before the cut
		return nil, err
	}
	all, err := whole(clone) // the ignored files too
	if err != nil {
		return nil, err
	}
	var found []finding
	ev, err := a.run(clone, "ponytail", "ponytail-review", text, func(r map[string]any) (err error) {
		found, err = findings(r, files)
		return err
	})
	if err != nil {
		return ev, err
	}
	if err := moved(clone, base, "the ponytail agent"); err != nil {
		return ev, err
	}
	after, err := snapshot(clone)
	if err != nil {
		return ev, err
	}
	why, lines, found, rejectedFiles, err := judge(clone, sha, before, after, all, files, found, c.TestPatterns)
	ev["rejected_files"] = rejectedFiles
	if err != nil {
		return ev, err
	}
	ev["net_lines"] = lines
	if why == "" {
		if why, err = fresh(clone, all); err != nil {
			return ev, err
		}
	}
	if why == "" {
		if why, found, err = hold(ev, a.dir, clone, sha, base, before, all, files, found, proofs, c); err != nil {
			return ev, err
		}
	}
	if err := moved(clone, base, "the commands"); err != nil {
		return ev, err
	}
	ev["kept"] = why == ""
	if why != "" {
		ev["reason"] = why
		ev["rejected_files"], err = restoreCut(clone, all, why, ev["rejected_files"].([]rejectedFile))
		return ev, err
	}
	ev["commits"], ev["tombstones"], err = commit(root, clone, sha, before, i, found)
	return ev, err
}

// restoreCut puts the remaining cut back and records why, preserving each file's earlier rejection reason.
func restoreCut(clone, tree, why string, rejectedFiles []rejectedFile) ([]rejectedFile, error) {
	now, err := whole(clone)
	if err != nil {
		return rejectedFiles, err
	}
	changed, err := every(clone, "diff", "--name-only", "--no-renames", "-z", tree, now)
	if err != nil {
		return rejectedFiles, err
	}
	for _, path := range strings.Split(changed, "\x00") {
		if path != "" && !slices.ContainsFunc(rejectedFiles, func(f rejectedFile) bool { return f.Path == path }) {
			rejectedFiles = append(rejectedFiles, rejectedFile{Path: path, Reason: why})
		}
	}
	slices.SortFunc(rejectedFiles, func(a, b rejectedFile) int { return strings.Compare(a.Path, b.Path) })
	return rejectedFiles, restore(clone, tree)
}

// hold is undo on the cut left in clone, whole's tree all before it, on the run's work from sha, tree before,
// which changes files. It records in ev the last re-proofs and their commands, and each file undone beside
// those already rejected. What it keeps is fault again, the findings of its files kept.
func hold(ev map[string]any, runDir, clone, sha, base, before, all string, files []string, found []finding,
	proofs []proof, c config.Config) (string, []finding, error) {
	why, ran, cmds, undone, err := undo(runDir, clone, base, all, proofs, c)
	ev["reproofs"], ev["commands"] = ran, cmds
	rejectedFiles, _ := ev["rejected_files"].([]rejectedFile)
	for path, reason := range undone {
		rejectedFiles = append(rejectedFiles, rejectedFile{Path: path, Reason: reason})
	}
	slices.SortFunc(rejectedFiles, func(a, b rejectedFile) int { return strings.Compare(a.Path, b.Path) })
	ev["rejected_files"] = rejectedFiles
	if err != nil || why != "" || len(undone) == 0 {
		return why, found, err
	}
	found = slices.DeleteFunc(found, func(f finding) bool { return undone[f.File] != "" })
	after, err := snapshot(clone)
	if err == nil {
		why, ev["net_lines"], err = fault(clone, sha, before, after, files, found, c.TestPatterns)
	}
	return why, found, err
}

// undo is reprove on the cut left in clone, whole's tree all before it, that puts back what fails: every cut
// file the failure names, again while it names one, then alone's removal. undone is each file put back with the
// failure it caused; why stays when no part of the cut passes. It re-proves at most twice per cut file, plus
// once.
func undo(runDir, clone, base, all string, proofs []proof, c config.Config) (why string, ran []unitResult,
	cmds []cmdResult, undone map[string]string, err error) {
	undone = map[string]string{}
	cut, err := whole(clone)
	names, e := every(clone, "diff", "--name-only", "--no-renames", "-z", all, cut)
	left := strings.Split(strings.TrimRight(names, "\x00"), "\x00")
	for err = errors.Join(err, e); err == nil; {
		if why, ran, cmds, err = reprove(runDir, clone, base, proofs, c); err != nil || why == "" {
			return why, ran, cmds, undone, err
		}
		named := slices.DeleteFunc(slices.Clone(left), func(p string) bool {
			named := regexp.MustCompile(`(^|[[:space:]"'([{=:;,])(\./)?` + regexp.QuoteMeta(p) + `($|[[:space:]"')\]}:;,])`)
			return !named.MatchString(strings.ReplaceAll(why, `\`, "/"))
		})
		if len(named) == 0 {
			break
		}
		for _, p := range named {
			undone[p] = why
		}
		left = slices.DeleteFunc(left, func(p string) bool { return undone[p] != "" })
		if err = checkout(clone, all, named); err != nil || len(left) == 0 {
			return why, ran, cmds, undone, err
		}
	}
	if len(left) == 1 || err != nil { // undoing it leaves no cut
		return why, ran, cmds, undone, err
	}
	p, r, cm, err := alone(runDir, clone, base, all, cut, left, proofs, c)
	if p == "" || err != nil {
		return why, ran, cmds, undone, err
	}
	undone[p] = why
	return "", r, cm, undone, nil
}

// alone puts back in clone each of left, the cut files, from tree all, once and on its own, and is the first
// whose removal passes reprove, with that re-proof and its commands, "" when none does. Each file that does not
// comes back from tree cut.
func alone(runDir, clone, base, all, cut string, left []string, proofs []proof, c config.Config) (p string,
	ran []unitResult, cmds []cmdResult, err error) {
	for i := 0; err == nil && i < len(left); i++ {
		why := ""
		if err = checkout(clone, all, left[i:i+1]); err == nil {
			why, ran, cmds, err = reprove(runDir, clone, base, proofs, c)
		}
		if err != nil || why == "" {
			return left[i], ran, cmds, err
		}
		err = checkout(clone, cut, left[i:i+1])
	}
	return "", nil, nil, err
}

// checkout puts each of paths back in clone byte for byte from tree, whole's.
func checkout(clone, tree string, paths []string) error {
	_, err := every(clone, append([]string{"--literal-pathspecs", "checkout", tree, "--"}, paths...)...)
	return err
}

// reprove says why the work in clone fails the unit's proof or checks again, or "" when neither fails. It
// returns the repeated proof and check commands.
func reprove(runDir, clone, base string, history []proof, c config.Config) (string, []unitResult, []cmdResult, error) {
	for n, p := range history {
		if p.missing || !testedType(p.typ) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(clone, p.test))
		if err != nil || string(content) != p.testContent {
			return fmt.Sprintf("unit %d: test %s changed since it was proven", n+1, p.test), nil, nil, nil
		}
	}
	var ran []unitResult
	for n, p := range history[:len(history)-1] {
		if _, err := git(clone, "reset", "-q", p.base); err != nil {
			return "", ran, nil, err
		}
		restoreATM, err := hideATM(clone, runDir)
		if err != nil {
			return "", ran, nil, err
		}
		var red, green try
		var proofErr error
		proofSpan, proofErr := row(liveIn(clone), proofs[p.typ], func() error {
			red, green, proofErr = prove(clone, p.typ, filepath.ToSlash(p.test), c)
			return proofErr
		})
		proofErr = errors.Join(proofErr, restoreATM())
		ran = append(ran, unitResult{Unit: n + 1, Base: p.base, TestFile: p.test, Type: p.typ,
			Red: red, Green: green, Result: outcome(proofErr), span: proofSpan})
		if e := moved(clone, p.base, "the commands"); e != nil {
			return "", ran, nil, e
		}
		if _, e := git(clone, "reset", "-q", base); e != nil {
			return "", ran, nil, e
		}
		if proofErr != nil {
			return fmt.Sprintf("unit %d: %v", n+1, proofErr), ran, nil, nil
		}
	}
	last := history[len(history)-1]
	result, err := checks(clone, runDir, base, last.typ, last.test, last.missing, c)
	red, _ := result["red"].(try)
	green, _ := result["green"].(try)
	proofSpan, _ := result["proof_span"].(span)
	ran = append(ran, unitResult{Unit: len(history), Base: last.base, TestFile: last.test, Type: last.typ,
		Red: red, Green: green, Result: outcome(err), span: proofSpan})
	cmds, _ := result["commands"].([]cmdResult)
	if err != nil {
		return err.Error(), ran, cmds, nil
	}
	return "", ran, cmds, nil
}

// review is the run's work in clone, at base commit sha, as a tree, the files it changes, and its
// brief-ponytail.md for issue i under config c, written next to brief.md in the run's directory dir.
func review(dir, clone, sha string, i Issue, c config.Config) (tree string, files []string, text string,
	err error) {
	if tree, err = snapshot(clone); err != nil {
		return
	}
	diff, err := git(clone, "diff", sha, tree)
	if err != nil {
		return
	}
	names, err := git(clone, "diff", "--name-only", "-z", sha, tree)
	if err != nil {
		return
	}
	files = strings.Split(strings.TrimRight(names, "\x00"), "\x00")
	if text, err = ponytailBrief(i, c, diff); err == nil {
		err = os.WriteFile(filepath.Join(dir, "brief-ponytail.md"), []byte(text), 0o644)
	}
	return
}

// fault says why the cut from tree before to tree after, on the run's work from sha, which changes files,
// does not hold even before the checks, "" when it may: it must shorten the diff, report what it cut with an
// issue sentence, and add or delete no file, touch no test nor file outside files. lines are the
// net lines the run adds before and after it.
func fault(clone, sha, before, after string, files []string, found []finding, tests []string) (why string, lines [2]int,
	err error) {
	for n, tree := range []string{before, after} {
		if lines[n], err = added(clone, sha, tree); err != nil {
			return "", lines, err
		}
	}
	changed, err := git(clone, "diff", "--name-status", "--no-renames", "-z", before, after)
	switch {
	case lines[1] >= lines[0]:
		return fmt.Sprintf("the cut does not shorten the diff: %d net added lines, %d before", lines[1], lines[0]),
			lines, err
	case len(found) == 0:
		return "the cut reports no finding", lines, err
	}
	for _, f := range found {
		if why = sentenceFault(f); why != "" {
			return why, lines, err
		}
	}
	for f := strings.Split(changed, "\x00"); len(f) > 1; f = f[2:] {
		if why = fileFault(f[0], f[1], files, tests); why != "" {
			return why, lines, err
		}
	}
	return "", lines, err
}

// judge is fault on the cut from before to after, once each file a finding names with a missing issue sentence,
// or that the cut deletes, adds, or touches as a test or outside files, is back byte for byte
// from whole's tree all.
// Nothing of the cut left is fault on it as it stands, the same "no cut". kept are the findings of the files
// that stay; rejectedFiles names each rejected file with its rule's reason.
func judge(clone, sha, before, after, all string, files []string, found []finding, tests []string) (why string,
	lines [2]int, kept []finding, rejectedFiles []rejectedFile, err error) {
	rejectedFiles = []rejectedFile{}
	changed, err := git(clone, "diff", "--name-status", "--no-renames", "-z", before, after)
	if err != nil {
		return "", lines, nil, rejectedFiles, err
	}
	bad, live := rejected(changed, files, found, tests)
	for f := strings.Split(changed, "\x00"); len(f) > 1; f = f[2:] {
		if reason := bad[f[1]]; reason != "" {
			rejectedFiles = append(rejectedFiles, rejectedFile{Path: f[1], Reason: reason})
		}
	}
	if !live || len(bad) == 0 {
		why, lines, err = fault(clone, sha, before, after, files, found, tests)
		return why, lines, found, rejectedFiles, err
	}
	if err = restoreFiles(clone, all, changed, bad); err != nil {
		return "", lines, nil, rejectedFiles, err
	}
	if after, err = snapshot(clone); err != nil {
		return "", lines, nil, rejectedFiles, err
	}
	kept = slices.DeleteFunc(found, func(f finding) bool { return bad[f.File] != "" })
	why, lines, err = fault(clone, sha, before, after, files, kept, tests)
	return why, lines, kept, rejectedFiles, err
}

// rejected is each file of changed, the cut's name-status, that does not stay, and whether any file of it does.
func rejected(changed string, files []string, found []finding, tests []string) (bad map[string]string, live bool) {
	bad = map[string]string{}
	for _, f := range found {
		if why := sentenceFault(f); why != "" && bad[f.File] == "" {
			bad[f.File] = why
		}
	}
	for f := strings.Split(changed, "\x00"); len(f) > 1; f = f[2:] {
		if why := fileFault(f[0], f[1], files, tests); why != "" && bad[f[1]] == "" {
			bad[f[1]] = why
		}
		live = live || bad[f[1]] == ""
	}
	return bad, live
}

// sentenceFault is why finding f lacks its issue sentence, "" when it has one: the sentence it quotes, recorded,
// decides nothing.
func sentenceFault(f finding) string {
	if f.IssueSentence == nil {
		return fmt.Sprintf("the cut touches the issue: finding %q lacks issue_sentence", f.Finding)
	}
	return ""
}

// fileFault is why the cut's change of path, status its name-status, does not hold, "" when it may.
func fileFault(status, path string, files, tests []string) string {
	switch {
	case status == "D":
		return "the cut deletes " + path
	case first([]string{path}, tests, true) != "":
		return "the cut touches the test " + path
	case status == "A":
		return "the cut adds " + path
	case !slices.Contains(files, path):
		return "the cut touches " + path + ", outside the run's diff"
	}
	return ""
}

// restoreFiles puts each path of changed, the cut's name-status, that bad names back to whole's tree in clone.
// An added file is removed; any other is checked out byte for byte.
func restoreFiles(clone, tree, changed string, bad map[string]string) error {
	var back []string
	for f := strings.Split(changed, "\x00"); len(f) > 1; f = f[2:] {
		if bad[f[1]] == "" {
			continue
		}
		if f[0] == "A" {
			if err := os.Remove(filepath.Join(clone, filepath.FromSlash(f[1]))); err != nil {
				return err
			}
			continue
		}
		back = append(back, f[1])
	}
	if len(back) == 0 {
		return nil
	}
	_, err := every(clone, append([]string{"--literal-pathspecs", "checkout", tree, "--"}, back...)...)
	return err
}

// findings is the ponytail agent's report r, which holds findings and a summary and nothing else, each
// finding on one of files, of one of families, in one line.
func findings(r map[string]any, files []string) ([]finding, error) {
	b, _ := json.Marshal(r)
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var got struct {
		Findings *[]finding
		Summary  *string
	}
	if err := dec.Decode(&got); err != nil {
		return nil, fmt.Errorf("invalid ponytail report: %w", err)
	}
	if got.Findings == nil || got.Summary == nil {
		return nil, errors.New("invalid ponytail report: want findings and summary")
	}
	for _, f := range *got.Findings {
		switch {
		case !slices.Contains(files, f.File):
			return nil, fmt.Errorf("invalid ponytail report: %q is not a file of the diff", f.File)
		case !slices.Contains(families, f.Family):
			return nil, fmt.Errorf("invalid ponytail report: %q is not a slop family", f.Family)
		case f.Finding == "" || strings.ContainsAny(f.Finding, "\r\n"):
			return nil, fmt.Errorf("invalid ponytail report: finding %q is not one line", f.Finding)
		}
	}
	return *got.Findings, nil
}

// snapshot is the tree of everything in clone's working tree, its index left as HEAD.
func snapshot(clone string) (string, error) {
	if _, err := git(clone, "add", "-A"); err != nil {
		return "", err
	}
	tree, err := git(clone, "write-tree")
	_, e := git(clone, "reset", "-q")
	return tree, errors.Join(err, e)
}

// fresh names the first file in clone, ignored or not, that tree, whole's before the cut, lacks: "" for none.
func fresh(clone, tree string) (string, error) {
	now, err := whole(clone)
	if err != nil {
		return "", err
	}
	out, err := every(clone, "diff", "--name-only", "--diff-filter=A", "-z", tree, now)
	if f, _, _ := strings.Cut(out, "\x00"); f != "" {
		return "the cut adds " + f, err
	}
	return "", err
}

// restore brings clone's working tree back to tree, whole's, new files gone, the ignored ones too, and its
// index to HEAD.
func restore(clone, tree string) error {
	for _, args := range [][]string{{"add", "-A", "-f", "."}, {"read-tree", "-u", "--reset", tree}} {
		if _, err := every(clone, args...); err != nil {
			return err
		}
	}
	_, err := git(clone, "reset", "-q")
	return err
}

// added is the net lines the diff from sha to tree adds; a binary file adds none.
func added(clone, sha, tree string) (int, error) {
	out, err := git(clone, "diff", "--numstat", sha, tree)
	n := 0
	for _, line := range strings.Split(out, "\n") {
		var plus, minus int
		if _, e := fmt.Sscanf(line, "%d %d", &plus, &minus); e == nil {
			n += plus - minus
		}
	}
	return n, err
}

// commit commits in clone, on HEAD, the run's unit from base commit sha, tree unit, then the cut, the
// working tree with a tombstone per finding, under the git identity of the repository at root, and returns
// both commits and the tombstones.
func commit(root, clone, sha, unit string, i Issue, found []finding) (commits, tombs []string, err error) {
	id, err := identity(root)
	if err != nil {
		return nil, nil, err
	}
	parent := "HEAD"
	units, err := git(clone, "rev-list", "--count", sha+"..HEAD") // the commits before the unit's
	if err != nil {
		return nil, nil, err
	}
	n, _ := strconv.Atoi(units)
	if tombs, err = tombstones(clone, found); err != nil {
		return nil, tombs, err
	}
	cut, err := snapshot(clone)
	if err != nil {
		return nil, tombs, err
	}
	msg := fmt.Sprintf("ponytail: %d cuts\n", len(found))
	for _, f := range found {
		msg += fmt.Sprintf("\n- %s: %s (%s)", f.File, f.Finding, f.Family)
	}
	for _, c := range [][2]string{{unit, unitMessage(i, n+1)}, {cut, msg}} {
		if parent, err = git(clone, append(id, "commit-tree", c[0], "-p", parent, "-m", c[1])...); err != nil {
			return commits, tombs, err
		}
		commits = append(commits, parent)
	}
	_, err = git(clone, "reset", "-q", parent)
	return commits, tombs, err
}

// identity is the git identity of the repository at root, as -c flags for git in its clone, which does not
// inherit root's own config. None, or an email at localhost, is an error: ATM never makes one up.
func identity(root string) ([]string, error) {
	var flags []string
	for _, who := range []string{"author", "committer"} {
		id, err := git(root, "-c", "user.useConfigOnly=true", "var", "GIT_"+strings.ToUpper(who)+"_IDENT")
		if err != nil {
			return nil, fmt.Errorf("no git %s identity in %s: %w", who, root, err)
		}
		name, rest, _ := strings.Cut(id, " <")
		email, _, _ := strings.Cut(rest, ">")
		if strings.HasSuffix(email, "@localhost") {
			return nil, fmt.Errorf("the git %s identity of %s is %s, at localhost", who, root, email)
		}
		flags = append(flags, "-c", who+".name="+name, "-c", who+".email="+email)
	}
	return flags, nil
}

// tombstones writes in clone a slopslint tombstone for each finding and returns their paths. fault rejects
// deleted files before this call so each tombstone names an existing artifact.
func tombstones(clone string, found []finding) ([]string, error) {
	now := time.Now()
	var paths []string
	for n, f := range found {
		id := fmt.Sprintf("T-PONYTAIL-%s-%d", now.Format("20060102-150405"), n+1)
		rel := ".slop/tombstones/" + id + ".yml"
		path := filepath.Join(clone, filepath.FromSlash(rel))
		example := f.Finding
		if f.IssueSentence != nil && *f.IssueSentence != "" {
			example += "\nissue_sentence: " + *f.IssueSentence
		}
		text := fmt.Sprintf(`schema: 1
id: %[1]s
status: accepted
category: alien_code
title: %[2]q
created_at: %[3]s
incident:
  pattern: %[2]q
  what_went_wrong: "A coding agent wrote it and the ponytail pass cut it before delivery."
  root_cause: "The coding agent wrote more than the issue needed."
  rule_established: "Cut by the ponytail pass; it does not come back."
  evidence:
    - family: %[4]s
      example: %[6]q
      artifact: %[5]q
match:
  family: %[4]s
  artifact: %[5]q
`, id, f.Finding, now.Format("2006-01-02"), f.Family, f.File, example)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return paths, err
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			return paths, err
		}
		paths = append(paths, rel)
	}
	return paths, nil
}
