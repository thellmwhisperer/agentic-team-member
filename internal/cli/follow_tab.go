package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
	"github.com/thellmwhisperer/agentic-team-member/internal/tui"
)

// followOut is --follow's stdout. With HERDR_ENV=1 and herdr_workspace set, the first line opens the run's tab
// and a terminal line (merged, closed, run_failed) closes it after that line is written, before exit.
type followOut struct {
	w   io.Writer
	buf []byte
	tab *runTab
}

func newFollowOut(root string, args []string, w io.Writer) *followOut {
	o := &followOut{w: w}
	if os.Getenv("HERDR_ENV") != "1" {
		return o
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return o
	}
	ws, err := config.HerdrWorkspace(home)
	if err != nil || ws == "" {
		return o
	}
	o.tab = &runTab{workspace: ws, label: "atm-" + issueArg(args), cwd: root}
	return o
}

func (o *followOut) Write(p []byte) (int, error) {
	n, err := o.w.Write(p)
	if o.tab == nil || n == 0 {
		return n, err
	}
	o.buf = append(o.buf, p[:n]...)
	for {
		i := bytes.IndexByte(o.buf, '\n')
		if i < 0 {
			break
		}
		o.seen(o.buf[:i])
		o.buf = o.buf[i+1:]
	}
	return n, err
}

func (o *followOut) seen(line []byte) {
	var ev struct{ Run, Event string }
	if json.Unmarshal(line, &ev) != nil || ev.Run == "" {
		return
	}
	if o.tab.id == "" && !o.tab.gaveUp {
		o.tab.open(ev.Run)
	}
	if ev.Event == "merged" || ev.Event == "closed" || ev.Event == "run_failed" {
		o.tab.terminal = true
	}
}

func (o *followOut) close() {
	if o == nil || o.tab == nil || !o.tab.terminal || o.tab.id == "" {
		return
	}
	id := o.tab.id
	o.tab.id = ""
	if err := tui.CloseHerdrTab(id); err != nil {
		herdrLine(err)
	}
}

type runTab struct {
	workspace, label, cwd, id string
	terminal, gaveUp          bool
}

func (t *runTab) open(runID string) {
	id, err := tui.OpenHerdrTab(t.workspace, t.label, t.cwd, "atm attach "+runID)
	t.id = id
	if id == "" {
		t.gaveUp = true
	}
	if err != nil {
		herdrLine(err)
	}
}

func herdrLine(err error) {
	fmt.Fprintf(os.Stderr, "atm: herdr: %s\n", strings.ReplaceAll(err.Error(), "\n", " "))
}

// issueArg is the issue on atm run's command line, flags and their values skipped.
func issueArg(args []string) string {
	skip := map[string]bool{"--base-ref": true, "--harness": true, "--model": true, "--effort": true,
		"--harness-arg": true, "--env": true, "--watcher": true, "--resume": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if name, _, cut := strings.Cut(a, "="); cut && skip[name] {
			continue
		}
		if skip[a] {
			i++
			continue
		}
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}
