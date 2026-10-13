package run

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// atm run --follow launches a run and is its watcher: one JSON line per event of the run, then of its no-mistakes
// gate, until the PR merges, exit 0, or closes or the run fails, exit 1. A session waits on it alone.

// stall is how long a follow goes without an event before it says so, and again each time after.
var stall = time.Hour

// followEvent is a line of atm run --follow: its event and the event's fields.
type followEvent = map[string]any

// FollowRun is atm run --follow with args, atm run's command line: it starts the run in the repository at root,
// bound to itself as its watcher, and prints its events on w until ctx ends or the run is over.
func FollowRun(ctx context.Context, root string, args []string, w io.Writer) error {
	id, l, err := hold(root)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	beating := make(chan struct{})
	defer func() { cancel(); <-beating; _ = os.Remove(leasePath(root, id)) }()
	go func() { // its own beat: a slow start, gate or reader must not let the lease go stale
		defer close(beating)
		t := time.NewTicker(beat)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				l.Heartbeat = time.Now()
				_ = l.write(root, id) // a missed beat is caught up at the next
			}
		}
	}()
	o, err := Start(root, append(args, "--watcher", id))
	if err != nil {
		return err
	}
	frames, ended := screenOf(ctx, root, o.Run)
	g := follower{fixes: map[string]bool{}, quiet: time.Now(), told: time.Now()}
	t := time.NewTicker(beat)
	defer t.Stop()
	for {
		var evs []followEvent
		select {
		case <-ctx.Done():
			return nil
		case f := <-frames:
			evs = g.frame(f)
		case end := <-ended:
			if evs, ended = g.end(end), nil; evs == nil && g.step != "delivery" {
				return nil // nothing delivered, no PR to follow
			}
		case <-t.C:
			evs = g.tick(root)
		}
		if err := g.say(w, o, evs); !errors.Is(err, errGoesOn) {
			return err
		}
	}
}

// tick is the events of the run's gate, from delivery on, or else stalled once nothing happened for stall.
func (g *follower) tick(root string) (evs []followEvent) {
	if g.step == "delivery" {
		evs = g.poll(root)
	}
	if len(evs) == 0 && time.Since(g.told) >= stall {
		evs = []followEvent{{"event": "stalled", "minutes": int(time.Since(g.quiet).Minutes())}}
	}
	return evs
}

// say prints evs, events of run o, on w and returns how the follow ends after them, errGoesOn while it does not.
func (g *follower) say(w io.Writer, o Outcome, evs []followEvent) error {
	for _, ev := range evs {
		if g.told = time.Now(); ev["event"] != "stalled" {
			g.quiet = g.told
		}
		if err := tell(w, o, ev); err != nil {
			return err
		}
	}
	if len(evs) == 0 {
		return errGoesOn
	}
	return over(evs[len(evs)-1])
}

var errGoesOn = errors.New("the run goes on")

// over is how a follow ends after ev: errGoesOn when it does not.
func over(ev followEvent) error {
	switch ev["event"] {
	case "merged":
		return nil
	case "closed":
		return exitError{1, fmt.Errorf("the PR was closed unmerged: %s", ev["url"])}
	case "run_failed":
		return exitError{1, fmt.Errorf("the run failed at %s: %s", ev["step"], ev["reason"])}
	}
	return errGoesOn
}

// tell prints ev, an event of run o, on w: a JSON line.
func tell(w io.Writer, o Outcome, ev followEvent) error {
	ev["time"], ev["run"], ev["issue"] = time.Now().UTC().Format(time.RFC3339), o.Run, o.Issue
	b, _ := json.Marshal(ev)
	_, err := fmt.Fprintf(w, "%s\n", b)
	return err
}

// screenOf hands each frame of run label's screen, until ctx ends, then how the run ended, a failure when the
// background process ended first.
func screenOf(ctx context.Context, root, label string) (<-chan Frame, chan Outcome) {
	frames, ended := make(chan Frame), make(chan Outcome, 1)
	go func() {
		end, err := Follow(root, label, nil, nil, func(f Frame) {
			select {
			case frames <- f:
			case <-ctx.Done():
			}
		})
		if err != nil {
			end = Outcome{Outcome: "failed", Reason: err.Error()}
		}
		ended <- end
	}()
	return frames, ended
}

// follower is what a follow knows of its run, and printed of its gate.
type follower struct {
	gate                gate
	clone, step, branch string
	wait                string          // the step and finding ids of the wait last printed
	fixes               map[string]bool // the CI fix commits printed
	quiet, told         time.Time       // its last event and its last line
}

// frame is the event of f, a frame of the run's screen: a step, or delivery, started.
func (g *follower) frame(f Frame) []followEvent {
	var e struct{ Step, State, Clone, Branch string }
	if f.Line == nil || json.Unmarshal(f.Event, &e) != nil { // a node's start or end has a line
		return nil
	}
	g.clone = cmp.Or(e.Clone, g.clone)
	g.branch = cmp.Or(e.Branch, g.branch)
	switch {
	case e.State != "started":
		return nil
	case e.Step == "delivery":
		g.step = e.Step
		return []followEvent{{"event": "delivery", "clone": g.clone}}
	}
	g.step = e.Step
	return []followEvent{{"event": "step", "step": g.step}}
}

// end is the event of the run's end, o: none when it passed.
func (g *follower) end(o Outcome) []followEvent {
	if o.Outcome == "passed" {
		return nil
	}
	g.step = cmp.Or(o.FailedNode, g.step)
	return []followEvent{{"event": "run_failed", "step": g.step, "reason": o.Reason}}
}

// poll is the events of the run's gate since g: its `no-mistakes axi status` in the clone, as atm watch reads it,
// and, once it has a PR, the PR's state and the CI fixes on it, as gh says.
func (g *follower) poll(root string) (evs []followEvent) {
	status, err := gateStatus(g.clone) // none, or no answer: nothing changed
	s := parseGate(status)
	var pr struct {
		State, URL string
		Commits    []struct{ Oid, MessageHeadline string }
	}
	url := g.gate.pr
	if strings.HasPrefix(s.pr, "http") {
		url = s.pr
	}
	if errors.Is(err, os.ErrNotExist) {
		url = cmp.Or(url, g.branch) // delivery may remove the clone before the first gate poll
	}
	if url != "" {
		out, _ := probe(root, "gh", "pr", "view", url, "--json", "state,commits,url")
		_ = json.Unmarshal([]byte(out), &pr) // no answer: nothing changed
	}
	for _, c := range pr.Commits { // oldest first
		evs = append(evs, g.fix(c.Oid, c.MessageHeadline)...)
	}
	if g.gate.pr == "" && pr.URL != "" {
		g.gate.pr = pr.URL
		evs = append(evs, followEvent{"event": "pr_opened", "url": pr.URL})
	}
	lines, _ := g.gate.events(status, time.Now())
	for _, l := range lines {
		switch step, failed := strings.CutPrefix(l, "gate failed: "); {
		case strings.HasPrefix(l, "pr "):
			evs = append(evs, followEvent{"event": "pr_opened", "url": g.gate.pr})
		case l == "ci green":
			evs = append(evs, followEvent{"event": "ci_green", "url": g.gate.pr})
		case failed && !g.gate.merged && pr.State != "MERGED" && pr.State != "CLOSED":
			evs = append(evs, followEvent{"event": "run_failed", "step": "delivery",
				"reason": "no-mistakes failed at " + step})
		}
	}
	evs = append(evs, g.waiting(s, status)...)
	switch {
	case g.gate.merged || pr.State == "MERGED":
		evs = append(evs, followEvent{"event": "merged", "url": g.gate.pr})
	case pr.State == "CLOSED":
		evs = append(evs, followEvent{"event": "closed", "url": g.gate.pr})
	}
	return evs
}

// fix is gate_fixing_ci for commit sha, subject, on the PR: once, and only for a CI fix of the gate.
func (g *follower) fix(sha, subject string) []followEvent {
	if !strings.HasPrefix(subject, "no-mistakes(ci):") || g.fixes[sha] {
		return nil
	}
	g.fixes[sha] = true
	return []followEvent{{"event": "gate_fixing_ci", "commit": map[string]string{"sha": sha, "subject": subject}}}
}

// waiting is gate_waiting for s, the gate's state, status, once per wait: its step and its findings' ids.
func (g *follower) waiting(s gateState, status string) []followEvent {
	if s.waiting == "" {
		if s.seen {
			g.wait = ""
		}
		return nil
	}
	findings, wait := gateFindings(status), s.waiting
	for _, f := range findings {
		wait += " " + f["id"]
	}
	if wait == g.wait {
		return nil
	}
	g.wait = wait
	return []followEvent{{"event": "gate_waiting", "step": s.waiting, "clone": g.clone, "findings": findings}}
}

var findingsTable, toonCell = regexp.MustCompile(`^findings\[(\d+)\]\{([^}]*)\}:$`),
	regexp.MustCompile(`("(?:[^"\\]|\\.)*"|[^,]*)(?:,|$)`)

// gateFindings is each finding's id, severity, file and description in status, `no-mistakes axi status`, whose
// table findings[n]{field,...}: lists them, a row each.
func gateFindings(status string) []map[string]string {
	findings := []map[string]string{}
	lines := strings.Split(status, "\n")
	for i, l := range lines {
		m := findingsTable.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		fields := strings.Split(m[2], ",")
		for _, row := range lines[i+1 : min(i+1+n, len(lines))] {
			f := map[string]string{}
			for j, cell := range toonCell.FindAllStringSubmatch(strings.TrimSpace(row), -1) {
				if j < len(fields) && slices.Contains([]string{"id", "severity", "file", "description"}, fields[j]) {
					f[fields[j]] = cell[1]
					if v, err := strconv.Unquote(cell[1]); err == nil {
						f[fields[j]] = v
					}
				}
			}
			findings = append(findings, f)
		}
		break
	}
	return findings
}
