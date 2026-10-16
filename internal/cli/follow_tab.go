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

// followOut is --follow's stdout. With HERDR_ENV=1 and herdr_workspace set, open makes the run's tab before the
// run starts, the first line runs atm attach in its root pane, and after a terminal line (merged, closed,
// run_failed) is written, close closes the tab before exit.
type followOut struct {
	w   io.Writer
	buf []byte
	tab *runTab
}

func newFollowOut(root string, w io.Writer) *followOut {
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
	o.tab = &runTab{workspace: ws, cwd: root}
	return o
}

func (o *followOut) open(issue string) string {
	if o.tab == nil {
		return ""
	}
	id, pane, err := tui.OpenHerdrTab(o.tab.workspace, "atm-"+issue, o.tab.cwd)
	o.tab.id, o.tab.pane = id, pane
	if err != nil {
		herdrLine(err)
	}
	return pane
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
	if o.tab.pane != "" {
		if err := tui.RunHerdrPane(o.tab.pane, "atm attach "+ev.Run); err != nil {
			herdrLine(err)
		}
		o.tab.pane = ""
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
	workspace, cwd, id, pane string
	terminal                 bool
}

func herdrLine(err error) {
	fmt.Fprintf(os.Stderr, "atm: herdr: %s\n", strings.ReplaceAll(err.Error(), "\n", " "))
}
