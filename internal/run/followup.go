package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// maxUnits is the most units a run makes: the first, then one a chained follow-up. ponytail: fixed; a key in
// .atm.yaml is the upgrade.
const maxUnits = 3

// followUp is a behaviour gap a unit's agent found and did not fix, as its report declares it, and Test, the
// red test that proves it.
type followUp struct {
	Title     string `json:"title"`
	RedTest   string `json:"red_test"`
	Criterion string `json:"criterion"`
	Test      string `json:"test"`
}

// followUps sorts the follow-ups of the agent's report r on its unit's work in clone, with verdicts saying
// where each went. Follow-up n's red test, .atm/follow-ups/<n>/<red_test>, runs alone at red_test with tmpl
// and must fail, or the follow-up is rejected. An accepted one goes to in when its criterion is a sentence
// of the issue's body, to out otherwise.
func followUps(clone string, r map[string]any, body, tmpl string) (in, out []followUp, verdicts []string,
	err error) {
	b, _ := json.Marshal(r["follow_ups"])
	var all []followUp
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid follow_ups in the agent report: %w", err)
	}
	for n, f := range all {
		why, err := red(clone, n+1, &f, tmpl)
		switch {
		case err != nil:
			return in, out, verdicts, err
		case why != "":
			verdicts = append(verdicts, f.RedTest+": rejected: "+why)
		case quoted(body, f.Criterion):
			in, verdicts = append(in, f), append(verdicts, f.RedTest+": on the issue")
		default:
			out, verdicts = append(out, f), append(verdicts, f.RedTest+": to follow-ups.json")
		}
	}
	return in, out, verdicts, nil
}

// red says why follow-up n, f, is not proven, "" when its red test, which it reads into f.Test, fails on the
// work in clone. The clone comes back as it was.
func red(clone string, n int, f *followUp, tmpl string) (string, error) {
	if !filepath.IsLocal(f.RedTest) {
		return fmt.Sprintf("red_test %q is not a path in the clone", f.RedTest), nil
	}
	if err := newTestPath(clone, f.RedTest); err != nil {
		return err.Error(), nil
	}
	line, err := testLine(tmpl, f.RedTest)
	if err != nil {
		return err.Error(), nil
	}
	rel := filepath.Join(".atm", "follow-ups", strconv.Itoa(n), filepath.FromSlash(f.RedTest))
	b, err := os.ReadFile(filepath.Join(clone, rel))
	if err != nil {
		return "no red test at " + filepath.ToSlash(rel), nil
	}
	f.Test = string(b)
	tree, err := snapshot(clone)
	if err != nil {
		return "", err
	}
	if err := place(clone, *f); err != nil {
		return "", errors.Join(err, restore(clone, tree))
	}
	_, failed := sh(clone, line)
	if err := restore(clone, tree); err != nil {
		return "", err
	}
	switch {
	case failed == nil:
		return "it passes today", nil
	case errors.Is(failed, errTimeout):
		return "it times out", nil
	}
	return "", nil
}

// place writes f's red test at its path in clone.
func place(clone string, f followUp) error {
	if err := newTestPath(clone, f.RedTest); err != nil {
		return err
	}
	path := filepath.Join(clone, filepath.FromSlash(f.RedTest))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(f.Test), 0o644)
}

func newTestPath(clone, name string) error {
	name = filepath.Clean(filepath.FromSlash(name))
	if !filepath.IsLocal(name) {
		return fmt.Errorf("red_test %s is not a new test path", name)
	}
	s := filepath.ToSlash(name)
	if s == ".atm" || strings.HasPrefix(s, ".atm/") {
		return fmt.Errorf("red_test %s is not a new test path", name)
	}
	if _, err := os.Lstat(filepath.Join(clone, name)); err == nil {
		return fmt.Errorf("red_test %s is not a new test path", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// quoted accepts a whole issue line after list markers and final punctuation are removed, or a
// whole sentence of body, verbatim but for line breaks.
func quoted(body, criterion string) bool {
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	c := flat(criterion)
	if c == "" {
		return false
	}
	marker := regexp.MustCompile(`^(?:[-*]|\d+[.)])\s+`)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		line = marker.ReplaceAllString(line, "")
		if strings.TrimRight(flat(strings.TrimSpace(line)), ".!?") == strings.TrimRight(c, ".!?") {
			return true
		}
	}
	ok, _ := regexp.MatchString(`(?:^|[.!?:*-] )`+regexp.QuoteMeta(c)+`(?: |$)`, flat(body))
	return ok && strings.ContainsAny(c[max(0, len(c)-1):], ".!?")
}

// chain commits the work in clone on HEAD as unit n of the issue title, under the identity of the repository
// at root, sets aside its follow-ups and puts f's red test in place for the next unit. It returns the new HEAD.
func chain(root, clone string, n int, title string, f followUp) (string, error) {
	id, err := identity(root)
	if err != nil {
		return "", err
	}
	tree, err := snapshot(clone)
	if err != nil {
		return "", err
	}
	head, err := git(clone, append(id, "commit-tree", tree, "-p", "HEAD", "-m",
		fmt.Sprintf("atm unit %d: %s", n, title))...)
	if err == nil {
		_, err = git(clone, "reset", "-q", head)
	}
	if err == nil {
		err = os.RemoveAll(filepath.Join(clone, ".atm", "follow-ups"))
	}
	if err == nil {
		err = place(clone, f)
	}
	return head, err
}

// saveFollowUps writes follow-ups.json in the run's directory dir: the run's accepted follow-ups it did not chain, each
// with its red test, to become new issues.
func saveFollowUps(dir string, fs []followUp) error {
	if fs == nil {
		fs = []followUp{}
	}
	b, _ := json.MarshalIndent(fs, "", "  ")
	return os.WriteFile(filepath.Join(dir, "follow-ups.json"), append(b, '\n'), 0o644)
}
