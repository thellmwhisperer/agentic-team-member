package run

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// verdict is report.json, written at every node's start and end: why the run stopped, then each node's result,
// the task type, each unit's proof, the proofs the slop detector re-ran and each command's result.
type verdict struct {
	FailedNode string       `json:"failed_node"`
	Reason     string       `json:"reason"`
	Nodes      []*node      `json:"nodes"`
	Type       string       `json:"type"`
	Units      []unitResult `json:"units"`
	Reproofs   []unitResult `json:"reproofs"`
	Commands   []cmdResult  `json:"commands"`
	HeadSHA    string       `json:"head_sha,omitempty"` // the delivered branch's
	path       string
	out        io.Writer // the log: each node's start and end, one JSON object a line
	err        error     // the failed node's
	at         string    // the node that runs
}

type node struct {
	Name   string `json:"name"`
	Result string `json:"result"` // passed, failed, running, skipped or not run
	span
}

// span is when a node, trial or command started, in UTC to the millisecond, and how long it took: "" and 0 when
// it never ran.
type span struct {
	StartedAt  string `json:"started_at,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// timed runs fn and returns its span.
func timed(fn func()) span {
	start := time.Now()
	fn()
	return span{start.UTC().Format("2006-01-02T15:04:05.000Z07:00"), time.Since(start).Milliseconds()}
}

// unitResult is a unit's proof: the unit, its base commit, test and task type, its trials and how it ended.
type unitResult struct {
	Unit     int    `json:"unit"`
	Base     string `json:"base"`
	TestFile string `json:"test_file"`
	Type     string `json:"type"`
	Red      try    `json:"red,omitzero"`
	Green    try    `json:"green,omitzero"`
	Result   string `json:"result"`
	span
}

type cmdResult struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Result  string `json:"result"`
	Tail    string `json:"tail"` // the last 60 lines of its output
	span
}

// newVerdict is the report of a run in its directory dir, every node not run yet.
func newVerdict(dir string, out io.Writer) *verdict {
	r := &verdict{path: filepath.Join(dir, "report.json"), out: out}
	for _, name := range []string{"issue", "clone", "contract", "agent", "checks", "ponytail", "delivery"} {
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
	n.Result, r.at = "running", name
	start := time.Now()
	n.StartedAt = start.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	r.emit(name, map[string]any{"state": "started"})
	var ev map[string]any
	var err error
	ev, err = fn()
	n.DurationMS = time.Since(start).Milliseconds()
	if ev == nil {
		ev = map[string]any{}
	}
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

// watcher is a log a screen watches: it takes the live events inside a node too, which the log leaves out.
type watcher interface{ watch(ev []byte) }

// live hands ev, an event inside the node that runs, to the screen that watches the log, if one does.
func (r *verdict) live(ev map[string]any) {
	if w, ok := r.out.(watcher); ok {
		ev["ts"], ev["step"] = time.Now().UTC().Format(time.RFC3339Nano), r.at
		b, _ := json.Marshal(ev)
		w.watch(b)
	}
}

// byClone is each run's verdict by its clone, which every node from clone on runs in. ponytail: what runs in a
// clone finds its run's screen by it; the verdict down every call is the upgrade.
var byClone sync.Map

// liveIn is the live events of the run in clone dir, none when no run is there.
func liveIn(dir string) func(map[string]any) {
	if r, ok := byClone.Load(dir); ok {
		return r.(*verdict).live
	}
	return func(map[string]any) {}
}

// lineWriter hands fn each line written to it, without its newline; flush hands it what is left.
type lineWriter struct {
	fn  func(string)
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(bytes.TrimSuffix(w.buf[:i], []byte("\r")))
		w.buf = w.buf[i+1:]
		w.fn(line)
	}
}

func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.fn(string(w.buf))
		w.buf = nil
	}
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
			how = Human(time.Duration(n.DurationMS) * time.Millisecond)
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

// Human is d as ATM prints every time: 0.9 s, 4 min 28 s, 1 h 02 min.
func Human(d time.Duration) string {
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
