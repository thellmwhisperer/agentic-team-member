//go:build !unix

package run

import "os/exec"

// ownGroup leaves cmd as it is. ponytail: off Unix the timeout kills only the agent, not what it started;
// a Windows job object is the upgrade.
func ownGroup(*exec.Cmd) {}
