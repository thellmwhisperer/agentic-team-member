package run

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"time"
)

// Stream follows run o.Run of the repository at root until its PR merges, 0, or closes, or the run or its gate
// fails, 1: a JSON object a line on w, its time, run, issue and event, as each happens. Its steps and delivery come
// from the run's screen; its gate's waits, CI repair commits and green CI from `no-mistakes axi status` in the
// clone, and its PR from gh, by the delivery's branch once the clone is gone. It reads them every 10 s at most,
// on now and after, time.Now and time.After but in tests; an hour without a line is stalled, and so each hour
// after. Read errors go to stderr: the next poll reads again. Ending ctx stops it, never the run: 1.
func (o Outcome) Stream(
	ctx context.Context,
	root string,
	w io.Writer,
	now func() time.Time,
	after func(time.Duration) <-chan time.Time,
) int {
	ctx, cancel := context.WithCancel(ctx)
	frames, ended, in, followed := make(chan Frame), make(chan Outcome, 1), make(chan Input), make(chan struct{})
	go func() {
		defer close(followed)
		end, err := Follow(root, o.Run, nil, in, func(f Frame) {
			select {
			case frames <- f:
			case <-ctx.Done():
			}
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "atm:", o.Run+":", err)
		}
		ended <- end
	}()
	defer func() { cancel(); close(in); <-followed }() // closing in leaves the run's screen
	s := &observer{o: o, w: w, now: now, said: now(), quiet: now(), ci: map[string]bool{}}
	var polled time.Time
	for {
		if t := now(); !t.Before(polled.Add(10 * time.Second)) {
			polled = t
			if code, done := s.poll(ctx, root); done {
				return code
			}
		}
		if t := now(); t.Sub(s.said) >= time.Hour {
			s.say("stalled", map[string]any{"minutes": int(t.Sub(s.quiet).Minutes())})
		}
		select {
		case <-ctx.Done():
			return 1
		case f := <-frames:
			s.frame(f)
		case end := <-ended:
			if code, done := s.end(root, end); done {
				return code
			}
		case <-after(polled.Add(10 * time.Second).Sub(now())):
		}
	}
}

// observer is what Stream has seen of a run and said of it: when it last wrote a line, and one but stalled.
type observer struct {
	o                          Outcome
	w                          io.Writer
	now                        func() time.Time
	said, quiet                time.Time
	clone, branch, pr, waiting string
	delivering, seen, green    bool
	reason                     string // the run's, failed at delivery after its command ran: its gate may go on
	ci                         map[string]bool
}

// say writes event, at now, with fields.
func (s *observer) say(event string, fields map[string]any) {
	t := s.now()
	line := map[string]any{"time": t.UTC().Format(time.RFC3339Nano), "run": s.o.Run, "issue": s.o.Issue,
		"event": event}
	maps.Copy(line, fields)
	b, _ := json.Marshal(line)
	_, _ = s.w.Write(append(b, '\n'))
	s.said = t
	if event != "stalled" {
		s.quiet = t
	}
}

func (s *observer) diag(err error) { fmt.Fprintln(os.Stderr, "atm:", s.o.Run+":", err) }

// frame takes a frame of the run's screen: each node's start is a step, delivery's its delivery.
func (s *observer) frame(f Frame) {
	var ev struct{ Step, State, Clone, Branch string }
	if f.Line == nil || json.Unmarshal(f.Event, &ev) != nil { // a node's start or end has a line
		return
	}
	s.clone, s.branch = cmp.Or(ev.Clone, s.clone), cmp.Or(ev.Branch, s.branch)
	if ev.State != "started" {
		return
	}
	s.say("step", map[string]any{"step": ev.Step})
	if ev.Step == "delivery" {
		s.delivering = true
		s.say("delivery", map[string]any{"clone": s.clone})
	}
}

// end takes how the run ended. One that failed at delivery after its delivery command ran may leave its gate
// going, which says how it ends.
func (s *observer) end(root string, o Outcome) (int, bool) {
	switch {
	case o.Ended.IsZero(): // its screen is lost; its gate is not
		return 0, false
	case o.Outcome == "passed" && s.delivering:
		return 0, false
	case o.Outcome == "passed":
		s.diag(errors.New("the run delivered nothing: no PR to follow"))
		return 1, true
	case o.FailedNode == "delivery" && !(&gate{seen: s.seen}).failedFirst(root, o.Run):
		s.reason = o.Reason
		return 0, false
	}
	s.say("run_failed", map[string]any{"step": cmp.Or(o.FailedNode, o.Step), "reason": o.Reason})
	return 1, true
}

// poll reads the gate, once the run delivers, and its PR, once there is one or the gate cannot be read.
func (s *observer) poll(ctx context.Context, root string) (int, bool) {
	if !s.delivering {
		return 0, false
	}
	var g gateState
	err := g.read(ctx, s.clone)
	switch {
	case err == nil:
		s.seen = true
		s.gate(g)
	case errors.Is(err, errNoGate) && s.reason != "" && !s.seen: // the delivery failed and started none
		s.say("run_failed", map[string]any{"step": "delivery", "reason": s.reason})
		return 1, true
	default:
		s.diag(err)
	}
	var prState string
	if err != nil || s.pr != "" {
		prState = s.prState(ctx, root)
	}
	if err == nil && g.failed != "" {
		s.say("run_failed", map[string]any{"step": g.failed,
			"reason": cmp.Or(s.reason, "no-mistakes step "+g.failed+" failed")})
		return 1, true
	}
	switch prState {
	case "MERGED":
		s.say("merged", map[string]any{"url": s.pr})
		return 0, true
	case "CLOSED":
		s.say("closed", map[string]any{"url": s.pr})
		return 1, true
	}
	return 0, false
}

// gate takes a gate's state: a new wait, the PR, new CI repair commits, CI going green.
func (s *observer) gate(g gateState) {
	switch {
	case g.waiting == "":
		s.waiting = ""
	case g.waiting != s.waiting:
		s.waiting = g.waiting
		findings := []map[string]string{}
		for _, f := range g.findings {
			findings = append(findings, map[string]string{"id": f.id, "severity": f.severity, "file": f.file,
				"description": f.description})
		}
		s.say("gate_waiting", map[string]any{"step": g.waiting, "clone": s.clone, "findings": findings})
	}
	if strings.HasPrefix(g.pr, "http") {
		s.opened(g.pr)
	}
	s.commits(g.ci)
	if g.green && !s.green {
		s.say("ci_green", map[string]any{"url": s.pr})
	}
	s.green = g.green
}

// prState reads the PR, by its URL or the delivery's branch, through gh: its CI repair commits, then its end.
func (s *observer) prState(ctx context.Context, root string) string {
	ref := cmp.Or(s.pr, s.branch)
	if ref == "" {
		return ""
	}
	var pr prView
	out, err := ghPR(ctx, root, ref, "--json", "url,state,commits")
	if err == nil {
		err = json.Unmarshal([]byte(out), &pr)
	}
	if err != nil {
		s.diag(err)
		return ""
	}
	s.opened(pr.URL)
	s.commits(pr.ci())
	return pr.State
}

func (s *observer) opened(url string) {
	if url != "" && s.pr == "" {
		s.pr = url
		s.say("pr_opened", map[string]any{"url": url})
	}
}

func (s *observer) commits(cs []ciCommit) {
	for _, c := range cs {
		if !s.ci[c.sha] {
			s.ci[c.sha] = true
			s.say("gate_fixing_ci", map[string]any{"commit": map[string]string{"sha": c.sha, "subject": c.subject}})
		}
	}
}
