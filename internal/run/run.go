// Package run is atm run in the foreground: a clone of the repository, the contract, the agent CLI on it,
// and red/green on the test it wrote.
package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
	"github.com/thellmwhisperer/agentic-team-member/internal/project"
)

// ErrNotStarted is a run that never reached the agent: a taken label, an unreadable issue, a failed clone.
var ErrNotStarted = errors.New("the run did not start")

// Options is one run.
type Options struct {
	Repo    string   // the project root
	Issue   string   // the issue file
	Label   string   // the run's directory under .atm/runs; empty: a timestamp
	BaseRef string   // what the clone checks out
	Scope   []string // globs the diff may touch; empty: derived from the issue
	Config  config.Config
	Out     io.Writer // the ▶ / ✓ / ✗ lines
}

type unitResult struct {
	Unit     int     `json:"unit"`
	Passed   bool    `json:"passed"`
	BaseSHA  string  `json:"base_sha"`
	TestFile string  `json:"test_file"`
	Verified verdict `json:"verified"`
	agentRun
	ReportParseError string `json:"report_parse_error,omitempty"`
}

// Report is report.json: unit 1's fields at the top, as the units that follow will extend it.
type Report struct {
	unitResult
	Harness     string       `json:"harness"`
	Model       string       `json:"model"`
	Effort      string       `json:"effort"`
	BaseRef     string       `json:"base_ref"`
	Clone       string       `json:"clone"`
	Brief       string       `json:"brief"`
	Log         string       `json:"log"`
	ScopeSource string       `json:"scope_source"`
	Scope       []string     `json:"scope"`
	Units       []unitResult `json:"units"`
}

// Run runs o and writes .atm/runs/<label>/report.json. A unit that does not pass is an error.
func Run(o Options) error {
	// Stale .pyc files survive same-size edits the red/green stash restores within a second.
	_ = os.Setenv("PYTHONDONTWRITEBYTECODE", "1")
	stamp := time.Now().Format("20060102-150405")
	if o.Label == "" {
		o.Label = stamp
	}
	if o.Label != filepath.Base(o.Label) || strings.HasPrefix(o.Label, ".") {
		return fmt.Errorf("%w: label %q is not a plain name", ErrNotStarted, o.Label)
	}
	dir := filepath.Join(o.Repo, project.Dir, "runs", o.Label)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fmt.Errorf("%w: %w", ErrNotStarted, err)
	}
	if err := os.Mkdir(dir, 0o755); errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w: label already used: %s", ErrNotStarted, dir)
	} else if err != nil {
		return fmt.Errorf("%w: %w", ErrNotStarted, err)
	}
	f, err := os.Create(filepath.Join(dir, "worker-"+stamp+".jsonl"))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotStarted, err)
	}
	defer func() { _ = f.Close() }()
	log := &runLog{f: f, out: o.Out, harness: o.Config.Harness}
	r := Report{Harness: o.Config.Harness, Model: o.Config.Model, Effort: o.Config.Effort, BaseRef: o.BaseRef,
		Brief: filepath.Join(dir, "brief.md"), Log: f.Name()}
	var text string
	if !log.step("Prepare", func() bool { text, err = prepare(o, &r); return err == nil }) {
		log.log("prepare_failed", obj{"error": err.Error()})
		return fmt.Errorf("%w: %w", ErrNotStarted, err)
	}
	unit := runUnit(o, r, text, dir, log)
	r.unitResult, r.Units = unit, []unitResult{unit}
	b, _ := json.MarshalIndent(r, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "report.json"), b, 0o644); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(o.Out, "red/green: %s\nreport: %s\n", unit.Verified.Message, filepath.Join(dir, "report.json"))
	if !unit.Passed {
		return errors.New("unit 1 did not pass")
	}
	return nil
}

// prepare clones the repository and writes the brief to r.Brief; it returns the brief.
func prepare(o Options, r *Report) (string, error) {
	issue, err := os.ReadFile(o.Issue)
	if err != nil {
		return "", err
	}
	title, body := parseIssue(string(issue))
	if r.Clone, r.BaseSHA, err = clone(o.Repo, o.BaseRef); err != nil {
		return "", err
	}
	r.ScopeSource, r.Scope = scopeOf(o.Scope, title, body, r.Clone)
	test, typecheck := suite(r.Clone, o.Config.Commands.Test, o.Config.Commands.Typecheck)
	var b strings.Builder
	err = brief.Execute(&b, contract{Title: title, Body: body, Clone: r.Clone, BaseRef: o.BaseRef, Test: test,
		Typecheck: typecheck, Forbidden: o.Config.Forbidden[language(r.Clone)], Scope: r.Scope})
	if err == nil {
		err = os.WriteFile(r.Brief, []byte(b.String()), 0o644)
	}
	return b.String(), err
}

// runUnit has the agent work the brief in the clone, then proves the test it wrote with red/green.
func runUnit(o Options, r Report, text, dir string, log *runLog) unitResult {
	agent := agentCall{harness: o.Config.Harness, model: o.Config.Model, effort: o.Config.Effort, clone: r.Clone,
		schema: filepath.Join(dir, "report.schema.json"), lastMsg: filepath.Join(dir, "last-message.txt"),
		timeout: time.Duration(o.Config.Timeouts.Agent) * time.Second}
	u := unitResult{Unit: 1, BaseSHA: r.BaseSHA}
	if agent.harness == "codex" {
		_ = os.WriteFile(agent.schema, []byte(reportSchema), 0o644)
	}
	log.step("Agent · unit 1", func() bool {
		u.agentRun = agent.run(text, log)
		return u.ExitCode == 0 && !u.TimedOut
	})
	report, parseError := extractReport(u.final)
	u.ReportParseError = parseError
	log.log("harness_done", obj{"unit": 1, "run": u.agentRun, "report": report, "report_parse_error": parseError})
	u.TestFile = pickTestFile(report, r.Clone)
	argv, err := testArgv(r.Clone, u.TestFile, o.Config.Commands.Test)
	switch {
	case u.TestFile == "":
		u.Verified.Message = "no test file found"
	case err != nil:
		u.Verified.Message = err.Error()
	default:
		u.Verified = redGreen(r.Clone, u.TestFile, argv, time.Duration(o.Config.Timeouts.Test)*time.Second, log)
	}
	u.Passed = u.Verified.OK
	return u
}

var testName = regexp.MustCompile(`(^test([_.-]|$)|[_.-]test([_.-]|$))`)

// pickTestFile is the test file the agent reported, if it is a file in the clone, else the first new test file.
func pickTestFile(report map[string]any, clone string) string {
	if hint, _ := report["test_file"].(string); hint != "" {
		full := hint
		if !filepath.IsAbs(full) {
			full = filepath.Join(clone, hint)
		}
		rel, err := filepath.Rel(clone, full)
		info, statErr := os.Stat(full)
		if err == nil && !strings.HasPrefix(rel, "..") && statErr == nil && info.Mode().IsRegular() {
			return filepath.ToSlash(rel)
		}
	}
	added := gitLines(clone, "ls-files", "--others", "--exclude-standard")
	added = slices.DeleteFunc(added, func(p string) bool {
		return isAtmPath(p) || !testName.MatchString(strings.ToLower(filepath.Base(p)))
	})
	slices.Sort(added)
	if len(added) == 0 {
		return ""
	}
	return added[0]
}
