//go:build !unix

package run

import "os/exec"

// inOwnGroup leaves cmd as it is.
// ponytail: off Unix the cancel kills the process alone, and cmd.WaitDelay stops ATM waiting on a
// grandchild that still holds its output. A Windows job object is the upgrade path.
func inOwnGroup(*exec.Cmd) {}
