//go:build windows

package run

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

const (
	createSuspended                = 0x00000004
	jobObjectLimitKillOnJobClose   = 0x00002000
	jobObjectBasicLimitInformation = 2
	processSetQuota                = 0x0100
	processTerminate               = 0x0001
	processSuspendResume           = 0x0800
)

type jobBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	createJobObjectW   = kernel32.NewProc("CreateJobObjectW")
	setInformationJob  = kernel32.NewProc("SetInformationJobObject")
	assignProcessToJob = kernel32.NewProc("AssignProcessToJobObject")
	terminateJob       = kernel32.NewProc("TerminateJobObject")
	ntdll              = syscall.NewLazyDLL("ntdll.dll")
	resumeProcess      = ntdll.NewProc("NtResumeProcess")
)

func inOwnGroup(cmd *exec.Cmd) (func() error, func(), error) {
	r, _, callErr := createJobObjectW.Call(0, 0)
	if r == 0 {
		return nil, nil, callErr
	}
	job := syscall.Handle(r)
	close := func() { _ = syscall.CloseHandle(job) }
	limits := jobBasicLimitInformation{LimitFlags: jobObjectLimitKillOnJobClose}
	r, _, callErr = setInformationJob.Call(uintptr(job), jobObjectBasicLimitInformation,
		uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits))
	if r == 0 {
		close()
		return nil, nil, callErr
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createSuspended
	var mu sync.Mutex
	canceled := false
	cmd.Cancel = func() error {
		mu.Lock()
		defer mu.Unlock()
		canceled = true
		r, _, err := terminateJob.Call(uintptr(job), 1)
		if r == 0 {
			return err
		}
		return nil
	}
	afterStart := func() error {
		mu.Lock()
		defer mu.Unlock()
		if canceled {
			return fmt.Errorf("process group was canceled before it started")
		}
		process, err := syscall.OpenProcess(processSetQuota|processTerminate|processSuspendResume, false, uint32(cmd.Process.Pid))
		if err != nil {
			return err
		}
		defer syscall.CloseHandle(process)
		r, _, callErr := assignProcessToJob.Call(uintptr(job), uintptr(process))
		if r == 0 {
			return callErr
		}
		status, _, _ := resumeProcess.Call(uintptr(process))
		if int32(status) < 0 {
			return fmt.Errorf("resume suspended process: NTSTATUS 0x%08x", uint32(status))
		}
		return nil
	}
	return afterStart, close, nil
}
