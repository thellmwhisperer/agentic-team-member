package plan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// fixture writes the plan, and runs.jsonl's lines, in a new repository root, which it returns with the plan's path.
func fixture(t *testing.T, plan string, runs ...string) (root, path string) {
	t.Helper()
	root = t.TempDir()
	path = filepath.Join(root, "plan.json")
	if err := os.WriteFile(path, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	jsonl := []byte(strings.Join(runs, "\n"))
	if err := os.WriteFile(filepath.Join(root, ".atm", "runs.jsonl"), jsonl, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, path
}

// gh is a fake GitHub: the issues it names closed are closed, every other open, with the body it names.
type gh struct {
	closed map[int]bool
	bodies map[int]string
	fail   bool // the next read fails
}

func (g *gh) issue(n int) (Issue, error) {
	if g.fail {
		g.fail = false
		return Issue{}, errors.New("gh: rate limited")
	}
	if g.closed[n] {
		return Issue{State: "CLOSED", Body: g.bodies[n]}, nil
	}
	return Issue{State: "OPEN", Body: g.bodies[n]}, nil
}

func status(t *testing.T, g *gh, plan string, runs ...string) Status {
	t.Helper()
	root, path := fixture(t, plan, runs...)
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Read(p, root, g.issue)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ints(n ...int) []int { return append([]int{}, n...) }

func runLine(issue, outcome, step string) string {
	return fmt.Sprintf(`{"outcome":%q,"run":"%s-1","issue":%q,"step":%q}`, outcome, issue, issue, step)
}

func TestReadSaysWhichTicketsCanLaunch(t *testing.T) {
	for _, c := range []struct {
		name string
		gh   gh
		plan string
		runs []string
		want Status
	}{
		{"an open dependency blocks", gh{}, `{"version":1,"max_active":2,"tickets":[{"issue":2,"depends_on":[1]}]}`,
			nil, Status{Open: ints(2), Ready: ints(), Free: 2}},
		{"closed dependencies do not", gh{closed: map[int]bool{1: true, 3: true}},
			`{"version":1,"max_active":2,"tickets":[{"issue":2,"depends_on":[1,3]}]}`,
			nil, Status{Open: ints(2), Ready: ints(2), Free: 2}},
		{"a dependency only in the issue's Depends on blocks", gh{closed: map[int]bool{1: true},
			bodies: map[int]string{2: "Type: feature\n\n## Depends on\n\n- #1\n- #4\n\n## Notes\n\n#5\n"}},
			`{"version":1,"max_active":2,"tickets":[{"issue":2,"depends_on":[]}]}`,
			nil, Status{Open: ints(2), Ready: ints(), Free: 2}},
		{"a running ticket that names it in conflicts_with blocks", gh{},
			`{"version":1,"max_active":2,"tickets":[{"issue":1},{"issue":2,"conflicts_with":[1]}]}`,
			[]string{runLine("2", "running", "agent")}, Status{Open: ints(1, 2), Ready: ints(), Working: ints(2), Free: 1}},
		{"runs in delivery take no slot", gh{}, `{"version":1,"max_active":4,"tickets":[]}`,
			[]string{runLine("7", "running", "agent"), runLine("8", "running", "agent"), runLine("9", "running", "delivery")},
			Status{Open: ints(), Ready: ints(), Free: 2}},
		{"only runs in their agent take a slot", gh{}, `{"version":1,"max_active":4,"tickets":[]}`,
			[]string{runLine("7", "running", "agent"), runLine("8", "running", "checks"), runLine("9", "running", "delivery")},
			Status{Open: ints(), Ready: ints(), Free: 3}},
		{"holds wait on the CoS or on Javi", gh{}, `{"version":1,"max_active":2,"tickets":[{"issue":1,"hold":"PR first"},` +
			`{"issue":2,"hold":"his call","hold_owner":"javi"}]}`,
			nil, Status{Open: ints(1, 2), Ready: ints(), CosHolds: ints(1), JaviHolds: ints(2), Free: 2}},
		{"closed and excluded tickets are in no list", gh{closed: map[int]bool{1: true}},
			`{"version":1,"max_active":2,"tickets":[{"issue":1},{"issue":2,"hold":"x"},{"issue":3}],"excluded":[2,3]}`,
			nil, Status{Open: ints(), Ready: ints(), Free: 2}},
		{"a passed ticket with its issue open goes on and blocks", gh{},
			`{"version":1,"max_active":2,"tickets":[{"issue":1,"conflicts_with":[2]},{"issue":2},{"issue":3}]}`,
			[]string{runLine("2", "running", "agent"), runLine("2", "passed", "delivery"), runLine("3", "failed", "agent")},
			Status{Open: ints(1, 2, 3), Ready: ints(3), Working: ints(2), Free: 2}},
		{"a passed ticket with its issue closed is gone", gh{closed: map[int]bool{2: true}},
			`{"version":1,"max_active":2,"tickets":[{"issue":1,"conflicts_with":[2]},{"issue":2}]}`,
			[]string{runLine("2", "passed", "delivery")}, Status{Open: ints(1), Ready: ints(1), Free: 2}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, l := range []*[]int{&c.want.CosHolds, &c.want.JaviHolds, &c.want.Working} {
				if *l == nil {
					*l = ints()
				}
			}
			if got := status(t, &c.gh, c.plan, c.runs...); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestLoadNamesTheFieldThatMakesThePlanInvalid(t *testing.T) {
	for _, c := range []struct{ plan, field string }{
		{`{"max_active":2,"tickets":[]}`, "version"},
		{`{"version":2,"max_active":2,"tickets":[]}`, "version"},
		{`{"version":1,"tickets":[]}`, "max_active"},
		{`{"version":1,"max_active":0,"tickets":[]}`, "max_active"},
		{`{"version":1,"max_active":2,"tickets":[{"issue":1},{"depends_on":[1]}]}`, "tickets[1]"},
	} {
		_, path := fixture(t, c.plan)
		if _, err := Load(path); run.ExitCode(err) != 2 || !strings.Contains(err.Error(), c.field) {
			t.Fatalf("%s: exit %d, %v, want an error naming %s", c.plan, run.ExitCode(err), err, c.field)
		}
	}
}

// follow follows the plan with a fake clock that starts at minute 0, each minute's change to g made when the clock
// reaches it, and returns each line written, after the minute it was written at, and what went to stderr.
func follow(t *testing.T, g *gh, plan string, at map[int]func()) (lines []string, stderr string) {
	t.Helper()
	root, path := fixture(t, plan)
	start := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	clock := start
	minute := func() int { return int(clock.Sub(start) / time.Minute) }
	sleep := func(d time.Duration) {
		clock = clock.Add(d)
		if f := at[minute()]; f != nil {
			f()
		}
		if minute() > 24*60 {
			t.Fatalf("still following after a day; wrote %q", lines)
		}
	}
	var errOut strings.Builder
	out := writer(func(b []byte) {
		lines = append(lines, fmt.Sprintf("%d %s", minute(), strings.TrimSuffix(string(b), "\n")))
	})
	if err := Follow(path, root, g.issue, out, &errOut, func() time.Time { return clock }, sleep); err != nil {
		t.Fatal(err)
	}
	return lines, errOut.String()
}

type writer func([]byte)

func (w writer) Write(b []byte) (int, error) { w(b); return len(b), nil }

func TestFollowWritesWhenThePlanChangesUntilItCloses(t *testing.T) {
	const (
		ready   = `{"open":[1],"ready":[1],"cos_holds":[],"javi_holds":[],"working":[],"free":2}`
		blocked = `{"open":[2],"ready":[],"cos_holds":[],"javi_holds":[],"working":[],"free":2}`
		javi    = `{"open":[3],"ready":[],"cos_holds":[],"javi_holds":[3],"working":[],"free":2}`
		closed  = `{"event":"plan_closed"}`
	)
	for _, c := range []struct {
		name, plan string
		at         func(g *gh) map[int]func()
		want       []string
		stderr     string
	}{
		{"a ticket that can launch is repeated every 30 minutes, not before",
			`{"version":1,"max_active":2,"tickets":[{"issue":1}]}`,
			func(g *gh) map[int]func() { return map[int]func(){61: func() { g.closed[1] = true }} },
			[]string{"0 " + ready, "30 " + ready, "60 " + ready, "61 " + closed}, ""},
		{"a dependency that closes makes its ticket ready",
			`{"version":1,"max_active":2,"tickets":[{"issue":2,"depends_on":[1]}]}`,
			func(g *gh) map[int]func() {
				return map[int]func(){5: func() { g.closed[1] = true }, 10: func() { g.closed[2] = true }}
			},
			[]string{"0 " + blocked, "5 " + strings.Replace(blocked, `"ready":[]`, `"ready":[2]`, 1), "10 " + closed}, ""},
		{"holds on Javi alone are not repeated",
			`{"version":1,"max_active":2,"tickets":[{"issue":3,"hold":"his call","hold_owner":"javi"}]}`,
			func(g *gh) map[int]func() { return map[int]func(){90: func() { g.closed[3] = true }} },
			[]string{"0 " + javi, "90 " + closed}, ""},
		{"a gh that fails once does not end it",
			`{"version":1,"max_active":2,"tickets":[{"issue":3,"hold":"his call","hold_owner":"javi"}]}`,
			func(g *gh) map[int]func() {
				return map[int]func(){1: func() { g.fail = true }, 3: func() { g.closed[3] = true }}
			},
			[]string{"0 " + javi, "3 " + closed}, "atm: gh: rate limited\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := &gh{closed: map[int]bool{}}
			lines, stderr := follow(t, g, c.plan, c.at(g))
			if !reflect.DeepEqual(lines, c.want) || stderr != c.stderr {
				t.Fatalf("wrote\n%s\nstderr %q, want\n%s\nstderr %q", strings.Join(lines, "\n"), stderr,
					strings.Join(c.want, "\n"), c.stderr)
			}
		})
	}
}
