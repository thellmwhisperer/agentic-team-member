//go:build !unix && !windows

package run

import "os/exec"

func inOwnGroup(*exec.Cmd) (func() error, func(), error) {
	return func() error { return nil }, func() {}, nil
}
