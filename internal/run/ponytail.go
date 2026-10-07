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
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// families are slopslint's families for a standing tombstone, the one a ponytail finding leaves.
var families = []string{"agent_artifact_in_repo", "documented_as_convention", "environment_layout_coupling",
	"format_churn", "inline_foreign_language", "mock_heavy_test", "runtime_dependency", "self_validating_test",
	"speculative_feature", "speculative_hardening", "subprocess_foreign_interpreter", "test_weakening"}

type finding struct{ File, Family, Finding string }

// ponytail is the slop detector on the run's work in clone, at base commit sha, for issue i, whose test is
// test: an agent that may only cut it. A cut that holds, a shorter diff that passes every check again, leaves
// two commits, the unit then the cut with a tombstone per finding; any other cut is undone.
func (a agent) ponytail(root, clone, sha, test string, i Issue, c config.Config) (map[string]any, error) {
	before, files, text, err := review(root, clone, sha, i, c)
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
	head, err := git(clone, "rev-parse", "HEAD")
	if err != nil {
		return ev, err
	}
	if head != sha {
		return ev, fmt.Errorf("the ponytail agent made commits: HEAD is %s, not the base %s", head, sha)
	}
	after, err := snapshot(clone)
	if err != nil {
		return ev, err
	}
	why, lines, err := fault(clone, sha, before, after, files, found)
	if err != nil {
		return ev, err
	}
	ev["net_lines"] = lines
	if why == "" {
		if _, err := checks(clone, sha, i.Type, test, c); err != nil {
			why = err.Error()
		}
	}
	ev["kept"] = why == ""
	if why != "" {
		ev["reason"] = why
		return ev, restore(clone, before)
	}
	ev["commits"], ev["tombstones"], err = commit(root, clone, sha, before, i.Title, found)
	return ev, err
}

// review is the run's work in clone, at base commit sha, as a tree, the files it changes, and its
// brief-ponytail.md for issue i under config c, written next to brief.md in root.
func review(root, clone, sha string, i Issue, c config.Config) (tree string, files []string, text string,
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
		err = os.WriteFile(filepath.Join(root, ".atm", "brief-ponytail.md"), []byte(text), 0o644)
	}
	return
}

// fault says why the cut from tree before to tree after, on the run's work from sha, which changes files,
// does not hold even before the checks, "" when it may: it must shorten the diff, report what it cut, and
// add no file nor touch one outside files. lines are the net lines the run adds before and after it.
func fault(clone, sha, before, after string, files []string, found []finding) (why string, lines [2]int,
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

// commit commits in clone, on sha, the unit, tree unit, then the cut, the working tree with a tombstone per
// finding, under the git identity of the repository at root, and returns both commits and the tombstones.
func commit(root, clone, sha, unit, title string, found []finding) (commits, tombs []string, err error) {
	id, err := identity(root)
	if err != nil {
		return nil, nil, err
	}
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
	// ponytail: one unit, until the run chains its follow-ups.
	parent := sha
	for _, c := range [][2]string{{unit, "atm unit 1: " + title}, {cut, msg}} {
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

// tombstones writes in clone a slopslint tombstone for each finding and returns their paths. ponytail: a
// finding on a file the cut deleted names an artifact slopslint refuses; dropping its tombstone is the upgrade.
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
