package run

import (
	"fmt"
	"syscall"
	"unsafe"
)

func processActivity(pid int) (string, error) {
	var usage struct {
		UUID [16]byte
		CPU  [2]uint64
		_    [14]uint64
		IO   [2]uint64
	}
	_, _, err := syscall.Syscall6(syscall.SYS_PROC_INFO, 9, uintptr(pid), 2, 0,
		uintptr(unsafe.Pointer(&usage)), 0)
	if err != 0 {
		return "", err
	}
	return fmt.Sprint(usage.CPU, usage.IO), nil
}
