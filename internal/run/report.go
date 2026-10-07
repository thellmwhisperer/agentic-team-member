package run

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// verdict is report.json, written at every node's start and end: why the run stopped, then each node's result,
// the task type and each command's result.
type verdict struct {
	FailedNode string      `json:"failed_node"`
	Reason     string      `json:"reason"`
	Nodes      []*node     `json:"nodes"`
	Type       string      `json:"type"`
	Commands   []cmdResult `json:"commands"`
	path       string
	out        io.Writer // the log: each node's start and end, one JSON object a line
	err        error     // the failed node's
}

type node struct {
	Name       string `json:"name"`
	Result     string `json:"result"` // passed, failed, running, skipped or not run
	DurationMS int64  `json:"duration_ms"`
}

type cmdResult struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Result  string `json:"result"`
	Tail    string `json:"tail"` // the last 60 lines of its output
}

// newVerdict is the report of a run in root, every node not run yet. ponytail: one report.json a repository,
// the last run's; a directory a run is the upgrade.
func newVerdict(root string, out io.Writer) *verdict {
	r := &verdict{path: filepath.Join(root, ".atm", "report.json"), out: out}
	for _, name := range []string{"issue", "contract", "clone", "agent", "checks", "delivery"} {
		r.Nodes = append(r.Nodes, &node{Name: name, Result: "not run"})
	}
	return r
}

func (r *verdict) node(name string) *node {
	for _, n := range r.Nodes {
		if n.Name == name {
			return n
		}
	}
	panic("no node " + name)
}

// step writes name's start event, runs fn, and writes its end event with fn's fields, its error if any, and
// its duration; fn's "commands" go to the report. After a failed node, it does nothing: name is not run.
func (r *verdict) step(name string, fn func() (map[string]any, error)) {
	if r.err != nil {
		return
	}
	n := r.node(name)
	n.Result = "running"
	r.emit(name, map[string]any{"state": "started"})
	start := time.Now()
	ev, err := fn()
	if ev == nil {
		ev = map[string]any{}
	}
	n.DurationMS = time.Since(start).Milliseconds()
	cmds, _ := ev["commands"].([]cmdResult)
	r.Commands = append(r.Commands, cmds...)
	n.Result = outcome(err)
	ev["state"], ev["duration_ms"] = n.Result, n.DurationMS
	if err != nil {
		ev["error"], r.FailedNode, r.Reason, r.err = err.Error(), name, err.Error(), err
	}
	r.emit(name, ev)
}

func (r *verdict) emit(name string, ev map[string]any) {
	ev["ts"], ev["step"] = time.Now().UTC().Format(time.RFC3339Nano), name
	b, _ := json.Marshal(ev)
	_, _ = r.out.Write(append(b, '\n'))
	_ = r.write() // the run's last write says whether report.json is on disk
}

func (r *verdict) write() error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(r.path), 0o755)
	}
	if err == nil {
		err = os.WriteFile(r.path, append(b, '\n'), 0o644)
	}
	return err
}

// finish writes report.json a last time and the summary to summary, and gives err, the run's, its exit code.
func (r *verdict) finish(err error, summary io.Writer) error {
	err = errors.Join(err, r.write())
	r.print(summary)
	if r.FailedNode != "" {
		return exitError{cmp.Or(codes[r.FailedNode], 2), err}
	}
	return err
}

// print writes the summary to w: each node, how it ended and its duration, then the result.
func (r *verdict) print(w io.Writer) {
	for _, n := range r.Nodes {
		mark, how := map[string]string{"passed": "✓", "failed": "✗"}[n.Result], n.Result
		if mark != "" {
			how = human(time.Duration(n.DurationMS) * time.Millisecond)
		}
		_, _ = fmt.Fprintf(w, "%-9s %s %s\n", n.Name, cmp.Or(mark, "–"), how)
	}
	result := "PASS"
	if r.FailedNode != "" {
		why, _, _ := strings.Cut(r.Reason, "\n")
		result = fmt.Sprintf("FAIL at %s: %s", r.FailedNode, why)
	}
	_, _ = fmt.Fprintf(w, "RESULT  %s\nreport  %s\n", result, r.path)
}

func outcome(err error) string {
	if err != nil {
		return "failed"
	}
	return "passed"
}

// human is d as ATM prints every time: 0.9 s, 4 min 28 s, 1 h 02 min.
func human(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.1f s", d.Truncate(100*time.Millisecond).Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%d min %02d s", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d h %02d min", int(d.Hours()), int(d.Minutes())%60)
}

// exitError is a run's error with the exit code its failed node means.
type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string { return e.err.Error() }
func (e exitError) Unwrap() error { return e.err }

// codes is the exit code of a failed node: 1 a unit failed, 4 the delivery command failed, 2 any other.
var codes = map[string]int{"agent": 1, "checks": 1, "delivery": 4}

// ExitCode is atm's exit code for Run's error: 0 none, the failed node's code, or 2 when the run never started.
func ExitCode(err error) int {
	var e exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &e):
		return e.code
	}
	return 2
}
