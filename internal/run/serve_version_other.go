//go:build !linux && !darwin

package run

import "errors"

func socketPeerPID(_ uintptr) (int, error) {
	return 0, errors.New("serve peer pid is unavailable on this platform")
}
