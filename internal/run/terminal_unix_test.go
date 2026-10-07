//go:build unix

package run

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The delivery command runs on the terminal attached to the run: what it prints goes to it as it is, its keys
// go to the command, and the screen gets the delivery's end after it. Off a terminal, its output is lines.
func TestDeliveryRunsOnTheAttachedTerminal(t *testing.T) {
	line := `if test -t 0 && test -t 1; then echo on-a-terminal; else echo not-a-terminal; fi; ` +
		`stty size; read x; echo got-$x`
	top := gitT(t, repo(t, t.TempDir(), atmSet(atmYAML, "delivery", line)), "rev-parse", "--show-toplevel")
	background(t, top)
	gitT(t, top, "config", "user.name", "Repo Dev")
	gitT(t, top, "config", "user.email", "dev@example.com")
	o, err := Start(top, []string{issueFile(t, issue)})
	if err != nil {
		t.Fatal(err)
	}
	in := make(chan Input, 1)
	var raw bytes.Buffer
	var after []string // the node events after the first raw output
	end, err := Follow(top, o.Run, &[2]int{100, 30}, in, func(f Frame) {
		if f.Raw != nil && raw.Len() == 0 {
			in <- Input{Keys: []byte("hi\r")}
		}
		raw.Write(f.Raw)
		var ev struct{ Step, State string }
		if raw.Len() > 0 && json.Unmarshal(f.Event, &ev) == nil && ev.State != "" {
			after = append(after, ev.Step+" "+ev.State)
		}
	})
	if err != nil || end.Outcome != "passed" {
		t.Fatalf("Follow = %+v, %v\n%s", end, err, raw.String())
	}
	for _, want := range []string{"on-a-terminal", "30 100", "got-hi"} {
		if !strings.Contains(raw.String(), want) {
			t.Fatalf("want %q on the terminal, got %q", want, raw.String())
		}
	}
	if len(after) != 1 || after[0] != "delivery passed" {
		t.Fatalf("node events after the terminal output: %v", after)
	}
	t.Run("off a terminal", func(t *testing.T) { offATerminal(t, top) })
}

func offATerminal(t *testing.T, top string) {
	o, err := Start(top, []string{issueFile(t, issue)})
	if err != nil {
		t.Fatal(err)
	}
	var screen bytes.Buffer
	if _, err := Attach(top, o.Run, &screen); err != nil {
		t.Fatal(err)
	}
	if s := screen.String(); !strings.Contains(s, "\nnot-a-terminal\n") || strings.Contains(s, "on-a-terminal") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestFollowDisconnectsWhenItsTerminalLeaves(t *testing.T) {
	top := gitT(t, repo(t, t.TempDir(), atmSet(atmYAML, "delivery", "sleep 3")), "rev-parse", "--show-toplevel")
	background(t, top)
	o, err := Start(top, []string{issueFile(t, issue)})
	if err != nil {
		t.Fatal(err)
	}
	in := make(chan Input)
	first := make(chan struct{}, 1)
	ended := make(chan error, 1)
	go func() {
		_, err := Follow(top, o.Run, &[2]int{100, 30}, in, func(Frame) {
			select {
			case first <- struct{}{}:
			default:
			}
		})
		ended <- err
	}()
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not send its first frame")
	}
	close(in)
	select {
	case err := <-ended:
		if err == nil {
			t.Fatal("detached Follow returned before the run ended without an error")
		}
	case <-time.After(time.Second):
		t.Fatal("Follow kept the terminal attached after its input closed")
	}
	if _, err := Attach(top, o.Run, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}
