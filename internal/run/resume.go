package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
)

// resumePoint is what a run that fails after its agent keeps in resume/, in its directory, for atm run
// --resume: its issue, base ref and base commit, the unit it failed at with its agent's report and the
// follow-up it chained, the proofs of the units before, and work.bundle: their commits, then the failed unit's
// work as one commit on them.
type resumePoint struct {
	Issue    string         `json:"issue"`
	BaseRef  string         `json:"base_ref"`
	Base     string         `json:"base"`
	Unit     int            `json:"unit"`
	Report   map[string]any `json:"report"`
	FollowUp *followUp      `json:"follow_up"`
	Pending  []followUp     `json:"pending"`
	Proofs   [][4]string    `json:"proofs"` // each unit's before: its base, test, type and test content
	run      string         // the run that kept it
}

// keepWork writes p and the work in clone, on HEAD, to resume/ in the run's directory dir. The work leaves the
// files git ignores out, but for the agent's follow-ups.
func keepWork(dir, clone string, p resumePoint) error {
	paths, err := declaredFollowUpPaths(clone, p.Report["follow_ups"])
	if err != nil {
		return err
	}
	if len(paths) > 0 {
		args := append([]string{"--literal-pathspecs", "add", "-f", "--"}, paths...)
		if _, err := git(clone, args...); err != nil {
			return err
		}
	}
	tree, err := snapshot(clone)
	if err != nil {
		return err
	}
	work, err := git(clone, "-c", "user.name=atm", "-c", "user.email=atm@atm.invalid", "commit-tree", tree,
		"-p", "HEAD", "-m", fmt.Sprintf("atm unit %d: its work, uncommitted", p.Unit))
	if err == nil {
		_, err = git(clone, "update-ref", "refs/atm/resume", work) // a bundle takes refs, not commits
	}
	keep := filepath.Join(dir, "resume")
	if err == nil {
		err = os.MkdirAll(keep, 0o755)
	}
	if err == nil {
		_, err = git(clone, "bundle", "create", filepath.Join(keep, "work.bundle"), "refs/atm/resume", "^"+p.Base)
	}
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	return os.WriteFile(filepath.Join(keep, "resume.json"), append(b, '\n'), 0o644)
}

func declaredFollowUpPaths(clone string, followUps any) ([]string, error) {
	b, err := json.Marshal(followUps)
	if err != nil {
		return nil, err
	}
	var declared []followUp
	if err := json.Unmarshal(b, &declared); err != nil {
		return nil, err
	}
	var paths []string
	for i, f := range declared {
		name := filepath.Clean(filepath.FromSlash(f.RedTest))
		if !filepath.IsLocal(name) || name == "." {
			continue
		}
		path := filepath.Join(".atm", "follow-ups", strconv.Itoa(i+1), name)
		info, err := os.Lstat(filepath.Join(clone, path))
		if err == nil && !info.IsDir() {
			paths = append(paths, filepath.ToSlash(path))
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return paths, nil
}

// keep keeps unit n's work in the run's clone, once its agent passed, in the run's directory dir, as p with
// the unit, its agent's report, the follow-up it chained and the proofs of the units before.
func (r *verdict) keep(dir, clone string, p resumePoint, n int, report map[string]any, f *followUp,
	proofs []proof, pending []followUp) {
	if r.saved = 0; r.err != nil {
		return
	}
	p.Unit, p.Report, p.FollowUp = n, report, f
	p.Pending = append([]followUp(nil), pending...)
	for _, q := range proofs {
		p.Proofs = append(p.Proofs, [4]string{q.base, q.test, q.typ, q.testContent})
	}
	if keepWork(dir, clone, p) == nil { // ponytail: work it fails to keep leaves the run not resumable
		r.saved = n
	}
}

// keepPending adds follow-ups collected from a unit whose checks failed to its resume point.
func keepPending(dir string, n int, pending []followUp) {
	path := filepath.Join(dir, "resume", "resume.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var p resumePoint
	if json.Unmarshal(b, &p) != nil || p.Unit != n {
		return
	}
	p.Pending = append([]followUp(nil), pending...)
	b, err = json.MarshalIndent(p, "", "  ")
	if err == nil {
		_ = os.WriteFile(path, append(b, '\n'), 0o644)
	}
}

// resumable marks the run resumable when it failed after its agent and its work is kept in resume/, in the
// run's directory dir, which it removes otherwise.
func (r *verdict) resumable(dir string) {
	if r.saved != 0 && slices.Contains([]string{"checks", "ponytail", "delivery"}, r.FailedNode) {
		r.Resumable, r.ResumableUnit = true, r.saved
		return
	}
	_ = os.RemoveAll(filepath.Join(dir, "resume"))
}

// putBack puts the work run p kept in clone, at p's base: the units' commits, then the failed unit's work,
// uncommitted. It returns the new HEAD, the last of those commits.
func putBack(root, clone string, p resumePoint) (string, error) {
	bundle := filepath.Join(runDir(root, p.run), "resume", "work.bundle")
	for _, args := range [][]string{{"fetch", "-q", bundle, "refs/atm/resume"}, {"reset", "-q", "--hard", "FETCH_HEAD"},
		{"reset", "-q", "HEAD~1"}} {
		if _, err := git(clone, args...); err != nil {
			return "", err
		}
	}
	if p.FollowUp != nil {
		name := filepath.Clean(filepath.FromSlash(p.FollowUp.RedTest))
		if !filepath.IsLocal(name) || name == "." {
			return "", fmt.Errorf("red_test %s is not a new test path", name)
		}
		path := filepath.Join(clone, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(p.FollowUp.Test), 0o644); err != nil {
			return "", err
		}
	}
	return git(clone, "rev-parse", "HEAD")
}

// resumeFrom is what run label of the repository at root kept for atm run --resume, or why it has nothing to
// resume: no such run, a run that is not resumable, or a base commit the repository no longer has.
func resumeFrom(root, label string) (p resumePoint, err error) {
	dir := runDir(root, label)
	if _, err := os.Stat(dir); err != nil {
		return p, fmt.Errorf("no run %s", label)
	}
	var r struct{ Resumable bool }
	b, _ := os.ReadFile(filepath.Join(dir, "report.json")) // none: it failed before its first node
	_ = json.Unmarshal(b, &r)
	if b, err = os.ReadFile(filepath.Join(dir, "resume", "resume.json")); r.Resumable && err == nil {
		err = json.Unmarshal(b, &p)
	}
	if !r.Resumable || err != nil {
		return p, fmt.Errorf("run %s is not resumable: only a run that failed after its agent is", label)
	}
	if _, err := git(root, "rev-parse", "--verify", "-q", p.Base+"^{commit}"); err != nil {
		return p, fmt.Errorf("cannot resume run %s: the repository no longer has its base commit %s", label, p.Base)
	}
	p.run = label
	return p, nil
}

// resuming is the run atm run --resume label picks up, nil when label is "", and the commit the run starts
// from: that run's base, base then its base ref, or else base's.
func resuming(root, label string, base *string) (*resumePoint, string, error) {
	if label == "" {
		return nil, *base, nil
	}
	p, err := resumeFrom(root, label)
	if err != nil {
		return nil, "", err
	}
	*base = p.BaseRef
	return &p, p.Base, nil
}

// proofs is p's proofs as the units' proofs.
func (p resumePoint) proofs() (ps []proof) {
	for _, q := range p.Proofs {
		ps = append(ps, proof{q[0], q[1], q[2], q[3]})
	}
	return ps
}
