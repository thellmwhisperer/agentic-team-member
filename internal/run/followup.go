package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// maxUnits is the most units a run makes: the first, then one a chained follow-up. ponytail: fixed; a key in
// .atm.yaml is the upgrade.
const maxUnits = 3

// grossBudget is the most lines, added plus deleted, the units after the first change together. ponytail:
// fixed, as maxUnits.
const grossBudget = 200

// followUp is a behaviour gap a unit's agent found and did not fix, as its report declares it. Test holds
// the agent's red test; Verified says ATM proved it failing on the unit's work. A follow-up that blocks the
// run says why in Cause, with the Diff of the unit it stopped and the commit it builds on, its Dependency.
// Paths are the paths its unit would change, and Scope their review against the issue before that unit starts.
type followUp struct {
	Title      string       `json:"title"`
	RedTest    string       `json:"red_test"`
	Criterion  string       `json:"criterion"`
	Paths      []string     `json:"paths,omitempty"`
	Test       string       `json:"test"`
	Verified   bool         `json:"verified"`
	Scope      *scopeReview `json:"scope,omitempty"`
	Diff       string       `json:"diff,omitempty"`
	Cause      string       `json:"cause,omitempty"`
	Dependency string       `json:"dependency,omitempty"`
}

// scopeReview is the review of Paths, a follow-up's paths and its red test, against the issue: approved, or why
// not, with the reviewer's report, when it ran.
type scopeReview struct {
	Paths    []string       `json:"paths"`
	Approved bool           `json:"approved"`
	Reason   string         `json:"reason,omitempty"`
	Report   map[string]any `json:"report,omitempty"`
}

// proposed is the paths f's unit would change, its red test last, as slash paths.
func proposed(f followUp) []string {
	var ps []string
	for _, p := range append(slices.Clone(f.Paths), f.RedTest) {
		ps = append(ps, filepath.ToSlash(filepath.Clean(filepath.FromSlash(p))))
	}
	return ps
}

// scoped is unmet once the first follow-up of in, which unit n+1 would make pass, had its paths reviewed against
// issue i: on a block, nothing chains, and unmet's error carries why.
func (a agent) scoped(root, clone, head string, n int, i Issue, pending, in []followUp) ([]followUp, []followUp,
	error) {
	var blocked error
	if len(in) > 0 && n < maxUnits {
		if blocked = a.reviewScope(clone, head, n+1, i, &in[0]); blocked != nil {
			pending, in = append(pending, in...), nil
		}
	}
	pending, in, err := unmet(root, clone, head, n, i, pending, in)
	return pending, in, errors.Join(err, blocked)
}

// beyond is the gross lines and the diff of the work in clone from commit head, f's unit n, and why it goes
// beyond what it may: a path f's scope review did not approve, or the gross lines of the units before, spent,
// and its own over grossBudget; "" when it does not.
func beyond(clone, head string, f followUp, n, spent int) (int, string, string, error) {
	g, diff, err := gross(clone, head, f.RedTest)
	var left []string
	if err == nil {
		left, err = unplanned(clone, head, f)
	}
	switch {
	case err != nil:
		return 0, "", "", err
	case len(left) > 0:
		return g, diff, fmt.Sprintf("unit %d changes %s, outside the paths its scope review approved: %s", n,
			strings.Join(left, ", "), strings.Join(f.Scope.Paths, ", ")), nil
	case spent+g > grossBudget:
		return g, diff, fmt.Sprintf("units 2 to %d change %d lines, added plus deleted, over the %d-line budget "+
			"of the work after unit 1", n, spent+g, grossBudget), nil
	}
	return g, diff, "", nil
}

// unplanned is the paths the work in clone changes from commit head that f's approved scope review did not
// approve.
func unplanned(clone, head string, f followUp) ([]string, error) {
	if f.Scope == nil || !f.Scope.Approved || !slices.Equal(f.Scope.Paths, proposed(f)) {
		return nil, fmt.Errorf("follow-up %s has no scope review that approved its paths", f.RedTest)
	}
	tree, err := snapshot(clone)
	if err != nil {
		return nil, err
	}
	out, err := git(clone, "diff", "--name-only", "--no-renames", "-z", head, tree)
	var left []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" && !slices.Contains(f.Scope.Paths, p) {
			left = append(left, p)
		}
	}
	return left, err
}

// declared is the follow-ups the agent's report r declares, neither tested nor verified.
func declared(r map[string]any) ([]followUp, error) {
	b, _ := json.Marshal(r["follow_ups"])
	var all []followUp
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("invalid follow_ups in the agent report: %w", err)
	}
	for n, f := range all {
		all[n] = followUp{Title: f.Title, RedTest: f.RedTest, Criterion: f.Criterion, Paths: f.Paths}
	}
	return all, nil
}

// unverified is every follow-up of the agent's report r on a unit that failed its checks, with its red test
// as the agent left it in clone, if any, and verdicts saying so: none is proven on the unit's work.
func unverified(clone string, r map[string]any) (out []followUp, verdicts []string, err error) {
	out, err = declared(r)
	for n, f := range out {
		if filepath.IsLocal(f.RedTest) {
			b, _ := os.ReadFile(filepath.Join(clone, ".atm", "follow-ups", strconv.Itoa(n+1), filepath.FromSlash(f.RedTest)))
			out[n].Test = string(b)
		}
		verdicts = append(verdicts, f.RedTest+": to follow-ups.json, not verified")
	}
	return out, verdicts, err
}

// followUps sorts the follow-ups of the agent's report r on its unit's work in clone, with verdicts saying
// where each went. Follow-up n's red test, .atm/follow-ups/<n>/<red_test>, runs alone at red_test with tmpl
// and must fail, or the follow-up is rejected. An accepted one, verified, goes to in when its criterion is a
// sentence of the issue's body, to out otherwise.
func followUps(clone string, r map[string]any, body, tmpl string) (in, out []followUp, verdicts []string,
	err error) {
	all, err := declared(r)
	if err != nil {
		return nil, nil, nil, err
	}
	for n, f := range all {
		why, err := red(clone, n+1, &f, tmpl)
		f.Verified = true
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
	rel := filepath.Join(".atm", "follow-ups", strconv.Itoa(n), filepath.FromSlash(f.RedTest))
	b, err := os.ReadFile(filepath.Join(clone, rel))
	if err != nil {
		return "no red test at " + filepath.ToSlash(rel), nil
	}
	f.Test = string(b)
	tree, err := whole(clone)
	if err != nil {
		return "", err
	}
	if err := place(clone, *f); err != nil {
		return "", errors.Join(err, restore(clone, tree))
	}
	line, err := testLine(clone, tmpl, f.RedTest) // {tests} reads the placed test
	if err != nil {
		return err.Error(), restore(clone, tree)
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

// chain commits the work in clone on HEAD as unit n of issue i, under the identity of the repository at
// root, sets aside its follow-ups and puts f's red test in place for the next unit. It returns the new HEAD.
func chain(root, clone string, n int, i Issue, f followUp) (string, error) {
	head, err := commitUnit(root, clone, n, i)
	if err == nil {
		err = os.RemoveAll(filepath.Join(clone, ".atm", "follow-ups"))
	}
	if err == nil {
		err = place(clone, f)
	}
	return head, err
}

// commitUnit commits the work in clone on HEAD as unit n of issue i, under the identity of the repository at
// root. It returns the new HEAD.
func commitUnit(root, clone string, n int, i Issue) (string, error) {
	id, err := identity(root)
	if err != nil {
		return "", err
	}
	tree, err := snapshot(clone)
	if err != nil {
		return "", err
	}
	head, err := git(clone, append(id, "commit-tree", tree, "-p", "HEAD", "-m", unitMessage(i, n))...)
	if err == nil {
		_, err = git(clone, "reset", "-q", head)
	}
	return head, err
}

// gross is the lines the work in clone adds plus those it deletes from commit head, and its diff, but for
// the red test red ATM put there. A binary file counts none. Unlike added, a line moved counts twice.
func gross(clone, head, red string) (int, string, error) {
	tree, err := snapshot(clone)
	if err != nil {
		return 0, "", err
	}
	spec := []string{head, tree, "--", ".", ":(exclude,literal)" + red}
	stat, err := git(clone, append([]string{"diff", "--numstat"}, spec...)...)
	if err != nil {
		return 0, "", err
	}
	n := 0
	for _, line := range strings.Split(stat, "\n") {
		if f := strings.Fields(line); len(f) > 2 {
			a, _ := strconv.Atoi(f[0])
			d, _ := strconv.Atoi(f[1])
			n += a + d
		}
	}
	diff, err := git(clone, append([]string{"diff"}, spec...)...)
	return n, diff, err
}

// errUnmet is the error of a run that ends with a criterion of its issue unmet.
var errUnmet = errors.New("criterion unmet")

// unmet is the follow-ups left, pending, and those on the issue that unit n, on commit head in clone, chains,
// in, as the run goes on: none after the last unit, in then pending. Once nothing chains, a follow-up of
// pending that is a criterion of issue i proved unmet blocks the run: unmet commits the unit's work, under the
// identity of the repository at root, and gives that follow-up the unit's diff, the cause and the new commit
// as its dependency.
func unmet(root, clone, head string, n int, i Issue, pending, in []followUp) ([]followUp, []followUp, error) {
	if len(in) > 0 && n < maxUnits {
		return pending, in, nil
	}
	pending = append(pending, in...)
	var sha, diff string
	var blocked error
	for k, f := range pending {
		if !f.Verified || !quoted(i.Body, f.Criterion) {
			continue
		}
		if sha == "" {
			var err error
			sha, err = commitUnit(root, clone, n, i)
			if err != nil {
				return pending, nil, err
			}
			diff, err = git(clone, "diff", head, sha)
			blocked = errors.Join(blocked, err)
		}
		cause := fmt.Errorf("%w after unit %d of %d: %q, its red test %s in follow-ups.json", errUnmet, n,
			maxUnits, f.Criterion, f.RedTest)
		pending[k].Diff, pending[k].Cause, pending[k].Dependency = diff, cause.Error(), sha
		blocked = errors.Join(blocked, cause)
	}
	return pending, nil, blocked
}

// saveFollowUps writes follow-ups.json in the run's directory dir: follow-ups it did not chain, including
// unverified ones from a unit that failed Checks.
func saveFollowUps(dir string, fs []followUp) error {
	if fs == nil {
		fs = []followUp{}
	}
	b, _ := json.MarshalIndent(fs, "", "  ")
	return os.WriteFile(filepath.Join(dir, "follow-ups.json"), append(b, '\n'), 0o644)
}
