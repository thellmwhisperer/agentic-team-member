//go:build windows

package run

import (
	"os"
	"os/exec"
)

// run never runs cmd on a terminal: Windows has no pseudo-terminal pty starts. ponytail: delivery output only
// goes as lines there; ConPTY is the upgrade.
func (w *screen) run(*exec.Cmd) (bool, error) { return false, nil }

func resize(*os.File, [2]int) {}
