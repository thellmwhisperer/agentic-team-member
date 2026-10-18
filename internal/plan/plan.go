// Package plan is atm axi plan: which tickets of a CoS's plan can launch now, read from GitHub and runs.jsonl.
// It never writes the plan.
package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// Plan is a CoS's plan, as it writes it.
type Plan struct {
	Version   *int     `json:"version"`
	MaxActive *int     `json:"max_active"`
	Tickets   []Ticket `json:"tickets"`
	Excluded  []int    `json:"excluded"`
}

// Ticket is an issue of a plan.
type Ticket struct {
	Issue         *int   `json:"issue"`
	DependsOn     []int  `json:"depends_on"`
	ConflictsWith []int  `json:"conflicts_with"`
	Hold          any    `json:"hold"` // a reason; none, false or "" is no hold
	HoldOwner     string `json:"hold_owner"`
}

// Status is what a plan says now: its open tickets, those that can launch, those that wait, on the CoS or on
// Javi, those going, and the free slots.
type Status struct {
	Open      []int `json:"open"`
	Ready     []int `json:"ready"`
	CosHolds  []int `json:"cos_holds"`
	JaviHolds []int `json:"javi_holds"`
	Working   []int `json:"working"`
	Free      int   `json:"free"`
}

// Issue is a GitHub issue as the plan needs it.
type Issue struct {
	State string `json:"state"` // OPEN or CLOSED
	Body  string `json:"body"`
}

// Load reads the plan at path, or says which field makes it invalid.
func Load(path string) (Plan, error) {
	var p Plan
	b, err := os.ReadFile(path)
	if err != nil {
		return p, fmt.Errorf("plan: %w", err)
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("plan: %w", err)
	}
	switch {
	case p.Version == nil || *p.Version != 1:
		return p, errors.New("plan: version must be 1")
	case p.MaxActive == nil || *p.MaxActive < 1:
		return p, errors.New("plan: max_active must be an integer above 0")
	}
	for i, t := range p.Tickets {
		if t.Issue == nil {
			return p, fmt.Errorf("plan: tickets[%d] has no issue", i)
		}
	}
	return p, nil
}

// GitHub reads issues of the GitHub repository repo with gh.
func GitHub(repo string) func(int) (Issue, error) {
	return func(n int) (Issue, error) {
		var i Issue
		if repo == "" {
			return i, fmt.Errorf("issue %d: origin is not a GitHub repository", n)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "gh", "issue", "view", strconv.Itoa(n), "--repo", repo, "--json", "state,body")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return i, fmt.Errorf("gh issue view %d: %w: %s", n, err, strings.TrimSpace(stderr.String()))
		}
		if err := json.Unmarshal(out, &i); err != nil {
			return i, fmt.Errorf("gh issue view %d returned no JSON: %w", n, err)
		}
		return i, nil
	}
}

var (
	dependsSection = regexp.MustCompile(`(?ms)^##[ \t]+Depends on[ \t]*\r?$(.*?)(?:^##[ \t]|\z)`)
	issueRef       = regexp.MustCompile(`#(\d+)`)
)

// Read is plan p's status now in the repository at root, its issues read with issue.
func Read(p Plan, root string, issue func(int) (Issue, error)) (Status, error) {
	s := Status{Open: []int{}, Ready: []int{}, CosHolds: []int{}, JaviHolds: []int{}, Working: []int{}}
	last, agents, err := runs(root)
	if err != nil {
		return s, err
	}
	s.Free = *p.MaxActive - agents
	r := &reader{issue: issue, issues: map[int]Issue{}, last: last}
	for _, t := range p.Tickets {
		n := *t.Issue
		if slices.Contains(p.Excluded, n) || !r.open(n) {
			continue
		}
		s.Open = append(s.Open, n)
		held := t.Hold != nil && t.Hold != false && t.Hold != ""
		switch {
		case held && t.HoldOwner == "javi":
			s.JaviHolds = append(s.JaviHolds, n)
		case held:
			s.CosHolds = append(s.CosHolds, n)
		}
		going := r.going(n)
		if going {
			s.Working = append(s.Working, n)
		}
		if !held && !going && !r.blocked(p, t) {
			s.Ready = append(s.Ready, n)
		}
	}
	return s, r.err
}

// reader reads each issue once, and keeps the first error.
type reader struct {
	issue  func(int) (Issue, error)
	issues map[int]Issue
	last   map[string]string // how the last run of each issue stands
	err    error
}

func (r *reader) get(n int) Issue {
	i, ok := r.issues[n]
	if !ok && r.err == nil {
		i, r.err = r.issue(n)
		r.issues[n] = i
	}
	return i
}

func (r *reader) open(n int) bool { return r.get(n).State != "CLOSED" }

// going is whether n is open and its last run running, or passed: delivered, the issue still open. A closed issue is
// never going, since a run whose client was killed never records its end.
func (r *reader) going(n int) bool {
	l := r.last[strconv.Itoa(n)]
	return (l == "running" || l == "passed") && r.open(n)
}

// blocked is whether t waits on an open dependency, in the plan or under its issue's Depends on, or conflicts
// with a ticket going, named in either one's conflicts_with.
func (r *reader) blocked(p Plan, t Ticket) bool {
	n, deps := *t.Issue, slices.Clone(t.DependsOn)
	if m := dependsSection.FindStringSubmatch(r.get(n).Body); m != nil {
		for _, ref := range issueRef.FindAllStringSubmatch(m[1], -1) {
			d, _ := strconv.Atoi(ref[1])
			deps = append(deps, d)
		}
	}
	return slices.ContainsFunc(deps, r.open) || slices.ContainsFunc(t.ConflictsWith, r.going) ||
		slices.ContainsFunc(p.Tickets, func(o Ticket) bool {
			return slices.Contains(o.ConflictsWith, n) && r.going(*o.Issue)
		})
}

// runs reads root's .atm/runs.jsonl: how the last run of each issue stands, and how many runs are in their agent.
func runs(root string) (last map[string]string, agents int, err error) {
	last = map[string]string{}
	all, err := run.RecordedRuns(root)
	if err != nil {
		return last, 0, err
	}
	for _, o := range all {
		last[o.Issue] = o.Outcome
		if o.Outcome == "running" && o.Step == "agent" {
			agents++
		}
	}
	return last, agents, nil
}

const (
	poll   = time.Minute
	remind = 30 * time.Minute
)

// Follow reads the plan at path and its status every minute until no ticket is open, then writes plan_closed.
// It writes the status to out, a JSON line, when it changes, and again every 30 minutes while a ticket can launch
// or the CoS holds one. Issue and runs-file errors go to errOut and it tries again.
func Follow(path, root string, issue func(int) (Issue, error), out, errOut io.Writer, now func() time.Time,
	sleep func(time.Duration)) error {
	var last []byte
	var wrote time.Time
	for ; ; sleep(poll) {
		p, err := Load(path)
		if err != nil {
			return err
		}
		s, err := Read(p, root, issue)
		if err != nil {
			_, _ = fmt.Fprintln(errOut, "atm:", err)
			continue
		}
		if len(s.Open) == 0 {
			_, err := io.WriteString(out, `{"event":"plan_closed"}`+"\n")
			return err
		}
		line, _ := json.Marshal(s)
		urgent := len(s.Ready) > 0 && s.Free > 0 || len(s.CosHolds) > 0
		if bytes.Equal(line, last) && (!urgent || now().Sub(wrote) < remind) {
			continue
		}
		if _, err := out.Write(append(line, '\n')); err != nil {
			return err
		}
		last, wrote = line, now()
	}
}
