package run

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// resumePoint is what a run that fails after its agent keeps in resume/, in its directory, for atm run
// --resume: its issue, as given and as first read, base ref and base commit, the unit it failed at with its
// agent's report and the follow-up it chained, the proofs of the units before, and work.bundle: their commits,
// then the failed unit's work as one commit on them.
type resumePoint struct {
	Issue    string         `json:"issue"`
	Original *Issue         `json:"original"`
	BaseRef  string         `json:"base_ref"`
	Base     string         `json:"base"`
	Unit     int            `json:"unit"`
	Report   map[string]any `json:"report"`
	FollowUp *followUp      `json:"follow_up"`
	Pending  []followUp     `json:"pending"`
	Proofs   [][4]string    `json:"proofs"` // each unit's before: its base, test, type and test content
	// Gross is the lines, added plus deleted, of the units after the first, before Unit.
	Gross       int          `json:"gross_lines"`
	run         string       // the run that kept it
	FailedRow   string       `json:"failed_row,omitempty"`
	E2EOutput   string       `json:"e2e_output,omitempty"`
	Session     string       `json:"session_id,omitempty"`
	SkillProved bool         `json:"skill_proved,omitempty"`
	Agent       config.Agent `json:"agent,omitempty"`
	Args        []string     `json:"harness_args,omitempty"`
	Env         []string     `json:"env,omitempty"`
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
// the unit, its agent's report, the follow-up it chained, the proofs of the units before and their gross lines.
func (r *verdict) keep(dir, clone string, p resumePoint, n int, report map[string]any, f *followUp,
	proofs []proof, pending []followUp, gross int) {
	if r.saved = 0; r.err != nil {
		return
	}
	p.Unit, p.Report, p.FollowUp, p.Gross = n, report, f, gross
	p.Pending = append([]followUp(nil), pending...)
	for _, q := range proofs {
		p.Proofs = append(p.Proofs, [4]string{q.base, q.test, q.typ, q.testContent})
	}
	if keepWork(dir, clone, p) == nil { // ponytail: work it fails to keep leaves the run not resumable
		r.saved = n
	}
}

// resumable marks the run resumable when it failed after its agent and its work is kept in resume/, in the
// run's directory dir, which it removes otherwise.
func (r *verdict) resumable(dir string) {
	if r.saved != 0 && slices.Contains([]string{"checks", "ponytail", "delivery"}, r.FailedNode) {
		path := filepath.Join(dir, "resume", "resume.json")
		b, err := os.ReadFile(path)
		var p resumePoint
		r.Resumable = err == nil && json.Unmarshal(b, &p) == nil
		if r.Resumable {
			for _, cmd := range r.Commands {
				if r.FailedNode == "checks" && cmd.Result == "failed" {
					p.FailedRow = cmd.Name
					if cmd.Name == "e2e" {
						p.E2EOutput = cmd.Tail
					}
				}
			}
			b, _ = json.MarshalIndent(p, "", "  ")
			r.Resumable = os.WriteFile(path, append(b, '\n'), 0o644) == nil
		}
		if r.Resumable {
			r.ResumableUnit = r.saved
		}
		return
	}
	_ = os.RemoveAll(filepath.Join(dir, "resume"))
}

func (p *resumePoint) worker(a agent, ev map[string]any) {
	p.Agent, p.Args, p.Env = a.Agent, a.args, a.env
	p.Session, _ = ev["session_id"].(string)
	p.SkillProved, _ = ev["skill_proved"].(bool)
}

func (a agent) resumeUnit(r *verdict, root, clone string, p resumePoint,
	keep *resumePoint) (head string, ev map[string]any, err error) {
	if err = before(clone); err == nil { // the work it puts back leaves the ignored files out
		head, err = putBack(root, clone, p)
	}
	if err == nil {
		a.Agent, a.args, a.env = p.Agent, p.Args, p.Env
		ev, err = a.afterE2E(r, clone, p)
		ev["resumed_from"] = p.run
		keep.worker(a, ev)
	}
	return head, ev, err
}

// afterE2E uses the saved worker's session once; old bundles keep the original resume behaviour.
func (a agent) afterE2E(r *verdict, clone string, p resumePoint) (map[string]any, error) {
	ev := map[string]any{"report": p.Report, "session_id": p.Session, "skill_proved": p.SkillProved}
	path := filepath.Join(a.dir, "agent-after-e2e.jsonl")
	final := filepath.Join(a.dir, "agent-after-e2e.final.md")
	prompt := "Checks failed at e2e: these changed lines are not executed by any end-to-end run of the real binary. " +
		"Delete them, or write the end-to-end test that reaches them from a real command. " +
		"Do not touch anything else.\n" + p.E2EOutput
	argv, stdin, ok := a.resumeArgv(clone, filepath.Join(clone, skillDirs[a.Harness], "ponytail"),
		final, p.Session, prompt)
	reason := ""
	switch {
	case p.FailedRow != "e2e":
		reason = "failed row is " + cmp.Or(p.FailedRow, "unknown")
	case p.Session == "":
		reason = "no saved session id"
	case !ok:
		reason = "harness " + a.Harness + " cannot resume"
	}
	if reason != "" {
		message := "resume: no agent turn (" + reason + ")"
		r.emit("agent", map[string]any{"message": message, "output": message})
		return ev, nil
	}
	const turn = "agent turn after e2e"
	// The screen logs this event inside Agent; it is not a new pipeline step.
	r.emit(turn, map[string]any{"state": "started", "harness": a.Harness, "output": turn})
	var err error
	s := timed(func() {
		if _, err = placeSkill(clone, a.Harness, "ponytail"); err == nil {
			stream, exit, cause, runErr := a.exec(clone, "ponytail", path, argv, stdin, false)
			var invalid error
			invalid, err = a.reported("worker", "ponytail", path, final, exit, cause, runErr, stream,
				p.SkillProved, testFile, ev)
			ev["report"] = p.Report
			if invalid != nil {
				delete(ev, "exit")
				delete(ev, "tail")
				err = nil
			}
		}
	})
	r.emit(turn, map[string]any{"state": outcome(err), "harness": a.Harness,
		"started_at": s.StartedAt, "duration_ms": s.DurationMS})
	return ev, err
}

// putBack puts the work run p kept in clone, at p's base: the units' commits, then the failed unit's work,
// uncommitted, byte for byte as kept, whatever core.autocrlf says. It returns the new HEAD, the last of those
// commits.
func putBack(root, clone string, p resumePoint) (string, error) {
	bundle := filepath.Join(runDir(root, p.run), "resume", "work.bundle")
	for _, args := range [][]string{{"fetch", "-q", bundle, "refs/atm/resume"},
		{"-c", "core.autocrlf=false", "reset", "-q", "--hard", "FETCH_HEAD"}, {"reset", "-q", "HEAD~1"}} {
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
	if p.Original == nil || strings.TrimSpace(p.Original.Text) == "" {
		return p, fmt.Errorf("cannot resume run %s: saved original issue text is missing", label)
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
