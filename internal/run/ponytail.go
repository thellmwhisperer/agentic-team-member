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
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// families are slopslint's families for a standing tombstone, the one a ponytail finding leaves.
var families = []string{"agent_artifact_in_repo", "documented_as_convention", "environment_layout_coupling",
	"format_churn", "inline_foreign_language", "mock_heavy_test", "runtime_dependency", "self_validating_test",
	"speculative_feature", "speculative_hardening", "subprocess_foreign_interpreter", "test_weakening"}

type finding struct{ File, Family, Finding string }

// ponytail is the slop detector on the run's work in clone, from base commit sha, for issue i as its last unit
// is proven, whose units were proven as proofs: an agent that may only cut it. A cut that holds, a shorter diff
// that passes every unit's proof and every check again, leaves two commits on the units before, the last unit
// then the cut with a tombstone per finding; a cut rejected by policy or checks is undone, while a commit by
// the agent or a re-check command fails the run.
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
	why, lines, err := fault(clone, sha, before, after, files, found, c.TestPatterns)
	if err != nil {
		return ev, err
	}
	ev["net_lines"] = lines
	if why == "" {
		if why, err = reprove(clone, base, proofs, c); err != nil {
			return ev, err
		}
	}
	if err := moved(clone, base, "the commands"); err != nil {
		return ev, err
	}
	ev["kept"] = why == ""
	if why != "" {
		ev["reason"] = why
		return ev, restore(clone, before)
	}
	ev["commits"], ev["tombstones"], err = commit(root, clone, sha, before, i.Title, found)
	return ev, err
}

// reprove says why the work in clone, on HEAD base, the last unit's, fails a unit's proof again, each unit
// before the last on its own base, or the last unit's checks, "" when it fails none.
func reprove(clone, base string, proofs []proof, c config.Config) (string, error) {
	for n, p := range proofs {
		content, err := os.ReadFile(filepath.Join(clone, p.test))
		if err != nil || string(content) != p.testContent {
			return fmt.Sprintf("unit %d: test %s changed since it was proven", n+1, p.test), nil
		}
	}
	for n, p := range proofs[:len(proofs)-1] {
		if _, err := git(clone, "reset", "-q", p.base); err != nil {
			return "", err
		}
		err := prove(clone, p.typ, filepath.ToSlash(p.test), c)
		if e := moved(clone, p.base, "the commands"); e != nil {
			return "", e
		}
		if _, e := git(clone, "reset", "-q", base); e != nil {
			return "", e
		}
		if err != nil {
			return fmt.Sprintf("unit %d: %v", n+1, err), nil
		}
	}
	last := proofs[len(proofs)-1]
	if _, err := checks(clone, base, last.typ, last.test, c); err != nil {
		return err.Error(), nil
	}
	return "", nil
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
// does not hold even before the checks, "" when it may: it must shorten the diff, report what it cut, and
// add or delete no file, touch no test nor file outside files. lines are the net lines the run adds before
// and after it.
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
	for f := strings.Split(changed, "\x00"); len(f) > 1; f = f[2:] {
		if f[0] == "D" {
			return "the cut deletes " + f[1], lines, err
		}
		if first([]string{f[1]}, tests, true) != "" {
			return "the cut touches the test " + f[1], lines, err
		}
		if f[0] == "A" {
			return "the cut adds " + f[1], lines, err
		}
		if !slices.Contains(files, f[1]) {
			return "the cut touches " + f[1] + ", outside the run's diff", lines, err
		}
	}
	return "", lines, err
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

// restore brings clone's working tree back to tree, new files gone, and its index to HEAD.
func restore(clone, tree string) error {
	for _, args := range [][]string{{"add", "-A"}, {"read-tree", "-u", "--reset", tree}, {"reset", "-q"}} {
		if _, err := git(clone, args...); err != nil {
			return err
		}
	}
	return nil
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

// commit commits in clone, on HEAD, the run's last unit from base commit sha, tree unit, then the cut, the
// working tree with a tombstone per finding, under the git identity of the repository at root, and returns
// both commits and the tombstones.
func commit(root, clone, sha, unit, title string, found []finding) (commits, tombs []string, err error) {
	id, err := identity(root)
	if err != nil {
		return nil, nil, err
	}
	parent := "HEAD"
	units, err := git(clone, "rev-list", "--count", sha+"..HEAD") // the units chained before it
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
	for _, c := range [][2]string{{unit, fmt.Sprintf("atm unit %d: %s", n+1, title)}, {cut, msg}} {
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
		text := fmt.Sprintf(`schema: 1
id: %[1]s
status: accepted
category: alien_code
title: %[2]q
created_at: %[3]s
incident:
  pattern: %[2]q
  what_went_wrong: "An ATM run wrote it and ATM's ponytail pass cut it before delivery."
  root_cause: "The coding agent wrote more than the issue needed."
  rule_established: "Cut by the ponytail pass; it does not come back."
  evidence:
    - family: %[4]s
      example: %[2]q
      artifact: %[5]q
match:
  family: %[4]s
  artifact: %[5]q
`, id, f.Finding, now.Format("2006-01-02"), f.Family, f.File)
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
