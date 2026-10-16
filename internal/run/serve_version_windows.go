package run

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func socketPeerPID(fd uintptr) (int, error) {
	// SIO_AF_UNIX_GETPEERPID: _WSAIOR(IOC_VENDOR, 256).
	const getPeerPID = 0x58000100
	var pid, returned uint32
	err := windows.WSAIoctl(windows.Handle(fd), getPeerPID, nil, 0,
		(*byte)(unsafe.Pointer(&pid)), uint32(unsafe.Sizeof(pid)), &returned, nil, 0)
	return int(pid), err
}
