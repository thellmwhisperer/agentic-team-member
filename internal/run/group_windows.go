//go:build windows

package run

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// ownGroup makes cancellation terminate cmd and its child processes.
func ownGroup(cmd *exec.Cmd) (func() error, func(), error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, nil, err
	}
	cmd.Cancel = func() error { return windows.TerminateJobObject(job, 1) }
	attach := func() error {
		process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|
			windows.PROCESS_SUSPEND_RESUME, false, uint32(cmd.Process.Pid))
		if err != nil {
			_ = cmd.Process.Kill()
			return err
		}
		defer windows.CloseHandle(process)
		if err := windows.AssignProcessToJobObject(job, process); err != nil {
			_ = cmd.Process.Kill()
			return err
		}
		status, _, _ := ntResumeProcess.Call(uintptr(process))
		if status != 0 {
			_ = windows.TerminateJobObject(job, 1)
			_ = cmd.Process.Kill()
			return windows.NTStatus(status)
		}
		return nil
	}
	closeJob := func() { _ = windows.CloseHandle(job) }
	return attach, closeJob, nil
}

// detach starts cmd without a console and in a group of its own, so the terminal that started it can close.
func detach(cmd *exec.Cmd) {
	const detachedProcess = 0x00000008
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess}
}
