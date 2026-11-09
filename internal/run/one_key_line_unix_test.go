//go:build unix

package run

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

func TestRealAttachShowsOneKeyLine(t *testing.T) {
	// Some terminals count the emoji's code points separately, while ATM measures
	// the grapheme. An automatic wrap then moves the footer out of its cached row.
	wide := strings.Repeat("👩‍💻", 20)
	for _, size := range [][2]uint16{{60, 40}, {120, 40}, {60, 24}, {120, 24}} {
		cols := size[0]
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			nm := gateFake(t)
			a := liveAttach(t, cols, size[1])
			a.until("q detach")
			a.send(`{"step":"agent","state":"started"}`)
			a.send(`{"step":"agent","says":"` + wide + `"}`)
			a.until("show thinking")
			a.send(fmt.Sprintf(`{"step":"agent","says":%q}`, strings.Repeat("working\n", 45)+"Agent is working"))
			a.until("Agent is working")
			a.key("t")
			a.until("hide tool use")
			assertOneKeyLine(t, a, int(cols), "q detach")
			a.send(`{"step":"agent","state":"passed"}`)
			writeFixture(t, filepath.Join(nm, "review.toon"),
				"findings[0]{id,severity,file,action,description}:\nlines: 1 total\nlog[1]{line}:\n  "+wide+"\n")
			a.delivered(t.TempDir())
			a.drawn(func(s string) bool { return strings.Contains(s, wide) && strings.Contains(s, "q close") })
			assertOneKeyLine(t, a, int(cols), "q close")
			if err := os.WriteFile(filepath.Join(nm, "done"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			a.drawn(gatePassed.MatchString)
			assertOneKeyLine(t, a, int(cols), "q close")
			a.key("q")
			a.exits()
			grid := assertOneKeyLine(t, a, int(cols), "q close")
			if _, err := grid.Write([]byte("\x1b[H" + strings.Repeat(" ", int(cols)) + "wrap")); err != nil {
				t.Fatal(err)
			}
			if cell := grid.CellAt(0, 1); cell == nil || cell.Content != "w" {
				t.Fatal("attach did not restore automatic wrapping on exit")
			}
		})
	}
}

func assertOneKeyLine(t *testing.T, a *attached, cols int, key string) *vt.Terminal {
	t.Helper()
	grid := vt.NewTerminal(cols, a.rows)
	// Discard replies to terminal queries; this replay only needs the screen.
	if err := grid.InputPipe().(io.Closer).Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := grid.Write([]byte(a.raw.String())); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for y := 0; y < a.rows; y++ {
		for x := 0; x < cols; x++ {
			if c := grid.CellAt(x, y); c != nil {
				text.WriteString(c.Content)
			} else {
				text.WriteByte(' ')
			}
		}
		text.WriteByte('\n')
	}
	screen := text.String()
	count := 0
	for _, line := range strings.Split(screen, "\n") {
		if strings.Contains(line, "q detach") || strings.Contains(line, "q close") {
			count++
		}
	}
	if count != 1 || !strings.Contains(screen, key) {
		t.Errorf("want exactly one key line with %q, got %d:\n%s", key, count, screen)
	}
	return grid
}
