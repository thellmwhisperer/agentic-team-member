package run

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/duration"
)

type obj = map[string]any

// runLog is worker-<timestamp>.jsonl: every agent line and every atm event, one JSON object a line, and
// the ▶ / ✓ / ✗ lines of each step on out.
type runLog struct {
	f       *os.File
	out     io.Writer
	harness string
}

func (l *runLog) write(event any) {
	b, err := json.Marshal(obj{"ts": time.Now().UTC().Format(time.RFC3339Nano), "harness": l.harness, "event": event})
	if err == nil {
		_, _ = l.f.Write(append(b, '\n'))
	}
}

func (l *runLog) log(name string, data obj) {
	data["type"] = "atm." + name
	l.write(data)
}

// step runs fn as the step name: its start is on disk before fn runs, its end after; fn says if it passed.
func (l *runLog) step(name string, fn func() bool) bool {
	l.log("step", obj{"step": name, "state": "started"})
	_, _ = fmt.Fprintf(l.out, "▶ %s\n", name)
	start := time.Now()
	ok := fn()
	took := time.Since(start)
	state, mark := "passed", "✓"
	if !ok {
		state, mark = "failed", "✗"
	}
	l.log("step", obj{"step": name, "state": state, "duration_seconds": took.Round(10 * time.Millisecond).Seconds()})
	_, _ = fmt.Fprintf(l.out, "%s %s %s\n", mark, name, duration.Format(took))
	return ok
}
