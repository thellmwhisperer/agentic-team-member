package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// watchEvery is how often watch reads a run and its gate, watchPatience how long it goes on without a good read.
// Strings, so a test's build sets them with -ldflags -X.
var watchEvery, watchPatience = "5s", "5m"

var watchEvents = []string{"gate_question", "gate_failed", "ci_running", "ci_green_awaiting_merge", "review_required",
	"done"}

// watchEvent is a moment of a run's gate a driver acts on: the gate step it is about, or a done run's outcome.
type watchEvent struct{ event, step, outcome string }

var gateRunID = regexp.MustCompile(`(?m)^\s*id: "([^"]+)"\s*$`)

// axiWatch is atm axi watch, which blocks until a run's next gate event and prints it. It only reads: the
// run's folder, the gate's no-mistakes status, as atm attach reads it, and its pull request with gh.
func axiWatch() *cobra.Command {
	c := &cobra.Command{Use: "watch [--json] [--after <event>] [run]", Short: "Print a run's next gate event",
		Args: cobra.MaximumNArgs(1)}
	asJSON := c.Flags().Bool("json", false, "print JSON")
	after := c.Flags().String("after", "", "skip this event while the run's state is the one watch first read")
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if *after != "" && !slices.Contains(watchEvents, *after) {
			return fmt.Errorf("--after: %s is none of %s", *after, strings.Join(watchEvents, ", "))
		}
		root, err := toplevel()
		if err != nil {
			return err
		}
		o, err := watched(root, strings.Join(args, ""))
		if err != nil {
			return exitCode{1, err}
		}
		e, err := watch(root, o.Run, *after)
		if err != nil {
			return exitCode{1, err}
		}
		line := toon(e.event) + ": " + toon(cmp.Or(e.outcome, e.step)) + "\n"
		if *asJSON {
			line = jsonObject([]string{"event", "run", "step", "outcome"}, []string{e.event, o.Run, e.step, e.outcome}) +
				"\n"
		}
		_, err = io.WriteString(cmd.OutOrStdout(), line)
		return err
	}
	return c
}

// watched is run label of the runs recorded in root, or the newest that has not ended when label is "".
func watched(root, label string) (run.Outcome, error) {
	runs, err := run.RecordedRuns(root)
	if err != nil {
		return run.Outcome{}, err
	}
	for _, o := range slices.Backward(runs) {
		if o.Run == label || label == "" && o.Ended.IsZero() {
			return o, nil
		}
	}
	if label == "" {
		return run.Outcome{}, errors.New("nothing runs")
	}
	return run.Outcome{}, fmt.Errorf("no run %s", label)
}

// watch reads run label every watchEvery until it reads an event, which it returns; after, an event skipped while
// the run's state is the one first read. It fails with the last error after watchPatience without a good read.
func watch(root, label, after string) (watchEvent, error) {
	every, _ := time.ParseDuration(watchEvery)
	patience, _ := time.ParseDuration(watchPatience)
	good, first, skipping := time.Now(), "", after != ""
	var last error
	for ; ; time.Sleep(every) {
		if last != nil && !time.Now().Before(good.Add(patience)) {
			return watchEvent{}, last
		}
		ctx, cancel := context.WithDeadline(context.Background(), good.Add(patience))
		e, state, err := readWatch(ctx, root, label)
		killed := ctx.Err() != nil // by watch, at its patience: the error before says more
		cancel()
		if err != nil {
			if last == nil || !killed {
				last = err
			}
			continue
		}
		last = nil
		good, first = time.Now(), cmp.Or(first, state)
		skipping = skipping && state == first
		if e.event != "" && (!skipping || e.event != after) {
			return e, nil
		}
	}
}

// readWatch reads run label once: its event, none for no event, and its state, what its events stand on. An
// error is a state watch cannot read, which has no event.
func readWatch(ctx context.Context, root, label string) (watchEvent, string, error) {
	o, err := watched(root, label)
	if err != nil || o.Outcome == "failed" { // watch reads the error before the event
		return watchEvent{"done", "", "failed"}, "failed", err
	}
	result, clone, err := readReport(o.Report)
	if err != nil {
		return watchEvent{}, "", fmt.Errorf("report of %s: %w", label, err)
	}
	dir, args, state := filepath.Dir(o.Report), []string{"axi", "status"}, o.Outcome+" "+result["delivery"]
	switch result["delivery"] {
	case "running": // the gate of the clone's branch, as atm attach asks it while the run goes
		dir = clone
	case "passed", "failed":
		id, err := savedGateRun(dir)
		if err != nil {
			return watchEvent{}, "", err
		}
		args = append(args, "--run", id)
	default: // no gate: an event only once the run passed, with no delivery
		e := watchEvent{}
		if o.Outcome == "passed" {
			e = watchEvent{"done", "", "passed"}
		}
		return e, state, nil
	}
	out, err := watchCall(ctx, dir, "no-mistakes", args...)
	values, tables := gateRun(string(out))
	if err == nil && (values["status"] == "" || values["branch"] == "") {
		err = fmt.Errorf("no-mistakes axi status in %s shows no gate run", dir)
	}
	if err != nil {
		return watchEvent{}, "", err
	}
	return gateEvent(ctx, root, values, tables, state)
}

// readReport is the result of each node in report.json at path, and the run's clone.
func readReport(path string) (map[string]string, string, error) {
	var report struct {
		Nodes []struct{ Name, Result, Clone string }
	}
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, &report)
	}
	result, clone := map[string]string{}, ""
	for _, n := range report.Nodes {
		result[n.Name], clone = n.Result, cmp.Or(clone, n.Clone)
	}
	return result, clone, err
}

// savedGateRun is the gate run Delivery started, by the id its output in run directory dir keeps, as attach reads it.
func savedGateRun(dir string) (string, error) {
	out := filepath.Join(dir, "delivery-output.txt")
	b, err := os.ReadFile(out)
	id := gateRunID.FindSubmatch(b)
	if err == nil && id == nil {
		err = fmt.Errorf("%s names no gate run", out)
	}
	if err != nil {
		return "", err
	}
	return string(id[1]), nil
}

// gateEvent is the event of the gate no-mistakes shows in values and tables, the pull request read with gh once
// the pr step completed, and the state it stands on, the run's state before it.
func gateEvent(ctx context.Context, root string, values map[string]string, tables map[string][]map[string]string,
	state string) (watchEvent, string, error) {
	state += " " + values["id"] + " " + values["status"]
	steps := map[string]string{}
	for _, s := range tables["steps"] {
		steps[s["step"]], state = s["status"], state+" "+s["step"]+":"+s["status"]
	}
	switch values["status"] {
	case "completed":
		return watchEvent{"done", "", "passed"}, state, nil
	case "failed", "cancelled":
		return watchEvent{"done", "", "failed"}, state, nil
	}
	open, hold := false, ""
	if steps["pr"] == "completed" {
		pr, err := pullRequest(ctx, root, values["branch"])
		if err != nil {
			return watchEvent{}, "", err
		}
		state += " pr:" + pr.State
		if pr.State == "MERGED" || pr.State == "CLOSED" {
			return watchEvent{"done", "", strings.ToLower(pr.State)}, state, nil
		}
		if open = pr.State == "OPEN"; open {
			hold = pr.hold()
			state += " hold:" + hold
		}
	}
	for _, want := range []struct{ event, status string }{{"gate_question", "awaiting_approval"},
		{"gate_question", "fix_review"}, {"gate_failed", "failed"}} {
		for _, s := range tables["steps"] {
			if s["status"] == want.status {
				return watchEvent{want.event, s["step"], ""}, state, nil
			}
		}
	}
	return ciEvent(steps["ci"] == "running", tables["active_steps"], open, hold, state)
}

// ciEvent is the event of the ci step, running or not, once the pull request is read: open, and held for the reason
// hold, or "". ci is green once its last activity, in active, says all CI checks passed, and an event then only when
// gh read the pull request open.
func ciEvent(ci bool, active []map[string]string, open bool, hold, state string) (watchEvent, string, error) {
	green := slices.ContainsFunc(active, func(a map[string]string) bool {
		return a["step"] == "ci" && strings.Contains(a["last_activity"], "all CI checks passed")
	})
	state += " green:" + strconv.FormatBool(green)
	switch {
	case ci && !green:
		return watchEvent{"ci_running", "ci", ""}, state, nil
	case hold != "": // a label or a risk level keeps an open pull request from merging, whatever its checks say
		return watchEvent{"review_required", "", hold}, state, nil
	case ci && open:
		return watchEvent{"ci_green_awaiting_merge", "ci", ""}, state, nil
	}
	return watchEvent{}, state, nil
}

// watchedPR is a pull request as gh reads it.
type watchedPR struct {
	State, Body, HeadRefName string
	Labels                   []struct{ Name string }
	Files                    []struct{ Path string }
}

// riskHeading is the line scripts/arm-auto-merge.sh starts reading the risk level after, riskEnd a heading that
// ends the section, riskWord the level in its first non-empty line.
var (
	riskHeading = regexp.MustCompile(`^## Risk Assessment\s*$`)
	riskEnd     = regexp.MustCompile(`^#+\s`)
	riskWord    = regexp.MustCompile(`[A-Za-z]+`)
)

// hold is why the pull request will not merge on its own, as scripts/arm-auto-merge.sh decides it: "" when nothing
// holds it. The review-required label comes first, then a risk level other than Low and Medium without the
// risk-reviewed label.
func (p watchedPR) hold() string {
	labels := map[string]bool{}
	for _, l := range p.Labels {
		labels[l.Name] = true
	}
	if labels["review-required"] {
		return "review-required label"
	}
	level := p.riskLevel()
	if level == "Low" || level == "Medium" || labels["risk-reviewed"] {
		return ""
	}
	return "risk level " + cmp.Or(level, "missing") + " without risk-reviewed"
}

// riskLevel is the first word of the first non-empty line under the body's Risk Assessment heading; Low for a
// release-please pull request that changes only the changelog and the manifest.
func (p watchedPR) riskLevel() string {
	releaseOnly := p.HeadRefName == "release-please--branches--main"
	for _, f := range p.Files {
		releaseOnly = releaseOnly && (f.Path == "CHANGELOG.md" || f.Path == ".release-please-manifest.json")
	}
	if releaseOnly {
		return "Low"
	}
	found := false
	for _, line := range strings.Split(p.Body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case !found:
			found = riskHeading.MatchString(line)
		case riskEnd.MatchString(line):
			return ""
		case strings.Trim(line, " \t") != "":
			return riskWord.FindString(line)
		}
	}
	return ""
}

// pullRequest is the pull request gh reads of branch, run in root.
func pullRequest(ctx context.Context, root, branch string) (watchedPR, error) {
	out, err := watchCall(ctx, root, "gh", "pr", "view", branch, "--json", "state,labels,body,files,headRefName")
	var pr watchedPR
	if err == nil {
		err = json.Unmarshal(out, &pr)
	}
	if err != nil {
		return watchedPR{}, fmt.Errorf("pull request: %w", err)
	}
	return pr, nil
}

// watchCall runs name args in dir, killed at ctx's deadline, and returns its stdout; its error carries stderr.
func watchCall(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Dir, cmd.Stderr, cmd.WaitDelay = dir, &stderr, time.Second
	out, err := cmd.Output()
	if err != nil {
		err = fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, err
}

// gateRun reads the run object of no-mistakes' status, a TOON document: its values by key, and its tables' rows
// by column.
func gateRun(doc string) (map[string]string, map[string][]map[string]string) {
	values, tables := map[string]string{}, map[string][]map[string]string{}
	var top, table string
	var cols []string
	for _, l := range strings.Split(doc, "\n") {
		line := strings.TrimLeft(l, " ")
		indent := len(l) - len(line)
		if indent == 0 {
			top, _, _ = strings.Cut(line, ":")
		}
		if top != "run" || indent == 0 {
			continue
		}
		if indent > 2 && cols != nil {
			row := map[string]string{}
			for i, v := range rowFields(line) {
				if i < len(cols) {
					row[cols[i]] = v
				}
			}
			tables[table] = append(tables[table], row)
			continue
		}
		key, value, _ := strings.Cut(line, ":")
		name, header, isTable := strings.Cut(strings.TrimSuffix(key, "}"), "{")
		if cols = nil; isTable {
			table, _, _ = strings.Cut(name, "[")
			cols = strings.Split(header, ",")
		} else if indent == 2 {
			values[key] = rowFields(strings.TrimSpace(value))[0]
		}
	}
	return values, tables
}

// rowFields is the values of a TOON row, split at the commas between them, each quoted one unquoted.
func rowFields(row string) (out []string) {
	for more := true; more; {
		field := ""
		if quoted, err := strconv.QuotedPrefix(row); err == nil {
			field, _ = strconv.Unquote(quoted)
			_, row, more = strings.Cut(row[len(quoted):], ",")
		} else {
			field, row, more = strings.Cut(row, ",")
		}
		out = append(out, field)
	}
	return out
}
