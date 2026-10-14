package plan

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func TestDependsOnKeepsReferencesUntilNextSection(t *testing.T) {
	g := &gh{bodies: map[int]string{2: "## Depends on\n#12\n### More\n#13\n## Notes\n#99\n"}}
	plan := `{"version":1,"max_active":2,"tickets":[{"issue":2}]}`
	if got := status(t, g, plan); len(got.Ready) != 0 {
		t.Fatalf("open dependencies made ticket ready: %+v", got)
	}
	g.closed = map[int]bool{12: true, 13: true}
	if got := status(t, g, plan); len(got.Ready) != 1 || got.Ready[0] != 2 {
		t.Fatalf("closed dependencies left ticket blocked by Notes: %+v", got)
	}
}

func TestFollowReturnsWhenPlanBecomesInvalid(t *testing.T) {
	root, path := fixture(t, `{"version":1,"max_active":2,"tickets":[{"issue":2}]}`)
	g := &gh{}
	var out, errOut strings.Builder
	clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	sleeps := 0
	sleep := func(d time.Duration) {
		sleeps++
		if sleeps > 1 {
			t.Fatal("follow retried an invalid plan")
		}
		clock = clock.Add(d)
		if err := os.WriteFile(path, []byte(`{"max_active":2,"tickets":[{"issue":2}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := Follow(path, root, g.issue, &out, &errOut, func() time.Time { return clock }, sleep)
	if run.ExitCode(err) != 2 || !strings.Contains(err.Error(), "version") || sleeps != 1 ||
		!strings.Contains(out.String(), `"open":[2]`) || errOut.String() != "" {
		t.Fatalf("follow output %q, stderr %q, exit %d (%v), sleeps %d", out.String(), errOut.String(),
			run.ExitCode(err), err, sleeps)
	}
}
