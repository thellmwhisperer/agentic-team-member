//go:build unix

package run

import (
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// run runs cmd on a terminal the size of the last one attached to the run, while one is, its output as it
// is to the screen and to where cmd's output goes, and waits for it. The attached terminals' keys go to it.
func (w *screen) run(cmd *exec.Cmd) (bool, error) {
	w.s.Lock()
	size, on := w.r.size, w.r.ttys > 0
	w.s.Unlock()
	if !on {
		return false, nil
	}
	out := cmd.Stdout
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	// A session of its own, so the terminal is its controlling one; it leads its process group all the same.
	p, err := pty.StartWithAttrs(cmd, winsize(size), &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		return true, err
	}
	w.s.Lock()
	w.r.pty = p
	w.s.Unlock()
	_, _ = io.Copy(out, p) // until nothing holds the terminal: EOF, or EIO on Linux
	err = cmd.Wait()
	w.s.Lock()
	w.r.pty = nil
	w.s.Unlock()
	_ = p.Close()
	return true, err
}

func winsize(size [2]int) *pty.Winsize {
	return &pty.Winsize{Cols: uint16(size[0]), Rows: uint16(size[1])}
}

// resize gives the terminal p the size of the attached one.
func resize(p *os.File, size [2]int) {
	_ = pty.Setsize(p, winsize(size))
}
